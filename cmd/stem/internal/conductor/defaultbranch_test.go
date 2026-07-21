package conductor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newBranchRepo builds a repository on the given branch, optionally recording
// a local refs/remotes/origin/HEAD pointing at defaultBranch — the local
// record of the remote's head that the offline resolver reads.
func newBranchRepo(t *testing.T, branch, remoteDefault string) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ambient@example.com"},
		{"config", "user.name", "Ambient Tester"},
		{"checkout", "-b", branch},
		{"commit", "--allow-empty", "-m", "initial"},
		{"remote", "add", "origin", "https://github.com/opentendril/opentendril.git"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if remoteDefault != "" {
		// Mirror what a clone records: a remote-tracking ref plus the
		// symbolic origin/HEAD that names the remote's default branch.
		if _, err := runGitCommand(ctx, repo, "update-ref", "refs/remotes/origin/"+remoteDefault, "HEAD"); err != nil {
			t.Fatalf("update-ref: %v", err)
		}
		if _, err := runGitCommand(ctx, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+remoteDefault); err != nil {
			t.Fatalf("symbolic-ref: %v", err)
		}
	}
	return repo
}

// TestResolveDefaultBranchLocalPrecedence pins the offline precedence:
// configuration beats the local remote head, which beats undetermined. No step
// ever falls back to a guessed name.
func TestResolveDefaultBranchLocalPrecedence(t *testing.T) {
	ctx := context.Background()

	repo := newBranchRepo(t, "feat/x", "trunk")
	resolution := ResolveDefaultBranchLocal(ctx, repo, "")
	if resolution.Source != DefaultBranchFromRemoteHead || resolution.Branch != "trunk" {
		t.Fatalf("resolution = %+v, want trunk from the local remote head", resolution)
	}

	// Configuration outranks discovery, and the refs/heads/ prefix is stripped.
	resolution = ResolveDefaultBranchLocal(ctx, repo, "refs/heads/release/2026")
	if resolution.Source != DefaultBranchFromConfig || resolution.Branch != "release/2026" {
		t.Fatalf("resolution = %+v, want the configured branch to win", resolution)
	}

	// No configuration and no local remote head: undetermined, NOT "main".
	bare := newBranchRepo(t, "feat/x", "")
	resolution = ResolveDefaultBranchLocal(ctx, bare, "")
	if resolution.Known() || resolution.Branch != "" {
		t.Fatalf("resolution = %+v, want an explicitly undetermined answer rather than a guess", resolution)
	}
}

// TestDefaultBranchProtectionFloor is the security-critical property: when the
// default branch cannot be determined, protection WIDENS to the well-known
// names rather than disappearing.
func TestDefaultBranchProtectionFloor(t *testing.T) {
	unknown := DefaultBranchResolution{Source: DefaultBranchUnknown}
	for _, branch := range []string{"main", "master", "refs/heads/main"} {
		if !unknown.IsProtected(branch) {
			t.Errorf("undetermined resolution left %q unprotected — protection must widen under uncertainty, never narrow", branch)
		}
	}
	if unknown.IsProtected("feat/x") {
		t.Error("the floor protected an ordinary feature branch")
	}

	// A KNOWN default branch protects exactly itself — a repository on trunk
	// does not protect a stray local branch that happens to be called main.
	known := DefaultBranchResolution{Branch: "trunk", Source: DefaultBranchFromRemoteHead}
	if !known.IsProtected("trunk") {
		t.Error("the resolved default branch was not protected")
	}
	if known.IsProtected("main") {
		t.Error("a repository whose default branch is trunk protected main as well — the floor must not apply when the answer is known")
	}
	if known.IsProtected("") {
		t.Error("an empty branch name was treated as protected")
	}
}

// TestResolveDefaultBranchPrefersAPI proves the authoritative resolver
// consults the interface ahead of the local record — and that the local record
// is the fallback when the interface cannot answer.
func TestResolveDefaultBranchPrefersAPI(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	repo := newBranchRepo(t, "feat/x", "stale-local-answer")

	apiCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 99001})
		case strings.Contains(r.URL.Path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		case r.URL.Path == "/repos/opentendril/opentendril":
			apiCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "trunk"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	defer func() { githubAPIBaseURL = orig }()
	appTokenMu.Lock()
	appTokenCache = map[string]cachedAppToken{}
	appTokenMu.Unlock()

	cred := ResolvedCredential{Method: CredentialApp, App: AppCredential{AppID: "4276558", PrivateKeyPath: keyPath}}
	resolution := ResolveDefaultBranch(context.Background(), repo, "", cred)
	if resolution.Source != DefaultBranchFromAPI || resolution.Branch != "trunk" {
		t.Fatalf("resolution = %+v, want trunk from the interface", resolution)
	}
	if apiCalls != 1 {
		t.Fatalf("interface calls = %d, want 1", apiCalls)
	}

	// The offline resolver must NOT dial the interface, even with the same
	// credential — a protection check never pays a round trip.
	before := apiCalls
	local := ResolveDefaultBranchLocal(context.Background(), repo, "")
	if local.Branch != "stale-local-answer" || local.Source != DefaultBranchFromRemoteHead {
		t.Fatalf("local resolution = %+v, want the local remote head", local)
	}
	if apiCalls != before {
		t.Fatal("the offline resolver dialed the interface")
	}
}

// TestGitCommitRefusesDefaultBranch: the protection fires at commit time — the
// earliest and cheapest point — and stages nothing.
func TestGitCommitRefusesDefaultBranch(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "trunk", "trunk")
	if err := os.WriteFile(filepath.Join(repo, "grown.txt"), []byte("grown\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	_, err := RunGitCommit(ctx, GitCommitExecution{
		Workspace:  repo,
		Message:    "chore: straight onto the default branch",
		Credential: ResolvedCredential{Identity: ResolvedIdentity{Name: "Bot", Email: "bot@example.com"}},
	})
	if err == nil {
		t.Fatal("a commit onto the repository's default branch was accepted")
	}
	if !strings.Contains(err.Error(), "default branch") || !strings.Contains(err.Error(), "tendril git branch") {
		t.Fatalf("error = %v, want a refusal naming the default branch and the operation that resolves it", err)
	}

	// Nothing may have been staged: the refusal must precede all side effects.
	staged, stageErr := runGitCommand(ctx, repo, "diff", "--cached", "--name-only")
	if stageErr != nil {
		t.Fatalf("git diff --cached: %v", stageErr)
	}
	if strings.TrimSpace(staged) != "" {
		t.Fatalf("staged %q, want nothing staged by a refused commit", staged)
	}
}

// TestGitCommitProtectionIsOptOutNotOptIn: the zero value protects, and only
// an explicit opt-out permits.
func TestGitCommitProtectionIsOptOutNotOptIn(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "trunk", "trunk")
	if err := os.WriteFile(filepath.Join(repo, "grown.txt"), []byte("grown\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	execution := GitCommitExecution{
		Workspace:                repo,
		Message:                  "chore: knowingly on the default branch",
		Credential:               ResolvedCredential{Identity: ResolvedIdentity{Name: "Bot", Email: "bot@example.com"}},
		AllowDefaultBranchCommit: true,
	}
	result, err := RunGitCommit(ctx, execution)
	if err != nil {
		t.Fatalf("explicit opt-out was still refused: %v", err)
	}
	if result.Status != "committed" {
		t.Fatalf("result = %+v, want a commit after an explicit opt-out", result)
	}
}

// TestGitCommitUnprotectedBranchUnaffected is the regression guard: ordinary
// feature-branch commits are untouched by any of this.
func TestGitCommitUnprotectedBranchUnaffected(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "feat/new-leaf", "trunk")
	if err := os.WriteFile(filepath.Join(repo, "grown.txt"), []byte("grown\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := RunGitCommit(ctx, GitCommitExecution{
		Workspace:  repo,
		Message:    "feat: grow a leaf",
		Credential: ResolvedCredential{Identity: ResolvedIdentity{Name: "Bot", Email: "bot@example.com"}},
	})
	if err != nil {
		t.Fatalf("commit on a feature branch: %v", err)
	}
	if result.Status != "committed" {
		t.Fatalf("result = %+v, want a normal commit", result)
	}
}

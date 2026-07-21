package conductor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunGitCommitValidatesExecution covers the plain-input requirements
// before any git command runs.
func TestRunGitCommitValidatesExecution(t *testing.T) {
	originalRun := runGitCommitCommandFn
	commands := 0
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		commands++
		return "", nil
	}
	defer func() { runGitCommitCommandFn = originalRun }()

	identity := ResolvedCredential{Identity: ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"}}
	if _, err := RunGitCommit(context.Background(), GitCommitExecution{Message: "chore: tidy", Credential: identity}); err == nil {
		t.Fatal("missing workspace accepted")
	}
	if _, err := RunGitCommit(context.Background(), GitCommitExecution{Workspace: "/tmp/workspace", Credential: identity}); err == nil {
		t.Fatal("missing message accepted")
	}
	if commands != 0 {
		t.Fatalf("%d git command(s) ran for invalid executions, want 0", commands)
	}
}

// TestRunGitCommitRequiresConfiguredIdentity is the deny-closed attribution
// rule: a delegated commit exists to be attributable, so a missing commit
// identity — name, email, or both — refuses the whole execution before any
// git command runs. No commit is ever created.
func TestRunGitCommitRequiresConfiguredIdentity(t *testing.T) {
	originalRun := runGitCommitCommandFn
	commands := 0
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		commands++
		return "", nil
	}
	defer func() { runGitCommitCommandFn = originalRun }()

	for _, credential := range []ResolvedCredential{
		{},
		{Identity: ResolvedIdentity{Name: "OpenTendril Bot"}},
		{Identity: ResolvedIdentity{Email: "bot@example.com"}},
		{Identity: ResolvedIdentity{Name: "  ", Email: "\t"}}, // whitespace-only counts as unset
	} {
		_, err := RunGitCommit(context.Background(), GitCommitExecution{
			Workspace:  "/tmp/workspace",
			Message:    "chore: tidy",
			Credential: credential,
		})
		if err == nil || !strings.Contains(err.Error(), "no configured commit identity") {
			t.Fatalf("identity %+v: error = %v, want a refused-without-identity report", credential.Identity, err)
		}
	}
	if commands != 0 {
		t.Fatalf("%d git command(s) ran for identity-less executions, want 0", commands)
	}
}

// newGitCommitRepo initializes a real repository with an ambient git identity
// and one initial commit, so RunGitCommit exercises real staging and
// committing.
func newGitCommitRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ambient@example.com"},
		{"config", "user.name", "Ambient Tester"},
		{"commit", "--allow-empty", "-m", "initial"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return repo
}

// TestRunGitCommitCreatesAttributedCommit proves a real commit is created and
// attributed (author and committer) to the configured identity — never to the
// ambient one — and that the reported hash is the repository's new HEAD.
func TestRunGitCommitCreatesAttributedCommit(t *testing.T) {
	ctx := context.Background()
	repo := newGitCommitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "grown.txt"), []byte("grown\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := RunGitCommit(ctx, GitCommitExecution{
		Workspace:  repo,
		Message:    "chore: record delegated growth",
		Credential: ResolvedCredential{Identity: ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Status != "committed" || result.CommitHash == "" {
		t.Fatalf("result = %+v, want a committed status with a hash", result)
	}

	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	if result.CommitHash != strings.TrimSpace(head) {
		t.Fatalf("reported hash %q is not HEAD %q", result.CommitHash, head)
	}

	attribution, err := runGitCommand(ctx, repo, "log", "-1", "--format=%an|%ae|%cn|%ce|%s")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	want := "OpenTendril Bot|bot@example.com|OpenTendril Bot|bot@example.com|chore: record delegated growth"
	if strings.TrimSpace(attribution) != want {
		t.Fatalf("attribution = %q, want %q", attribution, want)
	}
}

// TestRunGitCommitNothingToCommit proves a clean workspace returns cleanly —
// no error, no empty commit (unlike the Sprout status path, which
// deliberately allows one).
func TestRunGitCommitNothingToCommit(t *testing.T) {
	ctx := context.Background()
	repo := newGitCommitRepo(t)
	before, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}

	result, err := RunGitCommit(ctx, GitCommitExecution{
		Workspace:  repo,
		Message:    "chore: nothing here",
		Credential: ResolvedCredential{Identity: ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Status != "nothing-to-commit" || result.CommitHash != "" {
		t.Fatalf("result = %+v, want a hash-less nothing-to-commit status", result)
	}

	after, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	if before != after {
		t.Fatalf("HEAD moved from %q to %q; an empty delegated commit must never be created", before, after)
	}
}

// TestRunGitPushValidatesWorkspace covers the plain-input requirement before
// any git command runs.
func TestRunGitPushValidatesWorkspace(t *testing.T) {
	originalRead := runGitCommitCommandFn
	originalPush := runGitPushCommandFn
	commands := 0
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		commands++
		return "", nil
	}
	runGitPushCommandFn = func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
		commands++
		return "", nil
	}
	defer func() { runGitCommitCommandFn = originalRead; runGitPushCommandFn = originalPush }()

	if _, err := RunGitPush(context.Background(), GitPushExecution{}); err == nil {
		t.Fatal("missing workspace accepted")
	}
	if commands != 0 {
		t.Fatalf("%d git command(s) ran for an invalid execution, want 0", commands)
	}
}

// newGitPushRepoWithRemote builds a working repository on branch main with one
// commit and a bare origin remote on disk, so RunGitPush exercises a real
// authenticated-shaped push (no token needed for a local file remote).
func newGitPushRepoWithRemote(t *testing.T) (workspace, remote string) {
	t.Helper()
	ctx := context.Background()
	remote = t.TempDir()
	if _, err := runGitCommand(ctx, remote, "init", "--bare"); err != nil {
		t.Fatalf("git init --bare: %v", err)
	}
	workspace = t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ambient@example.com"},
		{"config", "user.name", "Ambient Tester"},
		{"checkout", "-b", "main"},
		{"commit", "--allow-empty", "-m", "initial"},
		{"remote", "add", "origin", remote},
	} {
		if _, err := runGitCommand(ctx, workspace, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	return workspace, remote
}

// TestRunGitPushPushesCurrentBranch proves the workspace's current branch lands
// on the remote when no explicit branch is given.
func TestRunGitPushPushesCurrentBranch(t *testing.T) {
	ctx := context.Background()
	workspace, remote := newGitPushRepoWithRemote(t)

	result, err := RunGitPush(ctx, GitPushExecution{Workspace: workspace})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.Status != "pushed" || result.Branch != "main" {
		t.Fatalf("result = %+v, want pushed on main", result)
	}

	local, err := runGitCommand(ctx, workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	remoteRef, err := runGitCommand(ctx, remote, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatalf("git rev-parse remote ref: %v", err)
	}
	if strings.TrimSpace(local) != strings.TrimSpace(remoteRef) {
		t.Fatalf("remote ref %q does not match local HEAD %q", remoteRef, local)
	}
}

// TestRunGitPushExplicitBranch proves an explicit branch is pushed and the
// refs/heads/ prefix is tolerated.
func TestRunGitPushExplicitBranch(t *testing.T) {
	ctx := context.Background()
	workspace, remote := newGitPushRepoWithRemote(t)

	result, err := RunGitPush(ctx, GitPushExecution{Workspace: workspace, Branch: "refs/heads/main"})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if result.Branch != "main" {
		t.Fatalf("branch = %q, want the refs/heads/ prefix stripped to main", result.Branch)
	}
	if _, err := runGitCommand(ctx, remote, "rev-parse", "refs/heads/main"); err != nil {
		t.Fatalf("remote did not receive refs/heads/main: %v", err)
	}
}

// TestRunGitCommitStagesOnlyGivenPaths proves the optional path list bounds
// staging: the named file is committed, the unnamed one stays uncommitted.
func TestRunGitCommitStagesOnlyGivenPaths(t *testing.T) {
	ctx := context.Background()
	repo := newGitCommitRepo(t)
	for _, name := range []string{"wanted.txt", "unwanted.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	result, err := RunGitCommit(ctx, GitCommitExecution{
		Workspace:  repo,
		Message:    "chore: commit only the wanted file",
		Paths:      []string{"wanted.txt"},
		Credential: ResolvedCredential{Identity: ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"}},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Status != "committed" {
		t.Fatalf("result = %+v, want committed", result)
	}

	committed, err := runGitCommand(ctx, repo, "show", "--name-only", "--format=", "HEAD")
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	if !strings.Contains(committed, "wanted.txt") || strings.Contains(committed, "unwanted.txt") {
		t.Fatalf("committed files = %q, want only wanted.txt", committed)
	}
}

// API-mode delegated commit (commit: api) — RunGitCommit routes to
// runAPICommit whenever the resolved credential's CommitMode is
// CommitModeAPI, sending the GitHub GraphQL createCommitOnBranch mutation
// instead of running local git.

// TestRunGitCommitAPIModeRequiresGitHubApp is the deny-closed rule for api
// mode: GitHub itself is the identity for an api-mode commit (it signs and
// attributes the commit server-side), but the connection authenticating the
// mutation must actually be a GitHub App — api mode against a PAT, SSH key,
// or ambient credential is refused before any git command runs.
func TestRunGitCommitAPIModeRequiresGitHubApp(t *testing.T) {
	originalRun := runGitCommitCommandFn
	commands := 0
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		commands++
		return "", nil
	}
	defer func() { runGitCommitCommandFn = originalRun }()

	_, err := RunGitCommit(context.Background(), GitCommitExecution{
		Workspace: "/tmp/workspace",
		Message:   "chore: tidy",
		Credential: ResolvedCredential{
			CommitMode: CommitModeAPI,
			Method:     CredentialPAT,
			TokenValue: "not-an-app",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "requires a GitHub App connection") {
		t.Fatalf("error = %v, want a requires-GitHub-App refusal", err)
	}
	if commands != 0 {
		t.Fatalf("%d git command(s) ran for an api-mode commit without a GitHub App, want 0", commands)
	}
}

// newAPICommitWorkspace builds a real repository on branch main with one
// tracked file and a remote origin URL (never dialed — api mode never pushes
// or fetches over it, only parses owner/repo from it), then makes local
// changes for runAPICommit to enumerate: a modified existing file, a new
// file, and a deleted file, exercising additions and deletions together.
func newAPICommitWorkspace(t *testing.T) (workspace, headBeforeChanges string) {
	t.Helper()
	ctx := context.Background()
	workspace = t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ambient@example.com"},
		{"config", "user.name", "Ambient Tester"},
		{"checkout", "-b", "main"},
		{"remote", "add", "origin", "https://github.com/opentendril/opentendril.git"},
	} {
		if _, err := runGitCommand(ctx, workspace, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "keep.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "remove.txt"), []byte("bye\n"), 0o644); err != nil {
		t.Fatalf("write remove.txt: %v", err)
	}
	if _, err := runGitCommand(ctx, workspace, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(ctx, workspace, "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	head, err := runGitCommand(ctx, workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	headBeforeChanges = strings.TrimSpace(head)

	if err := os.WriteFile(filepath.Join(workspace, "keep.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("modify keep.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "grown.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write grown.txt: %v", err)
	}
	if err := os.Remove(filepath.Join(workspace, "remove.txt")); err != nil {
		t.Fatalf("remove remove.txt: %v", err)
	}
	return workspace, headBeforeChanges
}

// fakeGitHubAppAndGraphQLServer stands up an httptest server answering both
// the GitHub App installation-token endpoints (githubAPIBaseURL) and the
// GraphQL endpoint (githubGraphQLURL), and points both package vars at it —
// no live GitHub App is required. It reports each endpoint's call count and
// captures the raw GraphQL request body for the caller to assert against.
func fakeGitHubAppAndGraphQLServer(t *testing.T, oid string) (installCalls, tokenCalls, graphqlCalls *int, graphQLBody *[]byte) {
	t.Helper()
	installCalls, tokenCalls, graphqlCalls = new(int), new(int), new(int)
	graphQLBody = new([]byte)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/graphql":
			*graphqlCalls++
			body, _ := io.ReadAll(r.Body)
			*graphQLBody = body
			if got := r.Header.Get("Authorization"); got != "Bearer ghs_installation_token" {
				t.Errorf("graphql request Authorization = %q, want the installation token bearer", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"createCommitOnBranch": map[string]any{
						"commit": map[string]any{"oid": oid},
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/installation"):
			*installCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 99001})
		case strings.Contains(r.URL.Path, "/access_tokens"):
			*tokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_installation_token",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	origBase, origGraphQL := githubAPIBaseURL, githubGraphQLURL
	githubAPIBaseURL = srv.URL
	githubGraphQLURL = srv.URL + "/graphql"
	t.Cleanup(func() { githubAPIBaseURL = origBase; githubGraphQLURL = origGraphQL })

	appTokenMu.Lock()
	appTokenCache = map[string]cachedAppToken{}
	appTokenMu.Unlock()

	return installCalls, tokenCalls, graphqlCalls, graphQLBody
}

// TestRunAPICommitSendsGraphQLMutation is the full success path: a real
// workspace with staged-equivalent local changes (never actually staged —
// api mode never touches the local index), a canned GraphQL response
// standing in for GitHub, and assertions that the request GitHub would have
// received carries every field the mutation requires.
func TestRunAPICommitSendsGraphQLMutation(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	workspace, headBeforeChanges := newAPICommitWorkspace(t)
	installCalls, tokenCalls, graphqlCalls, graphQLBody := fakeGitHubAppAndGraphQLServer(t, "abc123def")

	result, err := RunGitCommit(context.Background(), GitCommitExecution{
		Workspace: workspace,
		Message:   "feat: grow a new leaf\n\nDetailed body line.",
		Credential: ResolvedCredential{
			Method:     CredentialApp,
			CommitMode: CommitModeAPI,
			App:        AppCredential{AppID: "4276558", PrivateKeyPath: keyPath},
			// No Identity is configured: api mode does not require one — the
			// GitHub App is the identity, set server-side by GitHub.
		},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Status != "committed" || result.CommitHash != "abc123def" {
		t.Fatalf("result = %+v, want committed abc123def", result)
	}
	if *installCalls != 1 || *tokenCalls != 1 || *graphqlCalls != 1 {
		t.Fatalf("calls = install:%d token:%d graphql:%d, want 1/1/1", *installCalls, *tokenCalls, *graphqlCalls)
	}

	body := string(*graphQLBody)
	for _, want := range []string{
		"createCommitOnBranch",
		`"expectedHeadOid":"` + headBeforeChanges + `"`,
		`"branchName":"main"`,
		`"repositoryNameWithOwner":"opentendril/opentendril"`,
		`"headline":"feat: grow a new leaf"`,
		`"body":"Detailed body line."`,
		`"path":"keep.txt"`,
		`"contents":"` + base64.StdEncoding.EncodeToString([]byte("changed\n")) + `"`,
		`"path":"grown.txt"`,
		`"contents":"` + base64.StdEncoding.EncodeToString([]byte("new\n")) + `"`,
		`"deletions":[{"path":"remove.txt"}]`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("graphql request body missing %q; body=%s", want, body)
		}
	}

	// Api mode never touches the local workspace: no staging, no local
	// commit. The remote branch (simulated by the fake server) is what
	// advances — the local HEAD must be exactly what it was before.
	head, err := runGitCommand(context.Background(), workspace, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	if strings.TrimSpace(head) != headBeforeChanges {
		t.Fatalf("local HEAD moved to %q, want unchanged %q — api mode must never create a local commit", strings.TrimSpace(head), headBeforeChanges)
	}
}

// TestRunAPICommitRespectsPathsFilter proves the optional Paths filter bounds
// the enumerated changes in api mode exactly as it bounds staging in local
// mode: only the named file reaches the mutation.
func TestRunAPICommitRespectsPathsFilter(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	workspace, _ := newAPICommitWorkspace(t)
	_, _, _, graphQLBody := fakeGitHubAppAndGraphQLServer(t, "abc123def")

	_, err := RunGitCommit(context.Background(), GitCommitExecution{
		Workspace: workspace,
		Message:   "feat: only the wanted file",
		Paths:     []string{"grown.txt"},
		Credential: ResolvedCredential{
			Method:     CredentialApp,
			CommitMode: CommitModeAPI,
			App:        AppCredential{AppID: "4276558", PrivateKeyPath: keyPath},
		},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	body := string(*graphQLBody)
	if !strings.Contains(body, `"path":"grown.txt"`) {
		t.Fatalf("graphql request body missing the requested path; body=%s", body)
	}
	if strings.Contains(body, "keep.txt") || strings.Contains(body, "remove.txt") {
		t.Fatalf("graphql request body includes paths outside the filter; body=%s", body)
	}
}

// TestRunAPICommitNothingToCommit proves a clean workspace reports
// nothing-to-commit without ever dialing GitHub — no App token minted, no
// GraphQL request sent — mirroring the local path's empty-commit avoidance.
func TestRunAPICommitNothingToCommit(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	workspace, _ := newAPICommitWorkspace(t)
	// Undo the uncommitted local edits newAPICommitWorkspace leaves behind,
	// so the workspace is clean relative to its own HEAD.
	ctx := context.Background()
	if _, err := runGitCommand(ctx, workspace, "checkout", "--", "keep.txt"); err != nil {
		t.Fatalf("git checkout: %v", err)
	}
	if _, err := runGitCommand(ctx, workspace, "checkout", "--", "remove.txt"); err != nil {
		t.Fatalf("git checkout: %v", err)
	}
	if err := os.Remove(filepath.Join(workspace, "grown.txt")); err != nil {
		t.Fatalf("remove untracked grown.txt: %v", err)
	}

	installCalls, tokenCalls, graphqlCalls, _ := fakeGitHubAppAndGraphQLServer(t, "unused")

	result, err := RunGitCommit(ctx, GitCommitExecution{
		Workspace: workspace,
		Message:   "chore: nothing here",
		Credential: ResolvedCredential{
			Method:     CredentialApp,
			CommitMode: CommitModeAPI,
			App:        AppCredential{AppID: "4276558", PrivateKeyPath: keyPath},
		},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if result.Status != "nothing-to-commit" || result.CommitHash != "" {
		t.Fatalf("result = %+v, want a hash-less nothing-to-commit status", result)
	}
	if *installCalls != 0 || *tokenCalls != 0 || *graphqlCalls != 0 {
		t.Fatalf("calls = install:%d token:%d graphql:%d, want 0/0/0 (github must never be dialed for an empty change set)", *installCalls, *tokenCalls, *graphqlCalls)
	}
}

// TestSplitCommitMessage covers the headline/body split runAPICommit sends
// as the GraphQL mutation's CommitMessage input.
func TestSplitCommitMessage(t *testing.T) {
	cases := []struct {
		message        string
		headline, body string
	}{
		{"feat: add thing", "feat: add thing", ""},
		{"feat: add thing\n\nWith details.", "feat: add thing", "With details."},
		{"  feat: trimmed  \n  body line  ", "feat: trimmed", "body line"},
		{"feat: multi\n\nLine one.\nLine two.", "feat: multi", "Line one.\nLine two."},
	}
	for _, c := range cases {
		headline, body := splitCommitMessage(c.message)
		if headline != c.headline || body != c.body {
			t.Fatalf("splitCommitMessage(%q) = (%q, %q), want (%q, %q)", c.message, headline, body, c.headline, c.body)
		}
	}
}

package conductor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeForge stands in for GitHub's commit-to-pull-request lookup, keyed by
// commit hash, so every branch classification can be driven precisely.
type fakeForge struct {
	// byCommit maps a commit hash to the pull request payload the interface
	// would return. A hash absent from the map is reported as unknown (HTTP
	// 422), which is what the interface does for a commit it has never seen.
	byCommit map[string][]map[string]any
	// byHead maps a branch NAME to the pull requests ever opened from it —
	// the head-ref query, which reveals closed-unmerged pull requests that the
	// commit lookup does not associate.
	byHead  map[string][]map[string]any
	lookups int
}

func newFakeForge(t *testing.T, forge *fakeForge) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 99001})
		case strings.Contains(r.URL.Path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_installation_token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		case strings.HasSuffix(r.URL.Path, "/pulls") && r.URL.Query().Get("head") != "":
			head := r.URL.Query().Get("head")
			if index := strings.Index(head, ":"); index >= 0 {
				head = head[index+1:]
			}
			pulls := forge.byHead[head]
			if pulls == nil {
				pulls = []map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(pulls)
		case strings.Contains(r.URL.Path, "/commits/") && strings.HasSuffix(r.URL.Path, "/pulls"):
			forge.lookups++
			parts := strings.Split(r.URL.Path, "/")
			sha := parts[len(parts)-2]
			pulls, known := forge.byCommit[sha]
			if !known {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "No commit found for SHA: " + sha})
				return
			}
			_ = json.NewEncoder(w).Encode(pulls)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	orig := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	t.Cleanup(func() { githubAPIBaseURL = orig })
	appTokenMu.Lock()
	appTokenCache = map[string]cachedAppToken{}
	appTokenMu.Unlock()
}

// branchAt creates a branch with one distinct commit and returns its hash.
func branchAt(t *testing.T, repo, branch, content string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := runGitCommand(ctx, repo, "checkout", "-b", branch); err != nil {
		t.Fatalf("checkout -b %s: %v", branch, err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "--allow-empty", "-m", content); err != nil {
		t.Fatalf("commit on %s: %v", branch, err)
	}
	head, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(head)
}

// newLifecycleRepo builds a repository with one branch per classification the
// manual cleanup actually encountered.
func newLifecycleRepo(t *testing.T) (repo string, forge *fakeForge) {
	t.Helper()
	repo = newBranchRepo(t, "trunk", "trunk")
	forge = &fakeForge{byCommit: map[string][]map[string]any{}, byHead: map[string][]map[string]any{}}

	mergedSHA := branchAt(t, repo, "feat/merged", "merged work")
	forge.byCommit[mergedSHA] = []map[string]any{{"number": 101, "state": "closed", "merged_at": "2026-07-20T10:00:00Z"}}

	openSHA := branchAt(t, repo, "feat/open", "open work")
	forge.byCommit[openSHA] = []map[string]any{{"number": 102, "state": "open", "merged_at": nil}}

	closedSHA := branchAt(t, repo, "feat/closed-unmerged", "rejected work")
	forge.byCommit[closedSHA] = []map[string]any{{"number": 103, "state": "closed", "merged_at": nil}}

	knownNoPR := branchAt(t, repo, "feat/no-pull-request", "pushed but no pull request")
	forge.byCommit[knownNoPR] = []map[string]any{}

	// Deliberately NOT registered with the forge: never pushed.
	branchAt(t, repo, "feat/never-pushed", "local only")

	if _, err := runGitCommand(context.Background(), repo, "checkout", "trunk"); err != nil {
		t.Fatalf("checkout trunk: %v", err)
	}
	newFakeForge(t, forge)
	return repo, forge
}

func lifecycleCredential(t *testing.T) ResolvedCredential {
	t.Helper()
	_, keyPath := genTestKeyPEM(t)
	return ResolvedCredential{Method: CredentialApp, App: AppCredential{AppID: "4276558", PrivateKeyPath: keyPath}}
}

func classificationOf(result GitBranchListResult, name string) GitBranchInfo {
	for _, branch := range result.Branches {
		if branch.Name == name {
			return branch
		}
	}
	return GitBranchInfo{}
}

// TestBranchListClassifiesEveryRealCategory pins the classification against
// the categories the manual cleanup actually produced — including the two that
// a naive prune destroys.
func TestBranchListClassifiesEveryRealCategory(t *testing.T) {
	repo, _ := newLifecycleRepo(t)

	result, err := RunGitBranchList(context.Background(), GitBranchListExecution{
		Workspace: repo, ConfiguredBranch: "trunk", Credential: lifecycleCredential(t),
	})
	if err != nil {
		t.Fatalf("branch list: %v", err)
	}
	if !result.Verified {
		t.Fatal("merge state was not verified despite a working credential")
	}

	for name, want := range map[string]string{
		"feat/merged":          BranchMerged,
		"feat/open":            BranchPullRequestOpen,
		"feat/closed-unmerged": BranchPullRequestClosed,
		"feat/no-pull-request": BranchNoPullRequest,
		"feat/never-pushed":    BranchUnpushed,
		"trunk":                BranchCurrent,
	} {
		got := classificationOf(result, name)
		if got.Classification != want {
			t.Errorf("%s classified as %q, want %q (reason: %s)", name, got.Classification, want, got.Reason)
		}
	}

	// Exactly one branch may be deleted, and every classification carries a
	// reason a Pollinator can act on.
	deletable := []string{}
	for _, branch := range result.Branches {
		if branch.Deletable {
			deletable = append(deletable, branch.Name)
		}
		if strings.TrimSpace(branch.Reason) == "" {
			t.Errorf("%s has no reason for its classification", branch.Name)
		}
	}
	if len(deletable) != 1 || deletable[0] != "feat/merged" {
		t.Fatalf("deletable = %v, want only feat/merged", deletable)
	}
}

// TestBranchListDetectsSquashMergeThatGitCannot is the crux: git's own
// --merged check misses this branch entirely, and the forge evidence catches
// it. If this ever regresses, prune silently stops working.
func TestBranchListDetectsSquashMergeThatGitCannot(t *testing.T) {
	repo, _ := newLifecycleRepo(t)

	// git itself does NOT consider the squash-merged branch merged into trunk.
	merged, err := runGitCommand(context.Background(), repo, "branch", "--merged", "trunk")
	if err != nil {
		t.Fatalf("git branch --merged: %v", err)
	}
	if strings.Contains(merged, "feat/merged") {
		t.Fatal("fixture is wrong: git already considers the branch merged, so it does not exercise the squash case")
	}

	result, err := RunGitBranchList(context.Background(), GitBranchListExecution{
		Workspace: repo, ConfiguredBranch: "trunk", Credential: lifecycleCredential(t),
	})
	if err != nil {
		t.Fatalf("branch list: %v", err)
	}
	if got := classificationOf(result, "feat/merged"); !got.Deletable {
		t.Fatalf("the squash-merged branch was not detected as merged (%q) — this is exactly what git branch --merged gets wrong", got.Classification)
	}
}

// TestBranchListWithoutCredentialVerifiesNothing: deny-closed. No evidence
// means nothing is deletable, however it looks locally.
func TestBranchListWithoutCredentialVerifiesNothing(t *testing.T) {
	repo, _ := newLifecycleRepo(t)

	result, err := RunGitBranchList(context.Background(), GitBranchListExecution{
		Workspace: repo, ConfiguredBranch: "trunk", Credential: ResolvedCredential{Method: CredentialSSH},
	})
	if err != nil {
		t.Fatalf("branch list: %v", err)
	}
	if result.Verified {
		t.Fatal("merge state reported as verified with no interface credential")
	}
	for _, branch := range result.Branches {
		if branch.Deletable {
			t.Fatalf("%s was deletable without any merge evidence", branch.Name)
		}
	}
}

// TestPruneReportsByDefaultAndDeletesNothing: the safe path must be the one
// taken by accident.
func TestPruneReportsByDefaultAndDeletesNothing(t *testing.T) {
	repo, _ := newLifecycleRepo(t)

	result, err := RunGitPrune(context.Background(), GitPruneExecution{
		Workspace: repo, ConfiguredBranch: "trunk", Credential: lifecycleCredential(t),
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if result.Confirmed {
		t.Fatal("an unconfirmed prune reported itself as confirmed")
	}
	if len(result.Deleted) != 1 || result.Deleted[0].Name != "feat/merged" {
		t.Fatalf("would-delete = %+v, want only feat/merged", result.Deleted)
	}
	if result.Deleted[0].Head == "" {
		t.Fatal("no restore information recorded for the branch that would be deleted")
	}

	// Nothing may actually have been removed.
	branches, err := runGitCommand(context.Background(), repo, "branch", "--list")
	if err != nil {
		t.Fatalf("branch --list: %v", err)
	}
	if !strings.Contains(branches, "feat/merged") {
		t.Fatal("an unconfirmed prune deleted a branch")
	}
}

// TestPruneDeletesOnlyMergedWhenConfirmed is the destructive path, and the
// heart of this slice: everything that is not proven merged survives.
func TestPruneDeletesOnlyMergedWhenConfirmed(t *testing.T) {
	repo, _ := newLifecycleRepo(t)

	result, err := RunGitPrune(context.Background(), GitPruneExecution{
		Workspace: repo, ConfiguredBranch: "trunk", Credential: lifecycleCredential(t), Confirm: true,
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if !result.Confirmed || len(result.Deleted) != 1 || result.Deleted[0].Name != "feat/merged" {
		t.Fatalf("result = %+v, want exactly feat/merged deleted", result)
	}

	branches, err := runGitCommand(context.Background(), repo, "branch", "--list")
	if err != nil {
		t.Fatalf("branch --list: %v", err)
	}
	if strings.Contains(branches, "feat/merged") {
		t.Fatal("the merged branch was not deleted despite confirmation")
	}
	// Every category that could hold irrecoverable work must survive.
	for _, survivor := range []string{"feat/open", "feat/closed-unmerged", "feat/never-pushed", "feat/no-pull-request", "trunk"} {
		if !strings.Contains(branches, survivor) {
			t.Fatalf("prune destroyed %s:\n%s", survivor, branches)
		}
	}
}

// TestPruneNeverDeletesWithoutEvidence is the deny-closed guarantee stated as
// a single property: with no way to verify anything, prune deletes nothing
// even when explicitly confirmed.
func TestPruneNeverDeletesWithoutEvidence(t *testing.T) {
	repo, _ := newLifecycleRepo(t)

	result, err := RunGitPrune(context.Background(), GitPruneExecution{
		Workspace: repo, ConfiguredBranch: "trunk", Credential: ResolvedCredential{}, Confirm: true,
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(result.Deleted) != 0 {
		t.Fatalf("deleted %+v with no merge evidence — confirmation must never substitute for evidence", result.Deleted)
	}

	branches, err := runGitCommand(context.Background(), repo, "branch", "--list")
	if err != nil {
		t.Fatalf("branch --list: %v", err)
	}
	for _, survivor := range []string{"feat/merged", "feat/open", "feat/closed-unmerged", "feat/never-pushed"} {
		if !strings.Contains(branches, survivor) {
			t.Fatalf("prune destroyed %s without evidence:\n%s", survivor, branches)
		}
	}
}

// TestBranchListCachesLookupsPerCommit: several branches on one tip must not
// cost several interface calls.
func TestBranchListCachesLookupsPerCommit(t *testing.T) {
	repo, forge := newLifecycleRepo(t)
	head, err := runGitCommand(context.Background(), repo, "rev-parse", "feat/merged")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repo, "branch", "feat/merged-alias", strings.TrimSpace(head)); err != nil {
		t.Fatalf("branch alias: %v", err)
	}

	before := forge.lookups
	if _, err := RunGitBranchList(context.Background(), GitBranchListExecution{
		Workspace: repo, ConfiguredBranch: "trunk", Credential: lifecycleCredential(t),
	}); err != nil {
		t.Fatalf("branch list: %v", err)
	}
	// Five distinct tips exist; the alias shares one, so it must not add a
	// sixth lookup.
	if got := forge.lookups - before; got > 5 {
		t.Fatalf("%d interface lookups for 5 distinct tips — the per-commit cache is not working", got)
	}
}

// TestBranchListFindsClosedPullRequestTheCommitLookupMisses covers the real
// gap found live: GitHub's commit-to-pull-request endpoint does not associate a
// pull request that was CLOSED WITHOUT MERGING, so such a branch comes back
// looking as though it never had one. The head-ref fallback recovers it.
func TestBranchListFindsClosedPullRequestTheCommitLookupMisses(t *testing.T) {
	repo := newBranchRepo(t, "trunk", "trunk")
	forge := &fakeForge{byCommit: map[string][]map[string]any{}, byHead: map[string][]map[string]any{}}

	sha := branchAt(t, repo, "Pollinator/rejected-work", "work that was turned down")
	// The commit lookup knows the commit but reports no pull request…
	forge.byCommit[sha] = []map[string]any{}
	// …while the head query reveals it was closed without merging.
	forge.byHead["Pollinator/rejected-work"] = []map[string]any{{"number": 333, "state": "closed", "merged_at": nil}}

	if _, err := runGitCommand(context.Background(), repo, "checkout", "trunk"); err != nil {
		t.Fatalf("checkout trunk: %v", err)
	}
	newFakeForge(t, forge)

	result, err := RunGitBranchList(context.Background(), GitBranchListExecution{
		Workspace: repo, ConfiguredBranch: "trunk", Credential: lifecycleCredential(t),
	})
	if err != nil {
		t.Fatalf("branch list: %v", err)
	}
	branch := classificationOf(result, "Pollinator/rejected-work")
	if branch.Classification != BranchPullRequestClosed {
		t.Fatalf("classified as %q, want %q — a closed-unmerged pull request must not look like no pull request at all", branch.Classification, BranchPullRequestClosed)
	}
	if branch.PullRequest != 333 || branch.Deletable {
		t.Fatalf("branch = %+v, want pull request 333 reported and the branch kept", branch)
	}
}

// TestHeadFallbackNeverGrantsDeletability is the safety property of that
// fallback. A commit hash is unique forever; a branch NAME can be reused. If
// an old merged pull request used this name, the fallback must not mark a
// branch carrying different, unpushed work as deletable.
func TestHeadFallbackNeverGrantsDeletability(t *testing.T) {
	repo := newBranchRepo(t, "trunk", "trunk")
	forge := &fakeForge{byCommit: map[string][]map[string]any{}, byHead: map[string][]map[string]any{}}

	sha := branchAt(t, repo, "feat/reused-name", "brand new work under an old name")
	forge.byCommit[sha] = []map[string]any{}
	// An OLD pull request with this branch name merged long ago.
	forge.byHead["feat/reused-name"] = []map[string]any{{"number": 42, "state": "closed", "merged_at": "2026-01-01T00:00:00Z"}}

	if _, err := runGitCommand(context.Background(), repo, "checkout", "trunk"); err != nil {
		t.Fatalf("checkout trunk: %v", err)
	}
	newFakeForge(t, forge)

	result, err := RunGitBranchList(context.Background(), GitBranchListExecution{
		Workspace: repo, ConfiguredBranch: "trunk", Credential: lifecycleCredential(t),
	})
	if err != nil {
		t.Fatalf("branch list: %v", err)
	}
	branch := classificationOf(result, "feat/reused-name")
	if branch.Deletable || branch.Classification == BranchMerged {
		t.Fatalf("branch = %+v — a merged pull request found by NAME must never make the current tip deletable; the name was reused and this work would be destroyed", branch)
	}
}

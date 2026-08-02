package conductor

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// gitIn runs git in dir and fails the test on error, so setup problems surface
// as setup problems rather than as a confusing assertion failure later.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repoWithSiblingBranch builds a repository whose checkout has been left on a
// sibling branch carrying a commit the default branch does not have.
//
// This is the shape that produced the failure: a workspace cut from HEAD here
// inherits that commit and arrives carrying somebody else's change.
func repoWithSiblingBranch(t *testing.T, defaultBranch string) (dir, defaultCommit, siblingCommit string) {
	t.Helper()
	dir = t.TempDir()

	gitIn(t, dir, "init", "-q", "-b", defaultBranch)
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	defaultCommit = gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "-q", "-b", "sibling-in-flight")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "another change in flight")
	siblingCommit = gitIn(t, dir, "rev-parse", "HEAD")

	// Left standing on the sibling, exactly as a checkout is between runs.
	return dir, defaultCommit, siblingCommit
}

// stubFetch replaces the refresh so a test repository with no real remote does
// not spend time on a fetch that must fail.
func stubFetch(t *testing.T) {
	t.Helper()
	original := runGitFetchCommandFn
	runGitFetchCommandFn = func(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
		return "", nil
	}
	t.Cleanup(func() { runGitFetchCommandFn = original })
}

// The property: a workspace starts from the default branch even when the
// checkout is sitting on somebody else's in-flight branch.
//
// The remote-tracking refs are what make this distinct from the undetermined
// case below. Without them the default branch does not resolve, and this test
// would silently be a second copy of that one — which is what it was until the
// mutation run showed both failing to the same change.
func TestStartPointPrefersDefaultBranchOverASiblingCheckout(t *testing.T) {
	dir, defaultCommit, siblingCommit := repoWithSiblingBranch(t, "main")
	stubFetch(t)

	gitIn(t, dir, "update-ref", "refs/remotes/origin/main", defaultCommit)
	gitIn(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	if resolution := ResolveDefaultBranchLocal(context.Background(), dir, ""); !resolution.Known() {
		t.Fatal("precondition failed: the default branch did not resolve, so this test would duplicate the undetermined case")
	}

	got, err := workspaceStartPoint(context.Background(), dir)
	if err != nil {
		t.Fatalf("workspaceStartPoint: %v", err)
	}
	if got == siblingCommit {
		t.Fatalf("start point is the sibling in-flight commit — the new branch would inherit its change")
	}
	if got != defaultCommit {
		t.Errorf("start point = %s, want the default branch commit %s", got, defaultCommit)
	}
}

// The gap this change closes. With no origin/HEAD record the default branch is
// undetermined, and the floor is what stops the answer becoming HEAD — which
// here is the sibling branch.
func TestStartPointUsesTheFloorWhenTheDefaultBranchIsUndetermined(t *testing.T) {
	dir, defaultCommit, siblingCommit := repoWithSiblingBranch(t, "main")
	stubFetch(t)

	// No remote at all, so nothing records which branch is the default.
	if resolution := ResolveDefaultBranchLocal(context.Background(), dir, ""); resolution.Known() {
		t.Fatalf("precondition failed: default branch resolved to %q, so this test is not exercising the undetermined path", resolution.Branch)
	}

	got, err := workspaceStartPoint(context.Background(), dir)
	if err != nil {
		t.Fatalf("workspaceStartPoint: %v", err)
	}
	if got == siblingCommit {
		t.Fatalf("undetermined default branch fell through to HEAD on the sibling branch")
	}
	if got != defaultCommit {
		t.Errorf("start point = %s, want the floor branch commit %s", got, defaultCommit)
	}
}

// A repository whose only branch is named outside the floor still works: HEAD is
// genuinely the only commit available, and refusing would break a legitimate
// single-branch substrate.
func TestStartPointStillResolvesASingleBranchRepositoryOutsideTheFloor(t *testing.T) {
	stubFetch(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "trunk")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "only")
	want := gitIn(t, dir, "rev-parse", "HEAD")

	got, err := workspaceStartPoint(context.Background(), dir)
	if err != nil {
		t.Fatalf("workspaceStartPoint: %v", err)
	}
	if got != want {
		t.Errorf("start point = %s, want %s", got, want)
	}
}

func TestStartPointFailsWhenThereAreNoCommits(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")

	if _, err := workspaceStartPoint(context.Background(), dir); err == nil {
		t.Fatal("expected an error for a repository with no commits")
	}
}

// The refresh is attempted for the resolved default branch, and carries the
// setting that stops it waiting on a terminal. Asserted through the seam rather
// than against a network.
func TestStartPointRefreshesTheRemoteDefaultBranch(t *testing.T) {
	type attempt struct {
		env  []string
		args []string
	}
	var attempts []attempt

	original := runGitFetchCommandFn
	runGitFetchCommandFn = func(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
		attempts = append(attempts, attempt{env: extraEnv, args: args})
		return "", nil
	}
	t.Cleanup(func() { runGitFetchCommandFn = original })

	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	// A remote-tracking ref makes the default branch resolvable, which is what
	// the refresh is conditional on.
	gitIn(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	if _, err := workspaceStartPoint(context.Background(), dir); err != nil {
		t.Fatalf("workspaceStartPoint: %v", err)
	}

	if len(attempts) != 1 {
		t.Fatalf("fetch attempted %d time(s), want exactly 1", len(attempts))
	}
	if strings.Join(attempts[0].args, " ") != "fetch origin main" {
		t.Errorf("fetched %v, want the resolved default branch", attempts[0].args)
	}
	if !containsString(attempts[0].env, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("fetch ran without GIT_TERMINAL_PROMPT=0 (env %v), so a credential prompt could hang workspace creation", attempts[0].env)
	}
}

// A fetch that fails must not stop a workspace being created: a slightly stale
// default branch merges cleanly, and refusing would trade that for an outage.
func TestStartPointSurvivesAFailedRefresh(t *testing.T) {
	original := runGitFetchCommandFn
	runGitFetchCommandFn = func(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
		return "", context.DeadlineExceeded
	}
	t.Cleanup(func() { runGitFetchCommandFn = original })

	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	want := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	got, err := workspaceStartPoint(context.Background(), dir)
	if err != nil {
		t.Fatalf("a failed refresh stopped workspace creation: %v", err)
	}
	if got != want {
		t.Errorf("start point = %s, want %s", got, want)
	}
}

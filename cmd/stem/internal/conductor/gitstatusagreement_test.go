package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The read-side must never lie about the write-side.
//
// git.status predicts whether a commit will be allowed; git.commit decides it.
// If those two ever disagree, the read-side becomes actively harmful: a subject
// that is told "fine" and then refused learns to distrust status and goes back
// to guessing, which is the behaviour the whole ladder exists to remove.
//
// They share one predicate today (AssessDefaultBranchCommit), but sharing is a
// choice a future change can quietly undo. This test makes that undoing fail:
// for a matrix of workspace states it runs BOTH paths against the same
// workspace and asserts the prediction matches the outcome. It is the
// enforcement artifact for this slice, in the same spirit as the guard scripts
// under scripts/ — a decision is finished when a check makes the wrong version
// fail.
func TestStatusPredictionMatchesCommitOutcome(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		// build returns a workspace with uncommitted changes present, plus the
		// substrate settings both paths must be given identically.
		build             func(t *testing.T) (workspace, configuredBranch string, allowDefaultBranchCommit bool)
		wantCommitAllowed bool
	}{
		{
			name: "feature branch is committable",
			build: func(t *testing.T) (string, string, bool) {
				return dirtyRepo(t, "feat/new-leaf", "trunk"), "", false
			},
			wantCommitAllowed: true,
		},
		{
			name: "on the resolved default branch",
			build: func(t *testing.T) (string, string, bool) {
				return dirtyRepo(t, "trunk", "trunk"), "", false
			},
			wantCommitAllowed: false,
		},
		{
			name: "on the default branch with the opt-out set",
			build: func(t *testing.T) (string, string, bool) {
				return dirtyRepo(t, "trunk", "trunk"), "", true
			},
			wantCommitAllowed: true,
		},
		{
			name: "protection floor: undetermined default, workspace on main",
			build: func(t *testing.T) (string, string, bool) {
				return dirtyRepo(t, "main", ""), "", false
			},
			wantCommitAllowed: false,
		},
		{
			name: "protection floor: undetermined default, workspace on master",
			build: func(t *testing.T) (string, string, bool) {
				return dirtyRepo(t, "master", ""), "", false
			},
			wantCommitAllowed: false,
		},
		{
			name: "undetermined default does not protect a feature branch",
			build: func(t *testing.T) (string, string, bool) {
				return dirtyRepo(t, "feat/x", ""), "", true
			},
			wantCommitAllowed: true,
		},
		{
			name: "configured branch outranks the local remote head",
			build: func(t *testing.T) (string, string, bool) {
				// The workspace is on release/2026 and the local record says
				// the default is trunk — but configuration names release/2026,
				// so THAT is the protected branch.
				return dirtyRepo(t, "release/2026", "trunk"), "release/2026", false
			},
			wantCommitAllowed: false,
		},
		{
			name: "detached head is refused by both paths",
			build: func(t *testing.T) (string, string, bool) {
				repo := dirtyRepo(t, "feat/x", "trunk")
				head, err := runGitCommand(context.Background(), repo, "rev-parse", "HEAD")
				if err != nil {
					t.Fatalf("rev-parse: %v", err)
				}
				if _, err := runGitCommand(context.Background(), repo, "checkout", strings.TrimSpace(head)); err != nil {
					t.Fatalf("detach: %v", err)
				}
				return repo, "", false
			},
			wantCommitAllowed: false,
		},
		{
			name: "configured branch clears a branch the local record protects",
			build: func(t *testing.T) (string, string, bool) {
				// Local record says trunk is default, but configuration says
				// the default is release/2026 — so trunk is committable.
				return dirtyRepo(t, "trunk", "trunk"), "release/2026", false
			},
			wantCommitAllowed: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			workspace, configuredBranch, allow := testCase.build(t)

			status, err := RunGitStatus(ctx, GitStatusExecution{
				Workspace:                workspace,
				ConfiguredBranch:         configuredBranch,
				AllowDefaultBranchCommit: allow,
			})
			if err != nil {
				t.Fatalf("status: %v", err)
			}

			_, commitErr := RunGitCommit(ctx, GitCommitExecution{
				Workspace:                workspace,
				Message:                  "chore: exercise the guard",
				Credential:               ResolvedCredential{Identity: ResolvedIdentity{Name: "Bot", Email: "bot@example.com"}},
				ConfiguredBranch:         configuredBranch,
				AllowDefaultBranchCommit: allow,
			})
			commitSucceeded := commitErr == nil

			if status.CommitAllowed != commitSucceeded {
				t.Fatalf("status predicted commitAllowed=%v but the commit %s — the read-side must never disagree with the write-side (commit error: %v)",
					status.CommitAllowed, map[bool]string{true: "succeeded", false: "was refused"}[commitSucceeded], commitErr)
			}
			if status.CommitAllowed != testCase.wantCommitAllowed {
				t.Fatalf("commitAllowed = %v, want %v (both paths agreed, but on the wrong answer)", status.CommitAllowed, testCase.wantCommitAllowed)
			}
			// A blocked prediction must explain itself: a subject acts on the
			// reason, not just the boolean.
			if !status.CommitAllowed && status.BlockedReason == "" {
				t.Fatal("a blocked commit was predicted with no reason given")
			}
		})
	}
}

// dirtyRepo builds a repository on the given branch with one uncommitted
// change, so a commit attempt exercises the guard rather than reporting
// nothing to commit.
func dirtyRepo(t *testing.T, branch, remoteDefault string) string {
	t.Helper()
	repo := newBranchRepo(t, branch, remoteDefault)
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return repo
}

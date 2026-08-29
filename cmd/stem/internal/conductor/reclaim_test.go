package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ownedBranch creates a branch in a repository and registers it as owned,
// returning the base it was cut from.
func ownedBranch(t *testing.T, repo, branch string, purpose OwnedRefPurpose) string {
	t.Helper()
	ctx := context.Background()
	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	base = strings.TrimSpace(base)
	if _, err := runGitCommand(ctx, repo, "branch", branch); err != nil {
		t.Fatalf("branch %s: %v", branch, err)
	}
	if err := RegisterOwnedRef(OwnedRef{Repository: repo, Branch: branch, Purpose: purpose, Base: base}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return base
}

func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	_, err := runGitCommand(context.Background(), repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// TestReclaimRemovesEmptyOwnedBranch: a reference created in case work
// happened, where no work happened, is pure litter and goes without any
// network call.
func TestReclaimRemovesEmptyOwnedBranch(t *testing.T) {
	repo := newBranchRepo(t, "trunk", "trunk")
	ownedBranch(t, repo, "tendril/claude/work", PurposeDelegatedWorkspace)

	outcomes := ReclaimOwnedRefs(context.Background(), repo, ResolvedCredential{})
	if len(outcomes) != 1 || !outcomes[0].Reclaimed {
		t.Fatalf("outcomes = %+v, want the empty branch reclaimed", outcomes)
	}
	if branchExists(t, repo, "tendril/claude/work") {
		t.Fatal("the empty owned branch survived reclamation")
	}
	if refs := OwnedRefsFor(repo); len(refs) != 0 {
		t.Fatalf("registry still holds %+v after reclamation", refs)
	}
}

// TestReclaimKeepsUnpublishedWork is the property that matters most: automatic
// reclamation must never destroy commits. This is the exact category of branch
// the system used to abandon, and deleting them automatically would be a far
// worse failure than leaving them.
func TestReclaimKeepsUnpublishedWork(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "trunk", "trunk")
	ownedBranch(t, repo, "tendril/claude/work", PurposeDelegatedWorkspace)

	// Put a commit on the owned branch.
	if _, err := runGitCommand(ctx, repo, "checkout", "tendril/claude/work"); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("real work\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "unpublished work"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if _, err := runGitCommand(ctx, repo, "checkout", "trunk"); err != nil {
		t.Fatalf("checkout trunk: %v", err)
	}

	outcomes := ReclaimOwnedRefs(ctx, repo, ResolvedCredential{})
	if len(outcomes) != 1 || outcomes[0].Reclaimed {
		t.Fatalf("outcomes = %+v, want the branch kept", outcomes)
	}
	if !strings.Contains(outcomes[0].Reason, "commits") {
		t.Fatalf("reason = %q, want it to explain that commits are present", outcomes[0].Reason)
	}
	if !branchExists(t, repo, "tendril/claude/work") {
		t.Fatal("automatic reclamation destroyed unpublished work")
	}
}

// TestReclaimKeepsCheckedOutBranches: a branch someone is standing on belongs
// to work in progress, whether that is here or in another Pollinator's workspace.
func TestReclaimKeepsCheckedOutBranches(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "trunk", "trunk")
	ownedBranch(t, repo, "tendril/claude/work", PurposeDelegatedWorkspace)
	if _, err := runGitCommand(ctx, repo, "checkout", "tendril/claude/work"); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	outcomes := ReclaimOwnedRefs(ctx, repo, ResolvedCredential{})
	if len(outcomes) != 1 || outcomes[0].Reclaimed {
		t.Fatalf("outcomes = %+v, want the checked-out branch kept", outcomes)
	}
	if !branchExists(t, repo, "tendril/claude/work") {
		t.Fatal("the checked-out branch was reclaimed")
	}
}

// TestReclaimForgetsVanishedReferences: the registry must not accumulate its
// own litter when a branch is removed by other means.
func TestReclaimForgetsVanishedReferences(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "trunk", "trunk")
	ownedBranch(t, repo, "tendril/claude/work", PurposeDelegatedWorkspace)
	if _, err := runGitCommand(ctx, repo, "branch", "-D", "tendril/claude/work"); err != nil {
		t.Fatalf("manual delete: %v", err)
	}

	ReclaimOwnedRefs(ctx, repo, ResolvedCredential{})
	if refs := OwnedRefsFor(repo); len(refs) != 0 {
		t.Fatalf("registry still holds a vanished reference: %+v", refs)
	}
}

// TestReclaimUnusedIsolationBranchReturnsHome covers the Sprout case end to
// end: an empty protective branch goes, and the workspace is left where the
// run found it.
func TestReclaimUnusedIsolationBranchReturnsHome(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "trunk", "trunk")
	ownedBranch(t, repo, "sprout/task-step-9", PurposeSproutIsolation)
	if _, err := runGitCommand(ctx, repo, "checkout", "sprout/task-step-9"); err != nil {
		t.Fatalf("checkout: %v", err)
	}

	if !ReclaimUnusedIsolationBranch(ctx, repo, "sprout/task-step-9", "trunk", ResolvedCredential{}) {
		t.Fatal("an empty isolation branch was not reclaimed")
	}
	current, err := runGitCommand(ctx, repo, "branch", "--show-current")
	if err != nil {
		t.Fatalf("show-current: %v", err)
	}
	if strings.TrimSpace(current) != "trunk" {
		t.Fatalf("left on %q, want the branch the run started on", strings.TrimSpace(current))
	}
	if branchExists(t, repo, "sprout/task-step-9") {
		t.Fatal("the empty isolation branch survived")
	}
}

// TestReclaimUnusedIsolationBranchKeepsProducedWork: when the run produced
// commits, that branch IS the output. Deleting a run's output to keep a
// repository tidy would be the worst trade available.
func TestReclaimUnusedIsolationBranchKeepsProducedWork(t *testing.T) {
	ctx := context.Background()
	repo := newBranchRepo(t, "trunk", "trunk")
	ownedBranch(t, repo, "sprout/task-step-9", PurposeSproutIsolation)
	if _, err := runGitCommand(ctx, repo, "checkout", "sprout/task-step-9"); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "produced.txt"), []byte("output\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "the run produced this"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	if ReclaimUnusedIsolationBranch(ctx, repo, "sprout/task-step-9", "trunk", ResolvedCredential{}) {
		t.Fatal("a branch carrying the run's output was reclaimed")
	}
	if !branchExists(t, repo, "sprout/task-step-9") {
		t.Fatal("the run's output was destroyed")
	}
	current, err := runGitCommand(ctx, repo, "branch", "--show-current")
	if err != nil {
		t.Fatalf("show-current: %v", err)
	}
	if strings.TrimSpace(current) != "sprout/task-step-9" {
		t.Fatalf("left on %q, want to stay on the branch holding the work", strings.TrimSpace(current))
	}
}

// TestOwnedRefRegistryRoundTrip covers the registry itself, including that
// re-registering the same reference updates rather than duplicating it.
func TestOwnedRefRegistryRoundTrip(t *testing.T) {
	repo := t.TempDir()
	if err := RegisterOwnedRef(OwnedRef{Repository: repo, Branch: "a", Purpose: PurposeDelegatedWorkspace, Pollen: "claude"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := RegisterOwnedRef(OwnedRef{Repository: repo, Branch: "a", Purpose: PurposeDelegatedWorkspace, Pollen: "claude", Base: "abc"}); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	refs := OwnedRefsFor(repo)
	if len(refs) != 1 {
		t.Fatalf("registry holds %d entries for one branch, want 1: %+v", len(refs), refs)
	}
	if refs[0].Base != "abc" || refs[0].CreatedAt.IsZero() {
		t.Fatalf("ref = %+v, want the update applied and a creation time recorded", refs[0])
	}

	if err := RegisterOwnedRef(OwnedRef{Repository: repo, Branch: ""}); err == nil {
		t.Fatal("a reference with no branch was accepted")
	}
	if err := ForgetOwnedRef(repo, "a"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if refs := OwnedRefsFor(repo); len(refs) != 0 {
		t.Fatalf("registry still holds %+v", refs)
	}
}

// integratedIsolationFixture creates an owned Sprout isolation branch whose
// checkpoint commit is also the exact tip of a Seed branch.
func integratedIsolationFixture(t *testing.T) (string, RunWorkspace, string, string, string) {
	t.Helper()
	ctx := context.Background()
	repo := newBranchRepo(t, "trunk", "trunk")

	base, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	base = strings.TrimSpace(base)

	branch := "sprout/task-step-seed-reclaim"
	if _, err := runGitCommand(ctx, repo, "checkout", "-b", branch); err != nil {
		t.Fatalf("create isolation branch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "checkpoint.txt"), []byte("checkpoint\n"), 0o644); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "seed checkpoint"}} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	checkpoint, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve checkpoint: %v", err)
	}
	checkpoint = strings.TrimSpace(checkpoint)

	if _, err := runGitCommand(ctx, repo, "checkout", "trunk"); err != nil {
		t.Fatalf("checkout trunk: %v", err)
	}

	seedBranch := "tendril/seed-reclaim"
	if _, err := runGitCommand(ctx, repo, "branch", seedBranch, checkpoint); err != nil {
		t.Fatalf("create Seed branch: %v", err)
	}

	runID := "run-seed-reclaim"
	if err := RegisterOwnedRef(OwnedRef{
		Repository: repo,
		Branch:     branch,
		Purpose:    PurposeSproutIsolation,
		Base:       base,
		RunID:      runID,
	}); err != nil {
		t.Fatalf("register isolation ownership: %v", err)
	}

	workspace := RunWorkspace{
		Repository: repo,
		Branch:     branch,
		BaseCommit: base,
		RunID:      runID,
	}
	return repo, workspace, seedBranch, checkpoint, base
}

func TestReclaimIntegratedIsolationBranchRemovesExactTransferredBranch(t *testing.T) {
	repo, workspace, seedBranch, checkpoint, _ := integratedIsolationFixture(t)

	outcome := ReclaimIntegratedIsolationBranch(context.Background(), workspace, seedBranch, checkpoint)
	if !outcome.Reclaimed {
		t.Fatalf("outcome = %+v, want successful reclamation", outcome)
	}
	if branchExists(t, repo, workspace.Branch) {
		t.Fatal("temporary isolation branch survived successful reclamation")
	}
	seedTip, err := runGitCommand(context.Background(), repo, "rev-parse", "refs/heads/"+seedBranch)
	if err != nil {
		t.Fatalf("resolve Seed branch: %v", err)
	}
	if strings.TrimSpace(seedTip) != checkpoint {
		t.Fatalf("Seed tip = %q, want checkpoint %q", strings.TrimSpace(seedTip), checkpoint)
	}
	if refs := OwnedRefsFor(repo); len(refs) != 0 {
		t.Fatalf("ownership registry still contains %+v after successful reclamation", refs)
	}
}

func TestReclaimIntegratedIsolationBranchKeepsBranchWhenSeedTipDiffers(t *testing.T) {
	repo, workspace, seedBranch, checkpoint, base := integratedIsolationFixture(t)

	if _, err := runGitCommand(context.Background(), repo, "branch", "-f", seedBranch, base); err != nil {
		t.Fatalf("move Seed branch: %v", err)
	}

	outcome := ReclaimIntegratedIsolationBranch(context.Background(), workspace, seedBranch, checkpoint)
	if outcome.Reclaimed {
		t.Fatalf("outcome = %+v, want reclamation refused", outcome)
	}
	if !strings.Contains(outcome.Reason, "Seed branch does not point to the checkpoint commit") {
		t.Fatalf("reason = %q, want Seed-tip mismatch", outcome.Reason)
	}
	if !branchExists(t, repo, workspace.Branch) {
		t.Fatal("temporary isolation branch was destroyed despite Seed-tip mismatch")
	}
	if refs := OwnedRefsFor(repo); len(refs) != 1 || refs[0].Branch != workspace.Branch {
		t.Fatalf("ownership registry = %+v, want temporary branch ownership preserved", refs)
	}
}

func TestReclaimIntegratedIsolationBranchConditionalDeleteRejectsMovedRef(t *testing.T) {
	repo, workspace, seedBranch, checkpoint, base := integratedIsolationFixture(t)

	originalRun := runGitCommitCommandFn
	moved := false
	runGitCommitCommandFn = func(ctx context.Context, dir string, args ...string) (string, error) {
		if !moved &&
			len(args) == 4 &&
			args[0] == "update-ref" &&
			args[1] == "-d" &&
			args[2] == "refs/heads/"+workspace.Branch &&
			args[3] == checkpoint {
			moved = true
			if _, err := originalRun(
				ctx,
				dir,
				"update-ref",
				"refs/heads/"+workspace.Branch,
				base,
				checkpoint,
			); err != nil {
				return "", err
			}
		}
		return originalRun(ctx, dir, args...)
	}
	defer func() { runGitCommitCommandFn = originalRun }()

	outcome := ReclaimIntegratedIsolationBranch(context.Background(), workspace, seedBranch, checkpoint)
	if !moved {
		t.Fatal("test did not move the temporary ref before conditional deletion")
	}
	if outcome.Reclaimed {
		t.Fatalf("outcome = %+v, want moved ref preserved", outcome)
	}
	if !strings.Contains(outcome.Reason, "reclamation failed") {
		t.Fatalf("reason = %q, want conditional deletion failure", outcome.Reason)
	}

	tip, err := runGitCommand(context.Background(), repo, "rev-parse", "refs/heads/"+workspace.Branch)
	if err != nil {
		t.Fatalf("temporary branch was deleted after ref movement: %v", err)
	}
	if strings.TrimSpace(tip) != base {
		t.Fatalf("temporary branch tip = %q, want moved tip %q", strings.TrimSpace(tip), base)
	}
	if refs := OwnedRefsFor(repo); len(refs) != 1 || refs[0].Branch != workspace.Branch {
		t.Fatalf("ownership registry = %+v, want moved branch ownership preserved", refs)
	}
}

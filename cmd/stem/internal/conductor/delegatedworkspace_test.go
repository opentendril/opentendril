package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newDelegatedWorkspaceLifecycleFixture(t *testing.T) (string, string, DelegatedWorkspace) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repository := t.TempDir()
	gitIn(t, repository, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "add", "base.txt")
	gitIn(t, repository, "commit", "-q", "-m", "base")
	gitIn(t, repository, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	workspace, err := ResolveDelegatedWorkspaceWithDefaultBranch(context.Background(), "demo", repository, "pollen", ResolvedCredential{}, "main")
	if err != nil {
		t.Fatalf("create delegated workspace: %v", err)
	}
	return repository, workspace.Path, workspace
}

func TestInspectDelegatedWorkspaceReportsIndependentOccupancyAndFruitState(t *testing.T) {
	repository, _, workspace := newDelegatedWorkspaceLifecycleFixture(t)
	owned, ok := delegatedOwnedRef(repository, workspace.Branch, "pollen")
	if !ok {
		t.Fatal("workspace branch was not recorded as owned")
	}
	if err := RegisterOwnedRef(OwnedRef{
		Repository: repository, Branch: workspace.Branch, Purpose: PurposeDelegatedWorkspace,
		Pollen: "pollen", Base: owned.Base,
	}); err != nil {
		t.Fatal(err)
	}
	report, err := InspectDelegatedWorkspace(context.Background(), DelegatedWorkspaceTarget{
		Pollen: "pollen", Substrate: "demo", Repository: repository, ConfiguredBranch: "main",
	})
	if err != nil {
		t.Fatalf("inspect workspace: %v", err)
	}
	if !report.WorkspaceVerified || !report.CleanKnown || !report.Clean || report.CurrentBranch != workspace.Branch {
		t.Fatalf("workspace state = %+v, want verified clean current checkout", report)
	}
	if report.FruitState != FruitStateUnverified {
		t.Fatalf("Fruit state = %q, want unverified without forge credentials", report.FruitState)
	}
	if !report.UniqueWorkKnown || report.UniqueWork || !report.AutoReclaimable {
		t.Fatalf("safe empty workspace state = %+v, want known-empty and reclaimable despite unavailable PR evidence", report)
	}
}

func TestOpenFruitPreventsAutomaticReclamationEvenForEmptyOwnedBranch(t *testing.T) {
	repository, _, workspace := newDelegatedWorkspaceLifecycleFixture(t)
	if _, ok := delegatedOwnedRef(repository, workspace.Branch, "pollen"); !ok {
		t.Fatal("workspace branch was not recorded as owned")
	}
	gitIn(t, repository, "remote", "add", "origin", "https://github.com/owner/repo.git")
	head := gitIn(t, workspace.Path, "rev-parse", "HEAD")
	newFakeForge(t, &fakeForge{
		byCommit: map[string][]map[string]any{
			head: {{"number": 299, "state": "open", "merged_at": nil}},
		},
		byHead: map[string][]map[string]any{},
	})

	report, err := InspectDelegatedWorkspace(context.Background(), DelegatedWorkspaceTarget{
		Pollen: "pollen", Substrate: "demo", Repository: repository, ConfiguredBranch: "main",
		Credential: lifecycleCredential(t),
	})
	if err != nil {
		t.Fatalf("inspect workspace: %v", err)
	}
	if report.FruitState != FruitStateOpen || report.AutoReclaimable {
		t.Fatalf("open-Fruit workspace = %+v, want independently visible open Fruit and no automatic reclamation", report)
	}
	continued, err := ResolveDelegatedWorkspaceWithDefaultBranch(context.Background(), "demo", repository, "pollen", lifecycleCredential(t), "main")
	if err != nil {
		t.Fatalf("continue open-Fruit workspace: %v", err)
	}
	if continued.Path != workspace.Path || continued.Branch != workspace.Branch {
		t.Fatalf("open-Fruit continuation = %+v, want the same reviewable workspace", continued)
	}
	result, err := AbandonDelegatedWorkspace(context.Background(), DelegatedWorkspaceTarget{
		Pollen: "pollen", Substrate: "demo", Repository: repository, ConfiguredBranch: "main",
		Credential: lifecycleCredential(t),
	}, true)
	if err != nil {
		t.Fatalf("explicitly abandon open-Fruit workspace: %v", err)
	}
	if !result.WorktreeRemoved || !result.BranchPreserved || result.BranchDeleted {
		t.Fatalf("explicit abandon result = %+v, want only worktree removed and open Fruit branch preserved", result)
	}
	if !branchExists(t, repository, workspace.Branch) {
		t.Fatal("explicit abandonment deleted an open-Fruit branch")
	}
}

func TestResolveDelegatedWorkspaceReclaimsOnlyCleanEmptyWorkAndUsesFreshDefault(t *testing.T) {
	repository, path, first := newDelegatedWorkspaceLifecycleFixture(t)
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("new default\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "add", "base.txt")
	gitIn(t, repository, "commit", "-q", "-m", "advance default")
	currentDefault := gitIn(t, repository, "rev-parse", "main")

	second, err := ResolveDelegatedWorkspaceWithDefaultBranch(context.Background(), "demo", repository, "pollen", ResolvedCredential{}, "main")
	if err != nil {
		t.Fatalf("resolve fresh workspace: %v", err)
	}
	if second.Path != path || second.Branch != first.Branch {
		t.Fatalf("workspace = %+v, want same exact target and safely reused owned branch name", second)
	}
	if head := gitIn(t, second.Path, "rev-parse", "HEAD"); head != currentDefault {
		t.Fatalf("fresh HEAD = %s, want resolved current default %s", head, currentDefault)
	}
	owned, ok := delegatedOwnedRef(repository, second.Branch, "pollen")
	if !ok || owned.Base != currentDefault {
		t.Fatalf("fresh branch ownership = %+v, want a new base at %s", owned, currentDefault)
	}
}

func TestDirtyIgnoredWorkspaceContinuesWithoutAutomaticReclamation(t *testing.T) {
	repository, path, workspace := newDelegatedWorkspaceLifecycleFixture(t)
	exclude := gitIn(t, path, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(path, exclude)
	}
	if err := os.WriteFile(exclude, []byte("\nignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "ignored.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "base.txt"), []byte("advanced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repository, "add", "base.txt")
	gitIn(t, repository, "commit", "-q", "-m", "advance default")

	report, err := InspectDelegatedWorkspace(context.Background(), DelegatedWorkspaceTarget{
		Pollen: "pollen", Substrate: "demo", Repository: repository, ConfiguredBranch: "main",
	})
	if err != nil {
		t.Fatalf("inspect dirty workspace: %v", err)
	}
	if !report.CleanKnown || report.Clean || report.AutoReclaimable {
		t.Fatalf("ignored workspace state = %+v, want dirty and retained", report)
	}
	continued, err := ResolveDelegatedWorkspaceWithDefaultBranch(context.Background(), "demo", repository, "pollen", ResolvedCredential{}, "main")
	if err != nil {
		t.Fatalf("continue dirty active workspace: %v", err)
	}
	if continued.Path != path || continued.Branch != workspace.Branch {
		t.Fatalf("dirty workspace continuation = %+v, want same exact workspace and branch", continued)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("dirty retained workspace was removed: %v", err)
	}
	if branch := gitIn(t, path, "branch", "--show-current"); branch != workspace.Branch {
		t.Fatalf("dirty workspace branch changed to %s, want %s", branch, workspace.Branch)
	}
	if contents, err := os.ReadFile(filepath.Join(path, "ignored.txt")); err != nil || string(contents) != "keep me\n" {
		t.Fatalf("ignored work was changed or lost: contents %q, error %v", contents, err)
	}
}

func TestTerminalMergedWorkspaceIsReclaimedOnlyWhenClean(t *testing.T) {
	t.Run("clean merged Fruit is reclaimed", func(t *testing.T) {
		repository, path, _ := newDelegatedWorkspaceLifecycleFixture(t)
		gitIn(t, repository, "remote", "add", "origin", "https://github.com/owner/repo.git")
		if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("merged work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, path, "add", "work.txt")
		gitIn(t, path, "commit", "-q", "-m", "merged work")
		mergedHead := gitIn(t, path, "rev-parse", "HEAD")
		newFakeForge(t, &fakeForge{byCommit: map[string][]map[string]any{
			mergedHead: {{"number": 301, "state": "closed", "merged_at": "2026-10-01T00:00:00Z"}},
		}, byHead: map[string][]map[string]any{}})
		originalFetch := runGitFetchCommandFn
		runGitFetchCommandFn = func(context.Context, string, []string, ...string) (string, error) {
			return "", errors.New("network disabled in test")
		}
		t.Cleanup(func() { runGitFetchCommandFn = originalFetch })

		fresh, err := ResolveDelegatedWorkspaceWithDefaultBranch(context.Background(), "demo", repository, "pollen", lifecycleCredential(t), "main")
		if err != nil {
			t.Fatalf("resolve after merged terminal workspace: %v", err)
		}
		if fresh.Path != path {
			t.Fatalf("fresh workspace = %+v, want same isolated path", fresh)
		}
		currentDefault := gitIn(t, repository, "rev-parse", "main")
		if got := gitIn(t, fresh.Path, "rev-parse", "HEAD"); got != currentDefault || got == mergedHead {
			t.Fatalf("fresh workspace HEAD = %s, want current main", got)
		}
		owned, ok := delegatedOwnedRef(repository, fresh.Branch, "pollen")
		if !ok || owned.Base != currentDefault {
			t.Fatalf("replacement OwnedRef = %+v, want a new lifecycle record based on current main %s", owned, currentDefault)
		}
	})

	t.Run("dirty merged residue blocks normal resolution", func(t *testing.T) {
		repository, path, workspace := newDelegatedWorkspaceLifecycleFixture(t)
		gitIn(t, repository, "remote", "add", "origin", "https://github.com/owner/repo.git")
		if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("merged work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, path, "add", "work.txt")
		gitIn(t, path, "commit", "-q", "-m", "merged work")
		mergedHead := gitIn(t, path, "rev-parse", "HEAD")
		newFakeForge(t, &fakeForge{byCommit: map[string][]map[string]any{
			mergedHead: {{"number": 302, "state": "closed", "merged_at": "2026-10-01T00:00:00Z"}},
		}, byHead: map[string][]map[string]any{}})
		if err := os.WriteFile(filepath.Join(path, "uncommitted.txt"), []byte("preserve until recovery\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, err := ResolveDelegatedWorkspaceWithDefaultBranch(context.Background(), "demo", repository, "pollen", lifecycleCredential(t), "main"); err == nil {
			t.Fatal("dirty terminal merged workspace was reused")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dirty terminal workspace was removed: %v", err)
		}
		if branch := gitIn(t, path, "branch", "--show-current"); branch != workspace.Branch {
			t.Fatalf("dirty terminal workspace branch changed to %s, want %s", branch, workspace.Branch)
		}
		if contents, err := os.ReadFile(filepath.Join(path, "uncommitted.txt")); err != nil || string(contents) != "preserve until recovery\n" {
			t.Fatalf("dirty terminal residue changed: contents %q, error %v", contents, err)
		}
	})
}

func TestAbandonPreservesUnownedBranchAndFreshResolutionAvoidsCollision(t *testing.T) {
	repository, path, workspace := newDelegatedWorkspaceLifecycleFixture(t)
	gitIn(t, path, "switch", "-c", "feature/custom")
	for _, mode := range []DelegatedWorkspaceMode{ExistingDelegatedWorkspaceOnly, CreateDelegatedWorkspaceIfMissing} {
		if _, err := ResolveDelegatedWorkspaceWithMode(context.Background(), "demo", repository, "pollen", ResolvedCredential{}, mode); err == nil {
			t.Fatalf("resolver mode %d accepted an unowned branch as this Pollen's workspace", mode)
		}
	}
	if branch := gitIn(t, path, "branch", "--show-current"); branch != "feature/custom" {
		t.Fatalf("refused unowned workspace changed branch to %s", branch)
	}
	result, err := AbandonDelegatedWorkspace(context.Background(), DelegatedWorkspaceTarget{
		Pollen: "pollen", Substrate: "demo", Repository: repository, ConfiguredBranch: "main",
	}, true)
	if err != nil {
		t.Fatalf("abandon exact workspace: %v", err)
	}
	if !result.WorktreeRemoved || result.BranchDeleted || !result.BranchPreserved {
		t.Fatalf("abandon result = %+v, want worktree removed and unowned branch preserved", result)
	}
	if !branchExists(t, repository, "feature/custom") {
		t.Fatal("abandonment deleted the unowned current branch")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after abandonment: %v", err)
	}
	if !branchExists(t, repository, workspace.Branch) {
		t.Fatal("abandonment deleted the non-current original branch")
	}

	fresh, err := ResolveDelegatedWorkspaceWithDefaultBranch(context.Background(), "demo", repository, "pollen", ResolvedCredential{}, "main")
	if err != nil {
		t.Fatalf("create next workspace: %v", err)
	}
	if fresh.Branch == workspace.Branch {
		t.Fatalf("fresh branch reused preserved reference %q", fresh.Branch)
	}
	if head := gitIn(t, fresh.Path, "rev-parse", "HEAD"); head != gitIn(t, repository, "rev-parse", "main") {
		t.Fatalf("fresh workspace started at %s, not current default", head)
	}
}

func TestBotanistAbandonRetiresUnprovenPendingReservationAndPreservesBranch(t *testing.T) {
	repository, path, workspace := newDelegatedWorkspaceLifecycleFixture(t)
	pending, ok := delegatedOwnedRef(repository, workspace.Branch, "pollen")
	if !ok {
		t.Fatal("fixture workspace branch was not owned")
	}
	if err := ForgetOwnedRef(repository, workspace.Branch); err != nil {
		t.Fatalf("replace fixture ownership with pending reservation: %v", err)
	}
	pending.Pending = true
	if err := reserveDelegatedOwnedRef(pending); err != nil {
		t.Fatalf("reserve exact pending ownership: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "unproven.txt"), []byte("branch state to preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, path, "add", "unproven.txt")
	gitIn(t, path, "commit", "-q", "-m", "advance pending branch")

	result, err := AbandonDelegatedWorkspace(context.Background(), DelegatedWorkspaceTarget{
		Pollen: "pollen", Substrate: "demo", Repository: repository, ConfiguredBranch: "main",
	}, true)
	if err != nil {
		t.Fatalf("abandon workspace with unproven pending ownership: %v", err)
	}
	if result.Report.CurrentBranch != workspace.Branch || result.Report.BranchOwned ||
		!result.WorktreeRemoved || !result.BranchPreserved || result.BranchDeleted {
		t.Fatalf("recovery outcome = %+v, want exact pending retired and unproven branch preserved", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists after abandonment: %v", err)
	}
	if !branchExists(t, repository, workspace.Branch) {
		t.Fatal("unproven branch was deleted while retiring its pending reservation")
	}
	for _, ref := range OwnedRefsFor(repository) {
		if ref.Branch == workspace.Branch && ref.Pending {
			t.Fatalf("unproven pending reservation survived Botanist recovery: %+v", ref)
		}
	}
}

func TestBotanistAbandonRetiresPendingWhenWorkspaceEvidenceIsUnavailable(t *testing.T) {
	repository, path, workspace := newDelegatedWorkspaceLifecycleFixture(t)
	pending, ok := delegatedOwnedRef(repository, workspace.Branch, "pollen")
	if !ok {
		t.Fatal("fixture workspace branch was not owned")
	}
	if err := ForgetOwnedRef(repository, workspace.Branch); err != nil {
		t.Fatalf("replace fixture ownership with pending reservation: %v", err)
	}
	pending.Pending = true
	if err := reserveDelegatedOwnedRef(pending); err != nil {
		t.Fatalf("reserve exact pending ownership: %v", err)
	}
	gitIn(t, repository, "worktree", "remove", "--force", path)

	_, err := AbandonDelegatedWorkspace(context.Background(), DelegatedWorkspaceTarget{
		Pollen: "pollen", Substrate: "demo", Repository: repository, ConfiguredBranch: "main",
	}, true)
	if err == nil {
		t.Fatal("abandonment succeeded without exact workspace evidence")
	}
	if branch := branchExists(t, repository, workspace.Branch); !branch {
		t.Fatal("unproven branch was changed while retiring its pending reservation")
	}
	for _, ref := range OwnedRefsFor(repository) {
		if ref.Branch == workspace.Branch && ref.Pending {
			t.Fatalf("pending reservation survived recovery without workspace evidence: %+v", ref)
		}
	}
}

func TestAbandonRequiresExactTargetAndConfirmation(t *testing.T) {
	repository, path, workspace := newDelegatedWorkspaceLifecycleFixture(t)
	target := DelegatedWorkspaceTarget{Pollen: "pollen", Substrate: "demo", Repository: repository, ConfiguredBranch: "main"}
	if _, err := AbandonDelegatedWorkspace(context.Background(), target, false); err == nil {
		t.Fatal("abandonment without confirmation succeeded")
	}
	wrong := target
	wrong.Pollen = "other"
	if _, err := AbandonDelegatedWorkspace(context.Background(), wrong, true); err == nil {
		t.Fatal("abandonment against a different Pollen succeeded")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("refused abandonment changed the workspace: %v", err)
	}
	if !branchExists(t, repository, workspace.Branch) {
		t.Fatal("refused abandonment deleted its branch")
	}
}

func TestSafeReclamationFailsClosedBeforeRemovalWithoutResolvedDefault(t *testing.T) {
	repository, path, workspace := newDelegatedWorkspaceLifecycleFixture(t)
	gitIn(t, repository, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	if _, err := ResolveDelegatedWorkspace(context.Background(), "demo", repository, "pollen", ResolvedCredential{}); err == nil {
		t.Fatal("workspace resolution succeeded without an established default")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("workspace was removed before default resolution failed: %v", err)
	}
	if !branchExists(t, repository, workspace.Branch) {
		t.Fatal("branch was deleted before default resolution failed")
	}
}

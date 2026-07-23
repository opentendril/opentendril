package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newSeedRepo builds a real git repository on branch main with one commit, the
// local checkout RunSeed grows a Seed against.
func newSeedRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "seed@example.com"},
		{"config", "user.name", "Seed Tester"},
		{"checkout", "-b", "main"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return repo
}

// restoreSeeds saves and restores the injectable execution seams so a test's
// overrides never leak into another.
func restoreSeeds(t *testing.T) {
	t.Helper()
	build, verify := seedBuildFn, seedVerifyFn
	t.Cleanup(func() { seedBuildFn, seedVerifyFn = build, verify })
}

// fakeBuild simulates the Sprout builder: it creates the seed branch once (so
// the run has a reviewable branch) and records every prompt it was handed.
func fakeBuild(prompts *[]string) func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
	return func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		*prompts = append(*prompts, prompt)
		if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
				return SproutRunReport{}, err
			}
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete, Output: "deadbeef"}, nil
	}
}

func TestRunSeedSatisfiedOnFirstVerify(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var prompts []string
	seedBuildFn = fakeBuild(&prompts)
	seedVerifyFn = func(context.Context, string, string, []string, []string) (string, bool, error) {
		return "ok", true, nil
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusSatisfied {
		t.Fatalf("status = %q, want satisfied", res.Status)
	}
	if res.Iterations != 1 {
		t.Fatalf("iterations = %d, want 1", res.Iterations)
	}
	if res.Branch == "" {
		t.Fatal("no seed branch captured for review")
	}
	if len(prompts) != 1 {
		t.Fatalf("build ran %d time(s), want 1", len(prompts))
	}
}

// TestRunSeedExhaustedThreadsFeedback: a Seed whose verify never passes spends
// its whole iteration budget (exhausted), and each retry's prompt carries the
// previous deterministic verify failure so the Sprout fixes the real cause.
func TestRunSeedExhaustedThreadsFeedback(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var prompts []string
	seedBuildFn = fakeBuild(&prompts)
	seedVerifyFn = func(context.Context, string, string, []string, []string) (string, bool, error) {
		return "boom: a test failed", false, nil
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusExhausted {
		t.Fatalf("status = %q, want exhausted", res.Status)
	}
	if res.Iterations != 3 {
		t.Fatalf("iterations = %d, want 3", res.Iterations)
	}
	if len(prompts) != 3 {
		t.Fatalf("build ran %d time(s), want 3", len(prompts))
	}
	if strings.Contains(prompts[0], "boom") {
		t.Error("first prompt must carry no prior failure")
	}
	if !strings.Contains(prompts[1], "boom: a test failed") {
		t.Error("a retry prompt must feed back the deterministic verify failure")
	}
}

func TestRunSeedWitheredOnBuildError(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		return SproutRunReport{}, fmt.Errorf("sprout crashed")
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) (string, bool, error) {
		t.Fatal("verify must not run after a withered build")
		return "", false, nil
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
	if res.Iterations != 1 {
		t.Fatalf("iterations = %d, want 1", res.Iterations)
	}
}

// TestRunSeedWitheredOnVerifyInfraError: an infrastructure failure producing the
// verdict (not a clean non-zero exit) withers the run rather than being read as
// a failed verification to iterate on.
func TestRunSeedWitheredOnVerifyInfraError(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var prompts []string
	seedBuildFn = fakeBuild(&prompts)
	seedVerifyFn = func(context.Context, string, string, []string, []string) (string, bool, error) {
		return "", false, fmt.Errorf("terrarium unavailable")
	}

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
}

func TestRunSeedRequiresGitSubstrate(t *testing.T) {
	restoreSeeds(t)
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		t.Fatal("build must not run for a non-git substrate")
		return SproutRunReport{}, nil
	}
	dir := t.TempDir() // a directory, but not a git repository

	if _, err := RunSeed(context.Background(), SeedExecution{
		Substrate: dir, Goal: "g", Verify: []string{"true"}, MaxIterations: 2,
	}); err == nil {
		t.Fatal("a non-git substrate was accepted; seed.grow needs a branch + diff")
	}
}

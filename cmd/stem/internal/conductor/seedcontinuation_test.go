package conductor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

type continuationHarness struct {
	mu        sync.Mutex
	pending   []string
	inFlight  []string
	delivered [][]string
}

func (h *continuationHarness) accept(intent string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending = append(h.pending, intent)
}

func (h *continuationHarness) boundary() SeedContinuationBoundary {
	return SeedContinuationBoundary{
		DeliverPending: func(_ context.Context, basePrompt string) (string, error) {
			h.mu.Lock()
			take := append([]string(nil), h.pending...)
			h.pending = nil
			h.inFlight = take
			h.mu.Unlock()
			return core.ComposeContinuedIntentPrompt(basePrompt, take), nil
		},
		ConfirmDelivery: func(context.Context) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.delivered = append(h.delivered, append([]string(nil), h.inFlight...))
			h.inFlight = nil
			return nil
		},
		AcquireSettlementFence: func(context.Context) (bool, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return len(h.pending) == 0 && len(h.inFlight) == 0, nil
		},
		HasUnresolved: func(context.Context) (bool, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return len(h.pending) > 0 || len(h.inFlight) > 0, nil
		},
	}
}

func TestSeedContinuationPendingBeforeSprout1AppearsInSprout1(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var prompts []string
	seedBuildFn = fakeBuild(&prompts)
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}
	h := &continuationHarness{}
	h.accept("keep going")

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 2,
		SessionID: "tendril-cont-sprout1", Continuation: h.boundary(),
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if res.Status != SeedStatusSatisfied || res.Iterations != 1 {
		t.Fatalf("status/iterations = %q/%d", res.Status, res.Iterations)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "keep going") {
		t.Fatalf("sprout 1 prompt missing pending continuation: %q", prompts)
	}
	if !strings.Contains(prompts[0], "make it pass") {
		t.Fatalf("sprout 1 prompt replaced the original goal: %q", prompts[0])
	}
}

func TestSeedContinuationAcceptedDuringSprout1ReachesOnlySprout2(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	sprout1Started := make(chan struct{})
	sprout1Release := make(chan struct{})
	var prompts []string
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			close(sprout1Started)
			<-sprout1Release
		}
		if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
				return SproutRunReport{}, err
			}
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete, Output: "deadbeef"}, nil
	}
	pass := 0
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		pass++
		return seedVerifyReport{Output: "not yet", Passed: pass > 1}
	}
	h := &continuationHarness{}

	errCh := make(chan error, 1)
	var res SeedRunResult
	go func() {
		var runErr error
		res, runErr = RunSeed(context.Background(), SeedExecution{
			Substrate: repo, Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 2,
			SessionID: "tendril-cont-during", Continuation: h.boundary(),
		})
		errCh <- runErr
	}()
	select {
	case <-sprout1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("sprout 1 never started")
	}
	h.accept("mid-sprout intent")
	close(sprout1Release)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunSeed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunSeed did not finish")
	}
	if res.Status != SeedStatusSatisfied || res.Iterations != 2 {
		t.Fatalf("status/iterations = %q/%d", res.Status, res.Iterations)
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(prompts))
	}
	if strings.Contains(prompts[0], "mid-sprout intent") {
		t.Fatalf("continuation entered the live sprout: %q", prompts[0])
	}
	if !strings.Contains(prompts[1], "mid-sprout intent") {
		t.Fatalf("continuation missing from sprout 2: %q", prompts[1])
	}
}

func TestSeedContinuationSequenceOrderAndNoReplay(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	sprout1Started := make(chan struct{})
	sprout1Release := make(chan struct{})
	var prompts []string
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			close(sprout1Started)
			<-sprout1Release
		}
		if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
				return SproutRunReport{}, err
			}
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete}, nil
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "failed", Passed: false, ExitCode: intPtr(1)}
	}
	h := &continuationHarness{}
	h.accept("first intent")
	h.accept("second intent")

	errCh := make(chan error, 1)
	go func() {
		_, err := RunSeed(context.Background(), SeedExecution{
			Substrate: repo, Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 3,
			SessionID: "tendril-cont-order", Continuation: h.boundary(),
		})
		errCh <- err
	}()
	select {
	case <-sprout1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("sprout 1 never started")
	}
	h.accept("third intent")
	close(sprout1Release)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunSeed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunSeed did not finish")
	}
	if len(prompts) != 3 {
		t.Fatalf("prompts = %d, want 3", len(prompts))
	}
	if !strings.Contains(prompts[0], "1. first intent") || !strings.Contains(prompts[0], "2. second intent") {
		t.Fatalf("sprout 1 lost sequence order: %q", prompts[0])
	}
	if strings.Index(prompts[0], "first intent") > strings.Index(prompts[0], "second intent") {
		t.Fatalf("sprout 1 sequence reversed: %q", prompts[0])
	}
	if strings.Contains(prompts[0], "third intent") {
		t.Fatalf("third intent entered sprout 1: %q", prompts[0])
	}
	if !strings.Contains(prompts[1], "third intent") || strings.Contains(prompts[1], "first intent") {
		t.Fatalf("sprout 2 replayed or missed intent: %q", prompts[1])
	}
	if strings.Contains(prompts[2], "first intent") || strings.Contains(prompts[2], "second intent") || strings.Contains(prompts[2], "third intent") {
		t.Fatalf("delivered continuation replayed in sprout 3: %q", prompts[2])
	}
}

func TestSeedContinuationComposesWithVerificationFeedbackAndDoesNotChangeVerify(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	var prompts []string
	seedBuildFn = fakeBuild(&prompts)
	var verifyArgv [][]string
	exit := 1
	seedVerifyFn = func(_ context.Context, _ string, _ string, verify []string, _ []string) seedVerifyReport {
		copied := append([]string(nil), verify...)
		verifyArgv = append(verifyArgv, copied)
		if len(verifyArgv) == 1 {
			return seedVerifyReport{Output: "stdout: expected file differs", Passed: false, ExitCode: &exit}
		}
		return seedVerifyReport{Output: "ok", Passed: true}
	}
	seedCandidateDiffFn = func(context.Context, string, string, string) string {
		return "CANDIDATE-DIFF"
	}
	sprout1Started := make(chan struct{})
	sprout1Release := make(chan struct{})
	build := seedBuildFn
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		if len(prompts) == 0 {
			close(sprout1Started)
			<-sprout1Release
		}
		return build(ctx, orch, prompt)
	}
	h := &continuationHarness{}
	errCh := make(chan error, 1)
	go func() {
		_, err := RunSeed(context.Background(), SeedExecution{
			Substrate: repo, Goal: "make it pass", Verify: []string{"go", "test", "./secret"}, MaxIterations: 2,
			SessionID: "tendril-cont-compose", Continuation: h.boundary(),
		})
		errCh <- err
	}()
	select {
	case <-sprout1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("sprout 1 never started")
	}
	h.accept("fix the remaining case")
	close(sprout1Release)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunSeed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunSeed did not finish")
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d", len(prompts))
	}
	if !strings.Contains(prompts[1], "stdout: expected file differs") {
		t.Fatalf("sprout 2 missing verification feedback: %q", prompts[1])
	}
	if !strings.Contains(prompts[1], seedCandidateDiffHeading) || !strings.Contains(prompts[1], "CANDIDATE-DIFF") {
		t.Fatalf("sprout 2 missing candidate evidence: %q", prompts[1])
	}
	if !strings.Contains(prompts[1], "fix the remaining case") {
		t.Fatalf("sprout 2 missing continued intent: %q", prompts[1])
	}
	if len(verifyArgv) != 2 {
		t.Fatalf("verify calls = %d", len(verifyArgv))
	}
	for i, argv := range verifyArgv {
		if len(argv) != 3 || argv[0] != "go" || argv[1] != "test" || argv[2] != "./secret" {
			t.Fatalf("verify argv %d changed: %#v", i, argv)
		}
	}
}

func TestSeedContinuationPassingVerifyRunsAnotherIterationOnlyWhenOneRemains(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	sprout1Started := make(chan struct{})
	sprout1Release := make(chan struct{})
	var prompts []string
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			close(sprout1Started)
			<-sprout1Release
		}
		if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
				return SproutRunReport{}, err
			}
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete}, nil
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}
	h := &continuationHarness{}
	errCh := make(chan error, 1)
	var res SeedRunResult
	go func() {
		var runErr error
		res, runErr = RunSeed(context.Background(), SeedExecution{
			Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 2,
			SessionID: "tendril-cont-more-iter", Continuation: h.boundary(),
		})
		errCh <- runErr
	}()
	select {
	case <-sprout1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("sprout 1 never started")
	}
	h.accept("one more thing")
	close(sprout1Release)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("RunSeed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunSeed did not finish")
	}
	if res.Status != SeedStatusSatisfied || res.Iterations != 2 {
		t.Fatalf("status/iterations = %q/%d, want satisfied/2", res.Status, res.Iterations)
	}
	if len(prompts) != 2 || strings.Contains(prompts[0], "one more thing") || !strings.Contains(prompts[1], "one more thing") {
		t.Fatalf("prompts = %#v", prompts)
	}
}

func TestSeedContinuationFinalIterationPendingCannotSatisfy(t *testing.T) {
	restoreSeeds(t)
	repo := newSeedRepo(t)
	sprout1Started := make(chan struct{})
	sprout1Release := make(chan struct{})
	var prompts []string
	seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
		prompts = append(prompts, prompt)
		close(sprout1Started)
		<-sprout1Release
		if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
			if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
				return SproutRunReport{}, err
			}
		}
		return SproutRunReport{Outcome: SproutOutcomeComplete}, nil
	}
	seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
		return seedVerifyReport{Output: "ok", Passed: true}
	}
	h := &continuationHarness{}
	errCh := make(chan error, 1)
	var res SeedRunResult
	go func() {
		var runErr error
		res, runErr = RunSeed(context.Background(), SeedExecution{
			Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 1,
			SessionID: "tendril-cont-final", Continuation: h.boundary(),
		})
		errCh <- runErr
	}()
	select {
	case <-sprout1Started:
	case <-time.After(2 * time.Second):
		t.Fatal("sprout 1 never started")
	}
	h.accept("too late for another sprout")
	close(sprout1Release)
	select {
	case err := <-errCh:
		if !errors.Is(err, core.ErrContinuationUndeliverable) {
			t.Fatalf("RunSeed err = %v, want undeliverable", err)
		}
		if strings.Contains(err.Error(), "too late") {
			t.Fatalf("error leaked continued intent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunSeed did not finish")
	}
	if res.Status == SeedStatusSatisfied {
		t.Fatal("final-iteration pending continuation reported satisfied")
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
	if len(prompts) != 1 {
		t.Fatalf("widened iterations: %d prompts", len(prompts))
	}
}

func TestSeedContinuationTimeoutSproutAndVerifyFailureAccountPending(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		restoreSeeds(t)
		repo := newSeedRepo(t)
		started := make(chan struct{})
		release := make(chan struct{})
		seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
			close(started)
			<-release
			if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
				if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
					return SproutRunReport{}, err
				}
			}
			return SproutRunReport{Outcome: SproutOutcomeComplete}, nil
		}
		seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
			return seedVerifyReport{Output: "ok", Passed: true}
		}
		h := &continuationHarness{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		errCh := make(chan error, 1)
		var res SeedRunResult
		go func() {
			var runErr error
			res, runErr = RunSeed(ctx, SeedExecution{
				Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 2,
				SessionID: "tendril-cont-timeout", Continuation: h.boundary(),
			})
			errCh <- runErr
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("sprout never started")
		}
		h.accept("after timeout window")
		cancel()
		close(release)
		select {
		case err := <-errCh:
			if !errors.Is(err, core.ErrContinuationUndeliverable) {
				t.Fatalf("timeout err = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("RunSeed did not finish")
		}
		if res.Status == SeedStatusSatisfied {
			t.Fatal("timeout with pending continuation reported satisfied")
		}
	})

	t.Run("sprout failure", func(t *testing.T) {
		restoreSeeds(t)
		repo := newSeedRepo(t)
		started := make(chan struct{})
		release := make(chan struct{})
		seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
			close(started)
			<-release
			return SproutRunReport{}, errors.New("sprout crashed")
		}
		seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
			t.Fatal("verify ran after sprout failure")
			return seedVerifyReport{}
		}
		h := &continuationHarness{}
		errCh := make(chan error, 1)
		var res SeedRunResult
		go func() {
			var runErr error
			res, runErr = RunSeed(context.Background(), SeedExecution{
				Substrate: repo, Goal: "make it pass", Verify: []string{"true"}, MaxIterations: 3,
				SessionID: "tendril-cont-wither", Continuation: h.boundary(),
			})
			errCh <- runErr
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("sprout never started")
		}
		h.accept("during failed sprout")
		close(release)
		select {
		case err := <-errCh:
			if !errors.Is(err, core.ErrContinuationUndeliverable) {
				t.Fatalf("sprout failure err = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("RunSeed did not finish")
		}
		if res.Status != SeedStatusWithered {
			t.Fatalf("status = %q", res.Status)
		}
	})

	t.Run("verify failure", func(t *testing.T) {
		restoreSeeds(t)
		repo := newSeedRepo(t)
		started := make(chan struct{})
		release := make(chan struct{})
		seedBuildFn = func(ctx context.Context, orch *DockerOrchestrator, prompt string) (SproutRunReport, error) {
			close(started)
			<-release
			if !localBranchExists(orch.Substrate, orch.SubstrateBranch) {
				if _, err := runGitCommand(ctx, orch.Substrate, "branch", orch.SubstrateBranch, "HEAD"); err != nil {
					return SproutRunReport{}, err
				}
			}
			return SproutRunReport{Outcome: SproutOutcomeComplete}, nil
		}
		seedVerifyFn = func(context.Context, string, string, []string, []string) seedVerifyReport {
			code := 2
			return seedVerifyReport{Output: "failed", Passed: false, ExitCode: &code}
		}
		h := &continuationHarness{}
		errCh := make(chan error, 1)
		var res SeedRunResult
		go func() {
			var runErr error
			res, runErr = RunSeed(context.Background(), SeedExecution{
				Substrate: repo, Goal: "make it pass", Verify: []string{"false"}, MaxIterations: 1,
				SessionID: "tendril-cont-verify", Continuation: h.boundary(),
			})
			errCh <- runErr
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("sprout never started")
		}
		h.accept("during last verify")
		close(release)
		select {
		case err := <-errCh:
			if !errors.Is(err, core.ErrContinuationUndeliverable) {
				t.Fatalf("verify failure err = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("RunSeed did not finish")
		}
		if res.Status == SeedStatusSatisfied || res.Status == SeedStatusExhausted {
			t.Fatalf("status = %q, want accounted withered", res.Status)
		}
	})
}

func intPtr(v int) *int { return &v }

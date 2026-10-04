package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

func TestPrepareSeedSproutPersistsUniqueOpeningRows(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := core.WithPollen(context.Background(), "claude")
	spec := core.SeedSpec{
		Substrate:  "myrepo",
		Goal:       "make it pass",
		PhytomerID: "tendril-seed-ops",
		Origin:     "rest",
	}
	var ids []string
	for i := 1; i <= 3; i++ {
		orch := conductor.NewDockerOrchestrator()
		if err := prepareSeedSprout(ctx, store, spec, orch, i); err != nil {
			t.Fatalf("prepare iteration %d: %v", i, err)
		}
		if orch.SessionID != spec.PhytomerID {
			t.Fatalf("sessionID = %q, want %q", orch.SessionID, spec.PhytomerID)
		}
		if orch.StepID == "" {
			t.Fatal("missing unique step id")
		}
		ids = append(ids, orch.StepID)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("iteration ids collided: %v", ids)
		}
		seen[id] = true
	}

	runs, err := store.LoadSproutRuns(context.Background(), spec.PhytomerID, 10)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("persisted %d sprout rows, want 3", len(runs))
	}
	for _, run := range runs {
		if run.Pollen != "claude" || run.Substrate != "myrepo" || run.SessionID != spec.PhytomerID {
			t.Fatalf("opening row = %+v", run)
		}
		if run.Status != "running" {
			t.Fatalf("opening status = %q, want running", run.Status)
		}
	}
}

func TestSeedOutcomeFactsUseOnlyDeterministicEvidence(t *testing.T) {
	tests := []struct {
		name          string
		result        conductor.SeedRunResult
		runErr        error
		contextErr    error
		maxIterations int
		sproutFailure bool
		wantExecution string
		wantVerify    string
	}{
		{
			name: "completed verifier pass",
			result: conductor.SeedRunResult{Status: conductor.SeedStatusSatisfied, VerificationDiagnostics: []core.SeedVerificationDiagnostic{{
				Outcome: core.SeedVerificationOutcomePassed,
			}}},
			maxIterations: 3, wantExecution: core.SeedExecutionOutcomeCompleted, wantVerify: core.SeedVerificationOutcomePassed,
		},
		{
			name: "predicate failure spent iteration bound",
			result: conductor.SeedRunResult{Status: conductor.SeedStatusExhausted, Iterations: 3, VerificationDiagnostics: []core.SeedVerificationDiagnostic{{
				Outcome: core.SeedVerificationOutcomePredicateFailed,
			}}},
			maxIterations: 3, wantExecution: core.SeedExecutionOutcomeBoundsExhausted, wantVerify: core.SeedVerificationOutcomePredicateFailed,
		},
		{
			name:          "combined satisfied status without diagnostic is not enough",
			result:        conductor.SeedRunResult{Status: conductor.SeedStatusSatisfied},
			maxIterations: 3,
		},
		{
			name: "verifier timeout remains a verification fact",
			result: conductor.SeedRunResult{Status: conductor.SeedStatusWithered, VerificationDiagnostics: []core.SeedVerificationDiagnostic{{
				Outcome: core.SeedVerificationOutcomeInfrastructureFailed, TimedOut: true,
			}}},
			maxIterations: 3, wantVerify: core.SeedVerificationOutcomeTimedOut,
		},
		{
			name:          "proven terminal sprout failure",
			result:        conductor.SeedRunResult{Status: conductor.SeedStatusWithered},
			maxIterations: 3, sproutFailure: true, wantExecution: core.SeedExecutionOutcomeSproutFailed,
		},
		{
			name:       "whole growth deadline",
			result:     conductor.SeedRunResult{Status: conductor.SeedStatusExhausted},
			contextErr: context.DeadlineExceeded, maxIterations: 3,
			wantExecution: core.SeedExecutionOutcomeTimedOut,
		},
		{
			name:   "continuation boundary refusal",
			result: conductor.SeedRunResult{Status: conductor.SeedStatusWithered},
			runErr: core.ErrContinuationUndeliverable, maxIterations: 3,
			wantExecution: core.SeedExecutionOutcomeBoundaryRefused,
		},
		{
			name: "unknown diagnostic is left empty",
			result: conductor.SeedRunResult{Status: conductor.SeedStatusWithered, VerificationDiagnostics: []core.SeedVerificationDiagnostic{{
				Outcome: "configuration-guessed-from-stderr",
			}}},
			maxIterations: 3,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotExecution, gotVerify := seedOutcomeFacts(tc.result, tc.runErr, tc.contextErr, tc.maxIterations, tc.sproutFailure)
			if gotExecution != tc.wantExecution || gotVerify != tc.wantVerify {
				t.Fatalf("outcomes = %q / %q, want %q / %q", gotExecution, gotVerify, tc.wantExecution, tc.wantVerify)
			}
		})
	}
	if _, verify := seedOutcomeFacts(
		conductor.SeedRunResult{Status: conductor.SeedStatusExhausted, Iterations: 2, VerificationDiagnostics: []core.SeedVerificationDiagnostic{{Outcome: core.SeedVerificationOutcomePredicateFailed}}},
		nil, errors.New("canceled"), 3, false,
	); verify != core.SeedVerificationOutcomePredicateFailed {
		t.Fatalf("known verifier outcome = %q", verify)
	}
}

func TestSeedSproutFailureRequiresTheExactTerminalIteration(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RecordSproutRun(context.Background(), historydb.SproutRun{
		RunID: "seed-run-2", SessionID: "tendril-seed", StepID: "seed-tendril-seed-2-123",
		Status: "withered", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record withered Sprout: %v", err)
	}
	if !seedSproutFailureEstablished(context.Background(), store, "tendril-seed", 2) {
		t.Fatal("exact withered final iteration was not recognized")
	}
	if seedSproutFailureEstablished(context.Background(), store, "tendril-seed", 1) {
		t.Fatal("a different iteration was classified as the final Sprout failure")
	}
}

func TestPrepareSeedSproutDoesNotInventAFakeSproutWhenHistoryIsNil(t *testing.T) {
	orch := conductor.NewDockerOrchestrator()
	err := prepareSeedSprout(context.Background(), nil, core.SeedSpec{
		Substrate: "myrepo", PhytomerID: "tendril-seed-ops", Goal: "g",
	}, orch, 1)
	if err != nil {
		t.Fatalf("nil history prepare: %v", err)
	}
	if orch.SessionID != "tendril-seed-ops" {
		t.Fatalf("sessionID = %q", orch.SessionID)
	}
}

func TestPrepareSeedSproutRequiresPhytomer(t *testing.T) {
	err := prepareSeedSprout(context.Background(), nil, core.SeedSpec{Substrate: "myrepo"}, conductor.NewDockerOrchestrator(), 1)
	if err == nil {
		t.Fatal("missing phytomer was accepted")
	}
}

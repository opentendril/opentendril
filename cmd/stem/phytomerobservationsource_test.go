package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

func TestPhytomerObservationSourceCopiesPersistedUnsafeFields(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	hostile := "internal path /home/operator/private\nAuthorization: Bearer secret-token\nPRIVATE_PROMPT_CONTENT"
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-hostile", Pollen: "claude", PhytomerID: "tendril-hostile",
		Substrate: "myrepo", Status: "fruit-publication-failed", Goal: "PRIVATE_PROMPT_CONTENT",
		Diff: "internal path /home/operator/private", Logs: "Authorization: Bearer secret-token",
		Error: hostile, PublicationDiagnostic: &historydb.SeedPublicationDiagnostic{
			FailureCategory: "fruit-publication", ExecutionStatus: "satisfied", Phase: "reconciliation",
			Outcome: "reconciliation-unavailable", Message: "safe publication diagnostic", RequestID: "req-safe-123",
		}, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}
	if err := store.RecordSproutRun(context.Background(), historydb.SproutRun{
		RunID: "run-hostile", SessionID: "tendril-hostile", StepID: "run-hostile",
		Pollen: "claude", Substrate: "myrepo", Status: "withered",
		Transcript: "private reasoning", Output: "chain-of-thought hidden",
		Error: "Authorization: Bearer secret-token", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record sprout: %v", err)
	}

	src := phytomerObservationSource(store)
	seed, found, err := src.SeedByPhytomer(context.Background(), "tendril-hostile")
	if err != nil || !found {
		t.Fatalf("seed evidence found=%v err=%v", found, err)
	}
	if seed.Error != hostile || seed.Goal == "" || seed.Diff == "" || seed.Logs == "" {
		t.Fatalf("source dropped seed evidence Core must refuse: %+v", seed)
	}
	if seed.PublicationDiagnostic == nil || seed.PublicationDiagnostic.FailureCategory != "fruit-publication" {
		t.Fatalf("source dropped publication diagnostic: %+v", seed.PublicationDiagnostic)
	}
	if len(seed.VerificationDiagnostics) != 0 {
		t.Fatalf("invented verification diagnostics: %+v", seed.VerificationDiagnostics)
	}
	sprouts, err := src.SproutsByPhytomer(context.Background(), "tendril-hostile")
	if err != nil || len(sprouts) != 1 {
		t.Fatalf("sprout evidence = %d err=%v", len(sprouts), err)
	}
	if sprouts[0].Transcript == "" || sprouts[0].Output == "" || sprouts[0].Error == "" {
		t.Fatalf("source dropped sprout evidence Core must refuse: %+v", sprouts[0])
	}
	if sprouts[0].Pollen != "claude" || sprouts[0].Substrate != "myrepo" {
		t.Fatalf("source dropped sprout ownership evidence: %+v", sprouts[0])
	}
}

func TestPhytomerObservationSourceCopiesVerificationDiagnostics(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	code := 1
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-verify", Pollen: "claude", PhytomerID: "tendril-verify",
		Substrate: "myrepo", Status: "exhausted", Iterations: 1,
		VerificationDiagnostics: []historydb.SeedVerificationDiagnostic{{
			Iteration: 1, Outcome: "predicate-failed", ExitCode: &code, Message: "verify command exited 1",
		}},
		StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}

	src := phytomerObservationSource(store)
	seed, found, err := src.SeedByPhytomer(context.Background(), "tendril-verify")
	if err != nil || !found {
		t.Fatalf("seed evidence found=%v err=%v", found, err)
	}
	if len(seed.VerificationDiagnostics) != 1 || seed.VerificationDiagnostics[0].Outcome != "predicate-failed" {
		t.Fatalf("verification diagnostics = %+v", seed.VerificationDiagnostics)
	}
	if seed.VerificationDiagnostics[0].ExitCode == nil || *seed.VerificationDiagnostics[0].ExitCode != 1 {
		t.Fatalf("exit code = %+v", seed.VerificationDiagnostics[0])
	}
}

func TestPhytomerObservationSourceNilHistoryIsUnwired(t *testing.T) {
	src := phytomerObservationSource(nil)
	if src.SeedByPhytomer != nil || src.SproutsByPhytomer != nil {
		t.Fatal("nil history still wired an observation source")
	}
}

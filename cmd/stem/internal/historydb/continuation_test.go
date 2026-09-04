package historydb

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/heartwood"
)

func TestAcceptContinuationFirstInsert(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")

	rec, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "keep going",
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if rec.ContinuationID == "" || rec.Sequence != 1 || rec.DeliveryState != continuationDeliveryPending {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Substrate != "myrepo" || rec.Pollen != "claude" || rec.Intent != "keep going" {
		t.Fatalf("ownership/intent = %+v", rec)
	}
	if rec.IntentDigest != continuationIntentDigest("keep going") {
		t.Fatalf("digest = %q", rec.IntentDigest)
	}

	listed, err := store.ListContinuationsByPhytomer(ctx, "tendril-1")
	if err != nil || len(listed) != 1 || listed[0].ContinuationID != rec.ContinuationID {
		t.Fatalf("listed = %+v err=%v", listed, err)
	}
}

func TestAcceptContinuationIdempotentSameIntent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")

	first, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "keep going",
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "keep going",
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.ContinuationID != second.ContinuationID || first.Sequence != second.Sequence {
		t.Fatalf("idempotent retry diverged: %+v vs %+v", first, second)
	}
	listed, err := store.ListContinuationsByPhytomer(ctx, "tendril-1")
	if err != nil || len(listed) != 1 {
		t.Fatalf("want 1 row, got %d err=%v", len(listed), err)
	}
}

func TestAcceptContinuationIdempotencyConflict(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	if _, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "first intent",
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "different intent",
	})
	if !errors.Is(err, ErrContinuationIdempotencyConflict) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestAcceptContinuationConcurrentIdentical(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")

	const n = 16
	var wg sync.WaitGroup
	ids := make([]string, n)
	seqs := make([]int, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			rec, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
				PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "same", Intent: "same intent",
			})
			errs[i] = err
			ids[i] = rec.ContinuationID
			seqs[i] = rec.Sequence
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		if ids[i] != ids[0] || seqs[i] != seqs[0] {
			t.Fatalf("duplicate durable continuation: ids=%v seqs=%v", ids, seqs)
		}
	}
	listed, err := store.ListContinuationsByPhytomer(ctx, "tendril-1")
	if err != nil || len(listed) != 1 {
		t.Fatalf("want exactly one row, got %d err=%v", len(listed), err)
	}
}

func TestAcceptContinuationConcurrentDistinctSequences(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")

	const n = 12
	var wg sync.WaitGroup
	seqs := make([]int, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			rec, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
				PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo",
				IdempotencyKey: "k-" + string(rune('a'+i)), Intent: "intent-" + string(rune('a'+i)),
			})
			errs[i] = err
			seqs[i] = rec.Sequence
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	sort.Ints(seqs)
	for i := 0; i < n; i++ {
		if seqs[i] != i+1 {
			t.Fatalf("sequences = %v, want unique 1..%d", seqs, n)
		}
	}
}

func TestAcceptContinuationSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "rhizome.key")
	if err := os.WriteFile(keyPath, []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	path := filepath.Join(dir, "history.db")
	ctx := context.Background()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	first, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "keep going",
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, found, err := reopened.GetContinuation(ctx, first.ContinuationID)
	if err != nil || !found {
		t.Fatalf("get after reopen: found=%v err=%v", found, err)
	}
	if got.Intent != "keep going" || got.Sequence != first.Sequence || got.ContinuationID != first.ContinuationID {
		t.Fatalf("reopened = %+v", got)
	}
}

func TestAcceptContinuationEncryptedAtRest(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	secret := "PLAINTEXT-CONTINUATION-INTENT-DO-NOT-STORE"
	rec, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: secret,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	raw, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer raw.Close()
	var stored string
	if err := raw.QueryRow(`SELECT intent FROM continuations WHERE continuationId = ?`, rec.ContinuationID).Scan(&stored); err != nil {
		t.Fatalf("raw select: %v", err)
	}
	if stored == secret || !hasHeartwoodPrefix(stored) {
		t.Fatalf("intent column is not ciphertext: %q", stored)
	}
	if rec.IntentDigest == secret || rec.IntentDigest == "" {
		t.Fatalf("digest leaked plaintext: %q", rec.IntentDigest)
	}

	for _, path := range []string{store.Path(), store.Path() + "-wal", store.Path() + "-shm"} {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("plaintext intent found in %s", path)
		}
	}

	loaded, found, err := store.GetContinuation(ctx, rec.ContinuationID)
	if err != nil || !found || loaded.Intent != secret {
		t.Fatalf("round-trip: found=%v err=%v rec=%+v", found, err, loaded)
	}
}

func TestAcceptContinuationEncryptionOptOut(t *testing.T) {
	t.Setenv(EnvEncryptAtRest, "off")
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	if _, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "opt-out-intent",
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	raw, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer raw.Close()
	var stored string
	if err := raw.QueryRow(`SELECT intent FROM continuations LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("raw select: %v", err)
	}
	if stored != "opt-out-intent" {
		t.Fatalf("expected plaintext when opted out, got %q", stored)
	}
	listed, err := store.ListContinuationsByPhytomer(ctx, "tendril-1")
	if err != nil || len(listed) != 1 || listed[0].Intent != "opt-out-intent" {
		t.Fatalf("round-trip after opt-out: %+v err=%v", listed, err)
	}
}

func TestAcceptContinuationRefusesUnknownAndTerminal(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-missing", Pollen: "claude", IdempotencyKey: "k1", Intent: "go",
	})
	if !errors.Is(err, ErrContinuationNotFound) {
		t.Fatalf("unknown: %v", err)
	}

	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "myrepo")
	_, err = store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "other", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "go",
	})
	if !errors.Is(err, ErrContinuationPollenMismatch) {
		t.Fatalf("wrong pollen: %v", err)
	}

	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-term", Pollen: "claude", PhytomerID: "tendril-term", Substrate: "myrepo",
		Status: "satisfied", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record terminal: %v", err)
	}
	_, err = store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-term", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "go",
	})
	if !errors.Is(err, ErrContinuationNotEligible) {
		t.Fatalf("terminal: %v", err)
	}

	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle: "seed-blank", Pollen: "claude", PhytomerID: "tendril-blank", Substrate: "",
		Status: seedStatusRunning, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record blank: %v", err)
	}
	_, err = store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-blank", Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "go",
	})
	if !errors.Is(err, ErrContinuationTargetChanged) {
		t.Fatalf("blank live substrate vs expected ownership: %v", err)
	}
}

func TestAcceptContinuationRefusesUnrecognizedStatus(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	for _, status := range []string{"unknown", "matured", "settling"} {
		phytomerID := "tendril-" + status
		if err := store.RecordSeedRun(ctx, SeedRun{
			Handle: "seed-" + status, Pollen: "claude", PhytomerID: phytomerID, Substrate: "myrepo",
			Status: status, StartedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("record %q: %v", status, err)
		}
		_, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
			PhytomerID: phytomerID, Pollen: "claude", Substrate: "myrepo", IdempotencyKey: "k1", Intent: "go",
		})
		if !errors.Is(err, ErrContinuationNotEligible) {
			t.Fatalf("status %q: %v", status, err)
		}
		listed, err := store.ListContinuationsByPhytomer(ctx, phytomerID)
		if err != nil || len(listed) != 0 {
			t.Fatalf("status %q inserted a continuation: %+v err=%v", status, listed, err)
		}
	}
}

func TestAcceptContinuationRefusesStaleExpectedSubstrate(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	mustRecordRunningSeed(t, store, "seed-1", "tendril-1", "claude", "repo-a")
	if _, err := store.db.ExecContext(ctx, `UPDATE seedruns SET substrate = ? WHERE phytomerId = ?`, "repo-b", "tendril-1"); err != nil {
		t.Fatalf("mutate substrate: %v", err)
	}
	_, err := store.AcceptContinuation(ctx, ContinuationAcceptance{
		PhytomerID: "tendril-1", Pollen: "claude", Substrate: "repo-a", Handle: "seed-1",
		IdempotencyKey: "k1", Intent: "keep going",
	})
	if !errors.Is(err, ErrContinuationTargetChanged) {
		t.Fatalf("stale substrate: %v", err)
	}
	listed, err := store.ListContinuationsByPhytomer(ctx, "tendril-1")
	if err != nil || len(listed) != 0 {
		t.Fatalf("want no continuation after ownership change, got %+v err=%v", listed, err)
	}
}

func TestContinuationTableAddedWithoutSchemaBump(t *testing.T) {
	store := openTestStore(t)
	var version int
	if err := store.db.QueryRow(`SELECT version FROM schemaMeta WHERE id = 1`).Scan(&version); err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}
	var name string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='continuations'`).Scan(&name); err != nil {
		t.Fatalf("continuations table missing: %v", err)
	}
}

func mustRecordRunningSeed(t *testing.T, store *Store, handle, phytomerID, pollen, substrate string) {
	t.Helper()
	if err := store.RecordSeedRun(context.Background(), SeedRun{
		Handle: handle, Pollen: pollen, PhytomerID: phytomerID, Substrate: substrate,
		Status: seedStatusRunning, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed: %v", err)
	}
}

func hasHeartwoodPrefix(stored string) bool {
	return len(stored) >= len(heartwood.Prefix) && stored[:len(heartwood.Prefix)] == heartwood.Prefix
}

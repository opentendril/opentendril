package historydb

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func TestListFruitClaimsRequiresPersistedBranchAndCommit(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID:                 "sprout-fruit",
		SessionID:             "phytomer-fruit",
		StepID:                "step-fruit",
		Status:                "matured",
		StartedAt:             now,
		FinishedAt:            now,
		FruitRepository:       "github.com/example/repo",
		FruitWorkspace:        "/private/repo",
		FruitBranch:           "arbitrary/review",
		FruitCommit:           "abc123",
		FruitPublicationState: FruitPublicationPublished,
		FruitCreatedAt:        now,
	}); err != nil {
		t.Fatalf("RecordSproutRun fruit: %v", err)
	}
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID:       "sprout-branch-only",
		SessionID:   "phytomer-fruit",
		StepID:      "step-branch-only",
		Status:      "matured",
		StartedAt:   now.Add(time.Second),
		FinishedAt:  now.Add(time.Second),
		FruitBranch: "sprout/task-looking",
	}); err != nil {
		t.Fatalf("RecordSproutRun branch-only: %v", err)
	}
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle:     "seed-branch-only",
		PhytomerID: "phytomer-fruit",
		Status:     "complete",
		StartedAt:  now.Add(2 * time.Second),
		FinishedAt: now.Add(2 * time.Second),
		Branch:     "tendril/seed-not-fruit",
	}); err != nil {
		t.Fatalf("RecordSeedRun branch-only: %v", err)
	}
	for i := 0; i < 55; i++ {
		if err := store.RecordSproutRun(ctx, SproutRun{
			RunID:                 fmt.Sprintf("sprout-extra-%02d", i),
			SessionID:             "phytomer-fruit",
			Status:                "matured",
			StartedAt:             now.Add(time.Duration(3+i) * time.Second),
			FinishedAt:            now.Add(time.Duration(3+i) * time.Second),
			FruitRepository:       "github.com/example/repo",
			FruitBranch:           fmt.Sprintf("review/fruit-%02d", i),
			FruitCommit:           fmt.Sprintf("commit-%02d", i),
			FruitPublicationState: FruitPublicationPublished,
			FruitCreatedAt:        now.Add(time.Duration(3+i) * time.Second),
		}); err != nil {
			t.Fatalf("RecordSproutRun extra %d: %v", i, err)
		}
	}

	claims, err := store.ListFruitClaims(ctx)
	if err != nil {
		t.Fatalf("ListFruitClaims: %v", err)
	}
	if len(claims) != 56 {
		t.Fatalf("claims = %d, want all 56 persisted claims without an observation cap", len(claims))
	}
	var found bool
	for _, claim := range claims {
		if claim.Branch == "arbitrary/review" {
			found = true
			if claim.Commit != "abc123" || claim.Repository != "github.com/example/repo" || claim.PublicationState != FruitPublicationPublished {
				t.Fatalf("claim = %+v", claim)
			}
		}
	}
	if !found {
		t.Fatal("exact arbitrary branch/commit claim was not returned")
	}
}

func TestHistorySchemaV8MigrationDoesNotInventFruitProvenance(t *testing.T) {
	t.Setenv(EnvEncryptAtRest, "off")
	dbDir := t.TempDir()
	path := dbDir + "/history.db"
	ctx := context.Background()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.RecordSproutRun(ctx, SproutRun{RunID: "legacy", Status: "matured", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}
	for _, column := range []string{"fruitCreatedAt", "fruitPublicationState", "fruitCommit", "fruitBranch", "fruitWorkspace", "fruitRepository"} {
		if _, err := store.db.ExecContext(ctx, `ALTER TABLE sproutruns DROP COLUMN `+column); err != nil {
			t.Fatalf("drop v9 column %s: %v", column, err)
		}
	}
	for _, column := range []string{"fruitCreatedAt", "fruitPublicationState", "fruitWorkspace", "fruitRepository"} {
		if _, err := store.db.ExecContext(ctx, `ALTER TABLE seedruns DROP COLUMN `+column); err != nil {
			t.Fatalf("drop v9 Seed column %s: %v", column, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE schemaMeta SET version = 8 WHERE id = 1`); err != nil {
		t.Fatalf("mark v8: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close v8 store: %v", err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("migrate v8 store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var version int
	if err := store.db.QueryRowContext(ctx, `SELECT version FROM schemaMeta WHERE id = 1`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}

	var branch, commit, repository string
	if err := store.db.QueryRowContext(ctx, `SELECT fruitBranch, fruitCommit, fruitRepository FROM sproutruns WHERE runId = ?`, "legacy").Scan(&branch, &commit, &repository); err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("migrated legacy row disappeared")
		}
		t.Fatalf("read migrated provenance: %v", err)
	}
	if branch != "" || commit != "" || repository != "" {
		t.Fatalf("migration manufactured provenance: branch=%q commit=%q repository=%q", branch, commit, repository)
	}
}

func TestFruitPublicationStateIsClosed(t *testing.T) {
	store := openTestStore(t)
	if err := store.RecordSproutRun(context.Background(), SproutRun{
		RunID: "invalid-state", Status: "matured", StartedAt: time.Now().UTC(), FruitPublicationState: "merged",
	}); err == nil {
		t.Fatal("invalid Fruit publication state was accepted")
	}
	if err := store.RecordSeedRun(context.Background(), SeedRun{
		Handle: "invalid-state-seed", Status: "satisfied", StartedAt: time.Now().UTC(), FruitPublicationState: "accepted",
	}); err == nil {
		t.Fatal("invalid Seed Fruit publication state was accepted")
	}
}

func TestPruneOlderThanPreservesFruitStructureAndCompactsPayload(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-48 * time.Hour)
	originalVacuum := vacuumHistoryFn
	vacuumCalls := 0
	vacuumHistoryFn = func(ctx context.Context, db *sql.DB) error {
		vacuumCalls++
		return originalVacuum(ctx, db)
	}
	t.Cleanup(func() { vacuumHistoryFn = originalVacuum })

	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID:                 "old-fruit",
		SessionID:             "phytomer-old",
		Status:                "matured",
		Transcript:            "large transcript",
		Output:                "large output",
		Error:                 "large error",
		StartedAt:             old,
		FinishedAt:            old,
		FruitRepository:       "local:/repo",
		FruitWorkspace:        "/repo",
		FruitBranch:           "review/fruit",
		FruitCommit:           "deadbeef",
		FruitPublicationState: FruitPublicationLocalOnly,
		FruitCreatedAt:        old,
	}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}
	if err := store.RecordSeedRun(ctx, SeedRun{
		Handle:                "old-seed-fruit",
		PhytomerID:            "phytomer-old",
		Substrate:             "seed-substrate",
		Status:                "complete",
		Goal:                  "large goal",
		Diff:                  "large diff",
		Logs:                  "large logs",
		StartedAt:             old,
		FinishedAt:            old,
		Branch:                "seed/review",
		Commit:                "seedcommit",
		FruitRepository:       "github.com/example/seed",
		FruitWorkspace:        "/repo",
		FruitPublicationState: FruitPublicationPublished,
		FruitCreatedAt:        old,
	}); err != nil {
		t.Fatalf("RecordSeedRun: %v", err)
	}

	if _, err := store.PruneOlderThan(ctx, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}

	runs, err := store.LoadSproutRuns(ctx, "phytomer-old", 10)
	if err != nil {
		t.Fatalf("LoadSproutRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("retained sprout = %+v", runs)
	}
	sprout := runs[0]
	if sprout.SessionID != "phytomer-old" || sprout.Status != "matured" || !sprout.StartedAt.Equal(old) || !sprout.FinishedAt.Equal(old) {
		t.Fatalf("retained sprout lifecycle = %+v", sprout)
	}
	if sprout.FruitRepository != "local:/repo" || sprout.FruitWorkspace != "/repo" || sprout.FruitBranch != "review/fruit" || sprout.FruitCommit != "deadbeef" || sprout.FruitPublicationState != FruitPublicationLocalOnly || !sprout.FruitCreatedAt.Equal(old) {
		t.Fatalf("retained sprout provenance = %+v", sprout)
	}
	if sprout.Transcript != "" || sprout.Output != "" || sprout.Error != "" {
		t.Fatalf("retained sprout payload = %+v", sprout)
	}
	seed, ok, err := store.GetSeedRun(ctx, "old-seed-fruit")
	if err != nil || !ok {
		t.Fatalf("GetSeedRun: ok=%v err=%v", ok, err)
	}
	if seed.PhytomerID != "phytomer-old" || seed.Substrate != "seed-substrate" || seed.Status != "complete" || !seed.StartedAt.Equal(old) || !seed.FinishedAt.Equal(old) {
		t.Fatalf("retained seed lifecycle = %+v", seed)
	}
	if seed.Branch != "seed/review" || seed.Commit != "seedcommit" || seed.FruitRepository != "github.com/example/seed" || seed.FruitWorkspace != "/repo" || seed.FruitPublicationState != FruitPublicationPublished || !seed.FruitCreatedAt.Equal(old) {
		t.Fatalf("retained seed provenance = %+v", seed)
	}
	if seed.Goal != "" || seed.Diff != "" || seed.Logs != "" {
		t.Fatalf("retained seed payload = %+v", seed)
	}
	if vacuumCalls != 1 {
		t.Fatalf("first retention sweep vacuum calls = %d, want 1", vacuumCalls)
	}

	if pruned, err := store.PruneOlderThan(ctx, time.Now().UTC().Add(-24*time.Hour)); err != nil {
		t.Fatalf("identical PruneOlderThan: %v", err)
	} else if pruned != 0 {
		t.Fatalf("identical PruneOlderThan deleted %d rows, want 0", pruned)
	}
	if vacuumCalls != 1 {
		t.Fatalf("identical retention sweep vacuum calls = %d, want unchanged at 1", vacuumCalls)
	}
}

package historydb

import (
	"context"
	"testing"
	"time"
)

func TestLoadSproutLifecycleByIDUsesExplicitHistoryIdentity(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	finished := time.Now().UTC().Truncate(time.Second)
	if err := store.RecordSproutRun(ctx, SproutRun{
		RunID: "history-run-7", StepID: "step-7", SessionID: "phytomer-3",
		Pollen: "codex", Substrate: "sample", Status: "matured",
		FruitRepository: "/repos/sample", FruitBranch: "sprout/task-step-7",
		FruitCommit: "0123456789abcdef", FruitPublicationState: FruitPublicationPublished,
		FruitCreatedAt: finished, StartedAt: finished.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("RecordSproutRun: %v", err)
	}

	evidence, found, err := store.LoadSproutLifecycleByID(ctx, "history-run-7")
	if err != nil || !found {
		t.Fatalf("LoadSproutLifecycleByID: found=%t err=%v", found, err)
	}
	if evidence.RunID != "history-run-7" || evidence.StepID != "step-7" ||
		evidence.SessionID != "phytomer-3" || evidence.Status != "matured" ||
		evidence.Substrate != "sample" || evidence.Pollen != "codex" ||
		evidence.FruitBranch != "sprout/task-step-7" ||
		evidence.FruitCommit != "0123456789abcdef" ||
		evidence.FruitPublicationState != FruitPublicationPublished ||
		!evidence.FruitCreatedAt.Equal(finished) {
		t.Fatalf("lifecycle evidence = %+v", evidence)
	}

	if _, found, err := store.LoadSproutLifecycleByID(ctx, "step-7"); err != nil || found {
		t.Fatalf("lookup by StepID found=%t err=%v, want no inferred relation", found, err)
	}
	if _, found, err := store.LoadSproutLifecycleByID(ctx, "missing-history-run"); err != nil || found {
		t.Fatalf("missing lookup found=%t err=%v, want unknown evidence", found, err)
	}
}

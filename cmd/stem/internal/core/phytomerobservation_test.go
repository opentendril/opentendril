package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestProjectPhytomerObservationBeforeSprout(t *testing.T) {
	obs := core.ProjectPhytomerObservation(core.SeedObservationEvidence{
		Handle:     "seed-1",
		Pollen:     "claude",
		PhytomerID: "tendril-1",
		Substrate:  "myrepo",
		Status:     "running",
	}, nil)
	if obs.Handle != "seed-1" || obs.PhytomerID != "tendril-1" || obs.Pollen != "claude" || obs.Substrate != "myrepo" {
		t.Fatalf("identities = %+v", obs)
	}
	if obs.Status != "running" || obs.Iterations != 0 {
		t.Fatalf("progress = status %q iterations %d", obs.Status, obs.Iterations)
	}
	if obs.Commit != "" || obs.Branch != "" || len(obs.Sprouts) != 0 {
		t.Fatalf("fabricated fruit or sprout: %+v", obs)
	}

	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"sprouts"`) {
		t.Fatalf("missing sprouts were rewritten as a list: %s", raw)
	}
	if strings.Contains(string(raw), `"commit"`) {
		t.Fatalf("missing commit was fabricated: %s", raw)
	}
}

func TestProjectPhytomerObservationDoesNotInventCommitFromBranch(t *testing.T) {
	obs := core.ProjectPhytomerObservation(core.SeedObservationEvidence{
		Handle:     "seed-1",
		PhytomerID: "tendril-1",
		Status:     core.SeedStatusSatisfied,
		Branch:     "tendril/seed-fruit",
	}, nil)
	if obs.Branch != "tendril/seed-fruit" {
		t.Fatalf("branch = %q", obs.Branch)
	}
	if obs.Commit != "" {
		t.Fatalf("commit was invented from the branch: %q", obs.Commit)
	}
}

func TestProjectPhytomerObservationKeepsRealFruitAndSprouts(t *testing.T) {
	diag := &core.ProviderDiagnostic{StatusCode: 401, Message: "User not found", Provider: "anthropic"}
	obs := core.ProjectPhytomerObservation(core.SeedObservationEvidence{
		Handle:     "seed-1",
		Pollen:     "claude",
		PhytomerID: "tendril-1",
		Substrate:  "myrepo",
		Status:     core.SeedStatusWithered,
		Iterations: 2,
		Error:      "provider refused the principal",
	}, []core.SproutObservationEvidence{
		{
			RunID:                    "run-a",
			Status:                   "matured",
			Provider:                 "anthropic",
			Model:                    "claude-sonnet",
			Outcome:                  "complete",
			FailureCategory:          string(core.FailureCategoryMatured),
			ProviderRequestAttempted: true,
			ToolInvocations:          3,
			StartedAt:                time.Unix(1, 0).UTC(),
		},
		{
			RunID:                    "run-b",
			Status:                   "withered",
			Provider:                 "anthropic",
			Model:                    "claude-sonnet",
			Outcome:                  "failed",
			FailureCategory:          string(core.FailureCategoryProviderAuthRejected),
			ProviderDiagnostic:       diag,
			ProviderRequestAttempted: true,
			ToolInvocations:          0,
			StartedAt:                time.Unix(2, 0).UTC(),
		},
	})
	if obs.Status != core.SeedStatusWithered || obs.Iterations != 2 {
		t.Fatalf("withered seed = %+v", obs)
	}
	if len(obs.Sprouts) != 2 {
		t.Fatalf("sprouts = %d, want 2", len(obs.Sprouts))
	}
	if obs.Sprouts[1].FailureCategory != string(core.FailureCategoryProviderAuthRejected) {
		t.Fatalf("failure category = %q", obs.Sprouts[1].FailureCategory)
	}
	if obs.Sprouts[1].ProviderDiagnostic == nil || obs.Sprouts[1].ProviderDiagnostic.StatusCode != 401 {
		t.Fatalf("provider diagnostic = %+v", obs.Sprouts[1].ProviderDiagnostic)
	}
	if obs.Sprouts[1].ToolInvocations != 0 || !obs.Sprouts[1].ProviderRequestAttempted {
		t.Fatalf("zero tools / request-attempted were rewritten: %+v", obs.Sprouts[1])
	}

	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, banned := range []string{"transcript", "chain-of-thought", "Bearer ", "sk-", "provider refused the principal"} {
		if strings.Contains(body, banned) {
			t.Fatalf("unsafe material %q leaked: %s", banned, body)
		}
	}
}

func TestProjectPhytomerObservationSatisfiedFruit(t *testing.T) {
	obs := core.ProjectPhytomerObservation(core.SeedObservationEvidence{
		Handle:     "seed-1",
		PhytomerID: "tendril-1",
		Status:     core.SeedStatusSatisfied,
		Iterations: 1,
		Branch:     "tendril/seed-fruit",
		Commit:     "abc123def456",
	}, nil)
	if obs.Branch != "tendril/seed-fruit" || obs.Commit != "abc123def456" {
		t.Fatalf("fruit = branch %q commit %q", obs.Branch, obs.Commit)
	}
}

func TestProjectPhytomerObservationOmitsPersistedUnsafeFields(t *testing.T) {
	obs := core.ProjectPhytomerObservation(core.SeedObservationEvidence{
		Handle:     "seed-1",
		PhytomerID: "tendril-1",
		Status:     core.SeedStatusWithered,
		Goal:       "PRIVATE_PROMPT_CONTENT",
		Diff:       "internal path /home/operator/private",
		Logs:       "Authorization: Bearer secret-token",
		Error:      "internal path /home/operator/private\nAuthorization: Bearer secret-token\nPRIVATE_PROMPT_CONTENT",
	}, []core.SproutObservationEvidence{{
		RunID:      "run-a",
		Status:     "withered",
		Transcript: "private reasoning SECRET_TOKEN=sk-secret",
		Output:     "chain-of-thought hidden",
		Error:      "Authorization: Bearer secret-token",
		StartedAt:  time.Unix(1, 0).UTC(),
	}})
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, banned := range []string{
		"internal path /home/operator/private",
		"Authorization: Bearer secret-token",
		"PRIVATE_PROMPT_CONTENT",
		"private reasoning",
		"SECRET_TOKEN",
		"sk-secret",
		"chain-of-thought",
	} {
		if strings.Contains(body, banned) {
			t.Fatalf("unsafe material %q leaked: %s", banned, body)
		}
	}
	if strings.Contains(body, `"error"`) {
		t.Fatalf("raw error field was released: %s", body)
	}
}

func TestProjectPhytomerObservationOrdersSproutsByStartThenRunID(t *testing.T) {
	later := time.Unix(20, 0).UTC()
	earlier := time.Unix(10, 0).UTC()
	obs := core.ProjectPhytomerObservation(core.SeedObservationEvidence{
		Handle: "seed-1", PhytomerID: "tendril-1", Status: "running",
	}, []core.SproutObservationEvidence{
		{RunID: "run-z", StartedAt: later},
		{RunID: "run-b", StartedAt: earlier},
		{RunID: "run-a", StartedAt: earlier},
	})
	if len(obs.Sprouts) != 3 {
		t.Fatalf("sprouts = %d, want 3", len(obs.Sprouts))
	}
	got := []string{obs.Sprouts[0].RunID, obs.Sprouts[1].RunID, obs.Sprouts[2].RunID}
	want := []string{"run-a", "run-b", "run-z"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sprout order = %v, want %v", got, want)
		}
	}
}

func TestObservePhytomerUsesSourceAndProjectsSafely(t *testing.T) {
	svc := core.NewService(nil).WithPhytomerObservationSource(core.PhytomerObservationSource{
		SeedByPhytomer: func(_ context.Context, phytomerID string) (core.SeedObservationEvidence, bool, error) {
			if phytomerID != "tendril-1" {
				return core.SeedObservationEvidence{}, false, nil
			}
			return core.SeedObservationEvidence{
				Handle:     "seed-1",
				Pollen:     "claude",
				PhytomerID: "tendril-1",
				Substrate:  "myrepo",
				Status:     "running",
				Error:      "Authorization: Bearer secret-token",
			}, true, nil
		},
		SproutsByPhytomer: func(_ context.Context, phytomerID string) ([]core.SproutObservationEvidence, error) {
			return []core.SproutObservationEvidence{{
				RunID:      "run-b",
				Status:     "running",
				Transcript: "PRIVATE_PROMPT_CONTENT",
				StartedAt:  time.Unix(2, 0).UTC(),
			}, {
				RunID:     "run-a",
				Status:    "running",
				StartedAt: time.Unix(1, 0).UTC(),
			}}, nil
		},
	})
	obs, err := svc.ObservePhytomer(context.Background(), "tendril-1")
	if err != nil {
		t.Fatalf("ObservePhytomer: %v", err)
	}
	if obs.Handle != "seed-1" || len(obs.Sprouts) != 2 || obs.Sprouts[0].RunID != "run-a" {
		t.Fatalf("observation = %+v", obs)
	}
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, banned := range []string{"Authorization: Bearer secret-token", "PRIVATE_PROMPT_CONTENT"} {
		if strings.Contains(body, banned) {
			t.Fatalf("unsafe material %q leaked: %s", banned, body)
		}
	}
}

func TestObservePhytomerNotFoundAndNotWired(t *testing.T) {
	if _, err := core.NewService(nil).ObservePhytomer(context.Background(), "tendril-1"); !errors.Is(err, core.ErrPhytomerObservationNotWired) {
		t.Fatalf("unwired = %v, want not wired", err)
	}
	svc := core.NewService(nil).WithPhytomerObservationSource(core.PhytomerObservationSource{
		SeedByPhytomer: func(context.Context, string) (core.SeedObservationEvidence, bool, error) {
			return core.SeedObservationEvidence{}, false, nil
		},
		SproutsByPhytomer: func(context.Context, string) ([]core.SproutObservationEvidence, error) {
			return nil, nil
		},
	})
	if _, err := svc.ObservePhytomer(context.Background(), "tendril-missing"); !errors.Is(err, core.ErrPhytomerObservationNotFound) {
		t.Fatalf("missing = %v, want not found", err)
	}
}

func TestSeedStatusIsTerminal(t *testing.T) {
	for _, status := range []string{core.SeedStatusSatisfied, core.SeedStatusExhausted, core.SeedStatusWithered} {
		if !core.SeedStatusIsTerminal(status) {
			t.Fatalf("%q should be terminal", status)
		}
	}
	for _, status := range []string{"", "running", "matured", "unknown"} {
		if core.SeedStatusIsTerminal(status) {
			t.Fatalf("%q should not be terminal", status)
		}
	}
}

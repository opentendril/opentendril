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

func projectObservation(t *testing.T, seed core.SeedObservationEvidence, sprouts []core.SproutObservationEvidence) core.PhytomerObservation {
	t.Helper()
	return projectObservationWithContinuations(t, seed, sprouts, nil)
}

func projectObservationWithContinuations(t *testing.T, seed core.SeedObservationEvidence, sprouts []core.SproutObservationEvidence, continuations []core.ContinuationObservationEvidence) core.PhytomerObservation {
	t.Helper()
	obs, err := core.ProjectPhytomerObservation(seed, sprouts, continuations)
	if err != nil {
		t.Fatalf("ProjectPhytomerObservation: %v", err)
	}
	return obs
}

func emptyContinuations(context.Context, string) ([]core.ContinuationObservationEvidence, error) {
	return nil, nil
}

func TestProjectPhytomerObservationBeforeSprout(t *testing.T) {
	obs := projectObservation(t, core.SeedObservationEvidence{
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
	if obs.Commit != "" || obs.Branch != "" || len(obs.Sprouts) != 0 || len(obs.Continuations) != 0 {
		t.Fatalf("fabricated fruit, sprout, or continuation: %+v", obs)
	}

	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"sprouts"`) {
		t.Fatalf("missing sprouts were rewritten as a list: %s", raw)
	}
	if strings.Contains(string(raw), `"continuations"`) {
		t.Fatalf("missing continuations were rewritten as a list: %s", raw)
	}
	if strings.Contains(string(raw), `"commit"`) {
		t.Fatalf("missing commit was fabricated: %s", raw)
	}
}

func TestProjectPhytomerObservationDoesNotInventCommitFromBranch(t *testing.T) {
	obs := projectObservation(t, core.SeedObservationEvidence{
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
	obs := projectObservation(t, core.SeedObservationEvidence{
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
			Pollen:                   "claude",
			Substrate:                "myrepo",
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
			Pollen:                   "claude",
			Substrate:                "myrepo",
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
	obs := projectObservation(t, core.SeedObservationEvidence{
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
	obs := projectObservation(t, core.SeedObservationEvidence{
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
	obs := projectObservation(t, core.SeedObservationEvidence{
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
				Pollen:     "claude",
				Substrate:  "myrepo",
				Status:     "running",
				Transcript: "PRIVATE_PROMPT_CONTENT",
				StartedAt:  time.Unix(2, 0).UTC(),
			}, {
				RunID:     "run-a",
				Pollen:    "claude",
				Substrate: "myrepo",
				Status:    "running",
				StartedAt: time.Unix(1, 0).UTC(),
			}}, nil
		},
		ContinuationsByPhytomer: emptyContinuations,
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

func TestObservePhytomerRefusesSproutWithOtherPollen(t *testing.T) {
	svc := core.NewService(nil).WithPhytomerObservationSource(core.PhytomerObservationSource{
		SeedByPhytomer: func(context.Context, string) (core.SeedObservationEvidence, bool, error) {
			return core.SeedObservationEvidence{
				Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1",
				Substrate: "myrepo", Status: "running",
			}, true, nil
		},
		SproutsByPhytomer: func(context.Context, string) ([]core.SproutObservationEvidence, error) {
			return []core.SproutObservationEvidence{{
				RunID:     "run-intruder",
				Pollen:    "codex",
				Substrate: "myrepo",
				Status:    "running",
				Provider:  "intruder-provider",
				Model:     "intruder-model",
			}}, nil
		},
		ContinuationsByPhytomer: emptyContinuations,
	})
	obs, err := svc.ObservePhytomer(context.Background(), "tendril-1")
	if !errors.Is(err, core.ErrPhytomerObservationOwnershipConflict) {
		t.Fatalf("other pollen = %v, want ownership conflict", err)
	}
	assertObservationDoesNotContain(t, obs, "run-intruder", "intruder-provider", "intruder-model", "codex")
}

func TestObservePhytomerRefusesSproutWithOtherSubstrate(t *testing.T) {
	svc := core.NewService(nil).WithPhytomerObservationSource(core.PhytomerObservationSource{
		SeedByPhytomer: func(context.Context, string) (core.SeedObservationEvidence, bool, error) {
			return core.SeedObservationEvidence{
				Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1",
				Substrate: "myrepo", Status: "running",
			}, true, nil
		},
		SproutsByPhytomer: func(context.Context, string) ([]core.SproutObservationEvidence, error) {
			return []core.SproutObservationEvidence{{
				RunID:     "run-otherrepo",
				Pollen:    "claude",
				Substrate: "otherrepo",
				Status:    "running",
				Provider:  "foreign-provider",
				Model:     "foreign-model",
			}}, nil
		},
		ContinuationsByPhytomer: emptyContinuations,
	})
	obs, err := svc.ObservePhytomer(context.Background(), "tendril-1")
	if !errors.Is(err, core.ErrPhytomerObservationOwnershipConflict) {
		t.Fatalf("other substrate = %v, want ownership conflict", err)
	}
	assertObservationDoesNotContain(t, obs, "run-otherrepo", "foreign-provider", "foreign-model", "otherrepo")
}

func TestObservePhytomerKeepsMatchingMultiSproutSeed(t *testing.T) {
	svc := core.NewService(nil).WithPhytomerObservationSource(core.PhytomerObservationSource{
		SeedByPhytomer: func(context.Context, string) (core.SeedObservationEvidence, bool, error) {
			return core.SeedObservationEvidence{
				Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1",
				Substrate: "myrepo", Status: "running",
			}, true, nil
		},
		SproutsByPhytomer: func(context.Context, string) ([]core.SproutObservationEvidence, error) {
			return []core.SproutObservationEvidence{
				{RunID: "run-b", Pollen: "claude", Substrate: "myrepo", Status: "running", Provider: "anthropic", Model: "claude-sonnet", StartedAt: time.Unix(2, 0).UTC()},
				{RunID: "run-a", Pollen: "claude", Substrate: "myrepo", Status: "matured", Provider: "anthropic", Model: "claude-sonnet", StartedAt: time.Unix(1, 0).UTC()},
			}, nil
		},
		ContinuationsByPhytomer: emptyContinuations,
	})
	obs, err := svc.ObservePhytomer(context.Background(), "tendril-1")
	if err != nil {
		t.Fatalf("matching sprouts: %v", err)
	}
	if len(obs.Sprouts) != 2 || obs.Sprouts[0].RunID != "run-a" || obs.Sprouts[1].RunID != "run-b" {
		t.Fatalf("matching multi-sprout = %+v", obs.Sprouts)
	}
}

func assertObservationDoesNotContain(t *testing.T, obs core.PhytomerObservation, banned ...string) {
	t.Helper()
	if len(obs.Sprouts) != 0 || len(obs.Continuations) != 0 || obs.Handle != "" {
		t.Fatalf("contradictory observation was released: %+v", obs)
	}
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, item := range banned {
		if strings.Contains(body, item) {
			t.Fatalf("contradictory material %q leaked: %s", item, body)
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
		ContinuationsByPhytomer: emptyContinuations,
	})
	if _, err := svc.ObservePhytomer(context.Background(), "tendril-missing"); !errors.Is(err, core.ErrPhytomerObservationNotFound) {
		t.Fatalf("missing = %v, want not found", err)
	}
	partial := core.NewService(nil).WithPhytomerObservationSource(core.PhytomerObservationSource{
		SeedByPhytomer: func(context.Context, string) (core.SeedObservationEvidence, bool, error) {
			return core.SeedObservationEvidence{Handle: "seed-1", PhytomerID: "tendril-1", Status: "running"}, true, nil
		},
		SproutsByPhytomer: func(context.Context, string) ([]core.SproutObservationEvidence, error) {
			return nil, nil
		},
	})
	if _, err := partial.ObservePhytomer(context.Background(), "tendril-1"); !errors.Is(err, core.ErrPhytomerObservationNotWired) {
		t.Fatalf("missing continuation loader = %v, want not wired", err)
	}
}

func TestSeedStatusIsTerminal(t *testing.T) {
	for _, status := range []string{core.SeedStatusSatisfied, core.SeedStatusExhausted, core.SeedStatusWithered, core.SeedStatusFruitPublicationFailed} {
		if !core.SeedStatusIsTerminal(status) {
			t.Fatalf("%q should be terminal", status)
		}
	}
	for _, status := range []string{"", "running", "settling", "matured", "unknown"} {
		if core.SeedStatusIsTerminal(status) {
			t.Fatalf("%q should not be terminal", status)
		}
	}
}

func TestProjectPhytomerObservationPublishesSafeFruitFailureDiagnostic(t *testing.T) {
	obs := projectObservation(t, core.SeedObservationEvidence{
		Handle:     "seed-publication-failure",
		PhytomerID: "tendril-publication-failure",
		Status:     core.SeedStatusFruitPublicationFailed,
		Iterations: 2,
		Branch:     "",
		Commit:     "",
		Error:      "raw upstream-secret-content",
		PublicationDiagnostic: &core.SeedPublicationDiagnostic{
			FailureCategory: core.SeedFailureCategoryFruitPublication,
			ExecutionStatus: core.SeedStatusSatisfied,
			Phase:           "commit-mutation",
			Outcome:         "reconciliation-unavailable",
			RetrySafe:       false,
			Message:         "read-only GitHub reconciliation could not establish the target state",
			RequestID:       "req-safe-123",
		},
	}, nil)
	if obs.Status != core.SeedStatusFruitPublicationFailed || obs.Branch != "" || obs.Commit != "" {
		t.Fatalf("publication failure observation = %+v", obs)
	}
	if obs.PublicationDiagnostic == nil || obs.PublicationDiagnostic.FailureCategory != core.SeedFailureCategoryFruitPublication {
		t.Fatalf("publication diagnostic = %+v", obs.PublicationDiagnostic)
	}
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "raw upstream-secret-content") {
		t.Fatalf("raw Seed error leaked into observation: %s", raw)
	}
}

func TestProjectPhytomerObservationIncludesVerificationDiagnostics(t *testing.T) {
	code := 1
	obs := projectObservation(t, core.SeedObservationEvidence{
		Handle:     "seed-1",
		PhytomerID: "tendril-1",
		Status:     core.SeedStatusExhausted,
		Iterations: 1,
		VerificationDiagnostics: []core.SeedVerificationDiagnostic{{
			Iteration: 1,
			Outcome:   core.SeedVerificationOutcomePredicateFailed,
			ExitCode:  &code,
			TimedOut:  false,
			Message:   "verify command exited 1",
		}},
	}, nil)
	if len(obs.VerificationDiagnostics) != 1 {
		t.Fatalf("verification diagnostics = %+v", obs.VerificationDiagnostics)
	}
	if obs.VerificationDiagnostics[0].Outcome != core.SeedVerificationOutcomePredicateFailed {
		t.Fatalf("outcome = %q", obs.VerificationDiagnostics[0].Outcome)
	}
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"verificationDiagnostics"`) || !strings.Contains(body, `"predicate-failed"`) {
		t.Fatalf("observation omitted verification diagnostics: %s", body)
	}
	if strings.Contains(body, "/home/") || strings.Contains(body, "Bearer ") {
		t.Fatalf("unsafe material leaked: %s", body)
	}
}

func TestProjectPhytomerObservationOrdersContinuationsBySequenceThenID(t *testing.T) {
	obs := projectObservationWithContinuations(t, core.SeedObservationEvidence{
		Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1", Substrate: "myrepo", Status: "running",
	}, nil, []core.ContinuationObservationEvidence{
		{ContinuationID: "continuation-z", Pollen: "claude", Substrate: "myrepo", Sequence: 2, DeliveryState: core.ContinuationDeliveryPending},
		{ContinuationID: "continuation-b", Pollen: "claude", Substrate: "myrepo", Sequence: 1, DeliveryState: core.ContinuationDeliveryDelivered},
		{ContinuationID: "continuation-a", Pollen: "claude", Substrate: "myrepo", Sequence: 1, DeliveryState: core.ContinuationDeliveryFailed},
	})
	if len(obs.Continuations) != 3 {
		t.Fatalf("continuations = %d, want 3", len(obs.Continuations))
	}
	got := []string{obs.Continuations[0].ContinuationID, obs.Continuations[1].ContinuationID, obs.Continuations[2].ContinuationID}
	want := []string{"continuation-a", "continuation-b", "continuation-z"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("continuation order = %v, want %v", got, want)
		}
	}
	if obs.Continuations[0].Sequence != 1 || obs.Continuations[2].Sequence != 2 {
		t.Fatalf("continuation sequences = %+v", obs.Continuations)
	}
}

func TestProjectPhytomerObservationOmitsContinuationSecrets(t *testing.T) {
	obs := projectObservationWithContinuations(t, core.SeedObservationEvidence{
		Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1", Substrate: "myrepo",
		Status: core.SeedStatusSatisfied, Goal: "PRIVATE_PROMPT_CONTENT",
	}, nil, []core.ContinuationObservationEvidence{{
		ContinuationID: "continuation-1",
		Pollen:         "claude",
		Substrate:      "myrepo",
		Sequence:       1,
		DeliveryState:  core.ContinuationDeliveryDelivered,
	}})
	if len(obs.Continuations) != 1 || obs.Continuations[0].ContinuationID != "continuation-1" {
		t.Fatalf("continuations = %+v", obs.Continuations)
	}
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, banned := range []string{
		"intent", "idempotencyKey", "intentDigest", "PRIVATE_PROMPT_CONTENT", "keep going secretly",
	} {
		if strings.Contains(body, banned) {
			t.Fatalf("unsafe continuation material %q leaked: %s", banned, body)
		}
	}
	if !strings.Contains(body, `"continuationId"`) || !strings.Contains(body, `"deliveryState"`) {
		t.Fatalf("safe continuation summary missing: %s", body)
	}
}

func TestProjectPhytomerObservationRefusesUnknownContinuationState(t *testing.T) {
	for _, state := range []string{"", "paused", "unknown"} {
		obs, err := core.ProjectPhytomerObservation(core.SeedObservationEvidence{
			Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1", Substrate: "myrepo", Status: "running",
		}, nil, []core.ContinuationObservationEvidence{{
			ContinuationID: "continuation-bad",
			Pollen:         "claude",
			Substrate:      "myrepo",
			Sequence:       1,
			DeliveryState:  state,
		}})
		if !errors.Is(err, core.ErrPhytomerObservationContinuationInvalid) {
			t.Fatalf("state %q = %v, want invalid continuation evidence", state, err)
		}
		banned := []string{"continuation-bad"}
		if state != "" {
			banned = append(banned, state)
		}
		assertObservationDoesNotContain(t, obs, banned...)
	}
}

func TestProjectPhytomerObservationRefusesContinuationOwnershipMismatch(t *testing.T) {
	obs, err := core.ProjectPhytomerObservation(core.SeedObservationEvidence{
		Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1", Substrate: "myrepo", Status: "running",
	}, nil, []core.ContinuationObservationEvidence{{
		ContinuationID: "continuation-other-pollen",
		Pollen:         "codex",
		Substrate:      "myrepo",
		Sequence:       1,
		DeliveryState:  core.ContinuationDeliveryPending,
	}})
	if !errors.Is(err, core.ErrPhytomerObservationOwnershipConflict) {
		t.Fatalf("other pollen = %v, want ownership conflict", err)
	}
	assertObservationDoesNotContain(t, obs, "continuation-other-pollen", "codex")

	obs, err = core.ProjectPhytomerObservation(core.SeedObservationEvidence{
		Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1", Substrate: "myrepo", Status: "running",
	}, nil, []core.ContinuationObservationEvidence{{
		ContinuationID: "continuation-other-repo",
		Pollen:         "claude",
		Substrate:      "otherrepo",
		Sequence:       1,
		DeliveryState:  core.ContinuationDeliveryPending,
	}})
	if !errors.Is(err, core.ErrPhytomerObservationOwnershipConflict) {
		t.Fatalf("other substrate = %v, want ownership conflict", err)
	}
	assertObservationDoesNotContain(t, obs, "continuation-other-repo", "otherrepo")
}

func TestObservePhytomerProjectsContinuationsOnTerminalSeed(t *testing.T) {
	svc := core.NewService(nil).WithPhytomerObservationSource(core.PhytomerObservationSource{
		SeedByPhytomer: func(context.Context, string) (core.SeedObservationEvidence, bool, error) {
			return core.SeedObservationEvidence{
				Handle: "seed-1", Pollen: "claude", PhytomerID: "tendril-1",
				Substrate: "myrepo", Status: core.SeedStatusSatisfied,
			}, true, nil
		},
		SproutsByPhytomer: emptySprouts,
		ContinuationsByPhytomer: func(context.Context, string) ([]core.ContinuationObservationEvidence, error) {
			return []core.ContinuationObservationEvidence{{
				ContinuationID: "continuation-1",
				Pollen:         "claude",
				Substrate:      "myrepo",
				Sequence:       1,
				DeliveryState:  core.ContinuationDeliveryDelivered,
			}}, nil
		},
	})
	obs, err := svc.ObservePhytomer(context.Background(), "tendril-1")
	if err != nil {
		t.Fatalf("ObservePhytomer: %v", err)
	}
	if obs.Status != core.SeedStatusSatisfied || len(obs.Continuations) != 1 {
		t.Fatalf("terminal observation = %+v", obs)
	}
	if obs.Continuations[0].ContinuationID != "continuation-1" || obs.Continuations[0].DeliveryState != core.ContinuationDeliveryDelivered {
		t.Fatalf("terminal continuation = %+v", obs.Continuations[0])
	}
}

func emptySprouts(context.Context, string) ([]core.SproutObservationEvidence, error) {
	return nil, nil
}

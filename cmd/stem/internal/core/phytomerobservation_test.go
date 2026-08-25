package core_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestProjectPhytomerObservationBeforeSprout(t *testing.T) {
	obs := core.ProjectPhytomerObservation(core.SeedObservation{
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
	obs := core.ProjectPhytomerObservation(core.SeedObservation{
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
	obs := core.ProjectPhytomerObservation(core.SeedObservation{
		Handle:     "seed-1",
		Pollen:     "claude",
		PhytomerID: "tendril-1",
		Substrate:  "myrepo",
		Status:     core.SeedStatusWithered,
		Iterations: 2,
		Error:      "provider refused the principal",
	}, []core.SproutObservation{
		{
			RunID:                    "run-a",
			Status:                   "matured",
			Provider:                 "anthropic",
			Model:                    "claude-sonnet",
			Outcome:                  "complete",
			FailureCategory:          string(core.FailureCategoryMatured),
			ProviderRequestAttempted: true,
			ToolInvocations:          3,
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
		},
	})
	if obs.Status != core.SeedStatusWithered || obs.Iterations != 2 || obs.Error == "" {
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
	for _, banned := range []string{"transcript", "chain-of-thought", "Bearer ", "sk-"} {
		if strings.Contains(body, banned) {
			t.Fatalf("unsafe material %q leaked: %s", banned, body)
		}
	}
}

func TestProjectPhytomerObservationSatisfiedFruit(t *testing.T) {
	obs := core.ProjectPhytomerObservation(core.SeedObservation{
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

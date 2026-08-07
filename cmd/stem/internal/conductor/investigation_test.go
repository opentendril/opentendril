package conductor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// TestRunSproutInvestigationRecordsAnHonestAccount pins what an
// investigation-only run leaves behind.
//
// The shape being guarded is one the run has already gone wrong in once: the
// status file's verdict was assembled separately from the terminal event's, and
// the two disagreed on a reachable input — an investigation whose report came
// back empty was recorded as "reported" on disk while the bus said
// "no-engagement". Both surfaces now derive from the same classifier, and this
// test is what would notice if they stopped.
//
// It asserts the two endings together rather than in isolation, because the
// defect was not either value being wrong on its own; it was them differing.
func TestRunSproutInvestigationRecordsAnHonestAccount(t *testing.T) {
	testCases := []struct {
		name string
		// response is what the Sprout came back with. An investigation's whole
		// product is its report, so an empty one is a run that never engaged —
		// not a run that "investigated and found nothing".
		response     string
		wantOutcome  string
		wantTerminal eventbus.EventType
	}{
		{
			name:         "a report that says something matures as reported",
			response:     "the defect is in the retry loop",
			wantOutcome:  SproutOutcomeReported,
			wantTerminal: eventbus.EventSproutMatured,
		},
		{
			name:         "an empty report withers as no-engagement",
			response:     "",
			wantOutcome:  SproutOutcomeNoEngagement,
			wantTerminal: eventbus.EventSproutWithered,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := newOutcomeTestRepo(t)
			cwd := chdirToTempDir(t)
			writePatienceSubstrate(t, cwd, "investigated", root, "")

			stubRunSproutCollaborators(t, root, &mockSproutRunner{response: testCase.response}, nil)

			bus := eventbus.New()
			events := recordSproutLifecycle(bus)

			statusPath := filepath.Join(root, "tendril-status.json")
			orch := &DockerOrchestrator{
				Substrate:        "investigated",
				StepID:           "investigation-step",
				EventBus:         bus,
				Investigation:    true,
				DisableMergeBack: true,
				StatusPath:       statusPath,
			}
			if _, err := orch.RunSprout(context.Background(), "investigate and report"); err != nil {
				t.Fatalf("RunSprout failed: %v", err)
			}

			// The account has to exist at all. An investigation run bounds what
			// the Sprout may change; it does not bound the record of the run.
			payload, err := os.ReadFile(statusPath)
			if err != nil {
				t.Fatalf("investigation run wrote no status file: %v", err)
			}
			var status sproutExecutionStatus
			if err := json.Unmarshal(payload, &status); err != nil {
				t.Fatalf("decode status file: %v", err)
			}

			if status.Status != testCase.wantOutcome {
				t.Fatalf("status file says %q, want %q", status.Status, testCase.wantOutcome)
			}

			terminal := findTerminalEvent(t, events, testCase.wantTerminal)
			eventOutcome, _ := terminal.Data["outcome"].(string)
			if eventOutcome != testCase.wantOutcome {
				t.Fatalf("terminal event says %q, want %q", eventOutcome, testCase.wantOutcome)
			}

			// The assertion the earlier defect needed and did not have: the two
			// surfaces agree. Checking each against a constant separately would
			// still pass if one were derived and the other hardcoded to a value
			// that happened to match on the case being tested.
			if status.Status != eventOutcome {
				t.Fatalf("status file says %q and the terminal event says %q; the run's two accounts of itself disagree", status.Status, eventOutcome)
			}

			// filesModified is absent because nothing looked, not because
			// nothing changed. A bare null cannot say which, so the reason has
			// to travel with it.
			if len(status.FilesModified) != 0 {
				t.Fatalf("status file lists %v as modified; an investigation run takes no diff", status.FilesModified)
			}
			if status.FilesUnmeasured == "" {
				t.Fatal("status file leaves filesModified unexplained; measured-zero and never-looked are different answers")
			}
			if !strings.Contains(strings.ToLower(status.FilesUnmeasured), "investigation") {
				t.Fatalf("filesUnmeasured = %q, which does not say the run was investigation-only", status.FilesUnmeasured)
			}
		})
	}
}

// TestRunSproutOrdinaryRunStillCommitsItsAccount is the companion negative: the
// investigation path must not have quietly become the path every run takes.
// Without this, removing the Investigation guard from the early return would
// leave the tests above green and silently stop ordinary runs committing.
func TestRunSproutOrdinaryRunStillCommitsItsAccount(t *testing.T) {
	root := newOutcomeTestRepo(t)
	cwd := chdirToTempDir(t)
	writePatienceSubstrate(t, cwd, "ordinary", root, "")

	captured := stubRunSproutCollaborators(t, root, &mockSproutRunner{response: "done", wroteWorkspace: true}, []string{"pkg/thing.go"})

	orch := &DockerOrchestrator{
		Substrate:        "ordinary",
		StepID:           "ordinary-step",
		DisableMergeBack: true,
	}
	report, err := orch.RunSprout(context.Background(), "do the work")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}

	if report.Outcome == SproutOutcomeReported {
		t.Fatalf("an ordinary run reported outcome %q; the investigation shape is not opt-in", report.Outcome)
	}
	if captured.Status == "" {
		t.Fatal("the commit path never ran for an ordinary run; the investigation early return swallowed it")
	}
	if len(report.FilesModified) != 1 {
		t.Fatalf("report.FilesModified = %#v, want the measured file; the ordinary run stopped measuring", report.FilesModified)
	}
}

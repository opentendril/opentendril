package conductor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

// A step carrying an explicit Command must route to the deterministic verifier
// runner and never touch the LLM sprout path.
func TestCommandStepRoutesToVerifier(t *testing.T) {
	origVerifier := runVerifierCommandFn
	origSprout := runSequenceSproutFn
	t.Cleanup(func() {
		runVerifierCommandFn = origVerifier
		runSequenceSproutFn = origSprout
	})

	var gotCommand []string
	var gotWorkspace string
	runVerifierCommandFn = func(ctx context.Context, providerName, workspacePath string, command []string) (string, error) {
		gotWorkspace = workspacePath
		gotCommand = command
		return "🔬 ok", nil
	}
	runSequenceSproutFn = func(ctx context.Context, orch *DockerOrchestrator, taskPrompt string) (string, error) {
		t.Fatalf("LLM sprout path must not run for a deterministic command step")
		return "", nil
	}

	seq := &Sequence{Name: "verifier-test"}
	step := &SequenceStep{ID: "verifier-build", Command: []string{"go", "build", "./..."}}

	out, err := defaultSequenceStepRunnerWithOpts(context.Background(), seq, step, t.TempDir(), nil, "", "", "")
	if err != nil {
		t.Fatalf("runner returned error: %v", err)
	}
	if out != "🔬 ok" {
		t.Fatalf("unexpected output %q", out)
	}
	if strings.Join(gotCommand, " ") != "go build ./..." {
		t.Fatalf("verifier got command %v", gotCommand)
	}
	if strings.TrimSpace(gotWorkspace) == "" {
		t.Fatalf("verifier got empty workspace path")
	}
}

// A failed deterministic verifier step reports its result; it must not bud an
// LLM Debugger the way an LLM-driven verifier step does.
func TestShouldBudRecursiveDebuggerSkipsCommandSteps(t *testing.T) {
	llmVerifier := &SequenceStep{ID: "verifier-build"}
	if !shouldBudRecursiveDebugger(llmVerifier) {
		t.Fatalf("expected an LLM verifier step to bud a debugger")
	}

	commandVerifier := &SequenceStep{ID: "verifier-build", Command: []string{"go", "build", "./..."}}
	if shouldBudRecursiveDebugger(commandVerifier) {
		t.Fatalf("expected a deterministic command step not to bud a debugger")
	}
}

func TestSproutBuildSpecVerifierImage(t *testing.T) {
	context, dockerfile, err := sproutBuildSpec(verifierImage)
	if err != nil {
		t.Fatalf("sproutBuildSpec(%q) error: %v", verifierImage, err)
	}
	if context == "" || dockerfile == "" {
		t.Fatalf("verifier image not mapped: context=%q dockerfile=%q", context, dockerfile)
	}
	if filepath.Base(filepath.Dir(dockerfile)) != "go-verifier" {
		t.Fatalf("unexpected dockerfile path %q", dockerfile)
	}
}

func TestFormatVerifierReport(t *testing.T) {
	pass := formatVerifierReport([]string{"go", "build", "./..."}, terrarium.CommandResult{ExitCode: 0, Stdout: "ok\n"})
	if !strings.Contains(pass, "PASSED") || !strings.Contains(pass, "go build ./...") {
		t.Fatalf("pass report missing markers: %q", pass)
	}

	fail := formatVerifierReport([]string{"go", "test", "./..."}, terrarium.CommandResult{ExitCode: 1, Stderr: "FAIL pkg\n"})
	if !strings.Contains(fail, "FAILED") || !strings.Contains(fail, "FAIL pkg") {
		t.Fatalf("fail report missing markers: %q", fail)
	}
}

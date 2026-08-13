package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

// attributionToolCatalog advertises one tool of every shape the seam has to
// judge: two that cannot write, two that can, one the Stem answers itself, and
// one whose name means nothing here.
func attributionToolCatalog() []ToolDefinition {
	return []ToolDefinition{
		{Name: "readFile"},
		{Name: "listFiles"},
		{Name: "writeFile"},
		{Name: "execCommand"},
		{Name: "gitCommit"},
		{Name: "runFitnessSweep"},
	}
}

// TestSproutRecordsWhichToolsCouldWrite drives the tool loop and reads the
// signal off the result. It is the seam the outcome vocabulary now rests on:
// what the model asked the terrarium to do, rather than what the mount looks
// like afterwards.
func TestSproutRecordsWhichToolsCouldWrite(t *testing.T) {
	cases := []struct {
		name      string
		toolCall  string
		wantWrote bool
	}{
		{
			name:      "reading a file is not writing",
			toolCall:  `{"tool":"readFile","arguments":{"path":"README.md"}}`,
			wantWrote: false,
		},
		{
			name:      "listing files is not writing",
			toolCall:  `{"tool":"listFiles","arguments":{"path":"."}}`,
			wantWrote: false,
		},
		{
			name:      "writing a file is writing",
			toolCall:  `{"tool":"writeFile","arguments":{"path":"pkg/thing.go","content":"package pkg\n"}}`,
			wantWrote: true,
		},
		{
			// Nothing at this seam can tell `ls` from `rm -rf`, so a shell
			// command counts. Crediting the model with a diff it did not cause
			// is visible to a reviewer; dropping a file it did write is not.
			name:      "a shell command counts as writing",
			toolCall:  `{"tool":"execCommand","arguments":{"command":"ls -la"}}`,
			wantWrote: true,
		},
		{
			// The default the exclusion set exists to produce. A tool nobody
			// here has heard of is credited to the model.
			name:      "an unfamiliar tool counts as writing",
			toolCall:  `{"tool":"runFitnessSweep","arguments":{}}`,
			wantWrote: true,
		},
		{
			// Answered by the Stem with its commit policy and never handed to
			// the terrarium, so it cannot have written anything.
			name:      "an intercepted commit never reaches the workspace",
			toolCall:  `{"tool":"gitCommit","arguments":{"message":"wip"}}`,
			wantWrote: false,
		},
		{
			// Rejected before the terrarium sees it, for the same reason.
			name:      "a tool the terrarium does not offer never reaches the workspace",
			toolCall:  `{"tool":"summonKraken","arguments":{}}`,
			wantWrote: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			workspace := t.TempDir()
			client := &fakeLLM{responses: []string{testCase.toolCall, `{"final":"done"}`}}
			session := &fakeSession{tools: attributionToolCatalog()}

			sprout, err := newSprout(context.Background(), workspace, workspace, "", client, session, nil, "", "")
			if err != nil {
				t.Fatalf("newSprout returned error: %v", err)
			}

			result, err := sprout.Run(context.Background(), "do the task")
			if err != nil {
				t.Fatalf("Sprout.Run returned error: %v", err)
			}

			if result.WroteWorkspace != testCase.wantWrote {
				t.Fatalf("result.WroteWorkspace = %v, want %v after %s", result.WroteWorkspace, testCase.wantWrote, testCase.toolCall)
			}
		})
	}
}

// TestSproutRemembersAWriteAcrossLaterReads pins the accumulation: the signal
// is "did the model write at any point", not "what was the last thing it did".
// A run that edits a file and then re-reads it has still written.
func TestSproutRemembersAWriteAcrossLaterReads(t *testing.T) {
	workspace := t.TempDir()
	client := &fakeLLM{responses: []string{
		`{"tool":"writeFile","arguments":{"path":"pkg/thing.go","content":"package pkg\n"}}`,
		`{"tool":"readFile","arguments":{"path":"pkg/thing.go"}}`,
		`{"final":"done"}`,
	}}
	session := &fakeSession{tools: attributionToolCatalog()}

	sprout, err := newSprout(context.Background(), workspace, workspace, "", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout returned error: %v", err)
	}

	result, err := sprout.Run(context.Background(), "edit then check")
	if err != nil {
		t.Fatalf("Sprout.Run returned error: %v", err)
	}
	if !result.WroteWorkspace {
		t.Fatal("result.WroteWorkspace = false after a write followed by a read; the signal is being overwritten rather than accumulated")
	}
	if len(session.calls) != 2 {
		t.Fatalf("the terrarium saw %d calls, want 2; the loop did not run as written", len(session.calls))
	}
}

// failingAfterFirstTurnLLM answers once and then breaks, standing in for a run
// cut off after it had already started working — a watchdog kill, a provider
// dropping the connection, a budget expiring under the turn.
type failingAfterFirstTurnLLM struct {
	fakeLLM
	err error
}

func (f *failingAfterFirstTurnLLM) Call(ctx context.Context, messages []llm.Message) (string, error) {
	if len(f.responses) == 0 {
		return "", f.err
	}
	return f.fakeLLM.Call(ctx, messages)
}

func (f *failingAfterFirstTurnLLM) CallStream(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (string, error) {
	if len(f.responses) == 0 {
		if tokenChan != nil {
			close(tokenChan)
		}
		return "", f.err
	}
	return f.fakeLLM.CallStream(ctx, messages, tokenChan)
}

func (f *failingAfterFirstTurnLLM) CallPrompt(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if len(f.responses) == 0 {
		return "", f.err
	}
	return f.fakeLLM.CallPrompt(ctx, systemPrompt, userPrompt)
}

func (f *failingAfterFirstTurnLLM) CallWithResult(ctx context.Context, messages []llm.Message) (llm.Result, error) {
	if len(f.responses) == 0 {
		return llm.Result{}, f.err
	}
	return f.fakeLLM.CallWithResult(ctx, messages)
}

func (f *failingAfterFirstTurnLLM) CallStreamWithResult(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (llm.Result, error) {
	if len(f.responses) == 0 {
		if tokenChan != nil {
			close(tokenChan)
		}
		return llm.Result{}, f.err
	}
	return f.fakeLLM.CallStreamWithResult(ctx, messages, tokenChan)
}

func (f *failingAfterFirstTurnLLM) CallPromptWithResult(ctx context.Context, systemPrompt, userPrompt string) (llm.Result, error) {
	if len(f.responses) == 0 {
		return llm.Result{}, f.err
	}
	return f.fakeLLM.CallPromptWithResult(ctx, systemPrompt, userPrompt)
}

// TestSproutReportsItsWritesEvenWhenTheRunBreaks pins the error return.
//
// A run that wrote and then broke is still committed by the post-mortem — the
// work exists on disk whatever ended the run. If the failing return forgot the
// write signal, that work would be attributed to nobody and dropped from the
// commit, which is exactly the failure the narrowing must not introduce.
func TestSproutReportsItsWritesEvenWhenTheRunBreaks(t *testing.T) {
	workspace := t.TempDir()
	breakage := errors.New("provider dropped the connection")
	client := &failingAfterFirstTurnLLM{
		fakeLLM: fakeLLM{responses: []string{
			`{"tool":"writeFile","arguments":{"path":"pkg/thing.go","content":"package pkg\n"}}`,
		}},
		err: breakage,
	}
	session := &fakeSession{tools: attributionToolCatalog()}

	sprout, err := newSprout(context.Background(), workspace, workspace, "", client, session, nil, "", "")
	if err != nil {
		t.Fatalf("newSprout returned error: %v", err)
	}

	result, runErr := sprout.Run(context.Background(), "write then break")
	if !errors.Is(runErr, breakage) {
		t.Fatalf("Sprout.Run error = %v, want %v", runErr, breakage)
	}
	if !result.WroteWorkspace {
		t.Fatal("result.WroteWorkspace = false on a run that wrote and then broke; the post-mortem would drop that work from the commit")
	}
}

// TestRunSproutAttributesOnlyTheModelsChanges is the divergence itself, driven
// through the whole run.
//
// The mount's diff is not empty and the model never asked to write. That is
// not a contrived pairing: OpenTendril writes a repository map, a memory map
// and its own encrypted index into the workspace before the run, and the
// epigenetic genome into it afterwards — which a checkout that persists still
// carries when the next run measures it. So a run that only reads finds a diff
// waiting for it, and reported "complete" on the strength of it, committing
// and pushing files no task asked for as the Sprout's work.
//
// The file below is deliberately an ordinary repository path rather than one of
// those artifacts. A hard-coded exclusion list — the shape this codebase
// already carries for the index key and the repository map — would pass a test
// written against a known artifact name, and would need a new entry every time
// OpenTendril learns another write.
func TestRunSproutAttributesOnlyTheModelsChanges(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	runner := &stubSproutRunner{result: sproutResult{
		Response:       "I read the code and stopped",
		WroteWorkspace: false,
	}}
	captured := stubRunSproutCollaborators(t, root, runner, []string{"notes/scratch.md"})

	bus := eventbus.New()
	events := recordSproutLifecycle(bus)

	report, err := (&DockerOrchestrator{
		Substrate: root,
		StepID:    "attribution-runsprout",
		SessionID: "attribution-session",
		EventBus:  bus,
	}).RunSprout(context.Background(), "investigate and report")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}

	if report.Outcome != SproutOutcomeNoChanges {
		t.Fatalf("report.Outcome = %q, want %q; the model called no writing tool, so the run changed nothing", report.Outcome, SproutOutcomeNoChanges)
	}
	if report.FilesModified == nil {
		t.Fatal("report.FilesModified = nil; the diff was taken, so a measured-empty list is the honest answer and nil reads as unmeasured")
	}
	if len(report.FilesModified) != 0 {
		t.Fatalf("report.FilesModified = %v, want empty; the model wrote none of them", report.FilesModified)
	}
	if report.FilesUnmeasured != "" {
		t.Fatalf("report.FilesUnmeasured = %q, want empty; the measurement succeeded", report.FilesUnmeasured)
	}

	// The commit stages exactly the file list it is handed, so this is what
	// keeps OpenTendril's own bookkeeping out of the Sprout's commit.
	if captured.Status != "" {
		t.Fatalf("commit status = %q, want empty (commitTerrariumExecutionFn must not run for non-reviewable outcomes)", captured.Status)
	}
	if len(captured.FilesModified) != 0 {
		t.Fatalf("the commit was handed %v to stage; a run that wrote nothing has nothing to stage", captured.FilesModified)
	}

	terminal := append(
		filterEvents(*events, eventbus.EventSproutMatured),
		filterEvents(*events, eventbus.EventSproutWithered)...,
	)
	if len(terminal) != 1 {
		t.Fatalf("published %d terminal lifecycle events, want exactly 1: %+v", len(terminal), terminal)
	}
	if got := terminal[0].Data["outcome"]; got != SproutOutcomeNoChanges {
		t.Fatalf("terminal event outcome = %v, want %q", got, SproutOutcomeNoChanges)
	}
	published, present := terminal[0].Data["filesModified"].([]string)
	if !present {
		t.Fatalf("terminal event carries no filesModified; a measured-empty run must say so rather than go silent. Data = %#v", terminal[0].Data)
	}
	if len(published) != 0 {
		t.Fatalf("terminal event filesModified = %v, want empty", published)
	}
}

// TestRunSproutStillReportsWhatTheModelWrote is the negative companion. Zeroing
// the file list unconditionally would pass every assertion above and lose every
// real run's work, so the same setup with the write signal on must report the
// measured diff untouched.
func TestRunSproutStillReportsWhatTheModelWrote(t *testing.T) {
	root := newOutcomeTestRepo(t)
	chdirToTempDir(t)

	runner := &stubSproutRunner{result: sproutResult{
		Response:       "edited the package",
		WroteWorkspace: true,
	}}
	captured := stubRunSproutCollaborators(t, root, runner, []string{"notes/scratch.md", "pkg/thing.go"})

	report, err := (&DockerOrchestrator{
		Substrate: root,
		StepID:    "attribution-runsprout-wrote",
	}).RunSprout(context.Background(), "edit the package")
	if err != nil {
		t.Fatalf("RunSprout failed: %v", err)
	}

	if report.Outcome != SproutOutcomeComplete {
		t.Fatalf("report.Outcome = %q, want %q", report.Outcome, SproutOutcomeComplete)
	}
	if len(report.FilesModified) != 2 {
		t.Fatalf("report.FilesModified = %v, want both measured files", report.FilesModified)
	}
	if len(captured.FilesModified) != 2 {
		t.Fatalf("the commit was handed %v to stage, want both measured files", captured.FilesModified)
	}
}

// TestRunSequenceSproutAtPathAttributesOnlyTheModelsChanges covers the other
// call site. Both paths carry the post-mortem, and a fix to one of them leaves
// the other reporting the same vacuous run.
func TestRunSequenceSproutAtPathAttributesOnlyTheModelsChanges(t *testing.T) {
	cases := []struct {
		name        string
		wroteResult bool
		wantOutcome string
		wantFiles   int
	}{
		{
			name:        "model wrote nothing",
			wroteResult: false,
			wantOutcome: SproutOutcomeNoChanges,
			wantFiles:   0,
		},
		{
			name:        "model wrote",
			wroteResult: true,
			wantOutcome: SproutOutcomeComplete,
			wantFiles:   1,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := newOutcomeTestRepo(t)
			cwd := chdirToTempDir(t)
			writePatienceSubstrate(t, cwd, "attribution", root, "")

			stubSequenceAttributionCollaborators(t, root, &stubSproutRunner{result: sproutResult{
				Response:       "the run's answer",
				WroteWorkspace: testCase.wroteResult,
			}})
			captured := stubSequenceCommit(t)
			capturePostMortemContext(t, []string{"notes/scratch.md"}, nil)

			result, err := runSequenceSproutAtPath(context.Background(), &DockerOrchestrator{
				Substrate:        "attribution",
				StepID:           "attribution-seqpath",
				DisableMergeBack: true,
			}, "do the task", root, root)
			if err != nil {
				t.Fatalf("runSequenceSproutAtPath failed: %v", err)
			}

			if result.Outcome != testCase.wantOutcome {
				t.Fatalf("result.Outcome = %q, want %q", result.Outcome, testCase.wantOutcome)
			}
			if result.FilesModified == nil {
				t.Fatal("result.FilesModified = nil; the diff was taken, so nil reads as unmeasured")
			}
			if len(result.FilesModified) != testCase.wantFiles {
				t.Fatalf("result.FilesModified = %v, want %d file(s)", result.FilesModified, testCase.wantFiles)
			}
			if len(captured.FilesModified) != testCase.wantFiles {
				t.Fatalf("the commit was handed %v to stage, want %d file(s)", captured.FilesModified, testCase.wantFiles)
			}
			if testCase.wantOutcome == SproutOutcomeComplete {
				if captured.Status != testCase.wantOutcome {
					t.Fatalf("recorded status = %q, want %q", captured.Status, testCase.wantOutcome)
				}
			} else if captured.Status != "" {
				t.Fatalf("recorded status = %q, want empty (commitTerrariumExecutionFn must not run for non-reviewable outcomes)", captured.Status)
			}
		})
	}
}

// stubSequenceAttributionCollaborators fakes the sequence path's collaborators
// around a caller-supplied Sprout, so the run's own result is the only thing
// the assertions vary.
func stubSequenceAttributionCollaborators(t *testing.T, root string, runner sproutRunner) {
	t.Helper()

	originalEnsure := ensureSproutImageFn
	originalCreateShadow := createShadowWorktreeFn
	originalRemoveShadow := removeShadowWorktreeFn
	originalInjectCache := injectMycorrhizalCacheFn
	originalNewSprout := newSproutFn
	originalStash := stashHostWorkspaceFn
	originalSession := startTerrariumSessionFn
	originalDiff := collectGitDiffFn
	t.Cleanup(func() {
		ensureSproutImageFn = originalEnsure
		createShadowWorktreeFn = originalCreateShadow
		removeShadowWorktreeFn = originalRemoveShadow
		injectMycorrhizalCacheFn = originalInjectCache
		newSproutFn = originalNewSprout
		stashHostWorkspaceFn = originalStash
		startTerrariumSessionFn = originalSession
		collectGitDiffFn = originalDiff
	})

	ensureSproutImageFn = func(ctx context.Context, imageName string) error { return nil }
	createShadowWorktreeFn = func(sourcePath, substrateBranch string) (string, error) { return root, nil }
	removeShadowWorktreeFn = func(sourcePath, shadowPath string) {}
	injectMycorrhizalCacheFn = func(sourcePath, shadowPath string) {}
	stashHostWorkspaceFn = func(ctx context.Context, repoRoot, runID string) (bool, error) { return false, nil }
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		return &stubToolSession{}, nil
	}
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, bus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		return runner, nil
	}
	collectGitDiffFn = func(ctx context.Context, mountPath string) (string, error) { return "", nil }
}

// Attribution and countability are separate mechanisms and they have to
// compose. The tool seam answers "did the model write". This answers "whose
// write is this" — and it matters on its own, because a path OpenTendril wrote
// is not the Sprout's work even on a run where the model wrote plenty, and a
// reviewer seeing it in the commit is reading a change no task asked for.
//
// It also removes the evidence that made an idle run look productive: with the
// genome artifacts out of the diff, a run that only shelled around has nothing
// left to be credited with.
func TestGeneratedArtifactsAreNotTheSproutsWork(t *testing.T) {
	workspace := newSproutWorkspace(t)
	genome := filepath.Join(workspace, ".tendril", "genome")
	if err := os.MkdirAll(genome, 0o755); err != nil {
		t.Fatalf("mkdir genome: %v", err)
	}

	// Everything OpenTendril writes into a Substrate on its own behalf.
	for _, name := range []string{"epigenetics.md", "memorymap.md", "repomap.md", "fitness.json"} {
		if err := os.WriteFile(filepath.Join(genome, name), []byte("tendril wrote this\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// A genome file the OPERATOR authors. It is Substrate content, not
	// OpenTendril's accounting, so a Sprout asked to edit it must still be
	// credited with having done so.
	if err := os.WriteFile(filepath.Join(genome, "taxonomy-canonical.md"), []byte("operator content\n"), 0o644); err != nil {
		t.Fatalf("write taxonomy: %v", err)
	}

	files, err := collectStageableFiles(context.Background(), workspace)
	if err != nil {
		t.Fatalf("collectStageableFiles: %v", err)
	}

	for _, unwanted := range []string{
		".tendril/genome/epigenetics.md",
		".tendril/genome/memorymap.md",
		".tendril/genome/repomap.md",
		".tendril/genome/fitness.json",
	} {
		if slices.Contains(files, unwanted) {
			t.Errorf("%s is attributed to the run; OpenTendril wrote it, not the model", unwanted)
		}
	}
	if !slices.Contains(files, ".tendril/genome/taxonomy-canonical.md") {
		t.Errorf("files = %v, want the operator-authored genome file kept — excluding it would hide real work", files)
	}
}

// The composed result, stated as the measurement relies on it: a run whose only
// workspace difference is OpenTendril's own writing reports no-changes, even
// when the model used a tool that counts as able to write. Before, the stale
// epigenetic file from the previous run supplied a diff and the run reported
// complete on the strength of it.
func TestAnIdleRunIsNotRescuedByTendrilsOwnWrites(t *testing.T) {
	workspace := newSproutWorkspace(t)
	genome := filepath.Join(workspace, ".tendril", "genome")
	if err := os.MkdirAll(genome, 0o755); err != nil {
		t.Fatalf("mkdir genome: %v", err)
	}
	if err := os.WriteFile(filepath.Join(genome, "epigenetics.md"), []byte("left by the previous run\n"), 0o644); err != nil {
		t.Fatalf("write epigenetics: %v", err)
	}

	measured, err := collectStageableFiles(context.Background(), workspace)
	if err != nil {
		t.Fatalf("collectStageableFiles: %v", err)
	}

	// execCommand counts as able to write — nothing at the seam can tell `ls`
	// from `rm -rf`. That over-credit must not be enough on its own.
	shelled := changeEvidence{
		modelWrote:    toolCanWriteWorkspace("execCommand"),
		measured:      true,
		measuredFiles: measured,
	}
	if !shelled.modelWrote {
		t.Fatal("execCommand no longer counts as able to write; this test no longer probes the over-credit")
	}
	if shelled.changedAnything() {
		t.Fatalf("a run whose only diff was OpenTendril's own write counts as work: measured = %v", measured)
	}
	if got := classifySproutOutcome(nil, shelled, "I had a look around.", false); got != SproutOutcomeNoChanges {
		t.Fatalf("outcome = %q, want %q", got, SproutOutcomeNoChanges)
	}
}

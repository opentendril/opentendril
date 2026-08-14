package conductor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

func ptr[T any](v T) *T { return &v }

func completeUsage(prompt, completion, total int, amount, unit, provenance string) llm.Usage {
	return llm.Usage{
		PromptTokens:     ptr(prompt),
		CompletionTokens: ptr(completion),
		TotalTokens:      ptr(total),
		CostAmount:       ptr(amount),
		CostUnit:         ptr(unit),
		CostProvenance:   ptr(provenance),
	}
}

func nativeReadFileCall() llm.ToolCall {
	return llm.ToolCall{
		Function: llm.ToolCallFunction{
			Name:      "readFile",
			Arguments: `{"path":"a.txt"}`,
		},
	}
}

func usageTestSession() *fakeSession {
	return &fakeSession{
		tools: []ToolDefinition{{
			Name:        "readFile",
			Description: "read a file",
			Arguments:   []ToolArgument{{Name: "path", Type: "string", Required: true}},
		}},
	}
}

func newUsageTestSprout(t *testing.T, client llmCaller) *Sprout {
	t.Helper()
	sprout, err := newSprout(context.Background(), t.TempDir(), t.TempDir(), "", client, usageTestSession(), nil, "step", "session")
	if err != nil {
		t.Fatalf("newSprout: %v", err)
	}
	return sprout
}

func assertIntPtr(t *testing.T, got *int, want int, field string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is absent, want %d", field, want)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", field, *got, want)
	}
}

func assertStringPtr(t *testing.T, got *string, want, field string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is absent, want %q", field, want)
	}
	if *got != want {
		t.Fatalf("%s = %q, want %q", field, *got, want)
	}
}

func assertUsageAbsent(t *testing.T, usage llm.Usage) {
	t.Helper()
	if usage.PromptTokens != nil {
		t.Errorf("PromptTokens = %d, want absent", *usage.PromptTokens)
	}
	if usage.CompletionTokens != nil {
		t.Errorf("CompletionTokens = %d, want absent", *usage.CompletionTokens)
	}
	if usage.TotalTokens != nil {
		t.Errorf("TotalTokens = %d, want absent", *usage.TotalTokens)
	}
	if usage.CostAmount != nil {
		t.Errorf("CostAmount = %q, want absent", *usage.CostAmount)
	}
	if usage.CostUnit != nil {
		t.Errorf("CostUnit = %q, want absent", *usage.CostUnit)
	}
	if usage.CostProvenance != nil {
		t.Errorf("CostProvenance = %q, want absent", *usage.CostProvenance)
	}
}

func assertCostAbsent(t *testing.T, usage llm.Usage) {
	t.Helper()
	if usage.CostAmount != nil {
		t.Errorf("CostAmount = %q, want absent", *usage.CostAmount)
	}
	if usage.CostUnit != nil {
		t.Errorf("CostUnit = %q, want absent", *usage.CostUnit)
	}
	if usage.CostProvenance != nil {
		t.Errorf("CostProvenance = %q, want absent", *usage.CostProvenance)
	}
}

func TestAddDecimalStrings(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"1.00", "2.00", "3.00"},
		{"0.000001", "0.000002", "0.000003"},
		{"0.1", "0.2", "0.3"},
		{"1e2", "50", "150"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s+%s", tc.a, tc.b), func(t *testing.T) {
			got, err := addDecimalStrings(tc.a, tc.b)
			if err != nil {
				t.Fatalf("addDecimalStrings(%q, %q): %v", tc.a, tc.b, err)
			}
			if got != tc.want {
				t.Fatalf("addDecimalStrings(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// Scientific-notation JSON numbers must stay exact. The previous places
// calculation ignored the exponent, so 1e-6+2e-6 formatted as 0. A float64
// conversion would lose the trailing digits of the long coefficient.
func TestAddDecimalStringsScientificNotation(t *testing.T) {
	got, err := addDecimalStrings("1e-6", "2e-6")
	if err != nil {
		t.Fatalf("addDecimalStrings: %v", err)
	}
	if got != "0.000003" {
		t.Fatalf("1e-6+2e-6 = %q, want 0.000003 (0 means the exponent was ignored)", got)
	}

	got, err = addDecimalStrings("1E-6", "0")
	if err != nil {
		t.Fatalf("addDecimalStrings: %v", err)
	}
	if got != "0.000001" {
		t.Fatalf("1E-6+0 = %q, want 0.000001", got)
	}

	const long = "1.234567890123456789e-6"
	got, err = addDecimalStrings(long, "0")
	if err != nil {
		t.Fatalf("addDecimalStrings: %v", err)
	}
	if got != "0.000001234567890123456789" {
		t.Fatalf("%s+0 = %q, want the exact expanded decimal (float64 would round this)", long, got)
	}
}

func TestAggregateUsageTwoCompleteRequests(t *testing.T) {
	var run llm.Usage
	aggregateUsage(&run, completeUsage(10, 5, 15, "1.50", "USD", "api"), true)
	aggregateUsage(&run, completeUsage(20, 10, 30, "2.50", "USD", "api"), false)

	assertIntPtr(t, run.PromptTokens, 30, "PromptTokens")
	assertIntPtr(t, run.CompletionTokens, 15, "CompletionTokens")
	assertIntPtr(t, run.TotalTokens, 45, "TotalTokens")
	assertStringPtr(t, run.CostAmount, "4.00", "CostAmount")
	assertStringPtr(t, run.CostUnit, "USD", "CostUnit")
	assertStringPtr(t, run.CostProvenance, "api", "CostProvenance")
}

func TestAggregateUsageMissingPromptTokensMakesRunFieldUnavailable(t *testing.T) {
	var run llm.Usage
	aggregateUsage(&run, completeUsage(10, 5, 15, "1.00", "USD", "api"), true)
	second := completeUsage(0, 10, 30, "2.00", "USD", "api")
	second.PromptTokens = nil
	aggregateUsage(&run, second, false)

	if run.PromptTokens != nil {
		t.Fatalf("PromptTokens = %d, want absent after a request omitted it", *run.PromptTokens)
	}
	assertIntPtr(t, run.CompletionTokens, 15, "CompletionTokens")
}

func TestAggregateUsageMissingTotalTokensIsNeverDerived(t *testing.T) {
	var run llm.Usage
	req := completeUsage(10, 5, 0, "1.00", "USD", "api")
	req.TotalTokens = nil
	aggregateUsage(&run, req, true)

	if run.TotalTokens != nil {
		t.Fatalf("TotalTokens = %d, want absent; TotalTokens must not be derived from prompt+completion", *run.TotalTokens)
	}
	assertIntPtr(t, run.PromptTokens, 10, "PromptTokens")
	assertIntPtr(t, run.CompletionTokens, 5, "CompletionTokens")

	second := completeUsage(20, 10, 30, "2.00", "USD", "api")
	aggregateUsage(&run, second, false)
	if run.TotalTokens != nil {
		t.Fatalf("TotalTokens = %d, want still absent after a later request reported it", *run.TotalTokens)
	}
}

func TestAggregateUsageExactDecimalCostAddition(t *testing.T) {
	var run llm.Usage
	aggregateUsage(&run, completeUsage(1, 1, 2, "0.0000052349000001", "USD", "openrouter"), true)
	aggregateUsage(&run, completeUsage(1, 1, 2, "0.0000010000000002", "USD", "openrouter"), false)
	assertStringPtr(t, run.CostAmount, "0.0000062349000003", "CostAmount")

	var sci llm.Usage
	aggregateUsage(&sci, completeUsage(1, 1, 2, "1e-6", "USD", "openrouter"), true)
	aggregateUsage(&sci, completeUsage(1, 1, 2, "2e-6", "USD", "openrouter"), false)
	assertStringPtr(t, sci.CostAmount, "0.000003", "CostAmount")
}

func TestAggregateUsageCostCompleteness(t *testing.T) {
	t.Run("first request missing amount", func(t *testing.T) {
		var run llm.Usage
		first := completeUsage(1, 1, 2, "1.00", "USD", "api")
		first.CostAmount = nil
		aggregateUsage(&run, first, true)
		assertCostAbsent(t, run)
	})
	t.Run("first request missing unit", func(t *testing.T) {
		var run llm.Usage
		first := completeUsage(1, 1, 2, "1.00", "USD", "api")
		first.CostUnit = nil
		aggregateUsage(&run, first, true)
		assertCostAbsent(t, run)
	})
	t.Run("first request missing provenance", func(t *testing.T) {
		var run llm.Usage
		first := completeUsage(1, 1, 2, "1.00", "USD", "api")
		first.CostProvenance = nil
		aggregateUsage(&run, first, true)
		assertCostAbsent(t, run)
	})
	t.Run("first incomplete cost is not restored by a later complete request", func(t *testing.T) {
		var run llm.Usage
		first := completeUsage(1, 1, 2, "1.00", "USD", "api")
		first.CostUnit = nil
		aggregateUsage(&run, first, true)
		aggregateUsage(&run, completeUsage(1, 1, 2, "2.00", "USD", "api"), false)
		assertCostAbsent(t, run)
	})
	t.Run("mismatched unit", func(t *testing.T) {
		var run llm.Usage
		aggregateUsage(&run, completeUsage(1, 1, 2, "1.00", "USD", "api"), true)
		aggregateUsage(&run, completeUsage(1, 1, 2, "2.00", "credits", "api"), false)
		assertCostAbsent(t, run)
	})
	t.Run("mismatched provenance", func(t *testing.T) {
		var run llm.Usage
		aggregateUsage(&run, completeUsage(1, 1, 2, "1.00", "USD", "api"), true)
		aggregateUsage(&run, completeUsage(1, 1, 2, "2.00", "USD", "cache"), false)
		assertCostAbsent(t, run)
	})
	t.Run("second request missing amount", func(t *testing.T) {
		var run llm.Usage
		aggregateUsage(&run, completeUsage(1, 1, 2, "1.00", "USD", "api"), true)
		second := completeUsage(1, 1, 2, "2.00", "USD", "api")
		second.CostAmount = nil
		aggregateUsage(&run, second, false)
		assertCostAbsent(t, run)
	})
}

func TestAggregateUsageNoRequestsLeavesUsageAbsent(t *testing.T) {
	var run llm.Usage
	assertUsageAbsent(t, run)
}

func TestAggregateUsageZeroIsAMeasuredValue(t *testing.T) {
	var run llm.Usage
	aggregateUsage(&run, completeUsage(0, 0, 0, "0", "USD", "api"), true)
	aggregateUsage(&run, completeUsage(0, 0, 0, "0", "USD", "api"), false)
	assertIntPtr(t, run.PromptTokens, 0, "PromptTokens")
	assertIntPtr(t, run.CompletionTokens, 0, "CompletionTokens")
	assertIntPtr(t, run.TotalTokens, 0, "TotalTokens")
	assertStringPtr(t, run.CostAmount, "0", "CostAmount")
}

type seqCall struct {
	via    string
	nTools int
}

type sequenceMockLLM struct {
	results []llm.Result
	errs    []error
	calls   []seqCall
}

func (m *sequenceMockLLM) next() (llm.Result, error) {
	i := len(m.calls) - 1
	if i < 0 || i >= len(m.results) {
		return llm.Result{}, errors.New("out of mocks")
	}
	var err error
	if i < len(m.errs) {
		err = m.errs[i]
	}
	return m.results[i], err
}

func (m *sequenceMockLLM) CallWithResult(ctx context.Context, messages []llm.Message) (llm.Result, error) {
	m.calls = append(m.calls, seqCall{via: "result"})
	return m.next()
}

func (m *sequenceMockLLM) CallStreamWithResult(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (llm.Result, error) {
	m.calls = append(m.calls, seqCall{via: "stream"})
	return m.next()
}

func (m *sequenceMockLLM) CallWithTools(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition, tokenChan chan<- string) (llm.Result, error) {
	m.calls = append(m.calls, seqCall{via: "tools", nTools: len(tools)})
	return m.next()
}

func (m *sequenceMockLLM) ToolDefinitionsCapable() bool { return true }

type proseMockLLM struct {
	results []llm.Result
	errs    []error
	calls   int
}

func (m *proseMockLLM) next() (llm.Result, error) {
	if m.calls >= len(m.results) {
		return llm.Result{}, errors.New("out of mocks")
	}
	res := m.results[m.calls]
	var err error
	if m.calls < len(m.errs) {
		err = m.errs[m.calls]
	}
	m.calls++
	return res, err
}

func (m *proseMockLLM) CallWithResult(ctx context.Context, messages []llm.Message) (llm.Result, error) {
	return m.next()
}

func (m *proseMockLLM) CallStreamWithResult(ctx context.Context, messages []llm.Message, tokenChan chan<- string) (llm.Result, error) {
	return m.next()
}

func TestSproutTwoCompleteNativeRequestsAggregate(t *testing.T) {
	mock := &sequenceMockLLM{
		results: []llm.Result{
			{ToolCalls: []llm.ToolCall{nativeReadFileCall()}, Usage: completeUsage(10, 5, 15, "1.50", "USD", "api")},
			{Text: "done", Usage: completeUsage(20, 10, 30, "2.50", "USD", "api")},
		},
		errs: []error{nil, nil},
	}
	res, err := newUsageTestSprout(t, mock).Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertIntPtr(t, res.Usage.PromptTokens, 30, "PromptTokens")
	assertIntPtr(t, res.Usage.CompletionTokens, 15, "CompletionTokens")
	assertIntPtr(t, res.Usage.TotalTokens, 45, "TotalTokens")
	assertStringPtr(t, res.Usage.CostAmount, "4.00", "CostAmount")
	if len(mock.calls) != 2 {
		t.Fatalf("native requests = %d, want 2", len(mock.calls))
	}
}

func TestSproutProseRequestsAggregate(t *testing.T) {
	mock := &proseMockLLM{
		results: []llm.Result{
			{Text: `{"tool":"readFile","arguments":{"path":"a.txt"}}`, Usage: completeUsage(10, 5, 15, "1.50", "USD", "api")},
			{Text: `{"final":"done"}`, Usage: completeUsage(20, 10, 30, "2.50", "USD", "api")},
		},
		errs: []error{nil, nil},
	}
	res, err := newUsageTestSprout(t, mock).Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertIntPtr(t, res.Usage.PromptTokens, 30, "PromptTokens")
	assertStringPtr(t, res.Usage.CostAmount, "4.00", "CostAmount")
	if mock.calls != 2 {
		t.Fatalf("prose requests = %d, want 2", mock.calls)
	}
}

func TestSproutNativeRejectionProbeAndProseRetryCountEveryRequest(t *testing.T) {
	mock := &sequenceMockLLM{
		results: []llm.Result{
			{Usage: completeUsage(10, 1, 11, "1.0", "USD", "api")},
			{Usage: completeUsage(15, 1, 16, "1.5", "USD", "api")},
			{Text: `{"final":"done"}`, Usage: completeUsage(20, 1, 21, "2.0", "USD", "api")},
		},
		errs: []error{llm.ErrRejectedWithTools, nil, nil},
	}
	res, err := newUsageTestSprout(t, mock).Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(mock.calls) != 3 {
		t.Fatalf("actual LLM requests = %d (%v), want 3 (native + probe + prose)", len(mock.calls), mock.calls)
	}
	if mock.calls[0].via != "tools" || mock.calls[0].nTools == 0 {
		t.Fatalf("first call = %+v, want native tools request", mock.calls[0])
	}
	if mock.calls[1].via != "tools" || mock.calls[1].nTools != 0 {
		t.Fatalf("second call = %+v, want no-tools diagnostic probe", mock.calls[1])
	}
	if mock.calls[2].via != "result" {
		t.Fatalf("third call = %+v, want prose CallWithResult retry", mock.calls[2])
	}
	assertIntPtr(t, res.Usage.PromptTokens, 45, "PromptTokens")
	assertStringPtr(t, res.Usage.CostAmount, "4.5", "CostAmount")
}

func TestSproutTerminalErrorPreservesAggregatedUsage(t *testing.T) {
	mock := &sequenceMockLLM{
		results: []llm.Result{
			{ToolCalls: []llm.ToolCall{nativeReadFileCall()}, Usage: completeUsage(10, 5, 15, "1.00", "USD", "api")},
			{Usage: completeUsage(20, 10, 30, "2.00", "USD", "api")},
		},
		errs: []error{nil, errors.New("provider dropped the connection")},
	}
	res, err := newUsageTestSprout(t, mock).Run(context.Background(), "task")
	if err == nil {
		t.Fatal("Run succeeded, want the terminal provider error")
	}
	assertIntPtr(t, res.Usage.PromptTokens, 30, "PromptTokens")
	assertIntPtr(t, res.Usage.CompletionTokens, 15, "CompletionTokens")
	assertStringPtr(t, res.Usage.CostAmount, "3.00", "CostAmount")
}

type usageReportRunner struct {
	result sproutResult
	err    error
}

func (r usageReportRunner) Run(ctx context.Context, taskPrompt string) (sproutResult, error) {
	return r.result, r.err
}

func stubUsageReportRun(t *testing.T, runner sproutRunner) {
	t.Helper()
	originalEnsureSprout := ensureSproutImageFn
	originalStartSession := startTerrariumSessionFn
	originalNewSprout := newSproutFn
	originalPreflight := runSproutPreflightChecksFn
	originalRepoMap := generateRepoMapFn
	t.Cleanup(func() {
		ensureSproutImageFn = originalEnsureSprout
		startTerrariumSessionFn = originalStartSession
		newSproutFn = originalNewSprout
		runSproutPreflightChecksFn = originalPreflight
		generateRepoMapFn = originalRepoMap
	})
	ensureSproutImageFn = func(ctx context.Context, imageName string) error { return nil }
	runSproutPreflightChecksFn = func(ctx context.Context) error { return nil }
	generateRepoMapFn = func(ctx context.Context, dir string) (string, error) { return "", nil }
	startTerrariumSessionFn = func(ctx context.Context, providerName, imageName, mountPath string, readOnly bool, command []string, extraEnv []string, timeout time.Duration, observers ...terrarium.ActivationObserver) (toolSession, error) {
		return &stubToolSession{}, nil
	}
	newSproutFn = func(ctx context.Context, workspace, genotypeRoot, genotypeName string, client llmCaller, session toolSession, eventBus *eventbus.Bus, stepID, sessionID string) (sproutRunner, error) {
		return runner, nil
	}
}

func TestSproutRunReportCarriesAggregate(t *testing.T) {
	usage := completeUsage(30, 15, 45, "4.00", "USD", "api")
	stubUsageReportRun(t, usageReportRunner{result: sproutResult{Response: "done", Usage: usage}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := (&DockerOrchestrator{
		Substrate:        t.TempDir(),
		StepID:           "usage-report",
		DisableMergeBack: true,
	}).RunSprout(ctx, "task")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	assertIntPtr(t, report.Usage.PromptTokens, 30, "report.Usage.PromptTokens")
	assertIntPtr(t, report.Usage.TotalTokens, 45, "report.Usage.TotalTokens")
	assertStringPtr(t, report.Usage.CostAmount, "4.00", "report.Usage.CostAmount")
}

func TestSproutRunReportNoRequestsHasAbsentUsage(t *testing.T) {
	stubUsageReportRun(t, usageReportRunner{result: sproutResult{Response: "done"}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := (&DockerOrchestrator{
		Substrate:        t.TempDir(),
		StepID:           "usage-absent",
		DisableMergeBack: true,
	}).RunSprout(ctx, "task")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	assertUsageAbsent(t, report.Usage)
}

func TestSproutRunReportPreservesUsageOnTerminalError(t *testing.T) {
	usage := completeUsage(30, 15, 45, "3.00", "USD", "api")
	stubUsageReportRun(t, usageReportRunner{
		result: sproutResult{Usage: usage},
		err:    errors.New("sprout blew up"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := (&DockerOrchestrator{
		Substrate:        t.TempDir(),
		StepID:           "usage-error",
		DisableMergeBack: true,
	}).RunSprout(ctx, "task")
	if err == nil {
		t.Fatal("RunSprout succeeded, want the terminal error")
	}
	assertIntPtr(t, report.Usage.PromptTokens, 30, "report.Usage.PromptTokens")
	assertStringPtr(t, report.Usage.CostAmount, "3.00", "report.Usage.CostAmount")
}

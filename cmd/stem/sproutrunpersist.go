package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/roots/llm"
)

// sproutPersistenceFailure retains the first write failure observed by one
// persistence path. EventBus sinks and terminal callbacks may run on different
// goroutines, so the shared error is protected independently of the Store.
type sproutPersistenceFailure struct {
	mu  sync.Mutex
	err error
}

func (f *sproutPersistenceFailure) capture(err error) {
	if f == nil || err == nil {
		return
	}
	f.mu.Lock()
	if f.err == nil {
		f.err = err
	}
	f.mu.Unlock()
}

func (f *sproutPersistenceFailure) Err() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// oneShotHistorySink makes the one-shot adapter's owned telemetry writes
// accountable without changing EventBus's asynchronous, lossy daemon contract.
// RecordEvent remains the sole HistoryDB event path, retaining sanitisation,
// redaction, encryption, and insertion behavior.
type oneShotHistorySink struct {
	history *historydb.Store
	failure sproutPersistenceFailure
}

func (s *oneShotHistorySink) Consume(event eventbus.Event) {
	if s == nil || s.history == nil {
		return
	}
	if err := s.history.RecordEvent(context.Background(), event); err != nil {
		s.failure.capture(fmt.Errorf("persist %q event: %w", event.Type, err))
	}
}

func (s *oneShotHistorySink) Err() error {
	if s == nil {
		return nil
	}
	return s.failure.Err()
}

// sproutRunUsageFromReport copies the conductor's separate execution and
// post-run components onto the durable envelope. Components are omitted when
// no provider request occurred. No combined token or monetary total is built.
func sproutRunUsageFromReport(report conductor.SproutRunReport) historydb.SproutRunUsage {
	return historydb.SproutRunUsage{
		Execution: usageComponentFrom(report.RequestsMade, report.Usage, report.Provider, report.Model),
		PostRun:   usageComponentFrom(report.PostRun.RequestsMade, report.PostRun.Usage, report.PostRun.Provider, report.PostRun.Model),
	}
}

func usageComponentFrom(requestsMade bool, usage llm.Usage, provider, model string) *historydb.UsageComponent {
	if !requestsMade {
		return nil
	}
	return &historydb.UsageComponent{
		RequestsMade:     true,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CostAmount:       usage.CostAmount,
		CostUnit:         usage.CostUnit,
		CostProvenance:   usage.CostProvenance,
		Provider:         provider,
		Model:            model,
	}
}

// persistDispatchSproutRun commits the opening ownership row before any
// Terrarium work and before any "session ready" signal. Status is the
// existing non-terminal value "running". A later OnTerminal write settles
// the same runId.
//
// When history is nil, persistence is disabled and there is no ready signal:
// a delegated watcher would have nothing to prove ownership against.
func persistDispatchSproutRun(ctx context.Context, history *historydb.Store, run historydb.SproutRun) error {
	if history == nil {
		return nil
	}
	if strings.TrimSpace(run.SessionID) == "" {
		return fmt.Errorf("sprout dispatch requires a phytomer sessionId")
	}
	if strings.TrimSpace(run.Status) == "" {
		run.Status = "running"
	}
	persistCtx := ctx
	if persistCtx == nil {
		persistCtx = context.Background()
	}
	if err := history.RecordSproutRun(context.WithoutCancel(persistCtx), run); err != nil {
		return err
	}
	core.NotifySproutDispatch(ctx, core.SproutDispatch{
		SessionID: run.SessionID,
		StepID:    run.StepID,
	})
	return nil
}

func persistTerminalSproutRun(ctx context.Context, history *historydb.Store, opened historydb.SproutRun, report conductor.SproutRunReport, runErr error) error {
	if history == nil {
		return nil
	}
	run := opened
	run.FinishedAt = time.Now().UTC()
	if resolved := strings.TrimSpace(report.Model); resolved != "" {
		run.Model = resolved
	}
	if resolved := strings.TrimSpace(report.Provider); resolved != "" {
		run.Provider = resolved
	}
	run.Usage = sproutRunUsageFromReport(report)
	run.FruitRepository = report.FruitRepository
	run.FruitWorkspace = report.FruitWorkspace
	run.FruitBranch = report.FruitBranch
	run.FruitCommit = report.FruitCommit
	run.FruitPublicationState = report.FruitPublicationState
	run.FruitCreatedAt = report.FruitCreatedAt
	applyObservationToRun(&run, report, runErr)
	run.Status = core.ClassifyLifecycleStatus(core.FailureCategory(run.FailureCategory))
	if runErr != nil {
		run.Error = runErr.Error()
	} else {
		run.Output = report.Output
	}
	if recordErr := history.RecordSproutRun(ctx, run); recordErr != nil {
		log.Printf("[Sprout] Failed to record sprout run: %v", recordErr)
		return fmt.Errorf("persist terminal sprout run: %w", recordErr)
	}
	return nil
}

// applyObservationToRun copies Conductor observation fields onto the durable
// record. If the Conductor did not already classify, the adapter calls Core
// rather than inventing a category from error text.
func applyObservationToRun(run *historydb.SproutRun, report conductor.SproutRunReport, runErr error) {
	if run == nil {
		return
	}
	run.Outcome = report.Outcome
	run.ProviderRequestAttempted = report.RequestsMade
	run.ToolInvocations = report.ToolInvocations
	if report.ProviderDiagnostic != nil {
		run.ProviderDiagnostic = &historydb.ProviderDiagnostic{
			StatusCode: report.ProviderDiagnostic.StatusCode,
			Message:    report.ProviderDiagnostic.Message,
			Provider:   report.ProviderDiagnostic.Provider,
		}
	}
	run.FailureCategory = report.FailureCategory
	if run.FailureCategory == "" {
		statusCode := 0
		if run.ProviderDiagnostic != nil {
			statusCode = run.ProviderDiagnostic.StatusCode
		}
		run.FailureCategory = string(core.ClassifyFailure(core.ObservationFacts{
			Outcome:                  report.Outcome,
			RunFailed:                runErr != nil,
			ProviderRequestAttempted: report.RequestsMade,
			ProviderStatusCode:       statusCode,
		}))
	}
}

func installSproutTerminalHistory(orch *conductor.DockerOrchestrator, history *historydb.Store, persistCtx context.Context, opened historydb.SproutRun) *sproutPersistenceFailure {
	if orch == nil || history == nil {
		return nil
	}
	failure := &sproutPersistenceFailure{}
	orch.OnTerminal = func(report conductor.SproutRunReport, runErr error) {
		failure.capture(persistTerminalSproutRun(persistCtx, history, opened, report, runErr))
	}
	return failure
}

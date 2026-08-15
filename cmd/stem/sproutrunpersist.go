package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/roots/llm"
)

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

func persistTerminalSproutRun(ctx context.Context, history *historydb.Store, opened historydb.SproutRun, report conductor.SproutRunReport, runErr error) {
	if history == nil {
		return
	}
	run := opened
	run.FinishedAt = time.Now().UTC()
	if resolved := strings.TrimSpace(report.Model); resolved != "" {
		run.Model = resolved
	}
	if resolved := strings.TrimSpace(report.Provider); resolved != "" {
		run.Provider = resolved
	}
	if runErr != nil {
		run.Status = "withered"
		run.Error = runErr.Error()
	} else {
		run.Status = "matured"
		if report.Outcome == conductor.SproutOutcomeNoEngagement {
			run.Status = "withered"
		}
		run.Output = report.Output
	}
	run.Usage = sproutRunUsageFromReport(report)
	applyObservationToRun(&run, report, runErr)
	if recordErr := history.RecordSproutRun(ctx, run); recordErr != nil {
		log.Printf("[Sprout] Failed to record sprout run: %v", recordErr)
	}
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

func installSproutTerminalHistory(orch *conductor.DockerOrchestrator, history *historydb.Store, persistCtx context.Context, opened historydb.SproutRun) {
	if orch == nil || history == nil {
		return
	}
	orch.OnTerminal = func(report conductor.SproutRunReport, runErr error) {
		persistTerminalSproutRun(persistCtx, history, opened, report, runErr)
	}
}

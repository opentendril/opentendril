package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
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
	if recordErr := history.RecordSproutRun(ctx, run); recordErr != nil {
		log.Printf("[Sprout] Failed to record sprout run: %v", recordErr)
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

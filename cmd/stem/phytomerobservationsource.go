package main

import (
	"context"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

const phytomerObservationSproutLimit = 100

// phytomerObservationSource is the historydb-backed evidence port for Core
// current-state observation. It copies persisted Seed/Sprout fields into
// evidence, including fields Core will refuse to release. Safety projection
// stays in Core.
func phytomerObservationSource(history *historydb.Store) core.PhytomerObservationSource {
	if history == nil {
		return core.PhytomerObservationSource{}
	}
	return core.PhytomerObservationSource{
		SeedByPhytomer: func(ctx context.Context, phytomerID string) (core.SeedObservationEvidence, bool, error) {
			seed, found, err := history.GetSeedRunByPhytomer(ctx, phytomerID)
			if err != nil || !found {
				return core.SeedObservationEvidence{}, found, err
			}
			return core.SeedObservationEvidence{
				Handle:                  seed.Handle,
				Pollen:                  seed.Pollen,
				PhytomerID:              seed.PhytomerID,
				Substrate:               seed.Substrate,
				Status:                  seed.Status,
				Iterations:              seed.Iterations,
				Branch:                  seed.Branch,
				Commit:                  seed.Commit,
				Goal:                    seed.Goal,
				Diff:                    seed.Diff,
				Logs:                    seed.Logs,
				Error:                   seed.Error,
				PublicationDiagnostic:   coreSeedPublicationDiagnostic(seed.PublicationDiagnostic),
				VerificationDiagnostics: coreSeedVerificationDiagnostics(seed.VerificationDiagnostics),
			}, true, nil
		},
		SproutsByPhytomer: func(ctx context.Context, phytomerID string) ([]core.SproutObservationEvidence, error) {
			runs, err := history.LoadSproutRuns(ctx, phytomerID, phytomerObservationSproutLimit)
			if err != nil {
				return nil, err
			}
			out := make([]core.SproutObservationEvidence, 0, len(runs))
			for _, run := range runs {
				evidence := core.SproutObservationEvidence{
					RunID:                    run.RunID,
					Pollen:                   run.Pollen,
					Substrate:                run.Substrate,
					Status:                   run.Status,
					Provider:                 run.Provider,
					Model:                    run.Model,
					Outcome:                  run.Outcome,
					FailureCategory:          run.FailureCategory,
					ProviderRequestAttempted: run.ProviderRequestAttempted,
					ToolInvocations:          run.ToolInvocations,
					Transcript:               run.Transcript,
					Output:                   run.Output,
					Error:                    run.Error,
					StartedAt:                run.StartedAt,
				}
				if run.ProviderDiagnostic != nil {
					copied := core.ProviderDiagnostic{
						StatusCode: run.ProviderDiagnostic.StatusCode,
						Message:    run.ProviderDiagnostic.Message,
						Provider:   run.ProviderDiagnostic.Provider,
					}
					evidence.ProviderDiagnostic = &copied
				}
				out = append(out, evidence)
			}
			return out, nil
		},
	}
}

func coreSeedPublicationDiagnostic(diagnostic *historydb.SeedPublicationDiagnostic) *core.SeedPublicationDiagnostic {
	if diagnostic == nil {
		return nil
	}
	return &core.SeedPublicationDiagnostic{
		FailureCategory: diagnostic.FailureCategory,
		ExecutionStatus: diagnostic.ExecutionStatus,
		Phase:           diagnostic.Phase,
		Outcome:         diagnostic.Outcome,
		RetrySafe:       diagnostic.RetrySafe,
		Message:         diagnostic.Message,
		RequestID:       diagnostic.RequestID,
	}
}

func coreSeedVerificationDiagnostics(src []historydb.SeedVerificationDiagnostic) []core.SeedVerificationDiagnostic {
	if len(src) == 0 {
		return nil
	}
	out := make([]core.SeedVerificationDiagnostic, len(src))
	for i, diagnostic := range src {
		out[i] = core.SeedVerificationDiagnostic{
			Iteration: diagnostic.Iteration,
			Outcome:   diagnostic.Outcome,
			TimedOut:  diagnostic.TimedOut,
			Message:   diagnostic.Message,
		}
		if diagnostic.ExitCode != nil {
			code := *diagnostic.ExitCode
			out[i].ExitCode = &code
		}
	}
	return out
}

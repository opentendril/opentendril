package main

import (
	"context"
	"errors"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

// continuationPersistence is the historydb-backed continuation port. Core
// owns eligibility composition; this adapter only translates durable Seed
// ownership and atomic acceptance. A nil store fails honestly rather than
// keeping accepted intent in memory.
func continuationPersistence(history *historydb.Store) core.ContinuationPersistence {
	unavailable := func() core.ContinuationPersistence {
		return core.ContinuationPersistence{
			ResolveTarget: func(context.Context, string) (core.ContinuationTarget, bool, error) {
				return core.ContinuationTarget{}, false, core.ErrContinuationHistoryUnavailable
			},
			Accept: func(context.Context, core.ContinuationAcceptance) (core.ContinuationRecord, error) {
				return core.ContinuationRecord{}, core.ErrContinuationHistoryUnavailable
			},
			ClaimPending: func(context.Context, core.ContinuationTarget) ([]core.ContinuationRecord, error) {
				return nil, core.ErrContinuationHistoryUnavailable
			},
			MarkDelivered: func(context.Context, core.ContinuationTarget, []string) error {
				return core.ErrContinuationHistoryUnavailable
			},
			HasUnresolved: func(context.Context, core.ContinuationTarget) (bool, error) {
				return false, core.ErrContinuationHistoryUnavailable
			},
			AcquireSettlementFence: func(context.Context, core.ContinuationTarget) (bool, error) {
				return false, core.ErrContinuationHistoryUnavailable
			},
			CompleteSuccessfulSettlement: func(context.Context, core.SeedSettlement) error {
				return core.ErrContinuationHistoryUnavailable
			},
			AccountTerminalFailure: func(context.Context, core.SeedSettlement) (core.TerminalFailureAccount, error) {
				return core.TerminalFailureAccount{}, core.ErrContinuationHistoryUnavailable
			},
			ReconcileOrphaned: func(context.Context) error {
				return core.ErrContinuationHistoryUnavailable
			},
		}
	}
	if history == nil {
		return unavailable()
	}
	return core.ContinuationPersistence{
		ResolveTarget: func(ctx context.Context, phytomerID string) (core.ContinuationTarget, bool, error) {
			seed, found, err := history.ResolveContinuationTarget(ctx, phytomerID)
			if err != nil || !found {
				return core.ContinuationTarget{}, found, mapContinuationErr(err)
			}
			return core.ContinuationTarget{
				PhytomerID: seed.PhytomerID,
				Handle:     seed.Handle,
				Pollen:     seed.Pollen,
				Substrate:  seed.Substrate,
				Status:     seed.Status,
			}, true, nil
		},
		Accept: func(ctx context.Context, in core.ContinuationAcceptance) (core.ContinuationRecord, error) {
			rec, err := history.AcceptContinuation(ctx, historydb.ContinuationAcceptance{
				PhytomerID:     in.PhytomerID,
				Pollen:         in.Pollen,
				Substrate:      in.Substrate,
				Handle:         in.Handle,
				IdempotencyKey: in.IdempotencyKey,
				Intent:         in.Intent,
				IntentDigest:   in.IntentDigest,
			})
			if err != nil {
				return core.ContinuationRecord{}, mapContinuationErr(err)
			}
			return coreContinuation(rec), nil
		},
		ClaimPending: func(ctx context.Context, target core.ContinuationTarget) ([]core.ContinuationRecord, error) {
			recs, err := history.ClaimPendingContinuations(ctx, historySeedTarget(target))
			if err != nil {
				return nil, mapContinuationErr(err)
			}
			return coreContinuations(recs), nil
		},
		MarkDelivered: func(ctx context.Context, target core.ContinuationTarget, ids []string) error {
			return mapContinuationErr(history.MarkContinuationsDelivered(ctx, historySeedTarget(target), ids))
		},
		HasUnresolved: func(ctx context.Context, target core.ContinuationTarget) (bool, error) {
			ok, err := history.HasUnresolvedContinuations(ctx, historySeedTarget(target))
			return ok, mapContinuationErr(err)
		},
		AcquireSettlementFence: func(ctx context.Context, target core.ContinuationTarget) (bool, error) {
			fenced, err := history.AcquireSeedSettlementFence(ctx, historySeedTarget(target))
			return fenced, mapContinuationErr(err)
		},
		CompleteSuccessfulSettlement: func(ctx context.Context, settled core.SeedSettlement) error {
			return mapContinuationErr(history.CompleteSeedSettlement(ctx, historySeedTargetFromSettlement(settled), historySeedRun(settled)))
		},
		AccountTerminalFailure: func(ctx context.Context, settled core.SeedSettlement) (core.TerminalFailureAccount, error) {
			account, err := history.AccountSeedTerminalFailure(ctx, historySeedTargetFromSettlement(settled), historySeedRun(settled))
			if err != nil {
				return core.TerminalFailureAccount{}, mapContinuationErr(err)
			}
			return core.TerminalFailureAccount{UnresolvedFailed: account.UnresolvedFailed}, nil
		},
		ReconcileOrphaned: func(ctx context.Context) error {
			return mapContinuationErr(history.ReconcileOrphanedSeedWork(ctx))
		},
	}
}

func historySeedTarget(target core.ContinuationTarget) historydb.SeedTarget {
	return historydb.SeedTarget{
		PhytomerID: target.PhytomerID,
		Handle:     target.Handle,
		Pollen:     target.Pollen,
		Substrate:  target.Substrate,
	}
}

func historySeedTargetFromSettlement(settled core.SeedSettlement) historydb.SeedTarget {
	return historydb.SeedTarget{
		PhytomerID: settled.PhytomerID,
		Handle:     settled.Handle,
		Pollen:     settled.Pollen,
		Substrate:  settled.Substrate,
	}
}

func historySeedRun(settled core.SeedSettlement) historydb.SeedRun {
	return historydb.SeedRun{
		Handle:                  settled.Handle,
		Pollen:                  settled.Pollen,
		PhytomerID:              settled.PhytomerID,
		Substrate:               settled.Substrate,
		Goal:                    settled.Goal,
		Status:                  settled.Status,
		Iterations:              settled.Iterations,
		Branch:                  settled.Branch,
		Commit:                  settled.Commit,
		Diff:                    settled.Diff,
		Logs:                    settled.Logs,
		Error:                   settled.Error,
		PublicationDiagnostic:   historySeedPublicationDiagnostic(settled.PublicationDiagnostic),
		VerificationDiagnostics: historySeedVerificationDiagnostics(settled.VerificationDiagnostics),
		StartedAt:               settled.StartedAt,
		FinishedAt:              settled.FinishedAt,
	}
}

func coreContinuations(recs []historydb.Continuation) []core.ContinuationRecord {
	out := make([]core.ContinuationRecord, 0, len(recs))
	for _, rec := range recs {
		out = append(out, coreContinuation(rec))
	}
	return out
}

func coreContinuation(rec historydb.Continuation) core.ContinuationRecord {
	return core.ContinuationRecord{
		ContinuationID: rec.ContinuationID,
		PhytomerID:     rec.PhytomerID,
		Pollen:         rec.Pollen,
		Substrate:      rec.Substrate,
		IdempotencyKey: rec.IdempotencyKey,
		IntentDigest:   rec.IntentDigest,
		Intent:         rec.Intent,
		Sequence:       rec.Sequence,
		DeliveryState:  rec.DeliveryState,
		AcceptedAt:     rec.AcceptedAt,
		DeliveredAt:    rec.DeliveredAt,
		FailedAt:       rec.FailedAt,
	}
}

func mapContinuationErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, historydb.ErrContinuationNotFound):
		return core.ErrContinuationTargetNotFound
	case errors.Is(err, historydb.ErrContinuationPollenMismatch):
		return core.ErrContinuationPollenMismatch
	case errors.Is(err, historydb.ErrContinuationNotEligible):
		return core.ErrContinuationNotEligible
	case errors.Is(err, historydb.ErrContinuationTargetChanged):
		return core.ErrContinuationTargetChanged
	case errors.Is(err, historydb.ErrContinuationIdempotencyConflict):
		return core.ErrContinuationIdempotencyConflict
	case errors.Is(err, historydb.ErrContinuationInvalid):
		return core.ErrContinuationInvalid
	case errors.Is(err, historydb.ErrContinuationDeliveryState):
		return core.ErrContinuationDeliveryState
	case errors.Is(err, historydb.ErrSeedSettlementNotFenced):
		return core.ErrSeedSettlementNotFenced
	case errors.Is(err, historydb.ErrSeedSettlementInvalid):
		return core.ErrSeedSettlementInvalid
	default:
		return err
	}
}

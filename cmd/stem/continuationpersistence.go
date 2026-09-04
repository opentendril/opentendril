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
	if history == nil {
		return core.ContinuationPersistence{
			ResolveTarget: func(context.Context, string) (core.ContinuationTarget, bool, error) {
				return core.ContinuationTarget{}, false, core.ErrContinuationHistoryUnavailable
			},
			Accept: func(context.Context, core.ContinuationAcceptance) (core.ContinuationRecord, error) {
				return core.ContinuationRecord{}, core.ErrContinuationHistoryUnavailable
			},
		}
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
	}
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
	default:
		return err
	}
}

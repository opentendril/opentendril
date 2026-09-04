package historydb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const continuationSelectColumns = `continuationId, phytomerId, pollen, substrate, idempotencyKey, intentDigest, intent,
	sequence, deliveryState, acceptedAt, deliveredAt, failedAt`

func (t SeedTarget) trimmed() SeedTarget {
	return SeedTarget{
		PhytomerID: strings.TrimSpace(t.PhytomerID),
		Handle:     strings.TrimSpace(t.Handle),
		Pollen:     strings.TrimSpace(t.Pollen),
		Substrate:  strings.TrimSpace(t.Substrate),
	}
}

func (t SeedTarget) validate() error {
	if t.PhytomerID == "" || t.Handle == "" || t.Pollen == "" || t.Substrate == "" {
		return ErrContinuationInvalid
	}
	return nil
}

func seedStatusIsTerminalFailure(status string) bool {
	switch strings.TrimSpace(status) {
	case seedStatusExhausted, seedStatusWithered, seedStatusFruitPublicationFailed:
		return true
	default:
		return false
	}
}

func seedStatusIsTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case seedStatusSatisfied, seedStatusExhausted, seedStatusWithered, seedStatusFruitPublicationFailed:
		return true
	default:
		return false
	}
}

// ListPendingContinuations returns pending continuations for the exact Seed
// target in ascending durable sequence. Delivered and failed rows are omitted.
func (s *Store) ListPendingContinuations(ctx context.Context, target SeedTarget) ([]Continuation, error) {
	if s == nil {
		return nil, fmt.Errorf("history store is not available")
	}
	target = target.trimmed()
	if err := target.validate(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin pending continuation load: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := s.requireExactSeedTargetTx(ctx, tx, target); err != nil {
		return nil, err
	}
	recs, err := s.listContinuationsByStatesTx(ctx, tx, target.PhytomerID, continuationDeliveryPending)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit pending continuation load: %w", err)
	}
	committed = true
	return recs, nil
}

// ClaimPendingContinuations atomically loads pending continuations for the
// exact running Seed target and marks them delivering so they cannot be
// claimed twice or silently treated as delivered across the Sprout call.
func (s *Store) ClaimPendingContinuations(ctx context.Context, target SeedTarget) ([]Continuation, error) {
	if s == nil {
		return nil, fmt.Errorf("history store is not available")
	}
	target = target.trimmed()
	if err := target.validate(); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin continuation claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	seed, err := s.requireExactSeedTargetTx(ctx, tx, target)
	if err != nil {
		return nil, err
	}
	if err := seedRunIsContinuationEligible(seed.Status, seed.Substrate); err != nil {
		return nil, err
	}
	pending, err := s.listContinuationsByStatesTx(ctx, tx, target.PhytomerID, continuationDeliveryPending)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	claimed := make([]Continuation, 0, len(pending))
	for _, rec := range pending {
		if err := s.transitionContinuationDeliveryTx(ctx, tx, rec.ContinuationID, target.PhytomerID, continuationDeliveryPending, continuationDeliveryDelivering, now, ""); err != nil {
			return nil, err
		}
		rec.DeliveryState = continuationDeliveryDelivering
		claimed = append(claimed, rec)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit continuation claim: %w", err)
	}
	committed = true
	return claimed, nil
}

// MarkContinuationsDelivered advances delivering rows to delivered at the
// Sprout/Mycorrhizal handoff. Illegal or stale-state transitions fail closed.
func (s *Store) MarkContinuationsDelivered(ctx context.Context, target SeedTarget, continuationIDs []string) error {
	if s == nil {
		return fmt.Errorf("history store is not available")
	}
	target = target.trimmed()
	if err := target.validate(); err != nil {
		return err
	}
	ids := uniqueContinuationIDs(continuationIDs)
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin continuation delivery: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := s.requireExactSeedTargetTx(ctx, tx, target); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		if err := s.transitionContinuationDeliveryTx(ctx, tx, id, target.PhytomerID, continuationDeliveryDelivering, continuationDeliveryDelivered, now, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit continuation delivery: %w", err)
	}
	committed = true
	return nil
}

// HasUnresolvedContinuations reports whether pending or delivering
// continuations exist for the exact target.
func (s *Store) HasUnresolvedContinuations(ctx context.Context, target SeedTarget) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("history store is not available")
	}
	target = target.trimmed()
	if err := target.validate(); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin unresolved continuation check: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := s.requireExactSeedTargetTx(ctx, tx, target); err != nil {
		return false, err
	}
	count, err := s.countUnresolvedContinuationsTx(ctx, tx, target.PhytomerID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit unresolved continuation check: %w", err)
	}
	committed = true
	return count > 0, nil
}

// AcquireSeedSettlementFence is the race arbiter for successful settlement.
// With no unresolved continuation it atomically updates running -> settling.
// If unresolved continuation exists it leaves status running and returns false.
func (s *Store) AcquireSeedSettlementFence(ctx context.Context, target SeedTarget) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("history store is not available")
	}
	target = target.trimmed()
	if err := target.validate(); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin settlement fence: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	seed, err := s.requireExactSeedTargetTx(ctx, tx, target)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(seed.Status) != seedStatusRunning {
		return false, ErrContinuationNotEligible
	}
	unresolved, err := s.countUnresolvedContinuationsTx(ctx, tx, target.PhytomerID)
	if err != nil {
		return false, err
	}
	if unresolved > 0 {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit settlement fence refusal: %w", err)
		}
		committed = true
		return false, nil
	}

	result, err := tx.ExecContext(ctx, `
UPDATE seedruns SET status = ?
WHERE phytomerId = ? AND handle = ? AND pollen = ? AND substrate = ? AND status = ?`,
		seedStatusSettling,
		target.PhytomerID,
		target.Handle,
		target.Pollen,
		target.Substrate,
		seedStatusRunning,
	)
	if err != nil {
		return false, fmt.Errorf("acquire settlement fence: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("settlement fence rows: %w", err)
	}
	if affected != 1 {
		return false, ErrContinuationTargetChanged
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit settlement fence: %w", err)
	}
	committed = true
	return true, nil
}

// CompleteSeedSettlement persists successful Fruit only for an exact target
// that is still settling and has no unresolved continuation.
func (s *Store) CompleteSeedSettlement(ctx context.Context, target SeedTarget, run SeedRun) error {
	if s == nil {
		return fmt.Errorf("history store is not available")
	}
	target = target.trimmed()
	if err := target.validate(); err != nil {
		return err
	}
	if strings.TrimSpace(run.Status) != seedStatusSatisfied {
		return ErrSeedSettlementInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin successful settlement: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	seed, err := s.requireExactSeedTargetTx(ctx, tx, target)
	if err != nil {
		return err
	}
	if strings.TrimSpace(seed.Status) != seedStatusSettling {
		return ErrSeedSettlementNotFenced
	}
	unresolved, err := s.countUnresolvedContinuationsTx(ctx, tx, target.PhytomerID)
	if err != nil {
		return err
	}
	if unresolved > 0 {
		return ErrSeedSettlementNotFenced
	}
	run.Handle = target.Handle
	run.PhytomerID = target.PhytomerID
	run.Pollen = target.Pollen
	run.Substrate = target.Substrate
	run.Status = seedStatusSatisfied
	if run.FinishedAt.IsZero() {
		run.FinishedAt = time.Now().UTC()
	}
	if err := s.updateSeedRunResultTx(ctx, tx, run, seedStatusSettling); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit successful settlement: %w", err)
	}
	committed = true
	return nil
}

// AccountSeedTerminalFailure atomically terminalizes the exact Seed and fails
// every unresolved continuation for that Phytomer. If unresolved continuation
// exists, the durable Seed outcome is withered with a safe delivery failure.
func (s *Store) AccountSeedTerminalFailure(ctx context.Context, target SeedTarget, run SeedRun) (TerminalFailureAccount, error) {
	if s == nil {
		return TerminalFailureAccount{}, fmt.Errorf("history store is not available")
	}
	target = target.trimmed()
	if err := target.validate(); err != nil {
		return TerminalFailureAccount{}, err
	}
	status := strings.TrimSpace(run.Status)
	if !seedStatusIsTerminalFailure(status) {
		return TerminalFailureAccount{}, ErrSeedSettlementInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TerminalFailureAccount{}, fmt.Errorf("begin terminal failure accounting: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	seed, err := s.requireExactSeedTargetTx(ctx, tx, target)
	if err != nil {
		return TerminalFailureAccount{}, err
	}
	current := strings.TrimSpace(seed.Status)
	unresolved, err := s.failUnresolvedContinuationsTx(ctx, tx, target.PhytomerID)
	if err != nil {
		return TerminalFailureAccount{}, err
	}
	if unresolved > 0 {
		status = seedStatusWithered
		run.Error = continuationUndeliverableError
		run.Branch = ""
		run.Commit = ""
	}

	run.Handle = target.Handle
	run.PhytomerID = target.PhytomerID
	run.Pollen = target.Pollen
	run.Substrate = target.Substrate
	run.Status = status
	if run.FinishedAt.IsZero() {
		run.FinishedAt = time.Now().UTC()
	}

	switch {
	case current == seedStatusRunning || current == seedStatusSettling:
		if err := s.updateSeedRunResultTx(ctx, tx, run, current); err != nil {
			return TerminalFailureAccount{}, err
		}
	case seedStatusIsTerminal(current):
		if current == seedStatusSatisfied && status != seedStatusSatisfied {
			return TerminalFailureAccount{}, ErrSeedSettlementInvalid
		}
		if unresolved > 0 && current != seedStatusWithered {
			if err := s.updateSeedRunResultTx(ctx, tx, run, current); err != nil {
				return TerminalFailureAccount{}, err
			}
		}
	default:
		return TerminalFailureAccount{}, ErrContinuationNotEligible
	}

	if err := tx.Commit(); err != nil {
		return TerminalFailureAccount{}, fmt.Errorf("commit terminal failure accounting: %w", err)
	}
	committed = true
	return TerminalFailureAccount{UnresolvedFailed: unresolved}, nil
}

// ReconcileOrphanedSeedWork terminalizes running/settling Seeds from a previous
// process and fails unresolved continuation. Delivered rows stay delivered.
// The transaction is idempotent.
func (s *Store) ReconcileOrphanedSeedWork(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("history store is not available")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin orphaned seed reconciliation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	orphans, err := s.listSeedRunsByStatusTx(ctx, tx, seedStatusRunning, seedStatusSettling)
	if err != nil {
		return err
	}
	finishedAt := time.Now().UTC()
	for _, seed := range orphans {
		run := seed
		run.Status = seedStatusWithered
		run.Error = seedRestartInterruptedError
		run.Branch = ""
		run.Commit = ""
		run.FinishedAt = finishedAt
		if err := s.updateSeedRunResultTx(ctx, tx, run, strings.TrimSpace(seed.Status)); err != nil {
			return err
		}
		if _, err := s.failUnresolvedContinuationsTx(ctx, tx, seed.PhytomerID); err != nil {
			return err
		}
	}
	if _, err := s.failStaleUnresolvedContinuationsTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit orphaned seed reconciliation: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) requireExactSeedTargetTx(ctx context.Context, tx *sql.Tx, target SeedTarget) (SeedRun, error) {
	seed, found, err := s.getSeedRunByPhytomerTx(ctx, tx, target.PhytomerID)
	if err != nil {
		return SeedRun{}, err
	}
	if !found {
		return SeedRun{}, ErrContinuationNotFound
	}
	if strings.TrimSpace(seed.Pollen) != target.Pollen {
		return SeedRun{}, ErrContinuationPollenMismatch
	}
	if strings.TrimSpace(seed.Substrate) != target.Substrate {
		return SeedRun{}, ErrContinuationTargetChanged
	}
	if strings.TrimSpace(seed.Handle) != target.Handle {
		return SeedRun{}, ErrContinuationTargetChanged
	}
	if strings.TrimSpace(seed.PhytomerID) != target.PhytomerID {
		return SeedRun{}, ErrContinuationTargetChanged
	}
	return seed, nil
}

func (s *Store) listContinuationsByStatesTx(ctx context.Context, tx *sql.Tx, phytomerID string, states ...string) ([]Continuation, error) {
	if len(states) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(states))
	args := make([]any, 0, 1+len(states))
	args = append(args, phytomerID)
	for i, state := range states {
		placeholders[i] = "?"
		args = append(args, state)
	}
	query := `SELECT ` + continuationSelectColumns + `
FROM continuations
WHERE phytomerId = ? AND deliveryState IN (` + strings.Join(placeholders, ", ") + `)
ORDER BY sequence ASC`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list continuations by state: %w", err)
	}
	defer rows.Close()
	out := make([]Continuation, 0)
	for rows.Next() {
		rec, err := s.scanContinuation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate continuations by state: %w", err)
	}
	return out, nil
}

func (s *Store) countUnresolvedContinuationsTx(ctx context.Context, tx *sql.Tx, phytomerID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM continuations
WHERE phytomerId = ? AND deliveryState IN (?, ?)`,
		phytomerID, continuationDeliveryPending, continuationDeliveryDelivering,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count unresolved continuations: %w", err)
	}
	return count, nil
}

func (s *Store) failUnresolvedContinuationsTx(ctx context.Context, tx *sql.Tx, phytomerID string) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE continuations
SET deliveryState = ?, failedAt = ?
WHERE phytomerId = ? AND deliveryState IN (?, ?)`,
		continuationDeliveryFailed,
		now,
		phytomerID,
		continuationDeliveryPending,
		continuationDeliveryDelivering,
	)
	if err != nil {
		return 0, fmt.Errorf("fail unresolved continuations: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("fail unresolved continuation rows: %w", err)
	}
	return int(affected), nil
}

func (s *Store) failStaleUnresolvedContinuationsTx(ctx context.Context, tx *sql.Tx) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE continuations
SET deliveryState = ?, failedAt = ?
WHERE deliveryState IN (?, ?)
AND phytomerId NOT IN (SELECT phytomerId FROM seedruns WHERE status = ?)`,
		continuationDeliveryFailed,
		now,
		continuationDeliveryPending,
		continuationDeliveryDelivering,
		seedStatusRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("fail stale unresolved continuations: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("fail stale unresolved continuation rows: %w", err)
	}
	return int(affected), nil
}

func (s *Store) transitionContinuationDeliveryTx(ctx context.Context, tx *sql.Tx, continuationID, phytomerID, fromState, toState, now, deliveredAt string) error {
	continuationID = strings.TrimSpace(continuationID)
	if continuationID == "" || phytomerID == "" {
		return ErrContinuationInvalid
	}
	if !continuationDeliveryTransitionAllowed(fromState, toState) {
		return ErrContinuationDeliveryState
	}

	query := `
UPDATE continuations
SET deliveryState = ?`
	args := []any{toState}
	if toState == continuationDeliveryDelivered {
		query += `, deliveredAt = ?`
		args = append(args, deliveredAt)
	}
	if toState == continuationDeliveryFailed {
		query += `, failedAt = ?`
		args = append(args, now)
	}
	query += `
WHERE continuationId = ? AND phytomerId = ? AND deliveryState = ?`
	args = append(args, continuationID, phytomerID, fromState)

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("transition continuation delivery: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("continuation delivery rows: %w", err)
	}
	if affected != 1 {
		return ErrContinuationDeliveryState
	}
	return nil
}

func continuationDeliveryTransitionAllowed(fromState, toState string) bool {
	switch fromState {
	case continuationDeliveryPending:
		return toState == continuationDeliveryDelivering || toState == continuationDeliveryDelivered || toState == continuationDeliveryFailed
	case continuationDeliveryDelivering:
		return toState == continuationDeliveryDelivered || toState == continuationDeliveryFailed
	default:
		return false
	}
}

func (s *Store) listSeedRunsByStatusTx(ctx context.Context, tx *sql.Tx, statuses ...string) ([]SeedRun, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, len(statuses))
	for i, status := range statuses {
		placeholders[i] = "?"
		args[i] = status
	}
	query := `
SELECT handle, pollen, phytomerId, substrate, goal, status, iterations, branch, fruitCommit, diff, logs, error, startedAt, finishedAt, observation
FROM seedruns
WHERE status IN (` + strings.Join(placeholders, ", ") + `)`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list seed runs by status: %w", err)
	}
	defer rows.Close()
	out := make([]SeedRun, 0)
	for rows.Next() {
		var run SeedRun
		var startedAt, finishedAt, observation string
		if err := rows.Scan(
			&run.Handle, &run.Pollen, &run.PhytomerID, &run.Substrate, &run.Goal, &run.Status, &run.Iterations,
			&run.Branch, &run.Commit, &run.Diff, &run.Logs, &run.Error, &startedAt, &finishedAt, &observation); err != nil {
			return nil, fmt.Errorf("scan seed run by status: %w", err)
		}
		if err := s.decodeSeedRun(&run, startedAt, finishedAt, observation); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate seed runs by status: %w", err)
	}
	return out, nil
}

func (s *Store) updateSeedRunResultTx(ctx context.Context, tx *sql.Tx, run SeedRun, expectedStatus string) error {
	if strings.TrimSpace(run.Handle) == "" || strings.TrimSpace(run.PhytomerID) == "" {
		return ErrContinuationInvalid
	}
	finishedAt := ""
	if !run.FinishedAt.IsZero() {
		finishedAt = run.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	goal, err := s.enc(run.Goal, "historydb/seedruns/goal")
	if err != nil {
		return fmt.Errorf("encrypt seed run goal: %w", err)
	}
	diff, err := s.enc(run.Diff, "historydb/seedruns/diff")
	if err != nil {
		return fmt.Errorf("encrypt seed run diff: %w", err)
	}
	logs, err := s.enc(run.Logs, "historydb/seedruns/logs")
	if err != nil {
		return fmt.Errorf("encrypt seed run logs: %w", err)
	}
	runError, err := s.enc(run.Error, "historydb/seedruns/error")
	if err != nil {
		return fmt.Errorf("encrypt seed run error: %w", err)
	}
	observation, err := encodeSeedRunObservation(run)
	if err != nil {
		return fmt.Errorf("encode seed run observation: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE seedruns SET
	goal = ?,
	status = ?,
	iterations = ?,
	branch = ?,
	fruitCommit = ?,
	diff = ?,
	logs = ?,
	error = ?,
	finishedAt = ?,
	observation = CASE WHEN ? = '' THEN observation ELSE ? END
WHERE handle = ? AND phytomerId = ? AND pollen = ? AND substrate = ? AND status = ?`,
		goal,
		run.Status,
		run.Iterations,
		run.Branch,
		run.Commit,
		diff,
		logs,
		runError,
		finishedAt,
		observation,
		observation,
		run.Handle,
		run.PhytomerID,
		run.Pollen,
		run.Substrate,
		expectedStatus,
	)
	if err != nil {
		return fmt.Errorf("update seed run result: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("seed run result rows: %w", err)
	}
	if affected != 1 {
		return ErrContinuationTargetChanged
	}
	return nil
}

func uniqueContinuationIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

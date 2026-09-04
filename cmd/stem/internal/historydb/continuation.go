package historydb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	continuationDeliveryPending = "pending"
	continuationIntentAAD       = "historydb/continuations/intent"
)

// Continuation is one durable accepted continued-intent record for a
// Seed-owned Phytomer. Intent is encrypted at rest using the same
// historydb cipher as other payload columns.
type Continuation struct {
	ContinuationID string
	PhytomerID     string
	Pollen         string
	Substrate      string
	IdempotencyKey string
	IntentDigest   string
	Intent         string
	Sequence       int
	DeliveryState  string
	AcceptedAt     time.Time
	DeliveredAt    time.Time
	FailedAt       time.Time
}

// ContinuationAcceptance is the atomic insert request. Substrate is not
// accepted from the caller: ownership is read from seedruns inside the
// same transaction.
type ContinuationAcceptance struct {
	PhytomerID     string
	Pollen         string
	IdempotencyKey string
	Intent         string
	IntentDigest   string
}

var (
	ErrContinuationNotFound            = errors.New("phytomer continuation target not found")
	ErrContinuationPollenMismatch      = errors.New("phytomer continuation pollen does not match seed ownership")
	ErrContinuationNotEligible         = errors.New("phytomer is not continuation-eligible")
	ErrContinuationIdempotencyConflict = errors.New("phytomer continuation idempotency key was reused with different intent")
	ErrContinuationInvalid             = errors.New("phytomer continuation request is invalid")
)

func continuationIntentDigest(intent string) string {
	sum := sha256.Sum256([]byte(intent))
	return hex.EncodeToString(sum[:])
}

func generateContinuationID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("mint continuation id: %w", err)
	}
	return "continuation-" + hex.EncodeToString(buf), nil
}

func seedRunIsContinuationEligible(status, substrate string) error {
	if strings.TrimSpace(substrate) == "" {
		return ErrContinuationNotEligible
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return ErrContinuationNotEligible
	}
	switch status {
	case "satisfied", "exhausted", "withered", "fruit-publication-failed":
		return ErrContinuationNotEligible
	default:
		return nil
	}
}

// ResolveContinuationTarget returns the Seed ownership row for phytomerID.
// Missing ownership is not an error; the boolean is false. Multiple Seed
// rows for one Phytomer fail closed.
func (s *Store) ResolveContinuationTarget(ctx context.Context, phytomerID string) (SeedRun, bool, error) {
	if s == nil {
		return SeedRun{}, false, fmt.Errorf("history store is not available")
	}
	return s.GetSeedRunByPhytomer(ctx, phytomerID)
}

// AcceptContinuation atomically records continued intent for a
// continuation-eligible Seed-owned Phytomer. Idempotency is the tuple
// (phytomer, pollen, idempotency key). Sequence is assigned from durable
// MAX(sequence) inside the transaction, not a process-local counter.
func (s *Store) AcceptContinuation(ctx context.Context, in ContinuationAcceptance) (Continuation, error) {
	if s == nil {
		return Continuation{}, fmt.Errorf("history store is not available")
	}
	phytomerID := strings.TrimSpace(in.PhytomerID)
	pollen := strings.TrimSpace(in.Pollen)
	key := strings.TrimSpace(in.IdempotencyKey)
	intent := strings.TrimSpace(in.Intent)
	if phytomerID == "" || key == "" || intent == "" {
		return Continuation{}, ErrContinuationInvalid
	}
	digest := continuationIntentDigest(intent)
	if trimmed := strings.TrimSpace(in.IntentDigest); trimmed != "" && trimmed != digest {
		return Continuation{}, ErrContinuationInvalid
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Continuation{}, fmt.Errorf("begin continuation acceptance: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	seed, found, err := s.getSeedRunByPhytomerTx(ctx, tx, phytomerID)
	if err != nil {
		return Continuation{}, err
	}
	if !found {
		return Continuation{}, ErrContinuationNotFound
	}
	if strings.TrimSpace(seed.Pollen) != pollen {
		return Continuation{}, ErrContinuationPollenMismatch
	}
	if err := seedRunIsContinuationEligible(seed.Status, seed.Substrate); err != nil {
		return Continuation{}, err
	}

	existing, found, err := s.getContinuationByIdempotencyTx(ctx, tx, phytomerID, pollen, key)
	if err != nil {
		return Continuation{}, err
	}
	if found {
		if existing.IntentDigest != digest {
			return Continuation{}, ErrContinuationIdempotencyConflict
		}
		if err := tx.Commit(); err != nil {
			return Continuation{}, fmt.Errorf("commit continuation idempotent read: %w", err)
		}
		committed = true
		return existing, nil
	}

	id, err := generateContinuationID()
	if err != nil {
		return Continuation{}, err
	}
	var nextSeq int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM continuations WHERE phytomerId = ?`, phytomerID).Scan(&nextSeq); err != nil {
		return Continuation{}, fmt.Errorf("allocate continuation sequence: %w", err)
	}

	encryptedIntent, err := s.enc(intent, continuationIntentAAD)
	if err != nil {
		return Continuation{}, fmt.Errorf("encrypt continuation intent: %w", err)
	}
	acceptedAt := time.Now().UTC()
	const insert = `
INSERT INTO continuations (
	continuationId, phytomerId, pollen, substrate, idempotencyKey, intentDigest, intent,
	sequence, deliveryState, acceptedAt, deliveredAt, failedAt
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '')`

	_, err = tx.ExecContext(ctx, insert,
		id,
		phytomerID,
		pollen,
		strings.TrimSpace(seed.Substrate),
		key,
		digest,
		encryptedIntent,
		nextSeq,
		continuationDeliveryPending,
		acceptedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Continuation{}, fmt.Errorf("insert continuation: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Continuation{}, fmt.Errorf("commit continuation acceptance: %w", err)
	}
	committed = true

	return Continuation{
		ContinuationID: id,
		PhytomerID:     phytomerID,
		Pollen:         pollen,
		Substrate:      strings.TrimSpace(seed.Substrate),
		IdempotencyKey: key,
		IntentDigest:   digest,
		Intent:         intent,
		Sequence:       nextSeq,
		DeliveryState:  continuationDeliveryPending,
		AcceptedAt:     acceptedAt,
	}, nil
}

// GetContinuation returns one continuation by id. Missing is not an error.
func (s *Store) GetContinuation(ctx context.Context, continuationID string) (Continuation, bool, error) {
	continuationID = strings.TrimSpace(continuationID)
	if continuationID == "" {
		return Continuation{}, false, ErrContinuationInvalid
	}
	const query = `
SELECT continuationId, phytomerId, pollen, substrate, idempotencyKey, intentDigest, intent,
	sequence, deliveryState, acceptedAt, deliveredAt, failedAt
FROM continuations
WHERE continuationId = ?`
	return s.scanContinuationRow(s.db.QueryRowContext(ctx, query, continuationID))
}

// ListContinuationsByPhytomer returns accepted continuations in sequence order.
func (s *Store) ListContinuationsByPhytomer(ctx context.Context, phytomerID string) ([]Continuation, error) {
	phytomerID = strings.TrimSpace(phytomerID)
	if phytomerID == "" {
		return nil, ErrContinuationInvalid
	}
	const query = `
SELECT continuationId, phytomerId, pollen, substrate, idempotencyKey, intentDigest, intent,
	sequence, deliveryState, acceptedAt, deliveredAt, failedAt
FROM continuations
WHERE phytomerId = ?
ORDER BY sequence ASC`
	rows, err := s.db.QueryContext(ctx, query, phytomerID)
	if err != nil {
		return nil, fmt.Errorf("list continuations: %w", err)
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
		return nil, fmt.Errorf("iterate continuations: %w", err)
	}
	return out, nil
}

type continuationRow interface {
	Scan(dest ...any) error
}

func (s *Store) scanContinuationRow(row continuationRow) (Continuation, bool, error) {
	rec, err := s.scanContinuation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Continuation{}, false, nil
	}
	if err != nil {
		return Continuation{}, false, err
	}
	return rec, true, nil
}

func (s *Store) scanContinuation(row continuationRow) (Continuation, error) {
	var rec Continuation
	var acceptedAt, deliveredAt, failedAt string
	if err := row.Scan(
		&rec.ContinuationID,
		&rec.PhytomerID,
		&rec.Pollen,
		&rec.Substrate,
		&rec.IdempotencyKey,
		&rec.IntentDigest,
		&rec.Intent,
		&rec.Sequence,
		&rec.DeliveryState,
		&acceptedAt,
		&deliveredAt,
		&failedAt,
	); err != nil {
		return Continuation{}, err
	}
	var err error
	if rec.Intent, err = s.dec(rec.Intent, continuationIntentAAD); err != nil {
		return Continuation{}, fmt.Errorf("decrypt continuation intent: %w", err)
	}
	if rec.AcceptedAt, err = time.Parse(time.RFC3339Nano, acceptedAt); err != nil {
		return Continuation{}, fmt.Errorf("parse continuation acceptedAt: %w", err)
	}
	if deliveredAt != "" {
		if rec.DeliveredAt, err = time.Parse(time.RFC3339Nano, deliveredAt); err != nil {
			return Continuation{}, fmt.Errorf("parse continuation deliveredAt: %w", err)
		}
	}
	if failedAt != "" {
		if rec.FailedAt, err = time.Parse(time.RFC3339Nano, failedAt); err != nil {
			return Continuation{}, fmt.Errorf("parse continuation failedAt: %w", err)
		}
	}
	return rec, nil
}

func (s *Store) getContinuationByIdempotencyTx(ctx context.Context, tx *sql.Tx, phytomerID, pollen, key string) (Continuation, bool, error) {
	const query = `
SELECT continuationId, phytomerId, pollen, substrate, idempotencyKey, intentDigest, intent,
	sequence, deliveryState, acceptedAt, deliveredAt, failedAt
FROM continuations
WHERE phytomerId = ? AND pollen = ? AND idempotencyKey = ?`
	rec, found, err := s.scanContinuationRow(tx.QueryRowContext(ctx, query, phytomerID, pollen, key))
	if err != nil {
		return Continuation{}, false, fmt.Errorf("load continuation by idempotency: %w", err)
	}
	return rec, found, nil
}

func (s *Store) getSeedRunByPhytomerTx(ctx context.Context, tx *sql.Tx, phytomerID string) (SeedRun, bool, error) {
	const query = `
SELECT handle, pollen, phytomerId, substrate, goal, status, iterations, branch, fruitCommit, diff, logs, error, startedAt, finishedAt, observation
FROM seedruns
WHERE phytomerId = ?`

	rows, err := tx.QueryContext(ctx, query, phytomerID)
	if err != nil {
		return SeedRun{}, false, fmt.Errorf("get seed run by phytomer: %w", err)
	}
	defer rows.Close()

	var found []SeedRun
	for rows.Next() {
		var run SeedRun
		var startedAt, finishedAt, observation string
		if err := rows.Scan(
			&run.Handle, &run.Pollen, &run.PhytomerID, &run.Substrate, &run.Goal, &run.Status, &run.Iterations,
			&run.Branch, &run.Commit, &run.Diff, &run.Logs, &run.Error, &startedAt, &finishedAt, &observation); err != nil {
			return SeedRun{}, false, fmt.Errorf("scan seed run by phytomer: %w", err)
		}
		if err := s.decodeSeedRun(&run, startedAt, finishedAt, observation); err != nil {
			return SeedRun{}, false, err
		}
		found = append(found, run)
	}
	if err := rows.Err(); err != nil {
		return SeedRun{}, false, fmt.Errorf("iterate seed run by phytomer: %w", err)
	}
	if len(found) == 0 {
		return SeedRun{}, false, nil
	}
	if len(found) > 1 {
		return SeedRun{}, false, fmt.Errorf("phytomer %s has %d seed runs; refusing to choose", phytomerID, len(found))
	}
	return found[0], true, nil
}

// Package historydb persists the Go Stem's unified state to a lightweight
// local SQLite database (.tendril/history.db) using the CGO-free
// modernc.org/sqlite driver, keeping the tendril binary purely portable.
//
// It is the durable backbone of the Tendril OS: Tendril sessions, unified chat
// logs, all EventBus telemetry, and Sprout execution histories are written
// here so the future UI never loses state on a browser refresh. Setting
// TENDRIL_DB_LOGGING=false bypasses SQLite entirely for high-performance
// headless runs.
package historydb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/heartwood"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
	"github.com/opentendril/opentendril/cmd/stem/internal/telemetry"
)

const (
	// EnvDBLogging toggles SQLite persistence. Defaults to enabled; set to
	// "false" (or "0"/"off") to bypass the database entirely.
	EnvDBLogging = "TENDRIL_DB_LOGGING"

	// EnvDBPath overrides the database location. Defaults to
	// <repo-root>/.tendril/history.db.
	EnvDBPath = "TENDRIL_DB_PATH"

	// EnvEncryptAtRest, when off/false/0/no/disabled, writes payload columns in
	// plaintext. Reads still decrypt any pre-existing ciphertext.
	EnvEncryptAtRest = "TENDRIL_ENCRYPT_AT_REST"
)

// SproutRun is one Sprout execution history record.
type SproutRun struct {
	RunID      string    `json:"runId"`
	SessionID  string    `json:"sessionId,omitempty"`
	StepID     string    `json:"stepId,omitempty"`
	Origin     string    `json:"origin,omitempty"`
	Model      string    `json:"model,omitempty"`
	Genotype   string    `json:"genotype,omitempty"`
	Transcript string    `json:"transcript,omitempty"`
	Status     string    `json:"status"`
	Output     string    `json:"output,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

// SeedRun is one bounded-task (seed.grow) execution: the durable handle a
// Pollinator dispatches against and later collects, plus the reviewable Fruit
// (status, branch, diff, logs). It records the dispatching Pollen so collection
// can be scoped to the subject that owns the run.
type SeedRun struct {
	Handle     string    `json:"handle"`
	Pollen     string    `json:"pollen,omitempty"`
	Substrate  string    `json:"substrate,omitempty"`
	Goal       string    `json:"goal,omitempty"`
	Status     string    `json:"status"`
	Iterations int       `json:"iterations"`
	Branch     string    `json:"branch,omitempty"`
	Diff       string    `json:"diff,omitempty"`
	Logs       string    `json:"logs,omitempty"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
}

// EventRecord is one persisted EventBus telemetry row.
type EventRecord struct {
	ID        int64                  `json:"id"`
	SessionID string                 `json:"sessionId,omitempty"`
	Type      string                 `json:"type"`
	Source    string                 `json:"source,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
}

// Store is the SQLite-backed history database. It implements session.Store
// for the SessionManager and eventbus.Sink for telemetry persistence.
type Store struct {
	db            *sql.DB
	path          string
	eventErrors   atomic.Int64
	cipher        *heartwood.Cipher
	encryptWrites bool
}

func encryptionDisabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(EnvEncryptAtRest)))
	switch value {
	case "false", "0", "off", "no", "disabled":
		return true
	default:
		return false
	}
}

// LoggingEnabled reports whether SQLite persistence is switched on.
func LoggingEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(EnvDBLogging)))
	switch value {
	case "false", "0", "off", "no", "disabled":
		return false
	default:
		return true
	}
}

// DefaultPath returns the standard database location for a repo root.
func DefaultPath(root string) string {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	return filepath.Join(root, ".tendril", "history.db")
}

// OpenFromEnv opens the history database honoring the environment toggles.
// It returns (nil, nil) when TENDRIL_DB_LOGGING=false so callers can run
// fully headless without touching disk.
func OpenFromEnv(ctx context.Context, root string) (*Store, error) {
	if !LoggingEnabled() {
		return nil, nil
	}

	path := strings.TrimSpace(os.Getenv(EnvDBPath))
	if path == "" {
		path = DefaultPath(root)
	}
	return Open(ctx, path)
}

// Open opens (creating if needed) the history database at the given path.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create history database directory: %w", err)
	}

	keyPath := filepath.Join(filepath.Dir(path), "rhizome.key")
	material, err := heartwood.ResolveKey(keyPath)

	var cipher *heartwood.Cipher
	if err == nil {
		cipher, err = heartwood.NewCipher(material)
	}

	if err != nil {
		if encryptionDisabled() {
			log.Printf("⚠️ historydb: encryption opt-out set, ignoring cipher error: %v", err)
		} else {
			return nil, fmt.Errorf("resolve encryption cipher: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open history database %s: %w", path, err)
	}
	// modernc.org/sqlite serializes access per connection; a single connection
	// with WAL avoids SQLITE_BUSY under the concurrent gateway surfaces.
	db.SetMaxOpenConns(1)

	store := &Store{
		db:            db,
		path:          path,
		cipher:        cipher,
		encryptWrites: !encryptionDisabled() && cipher != nil,
	}
	if err := store.initSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path returns the database file location.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// enc returns the versioned ciphertext for a payload value, or the value
// unchanged when empty, encryption is off, or no cipher is configured.
func (s *Store) enc(plaintext, aad string) (string, error) {
	if plaintext == "" || !s.encryptWrites || s.cipher == nil {
		return plaintext, nil
	}
	return s.cipher.Encrypt(plaintext, []byte(aad))
}

// dec reverses enc and passes through pre-existing plaintext (LegacyPlaintext).
func (s *Store) dec(stored, aad string) (string, error) {
	if stored == "" || s.cipher == nil {
		return stored, nil
	}
	return s.cipher.Decrypt(stored, []byte(aad), heartwood.LegacyPlaintext)
}

func (s *Store) initSchema(ctx context.Context) error {
	const pragmas = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;`

	if _, err := s.db.ExecContext(ctx, pragmas); err != nil {
		return fmt.Errorf("apply history pragmas: %w", err)
	}

	const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	sessionId TEXT PRIMARY KEY,
	origin TEXT NOT NULL,
	createdAt TEXT NOT NULL,
	lastActiveAt TEXT NOT NULL,
	preferences TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sessionId TEXT NOT NULL,
	role TEXT NOT NULL,
	content TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	createdAt TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS messagesBySession ON messages(sessionId, id);

CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sessionId TEXT NOT NULL DEFAULT '',
	type TEXT NOT NULL,
	source TEXT NOT NULL DEFAULT '',
	data TEXT NOT NULL DEFAULT '{}',
	createdAt TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS eventsBySession ON events(sessionId, id);
CREATE INDEX IF NOT EXISTS eventsByType ON events(type, id);

CREATE TABLE IF NOT EXISTS sproutruns (
	runId TEXT PRIMARY KEY,
	sessionId TEXT NOT NULL DEFAULT '',
	stepId TEXT NOT NULL DEFAULT '',
	origin TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	genotype TEXT NOT NULL DEFAULT '',
	transcript TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	output TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	startedAt TEXT NOT NULL,
	finishedAt TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sproutrunsBySession ON sproutruns(sessionId, startedAt);
CREATE TABLE IF NOT EXISTS seedruns (
	handle TEXT PRIMARY KEY,
	pollen TEXT NOT NULL DEFAULT '',
	substrate TEXT NOT NULL DEFAULT '',
	goal TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	iterations INTEGER NOT NULL DEFAULT 0,
	branch TEXT NOT NULL DEFAULT '',
	diff TEXT NOT NULL DEFAULT '',
	logs TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	startedAt TEXT NOT NULL,
	finishedAt TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS seedrunsByPollen ON seedruns(pollen, startedAt);`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize history schema: %w", err)
	}
	return nil
}

// --- session.Store implementation -------------------------------------------

func (s *Store) SaveSession(ctx context.Context, sess session.Phytomer) error {
	prefsBytes, err := json.Marshal(sess.Preferences)
	if err != nil {
		return fmt.Errorf("encode session preferences: %w", err)
	}
	prefs, err := s.enc(string(prefsBytes), "historydb/sessions/preferences")
	if err != nil {
		return fmt.Errorf("encrypt session preferences: %w", err)
	}

	const statement = `
INSERT INTO sessions (sessionId, origin, createdAt, lastActiveAt, preferences)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(sessionId) DO UPDATE SET
	origin = excluded.origin,
	lastActiveAt = excluded.lastActiveAt,
	preferences = excluded.preferences`

	_, err = s.db.ExecContext(ctx, statement,
		sess.ID,
		sess.Origin,
		sess.CreatedAt.UTC().Format(time.RFC3339Nano),
		sess.LastActiveAt.UTC().Format(time.RFC3339Nano),
		prefs,
	)
	if err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

func (s *Store) DeleteSession(ctx context.Context, sessionID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE sessionId = ?`, sessionID); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE sessionId = ?`, sessionID); err != nil {
		return fmt.Errorf("delete session messages: %w", err)
	}
	return nil
}

func (s *Store) LoadSessions(ctx context.Context) ([]session.Phytomer, error) {
	const query = `SELECT sessionId, origin, createdAt, lastActiveAt, preferences FROM sessions ORDER BY lastActiveAt DESC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("load sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]session.Phytomer, 0)
	for rows.Next() {
		var sess session.Phytomer
		var createdAt, lastActiveAt, prefs string
		if err := rows.Scan(&sess.ID, &sess.Origin, &createdAt, &lastActiveAt, &prefs); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		if sess.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, fmt.Errorf("parse session createdAt: %w", err)
		}
		if sess.LastActiveAt, err = time.Parse(time.RFC3339Nano, lastActiveAt); err != nil {
			return nil, fmt.Errorf("parse session lastActiveAt: %w", err)
		}
		prefsDec, err := s.dec(prefs, "historydb/sessions/preferences")
		if err != nil {
			return nil, fmt.Errorf("decrypt session preferences: %w", err)
		}
		if err := json.Unmarshal([]byte(prefsDec), &sess.Preferences); err != nil {
			return nil, fmt.Errorf("decode session preferences: %w", err)
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return sessions, nil
}

func (s *Store) AppendMessage(ctx context.Context, msg session.Message) error {
	const statement = `
INSERT INTO messages (sessionId, role, content, model, createdAt)
VALUES (?, ?, ?, ?, ?)`

	createdAt := msg.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	encContent, err := s.enc(msg.Content, "historydb/messages/content")
	if err != nil {
		return fmt.Errorf("encrypt message content: %w", err)
	}

	_, err = s.db.ExecContext(ctx, statement,
		msg.SessionID,
		msg.Role,
		encContent,
		msg.Model,
		createdAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("append message: %w", err)
	}
	return nil
}

func (s *Store) LoadMessages(ctx context.Context, sessionID string, limit int) ([]session.Message, error) {
	if limit <= 0 {
		limit = 50
	}

	const query = `
SELECT sessionId, role, content, model, createdAt
FROM (
	SELECT id, sessionId, role, content, model, createdAt
	FROM messages
	WHERE sessionId = ?
	ORDER BY id DESC
	LIMIT ?
)
ORDER BY id ASC`

	rows, err := s.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("load messages: %w", err)
	}
	defer rows.Close()

	messages := make([]session.Message, 0)
	for rows.Next() {
		var msg session.Message
		var createdAt string
		if err := rows.Scan(&msg.SessionID, &msg.Role, &msg.Content, &msg.Model, &createdAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		if msg.Content, err = s.dec(msg.Content, "historydb/messages/content"); err != nil {
			return nil, fmt.Errorf("decrypt message content: %w", err)
		}
		if msg.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, fmt.Errorf("parse message createdAt: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return messages, nil
}

// --- EventBus telemetry persistence ------------------------------------------

// Consume implements eventbus.Sink: every published event lands in SQLite.
// It runs on the bus's dedicated sink goroutine, so failures are logged
// (sparsely) rather than propagated.
func (s *Store) Consume(event eventbus.Event) {
	if err := s.RecordEvent(context.Background(), event); err != nil {
		if s.eventErrors.Add(1)%100 == 1 {
			log.Printf("⚠️ historydb: failed to persist telemetry event: %v", err)
		}
	}
}

// RecordEvent writes one EventBus telemetry event.
func (s *Store) RecordEvent(ctx context.Context, event eventbus.Event) error {
	ev := event
	if !telemetry.RedactionDisabled() {
		ev = telemetry.RedactEvent(event)
	}

	data := "{}"
	if len(ev.Data) > 0 {
		encoded, err := json.Marshal(ev.Data)
		if err != nil {
			return fmt.Errorf("encode event data: %w", err)
		}
		data = string(encoded)
	}

	data, err := s.enc(data, "historydb/events/data")
	if err != nil {
		return fmt.Errorf("encrypt event data: %w", err)
	}

	timestamp := ev.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	const statement = `
INSERT INTO events (sessionId, type, source, data, createdAt)
VALUES (?, ?, ?, ?, ?)`

	_, err = s.db.ExecContext(ctx, statement,
		ev.SessionID,
		string(ev.Type),
		ev.Source,
		data,
		timestamp.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("record event: %w", err)
	}
	return nil
}

// LoadEvents returns recent telemetry rows, optionally filtered by session,
// in chronological order.
func (s *Store) LoadEvents(ctx context.Context, sessionID string, limit int) ([]EventRecord, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
SELECT id, sessionId, type, source, data, createdAt
FROM (
	SELECT id, sessionId, type, source, data, createdAt
	FROM events`
	args := []any{}
	if strings.TrimSpace(sessionID) != "" {
		query += `
	WHERE sessionId = ?`
		args = append(args, sessionID)
	}
	query += `
	ORDER BY id DESC
	LIMIT ?
)
ORDER BY id ASC`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}
	defer rows.Close()

	records := make([]EventRecord, 0)
	for rows.Next() {
		var record EventRecord
		var data, createdAt string
		if err := rows.Scan(&record.ID, &record.SessionID, &record.Type, &record.Source, &data, &createdAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		data, err = s.dec(data, "historydb/events/data")
		if err != nil {
			return nil, fmt.Errorf("decrypt event data: %w", err)
		}
		if data != "" && data != "{}" {
			if err := json.Unmarshal([]byte(data), &record.Data); err != nil {
				return nil, fmt.Errorf("decode event data: %w", err)
			}
		}
		if record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, fmt.Errorf("parse event createdAt: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return records, nil
}

// --- Sprout execution history -------------------------------------------------

// RecordSproutRun upserts one Sprout execution record; call it once when the
// sprout emerges (status "running") and again when it matures or withers.
func (s *Store) RecordSproutRun(ctx context.Context, run SproutRun) error {
	if strings.TrimSpace(run.RunID) == "" {
		return fmt.Errorf("sprout run requires runId")
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}

	finishedAt := ""
	if !run.FinishedAt.IsZero() {
		finishedAt = run.FinishedAt.UTC().Format(time.RFC3339Nano)
	}

	genotype, err := s.enc(run.Genotype, "historydb/sproutruns/genotype")
	if err != nil {
		return fmt.Errorf("encrypt sprout run genotype: %w", err)
	}
	transcript, err := s.enc(run.Transcript, "historydb/sproutruns/transcript")
	if err != nil {
		return fmt.Errorf("encrypt sprout run transcript: %w", err)
	}
	output, err := s.enc(run.Output, "historydb/sproutruns/output")
	if err != nil {
		return fmt.Errorf("encrypt sprout run output: %w", err)
	}
	runError, err := s.enc(run.Error, "historydb/sproutruns/error")
	if err != nil {
		return fmt.Errorf("encrypt sprout run error: %w", err)
	}

	const statement = `
INSERT INTO sproutruns (runId, sessionId, stepId, origin, model, genotype, transcript, status, output, error, startedAt, finishedAt)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(runId) DO UPDATE SET
	status = excluded.status,
	output = excluded.output,
	error = excluded.error,
	finishedAt = excluded.finishedAt`

	_, err = s.db.ExecContext(ctx, statement,
		run.RunID,
		run.SessionID,
		run.StepID,
		run.Origin,
		run.Model,
		genotype,
		transcript,
		run.Status,
		output,
		runError,
		run.StartedAt.UTC().Format(time.RFC3339Nano),
		finishedAt,
	)
	if err != nil {
		return fmt.Errorf("record sprout run: %w", err)
	}
	return nil
}

// LoadSproutRuns returns recent sprout executions, optionally filtered by
// session, most recent first.
func (s *Store) LoadSproutRuns(ctx context.Context, sessionID string, limit int) ([]SproutRun, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `
SELECT runId, sessionId, stepId, origin, model, genotype, transcript, status, output, error, startedAt, finishedAt
FROM sproutruns`
	args := []any{}
	if strings.TrimSpace(sessionID) != "" {
		query += `
WHERE sessionId = ?`
		args = append(args, sessionID)
	}
	query += `
ORDER BY startedAt DESC
LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load sprout runs: %w", err)
	}
	defer rows.Close()

	runs := make([]SproutRun, 0)
	for rows.Next() {
		var run SproutRun
		var startedAt, finishedAt string
		if err := rows.Scan(&run.RunID, &run.SessionID, &run.StepID, &run.Origin, &run.Model, &run.Genotype, &run.Transcript, &run.Status, &run.Output, &run.Error, &startedAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("scan sprout run: %w", err)
		}
		if run.Genotype, err = s.dec(run.Genotype, "historydb/sproutruns/genotype"); err != nil {
			return nil, fmt.Errorf("decrypt sprout run genotype: %w", err)
		}
		if run.Transcript, err = s.dec(run.Transcript, "historydb/sproutruns/transcript"); err != nil {
			return nil, fmt.Errorf("decrypt sprout run transcript: %w", err)
		}
		if run.Output, err = s.dec(run.Output, "historydb/sproutruns/output"); err != nil {
			return nil, fmt.Errorf("decrypt sprout run output: %w", err)
		}
		if run.Error, err = s.dec(run.Error, "historydb/sproutruns/error"); err != nil {
			return nil, fmt.Errorf("decrypt sprout run error: %w", err)
		}
		if run.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt); err != nil {
			return nil, fmt.Errorf("parse sprout run startedAt: %w", err)
		}
		if finishedAt != "" {
			if run.FinishedAt, err = time.Parse(time.RFC3339Nano, finishedAt); err != nil {
				return nil, fmt.Errorf("parse sprout run finishedAt: %w", err)
			}
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sprout runs: %w", err)
	}
	return runs, nil
}

// RecordSeedRun upserts one seed.grow execution keyed by its handle; call it
// once when the run is dispatched (status "running") and again when it settles
// (satisfied / exhausted / withered).
func (s *Store) RecordSeedRun(ctx context.Context, run SeedRun) error {
	if strings.TrimSpace(run.Handle) == "" {
		return fmt.Errorf("seed run requires a handle")
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
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

	const statement = `
INSERT INTO seedruns (handle, pollen, substrate, goal, status, iterations, branch, diff, logs, error, startedAt, finishedAt)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(handle) DO UPDATE SET
	status = excluded.status,
	iterations = excluded.iterations,
	branch = excluded.branch,
	diff = excluded.diff,
	logs = excluded.logs,
	error = excluded.error,
	finishedAt = excluded.finishedAt`

	_, err = s.db.ExecContext(ctx, statement,
		run.Handle,
		run.Pollen,
		run.Substrate,
		goal,
		run.Status,
		run.Iterations,
		run.Branch,
		diff,
		logs,
		runError,
		run.StartedAt.UTC().Format(time.RFC3339Nano),
		finishedAt,
	)
	if err != nil {
		return fmt.Errorf("record seed run: %w", err)
	}
	return nil
}

// GetSeedRun returns one seed.grow execution by handle. The boolean reports
// whether a record was found; a missing handle is not an error.
func (s *Store) GetSeedRun(ctx context.Context, handle string) (SeedRun, bool, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return SeedRun{}, false, fmt.Errorf("handle is required")
	}

	const query = `
SELECT handle, pollen, substrate, goal, status, iterations, branch, diff, logs, error, startedAt, finishedAt
FROM seedruns
WHERE handle = ?`

	var run SeedRun
	var startedAt, finishedAt string
	err := s.db.QueryRowContext(ctx, query, handle).Scan(
		&run.Handle, &run.Pollen, &run.Substrate, &run.Goal, &run.Status, &run.Iterations,
		&run.Branch, &run.Diff, &run.Logs, &run.Error, &startedAt, &finishedAt)
	if err == sql.ErrNoRows {
		return SeedRun{}, false, nil
	}
	if err != nil {
		return SeedRun{}, false, fmt.Errorf("get seed run: %w", err)
	}
	if run.Goal, err = s.dec(run.Goal, "historydb/seedruns/goal"); err != nil {
		return SeedRun{}, false, fmt.Errorf("decrypt seed run goal: %w", err)
	}
	if run.Diff, err = s.dec(run.Diff, "historydb/seedruns/diff"); err != nil {
		return SeedRun{}, false, fmt.Errorf("decrypt seed run diff: %w", err)
	}
	if run.Logs, err = s.dec(run.Logs, "historydb/seedruns/logs"); err != nil {
		return SeedRun{}, false, fmt.Errorf("decrypt seed run logs: %w", err)
	}
	if run.Error, err = s.dec(run.Error, "historydb/seedruns/error"); err != nil {
		return SeedRun{}, false, fmt.Errorf("decrypt seed run error: %w", err)
	}
	if run.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt); err != nil {
		return SeedRun{}, false, fmt.Errorf("parse seed run startedAt: %w", err)
	}
	if finishedAt != "" {
		if run.FinishedAt, err = time.Parse(time.RFC3339Nano, finishedAt); err != nil {
			return SeedRun{}, false, fmt.Errorf("parse seed run finishedAt: %w", err)
		}
	}
	return run, true, nil
}

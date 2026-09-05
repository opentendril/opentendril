// Package historydb persists the Go Stem's unified state to a lightweight
// local SQLite database (.tendril/history.db) using the CGO-free
// modernc.org/sqlite driver, keeping the tendril binary purely portable.
//
// It is the durable backbone of OpenTendril: Tendril sessions, unified chat
// logs, all EventBus telemetry, and Sprout execution histories are written
// here so the Greenhouse never loses state on a browser refresh. Setting
// TENDRIL_DB_LOGGING=false bypasses SQLite entirely for high-performance
// headless runs.
package historydb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/heartwood"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
	"github.com/opentendril/opentendril/cmd/stem/internal/telemetry"
)

// sqliteConstraint is SQLite's SQLITE_CONSTRAINT primary result code. Extended
// constraint codes keep this in the low 8 bits.
const sqliteConstraint = 19

// ErrSeedHandleExists is returned when an insert-only Seed opening collides
// with an already-recorded handle. Settlement of an established handle still
// uses RecordSeedRun's upsert.
var ErrSeedHandleExists = errors.New("seed handle already exists")

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

	// EnvHistoryRetentionDays configures age-based pruning of messages, events,
	// sproutruns, and seedruns. Unset, empty, non-numeric, or non-positive means
	// retention is disabled — the default is today's unbounded growth, unchanged.
	EnvHistoryRetentionDays = "TENDRIL_HISTORY_RETENTION_DAYS"
)

// historyRetentionDaysFromEnv reads EnvHistoryRetentionDays. Returns 0
// (disabled) when unset; logs a warning and also returns 0 for a
// present-but-invalid value, so a typo fails safe (no pruning) rather than
// pruning on some unintended default.
func historyRetentionDaysFromEnv() int {
	raw := strings.TrimSpace(os.Getenv(EnvHistoryRetentionDays))
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		log.Printf("⚠️ invalid %s=%q (want a positive integer number of days); retention disabled: %v", EnvHistoryRetentionDays, raw, err)
		return 0
	}
	return parsed
}

// currentSchemaVersion is the history database's current schema generation.
// Bump it and add a forward step in migrateSchema when a change alters an
// existing table rather than only adding a new IF NOT EXISTS one. The number is
// what stops an older binary opening a shape it would misread, so a shape
// change that leaves it alone is a shape change with no such guard.
//
// Version 4 adds sproutruns.provider and sproutruns.observation so a finished
// run can carry the structured observation contract (outcome, failure
// category, safe provider diagnostic, request-begun, tool count) without
// collapsing it to matured/withered + free-text.
//
// Version 5 records a Seed's canonical Phytomer identity and truthful Fruit
// commit on seedruns. Legacy rows keep an empty phytomerId; none is invented.
//
// Version 6 records the structured, safe Seed Fruit-publication diagnostic in
// seedruns.observation. Legacy rows keep an empty observation envelope.
//
// Version 7 records bounded Seed verification diagnostics in the same
// seedruns.observation envelope. Legacy rows keep an empty verification list;
// no historical verification facts are invented.
const currentSchemaVersion = 7

// SproutRun is one Sprout execution history record. It records the dispatching
// Pollen and the substrate the work targeted so the read surface can scope a
// run to the subject that owns it.
type SproutRun struct {
	RunID     string `json:"runId"`
	SessionID string `json:"sessionId,omitempty"`
	StepID    string `json:"stepId,omitempty"`
	Origin    string `json:"origin,omitempty"`
	// Pollen is the subject that dispatched the run, and empty for a run the
	// operator started directly. It is settled by the first write and never
	// reassigned afterwards, so a recorded run cannot change hands.
	Pollen string `json:"pollen,omitempty"`
	// Substrate names the workspace the run targeted. A delegated read is
	// evaluated against it, so observation is bounded by the same substrate
	// scope that bounded the dispatch.
	Substrate  string         `json:"substrate,omitempty"`
	Provider   string         `json:"provider,omitempty"`
	Model      string         `json:"model,omitempty"`
	Genotype   string         `json:"genotype,omitempty"`
	Transcript string         `json:"transcript,omitempty"`
	Status     string         `json:"status"`
	Output     string         `json:"output,omitempty"`
	Error      string         `json:"error,omitempty"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt time.Time      `json:"finishedAt,omitempty"`
	Usage      SproutRunUsage `json:"usage,omitempty"`
	// Outcome is the Conductor's SproutOutcome* verdict, persisted rather
	// than collapsed into status.
	Outcome string `json:"outcome,omitempty"`
	// FailureCategory is the Core-owned Botanist-facing class.
	FailureCategory string `json:"failureCategory,omitempty"`
	// ProviderDiagnostic is the credential-free provider explanation.
	ProviderDiagnostic *ProviderDiagnostic `json:"providerDiagnostic,omitempty"`
	// ProviderRequestAttempted is true when the first Mycorrhizal request
	// was issued.
	ProviderRequestAttempted bool `json:"providerRequestAttempted"`
	// ToolInvocations is how many terrarium tool calls the Sprout made.
	ToolInvocations int `json:"toolInvocations"`
}

// ProviderDiagnostic is the durable copy of the Core's safe provider
// explanation. It is stored as part of the observation envelope.
type ProviderDiagnostic struct {
	StatusCode int    `json:"statusCode,omitempty"`
	Message    string `json:"message,omitempty"`
	Provider   string `json:"provider,omitempty"`
}

// sproutRunObservation is the JSON envelope persisted in sproutruns.observation.
type sproutRunObservation struct {
	Outcome                  string              `json:"outcome,omitempty"`
	FailureCategory          string              `json:"failureCategory,omitempty"`
	ProviderDiagnostic       *ProviderDiagnostic `json:"providerDiagnostic,omitempty"`
	ProviderRequestAttempted bool                `json:"providerRequestAttempted,omitempty"`
	ToolInvocations          int                 `json:"toolInvocations,omitempty"`
}

// UsageComponent is one fail-honest usage component stored on a Sprout run.
// Pointer token and cost fields omit when nil so absence stays absence; a
// pointer to zero remains present so a measured zero is not rewritten as
// missing. CostAmount is the exact provider-native decimal string. There is
// no combined token or monetary total at this layer.
type UsageComponent struct {
	RequestsMade     bool    `json:"requestsMade"`
	PromptTokens     *int    `json:"promptTokens,omitempty"`
	CompletionTokens *int    `json:"completionTokens,omitempty"`
	TotalTokens      *int    `json:"totalTokens,omitempty"`
	CostAmount       *string `json:"costAmount,omitempty"`
	CostUnit         *string `json:"costUnit,omitempty"`
	CostProvenance   *string `json:"costProvenance,omitempty"`
	Provider         string  `json:"provider,omitempty"`
	Model            string  `json:"model,omitempty"`
}

// SproutRunUsage is the durable component envelope for one Sprout run.
// Execution and post-run stay separate; neither is folded into the other.
type SproutRunUsage struct {
	Execution *UsageComponent `json:"execution,omitempty"`
	PostRun   *UsageComponent `json:"postRun,omitempty"`
}

// SeedRun is one bounded-task (seed.grow) execution: the durable handle a
// Pollinator dispatches against and later collects, plus the reviewable Fruit
// (status, branch, diff, logs). It records the dispatching Pollen so collection
// can be scoped to the subject that owns the run.
type SeedRun struct {
	Handle string `json:"handle"`
	Pollen string `json:"pollen,omitempty"`
	// PhytomerID is the Stem-created execution/observation identity for this
	// Seed growth. Empty on historical rows that never had a truthful relation.
	PhytomerID              string                       `json:"phytomerId,omitempty"`
	Substrate               string                       `json:"substrate,omitempty"`
	Goal                    string                       `json:"goal,omitempty"`
	Status                  string                       `json:"status"`
	Iterations              int                          `json:"iterations"`
	Branch                  string                       `json:"branch,omitempty"`
	Commit                  string                       `json:"commit,omitempty"`
	Diff                    string                       `json:"diff,omitempty"`
	Logs                    string                       `json:"logs,omitempty"`
	Error                   string                       `json:"error,omitempty"`
	PublicationDiagnostic   *SeedPublicationDiagnostic   `json:"publicationDiagnostic,omitempty"`
	VerificationDiagnostics []SeedVerificationDiagnostic `json:"verificationDiagnostics,omitempty"`
	StartedAt               time.Time                    `json:"startedAt"`
	FinishedAt              time.Time                    `json:"finishedAt,omitempty"`
}

// SeedPublicationDiagnostic is the credential- and content-safe durable
// explanation of a managed Fruit publication failure.
type SeedPublicationDiagnostic struct {
	FailureCategory string `json:"failureCategory"`
	ExecutionStatus string `json:"executionStatus"`
	Phase           string `json:"phase"`
	Outcome         string `json:"outcome"`
	RetrySafe       bool   `json:"retrySafe"`
	Message         string `json:"message"`
	RequestID       string `json:"requestId,omitempty"`
}

// SeedVerificationDiagnostic is the credential- and content-safe durable
// record of one completed Seed verification iteration.
type SeedVerificationDiagnostic struct {
	Iteration int    `json:"iteration"`
	Outcome   string `json:"outcome"`
	ExitCode  *int   `json:"exitCode,omitempty"`
	TimedOut  bool   `json:"timedOut"`
	Message   string `json:"message,omitempty"`
}

type seedRunObservation struct {
	PublicationDiagnostic   *SeedPublicationDiagnostic   `json:"publicationDiagnostic,omitempty"`
	VerificationDiagnostics []SeedVerificationDiagnostic `json:"verificationDiagnostics,omitempty"`
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
	pollen TEXT NOT NULL DEFAULT '',
	substrate TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL DEFAULT '',
	genotype TEXT NOT NULL DEFAULT '',
	transcript TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	output TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	startedAt TEXT NOT NULL,
	finishedAt TEXT NOT NULL DEFAULT '',
	usage TEXT NOT NULL DEFAULT '',
	observation TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sproutrunsBySession ON sproutruns(sessionId, startedAt);
CREATE TABLE IF NOT EXISTS seedruns (
	handle TEXT PRIMARY KEY,
	pollen TEXT NOT NULL DEFAULT '',
	phytomerId TEXT NOT NULL DEFAULT '',
	substrate TEXT NOT NULL DEFAULT '',
	goal TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	iterations INTEGER NOT NULL DEFAULT 0,
	branch TEXT NOT NULL DEFAULT '',
	fruitCommit TEXT NOT NULL DEFAULT '',
	diff TEXT NOT NULL DEFAULT '',
	logs TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	startedAt TEXT NOT NULL,
	finishedAt TEXT NOT NULL DEFAULT '',
	observation TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS seedrunsByPollen ON seedruns(pollen, startedAt);

CREATE TABLE IF NOT EXISTS continuations (
	continuationId TEXT PRIMARY KEY,
	phytomerId TEXT NOT NULL,
	pollen TEXT NOT NULL,
	substrate TEXT NOT NULL,
	idempotencyKey TEXT NOT NULL,
	intentDigest TEXT NOT NULL,
	intent TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	deliveryState TEXT NOT NULL,
	acceptedAt TEXT NOT NULL,
	deliveredAt TEXT NOT NULL DEFAULT '',
	failedAt TEXT NOT NULL DEFAULT '',
	UNIQUE(phytomerId, pollen, idempotencyKey),
	UNIQUE(phytomerId, sequence)
);
CREATE INDEX IF NOT EXISTS continuationsByPhytomer ON continuations(phytomerId, sequence);

CREATE TABLE IF NOT EXISTS schemaMeta (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL
);`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize history schema: %w", err)
	}
	return s.migrateSchema(ctx)
}

func (s *Store) migrateSchema(ctx context.Context) error {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schemaMeta WHERE id = 1`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Never stamped: a fresh database, or an existing pre-versioning
		// .tendril/history.db opened for the first time after versioning
		// shipped. Both converge through the forward steps below.
	case err != nil:
		return fmt.Errorf("read history schema version: %w", err)
	case version > currentSchemaVersion:
		return fmt.Errorf("history database schema version %d is newer than this binary supports (%d) — refusing to open with an older binary", version, currentSchemaVersion)
	}

	// Forward steps. Each one inspects the live table before it alters, so a
	// database created moments ago by the schema literal and a database
	// carrying rows from an earlier generation converge on the same shape
	// without either needing to know which it is. Ordering matters only in
	// that an index may not name a column an earlier step has yet to add.
	if err := s.ensureColumn(ctx, "sproutruns", "pollen", `ALTER TABLE sproutruns ADD COLUMN pollen TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sproutruns", "substrate", `ALTER TABLE sproutruns ADD COLUMN substrate TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sproutruns", "usage", `ALTER TABLE sproutruns ADD COLUMN usage TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sproutruns", "provider", `ALTER TABLE sproutruns ADD COLUMN provider TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sproutruns", "observation", `ALTER TABLE sproutruns ADD COLUMN observation TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS sproutrunsByPollen ON sproutruns(pollen, startedAt)`); err != nil {
		return fmt.Errorf("index sprout runs by pollen: %w", err)
	}
	if err := s.ensureColumn(ctx, "seedruns", "phytomerId", `ALTER TABLE seedruns ADD COLUMN phytomerId TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "seedruns", "fruitCommit", `ALTER TABLE seedruns ADD COLUMN fruitCommit TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "seedruns", "observation", `ALTER TABLE seedruns ADD COLUMN observation TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS seedrunsByPhytomer ON seedruns(phytomerId, startedAt)`); err != nil {
		return fmt.Errorf("index seed runs by phytomer: %w", err)
	}

	const stamp = `INSERT INTO schemaMeta (id, version) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET version = excluded.version`
	if _, err := s.db.ExecContext(ctx, stamp, currentSchemaVersion); err != nil {
		return fmt.Errorf("stamp schema version: %w", err)
	}
	return nil
}

// ensureColumn adds a column when the table does not already carry it. SQLite
// has no ADD COLUMN IF NOT EXISTS, and re-running a plain ALTER is an error
// rather than a no-op, so the check is what makes the step safe to run on
// every open.
func (s *Store) ensureColumn(ctx context.Context, table, column, alter string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan %s column name: %w", table, err)
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s columns: %w", table, err)
	}
	if _, err := s.db.ExecContext(ctx, alter); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
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
		sess, err := s.scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return sessions, nil
}

// LoadSession loads one persisted session by ID. The second return value is
// false if no such session exists (sql.ErrNoRows is not an error).
func (s *Store) LoadSession(ctx context.Context, sessionID string) (session.Phytomer, bool, error) {
	const query = `SELECT sessionId, origin, createdAt, lastActiveAt, preferences FROM sessions WHERE sessionId = ?`

	row := s.db.QueryRowContext(ctx, query, sessionID)
	sess, err := s.scanSession(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return session.Phytomer{}, false, nil
		}
		return session.Phytomer{}, false, err
	}
	return sess, true, nil
}

// sessionRow is the common Scan surface shared by QueryRow and Rows.
type sessionRow interface {
	Scan(dest ...any) error
}

func (s *Store) scanSession(row sessionRow) (session.Phytomer, error) {
	var sess session.Phytomer
	var createdAt, lastActiveAt, prefs string
	if err := row.Scan(&sess.ID, &sess.Origin, &createdAt, &lastActiveAt, &prefs); err != nil {
		if err == sql.ErrNoRows {
			return session.Phytomer{}, err
		}
		return session.Phytomer{}, fmt.Errorf("scan session: %w", err)
	}
	var err error
	if sess.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return session.Phytomer{}, fmt.Errorf("parse session createdAt: %w", err)
	}
	if sess.LastActiveAt, err = time.Parse(time.RFC3339Nano, lastActiveAt); err != nil {
		return session.Phytomer{}, fmt.Errorf("parse session lastActiveAt: %w", err)
	}
	prefsDec, err := s.dec(prefs, "historydb/sessions/preferences")
	if err != nil {
		return session.Phytomer{}, fmt.Errorf("decrypt session preferences: %w", err)
	}
	if err := json.Unmarshal([]byte(prefsDec), &sess.Preferences); err != nil {
		return session.Phytomer{}, fmt.Errorf("decode session preferences: %w", err)
	}
	return sess, nil
}

func (s *Store) AppendMessage(ctx context.Context, msg session.Message) error {
	const statement = `
INSERT INTO messages (sessionId, role, content, model, createdAt)
VALUES (?, ?, ?, ?, ?)`

	createdAt := msg.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	content := msg.Content
	if msg.Role == "assistant" {
		content = telemetry.StripPrivateReasoning(content)
	}

	encContent, err := s.enc(content, "historydb/messages/content")
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
		if msg.Role == "assistant" {
			msg.Content = telemetry.StripPrivateReasoning(msg.Content)
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
	ev := telemetry.SanitizeObservationEvent(event)
	if !telemetry.RedactionDisabled() {
		ev = telemetry.RedactEvent(ev)
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

		e := eventbus.Event{
			Type: eventbus.EventType(record.Type),
			Data: record.Data,
		}
		e = telemetry.SanitizeObservationEvent(e)
		record.Data = e.Data

		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return records, nil
}

// --- Sprout execution history -------------------------------------------------

func encodeSproutRunUsage(usage SproutRunUsage) (string, error) {
	if usage.Execution == nil && usage.PostRun == nil {
		return "", nil
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeSproutRunUsage(raw string) (SproutRunUsage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SproutRunUsage{}, nil
	}
	var usage SproutRunUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return SproutRunUsage{}, err
	}
	return usage, nil
}

func encodeSproutRunObservation(run SproutRun) (string, error) {
	if run.Outcome == "" && run.FailureCategory == "" && run.ProviderDiagnostic == nil && !run.ProviderRequestAttempted && run.ToolInvocations == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(sproutRunObservation{
		Outcome:                  run.Outcome,
		FailureCategory:          run.FailureCategory,
		ProviderDiagnostic:       run.ProviderDiagnostic,
		ProviderRequestAttempted: run.ProviderRequestAttempted,
		ToolInvocations:          run.ToolInvocations,
	})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeSproutRunObservation(raw string) (sproutRunObservation, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return sproutRunObservation{}, nil
	}
	var observation sproutRunObservation
	if err := json.Unmarshal([]byte(raw), &observation); err != nil {
		return sproutRunObservation{}, err
	}
	return observation, nil
}

func encodeSeedRunObservation(run SeedRun) (string, error) {
	if run.PublicationDiagnostic == nil && len(run.VerificationDiagnostics) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(seedRunObservation{
		PublicationDiagnostic:   run.PublicationDiagnostic,
		VerificationDiagnostics: copySeedVerificationDiagnostics(run.VerificationDiagnostics),
	})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func copySeedVerificationDiagnostics(src []SeedVerificationDiagnostic) []SeedVerificationDiagnostic {
	if len(src) == 0 {
		return nil
	}
	out := make([]SeedVerificationDiagnostic, len(src))
	copy(out, src)
	for i := range src {
		if src[i].ExitCode != nil {
			code := *src[i].ExitCode
			out[i].ExitCode = &code
		}
	}
	return out
}

func decodeSeedRunObservation(raw string) (seedRunObservation, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return seedRunObservation{}, nil
	}
	var observation seedRunObservation
	if err := json.Unmarshal([]byte(raw), &observation); err != nil {
		return seedRunObservation{}, err
	}
	return observation, nil
}

// RecordSproutRun upserts one Sprout execution record; call it once when the
// sprout emerges (status "running") and again when it matures or withers.
//
// The model is settled on the finishing call, not the opening one: which model
// carried a run is only known once resolution has happened, and resolution
// happens inside the run. The update takes a non-empty model and keeps the
// stored one otherwise, so a later call that does not know the model cannot
// erase what an earlier one recorded.
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
	transcriptRaw := telemetry.SanitizeSproutTranscript(run.Transcript)
	transcript, err := s.enc(transcriptRaw, "historydb/sproutruns/transcript")
	if err != nil {
		return fmt.Errorf("encrypt sprout run transcript: %w", err)
	}
	outputRaw := telemetry.StripPrivateReasoning(run.Output)
	output, err := s.enc(outputRaw, "historydb/sproutruns/output")
	if err != nil {
		return fmt.Errorf("encrypt sprout run output: %w", err)
	}
	runError, err := s.enc(run.Error, "historydb/sproutruns/error")
	if err != nil {
		return fmt.Errorf("encrypt sprout run error: %w", err)
	}
	usage, err := encodeSproutRunUsage(run.Usage)
	if err != nil {
		return fmt.Errorf("encode sprout run usage: %w", err)
	}
	observation, err := encodeSproutRunObservation(run)
	if err != nil {
		return fmt.Errorf("encode sprout run observation: %w", err)
	}

	// Ownership is settled by whichever call first supplies it and is never
	// reassigned: a run that already names a dispatching subject keeps it, so
	// no later write can hand a recorded run to a different subject.
	//
	// Usage and observation follow the same "later writer must not erase"
	// rule as model: an opening or compatibility write with no envelope
	// leaves a stored value alone. A non-empty envelope settles an initially
	// empty row. Provider is settled the same way as model.
	const statement = `
INSERT INTO sproutruns (runId, sessionId, stepId, origin, pollen, substrate, provider, model, genotype, transcript, status, output, error, startedAt, finishedAt, usage, observation)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(runId) DO UPDATE SET
	status = excluded.status,
	pollen = CASE WHEN pollen = '' THEN excluded.pollen ELSE pollen END,
	substrate = CASE WHEN substrate = '' THEN excluded.substrate ELSE substrate END,
	provider = COALESCE(NULLIF(excluded.provider, ''), provider),
	model = COALESCE(NULLIF(excluded.model, ''), model),
	output = excluded.output,
	error = excluded.error,
	finishedAt = excluded.finishedAt,
	usage = COALESCE(NULLIF(excluded.usage, ''), usage),
	observation = COALESCE(NULLIF(excluded.observation, ''), observation)`

	_, err = s.db.ExecContext(ctx, statement,
		run.RunID,
		run.SessionID,
		run.StepID,
		run.Origin,
		strings.TrimSpace(run.Pollen),
		strings.TrimSpace(run.Substrate),
		strings.TrimSpace(run.Provider),
		run.Model,
		genotype,
		transcript,
		run.Status,
		output,
		runError,
		run.StartedAt.UTC().Format(time.RFC3339Nano),
		finishedAt,
		usage,
		observation,
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
SELECT runId, sessionId, stepId, origin, pollen, substrate, provider, model, genotype, transcript, status, output, error, startedAt, finishedAt, usage, observation
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
		var startedAt, finishedAt, usage, observation string
		if err := rows.Scan(&run.RunID, &run.SessionID, &run.StepID, &run.Origin, &run.Pollen, &run.Substrate, &run.Provider, &run.Model, &run.Genotype, &run.Transcript, &run.Status, &run.Output, &run.Error, &startedAt, &finishedAt, &usage, &observation); err != nil {
			return nil, fmt.Errorf("scan sprout run: %w", err)
		}
		if run.Genotype, err = s.dec(run.Genotype, "historydb/sproutruns/genotype"); err != nil {
			return nil, fmt.Errorf("decrypt sprout run genotype: %w", err)
		}
		if run.Transcript, err = s.dec(run.Transcript, "historydb/sproutruns/transcript"); err != nil {
			return nil, fmt.Errorf("decrypt sprout run transcript: %w", err)
		}
		run.Transcript = telemetry.SanitizeSproutTranscript(run.Transcript)
		if run.Output, err = s.dec(run.Output, "historydb/sproutruns/output"); err != nil {
			return nil, fmt.Errorf("decrypt sprout run output: %w", err)
		}
		run.Output = telemetry.StripPrivateReasoning(run.Output)
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
		if run.Usage, err = decodeSproutRunUsage(usage); err != nil {
			return nil, fmt.Errorf("decode sprout run usage: %w", err)
		}
		obs, err := decodeSproutRunObservation(observation)
		if err != nil {
			return nil, fmt.Errorf("decode sprout run observation: %w", err)
		}
		run.Outcome = obs.Outcome
		run.FailureCategory = obs.FailureCategory
		run.ProviderDiagnostic = obs.ProviderDiagnostic
		run.ProviderRequestAttempted = obs.ProviderRequestAttempted
		run.ToolInvocations = obs.ToolInvocations
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sprout runs: %w", err)
	}
	return runs, nil
}

// SproutRunOwner is one distinct pairing of dispatching subject and substrate
// recorded against a phytomer's sprout runs.
type SproutRunOwner struct {
	Pollen    string `json:"pollen,omitempty"`
	Substrate string `json:"substrate,omitempty"`
}

// SproutRunOwners returns the distinct subjects that dispatched the sprout runs
// recorded against one phytomer, each paired with the substrate that run
// targeted. A read surface uses it to decide who owns a phytomer without
// loading — or decrypting — any run content, and an empty result means nothing
// has ever been dispatched into it.
func (s *Store) SproutRunOwners(ctx context.Context, sessionID string) ([]SproutRunOwner, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("sprout run owners require a sessionId")
	}

	const query = `
SELECT DISTINCT pollen, substrate
FROM sproutruns
WHERE sessionId = ?
ORDER BY pollen, substrate`

	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load sprout run owners: %w", err)
	}
	defer rows.Close()

	owners := make([]SproutRunOwner, 0)
	for rows.Next() {
		var owner SproutRunOwner
		if err := rows.Scan(&owner.Pollen, &owner.Substrate); err != nil {
			return nil, fmt.Errorf("scan sprout run owner: %w", err)
		}
		owners = append(owners, owner)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sprout run owners: %w", err)
	}
	return owners, nil
}

type encodedSeedRun struct {
	handle      string
	pollen      string
	phytomerID  string
	substrate   string
	goal        string
	status      string
	iterations  int
	branch      string
	commit      string
	diff        string
	logs        string
	runError    string
	startedAt   string
	finishedAt  string
	observation string
}

func (s *Store) encodeSeedRun(run SeedRun) (encodedSeedRun, error) {
	if strings.TrimSpace(run.Handle) == "" {
		return encodedSeedRun{}, fmt.Errorf("seed run requires a handle")
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
		return encodedSeedRun{}, fmt.Errorf("encrypt seed run goal: %w", err)
	}
	diff, err := s.enc(run.Diff, "historydb/seedruns/diff")
	if err != nil {
		return encodedSeedRun{}, fmt.Errorf("encrypt seed run diff: %w", err)
	}
	logs, err := s.enc(run.Logs, "historydb/seedruns/logs")
	if err != nil {
		return encodedSeedRun{}, fmt.Errorf("encrypt seed run logs: %w", err)
	}
	runError, err := s.enc(run.Error, "historydb/seedruns/error")
	if err != nil {
		return encodedSeedRun{}, fmt.Errorf("encrypt seed run error: %w", err)
	}
	observation, err := encodeSeedRunObservation(run)
	if err != nil {
		return encodedSeedRun{}, fmt.Errorf("encode seed run observation: %w", err)
	}
	return encodedSeedRun{
		handle:      run.Handle,
		pollen:      run.Pollen,
		phytomerID:  run.PhytomerID,
		substrate:   run.Substrate,
		goal:        goal,
		status:      run.Status,
		iterations:  run.Iterations,
		branch:      run.Branch,
		commit:      run.Commit,
		diff:        diff,
		logs:        logs,
		runError:    runError,
		startedAt:   run.StartedAt.UTC().Format(time.RFC3339Nano),
		finishedAt:  finishedAt,
		observation: observation,
	}, nil
}

func sqliteConstraintFailed(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code()&0xff == sqliteConstraint
}

// RecordSeedOpening inserts a new Seed opening. Duplicate handles fail
// atomically; this is not an upsert. Settlement of an already-opened handle
// still uses RecordSeedRun.
func (s *Store) RecordSeedOpening(ctx context.Context, run SeedRun) error {
	encoded, err := s.encodeSeedRun(run)
	if err != nil {
		return err
	}
	const statement = `
INSERT INTO seedruns (handle, pollen, phytomerId, substrate, goal, status, iterations, branch, fruitCommit, diff, logs, error, startedAt, finishedAt, observation)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, statement,
		encoded.handle,
		encoded.pollen,
		encoded.phytomerID,
		encoded.substrate,
		encoded.goal,
		encoded.status,
		encoded.iterations,
		encoded.branch,
		encoded.commit,
		encoded.diff,
		encoded.logs,
		encoded.runError,
		encoded.startedAt,
		encoded.finishedAt,
		encoded.observation,
	)
	if err != nil {
		if sqliteConstraintFailed(err) {
			return fmt.Errorf("%w", ErrSeedHandleExists)
		}
		return fmt.Errorf("record seed opening: %w", err)
	}
	return nil
}

// RecordSeedRun upserts one seed.grow execution keyed by its handle. Use it to
// settle an already-opened handle. New openings must use RecordSeedOpening.
func (s *Store) RecordSeedRun(ctx context.Context, run SeedRun) error {
	encoded, err := s.encodeSeedRun(run)
	if err != nil {
		return err
	}

	const statement = `
INSERT INTO seedruns (handle, pollen, phytomerId, substrate, goal, status, iterations, branch, fruitCommit, diff, logs, error, startedAt, finishedAt, observation)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(handle) DO UPDATE SET
	status = excluded.status,
	iterations = excluded.iterations,
	branch = excluded.branch,
	fruitCommit = excluded.fruitCommit,
	diff = excluded.diff,
	logs = excluded.logs,
	error = excluded.error,
	finishedAt = excluded.finishedAt,
	observation = COALESCE(NULLIF(excluded.observation, ''), seedruns.observation),
	phytomerId = CASE WHEN seedruns.phytomerId = '' THEN excluded.phytomerId ELSE seedruns.phytomerId END`

	_, err = s.db.ExecContext(ctx, statement,
		encoded.handle,
		encoded.pollen,
		encoded.phytomerID,
		encoded.substrate,
		encoded.goal,
		encoded.status,
		encoded.iterations,
		encoded.branch,
		encoded.commit,
		encoded.diff,
		encoded.logs,
		encoded.runError,
		encoded.startedAt,
		encoded.finishedAt,
		encoded.observation,
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
SELECT handle, pollen, phytomerId, substrate, goal, status, iterations, branch, fruitCommit, diff, logs, error, startedAt, finishedAt, observation
FROM seedruns
WHERE handle = ?`

	var run SeedRun
	var startedAt, finishedAt, observation string
	err := s.db.QueryRowContext(ctx, query, handle).Scan(
		&run.Handle, &run.Pollen, &run.PhytomerID, &run.Substrate, &run.Goal, &run.Status, &run.Iterations,
		&run.Branch, &run.Commit, &run.Diff, &run.Logs, &run.Error, &startedAt, &finishedAt, &observation)
	if err == sql.ErrNoRows {
		return SeedRun{}, false, nil
	}
	if err != nil {
		return SeedRun{}, false, fmt.Errorf("get seed run: %w", err)
	}
	if err := s.decodeSeedRun(&run, startedAt, finishedAt, observation); err != nil {
		return SeedRun{}, false, err
	}
	return run, true, nil
}

// GetSeedRunByPhytomer returns the Seed growth whose canonical Phytomer is
// sessionID. Multiple rows for one Phytomer is contradictory evidence and
// fails closed rather than picking one. Historical rows with an empty
// phytomerId are never matched.
func (s *Store) GetSeedRunByPhytomer(ctx context.Context, phytomerID string) (SeedRun, bool, error) {
	phytomerID = strings.TrimSpace(phytomerID)
	if phytomerID == "" {
		return SeedRun{}, false, fmt.Errorf("phytomerId is required")
	}

	const query = `
SELECT handle, pollen, phytomerId, substrate, goal, status, iterations, branch, fruitCommit, diff, logs, error, startedAt, finishedAt, observation
FROM seedruns
WHERE phytomerId = ?`

	rows, err := s.db.QueryContext(ctx, query, phytomerID)
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

func (s *Store) decodeSeedRun(run *SeedRun, startedAt, finishedAt, observationRaw string) error {
	var err error
	if run.Goal, err = s.dec(run.Goal, "historydb/seedruns/goal"); err != nil {
		return fmt.Errorf("decrypt seed run goal: %w", err)
	}
	if run.Diff, err = s.dec(run.Diff, "historydb/seedruns/diff"); err != nil {
		return fmt.Errorf("decrypt seed run diff: %w", err)
	}
	if run.Logs, err = s.dec(run.Logs, "historydb/seedruns/logs"); err != nil {
		return fmt.Errorf("decrypt seed run logs: %w", err)
	}
	if run.Error, err = s.dec(run.Error, "historydb/seedruns/error"); err != nil {
		return fmt.Errorf("decrypt seed run error: %w", err)
	}
	observation, err := decodeSeedRunObservation(observationRaw)
	if err != nil {
		return fmt.Errorf("decode seed run observation: %w", err)
	}
	run.PublicationDiagnostic = observation.PublicationDiagnostic
	run.VerificationDiagnostics = copySeedVerificationDiagnostics(observation.VerificationDiagnostics)
	if run.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt); err != nil {
		return fmt.Errorf("parse seed run startedAt: %w", err)
	}
	if finishedAt != "" {
		if run.FinishedAt, err = time.Parse(time.RFC3339Nano, finishedAt); err != nil {
			return fmt.Errorf("parse seed run finishedAt: %w", err)
		}
	}
	return nil
}

// PruneOlderThan deletes rows from messages, events, sproutruns, and
// seedruns whose timestamp column is older than cutoff, then VACUUMs if
// anything was actually deleted (skipped on a no-op sweep to avoid the cost
// of rewriting the whole file for nothing). Never touches sessions — session
// lifecycle is session.Manager's own Prune/DeleteSession path, not this
// package's. Returns the total row count deleted across all four tables.
func (s *Store) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil {
		return 0, nil
	}
	cutoffStr := cutoff.UTC().Format(time.RFC3339Nano)

	var total int64
	for _, q := range []struct {
		table  string
		column string
	}{
		{"messages", "createdAt"},
		{"events", "createdAt"},
		{"sproutruns", "startedAt"},
		{"seedruns", "startedAt"},
	} {
		result, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s < ?", q.table, q.column), cutoffStr)
		if err != nil {
			return total, fmt.Errorf("prune %s older than %s: %w", q.table, cutoffStr, err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("count pruned rows in %s: %w", q.table, err)
		}
		total += n
	}

	if total > 0 {
		if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
			return total, fmt.Errorf("vacuum after pruning %d row(s): %w", total, err)
		}
	}
	return total, nil
}

// retentionSweepInterval is how often StartRetentionSweep re-checks and
// prunes. Fixed, not configurable — only the retention window itself
// (EnvHistoryRetentionDays) is an operator-facing knob; the sweep cadence is
// an implementation detail.
const retentionSweepInterval = 24 * time.Hour

// StartRetentionSweep launches a background loop that prunes rows older
// than the configured retention window, once immediately and then every
// retentionSweepInterval. No-op when retention is disabled (the default).
// Stops when ctx is cancelled — wire it to the daemon's shutdown ctx.
func (s *Store) StartRetentionSweep(ctx context.Context) {
	if s == nil {
		return
	}
	days := historyRetentionDaysFromEnv()
	if days <= 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	sweep := func() {
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		n, err := s.PruneOlderThan(ctx, cutoff)
		if err != nil {
			log.Printf("⚠️ historydb retention sweep failed: %v", err)
			return
		}
		if n > 0 {
			log.Printf("🧹 historydb retention: pruned %d row(s) older than %d day(s)", n, days)
		}
	}

	sweep()
	go func() {
		ticker := time.NewTicker(retentionSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}

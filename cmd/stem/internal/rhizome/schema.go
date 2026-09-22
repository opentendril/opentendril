package rhizome

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/heartwood"
	_ "modernc.org/sqlite"
)

type FileRecord struct {
	RepositoryName string
	Path           string
	Hash           string
	LastModified   time.Time
}

type Symbol struct {
	RepositoryName string
	Name           string
	Type           string
	FilePath       string
	LineStart      int
	LineEnd        int
	StubContent    string
}

type Origin string

const (
	OriginLegacy      Origin = "legacy"
	OriginBotanist    Origin = "botanist"
	OriginSubstrate   Origin = "substrate"
	OriginMycorrhizal Origin = "mycorrhizal"
)

type Authority string

const (
	AuthorityNone          Authority = "none"
	AuthorityBotanist      Authority = "botanist"
	AuthorityDeterministic Authority = "deterministic"
)

type Status string

const (
	StatusUnclassified Status = "unclassified"
	StatusEstablished  Status = "established"
	StatusProposed     Status = "proposed"
	StatusStale        Status = "stale"
	StatusConflicted   Status = "conflicted"
	StatusRejected     Status = "rejected"
	StatusSuperseded   Status = "superseded"
)

type Kind string

const (
	KindObservation            Kind = "observation"
	KindFact                   Kind = "fact"
	KindConstraint             Kind = "constraint"
	KindCorrection             Kind = "correction"
	KindRejectedInterpretation Kind = "rejected-interpretation"
)

type Memory struct {
	RepositoryName string    `json:"repositoryName"`
	Category       string    `json:"category"`
	Title          string    `json:"title"`
	Content        string    `json:"content"`
	Tags           string    `json:"tags"`
	CreatedAt      time.Time `json:"createdAt"`
	SessionID      string    `json:"sessionId"`

	Origin           Origin    `json:"origin,omitempty"`
	Authority        Authority `json:"authority,omitempty"`
	Status           Status    `json:"status,omitempty"`
	Kind             Kind      `json:"kind,omitempty"`
	Provenance       string    `json:"provenance,omitempty"`
	SourceClass      string    `json:"sourceClass,omitempty"`
	SourceIdentity   string    `json:"sourceIdentity,omitempty"`
	ContentIdentity  string    `json:"contentIdentity,omitempty"`
	RevisionIdentity string    `json:"revisionIdentity,omitempty"`
	StableID         string    `json:"stableId,omitempty"`
	RevisionMetadata string    `json:"revisionMetadata,omitempty"`
	Supersession     string    `json:"supersession,omitempty"`
}

// Validate returns an error if the envelope contains an invalid field combination.
// Established status requires botanist or deterministic authority.
// Mycorrhizal origin requires botanist authority to be established.
// Deterministic substrate establishment requires source identity and content/revision identity.
// Unknown fields or mismatched StableID fail closed.
func (m *Memory) Validate() error {
	if m.Origin != "" && m.Origin != OriginLegacy && m.Origin != OriginBotanist && m.Origin != OriginSubstrate && m.Origin != OriginMycorrhizal {
		return fmt.Errorf("unknown origin %q", m.Origin)
	}
	if m.Authority != "" && m.Authority != AuthorityNone && m.Authority != AuthorityBotanist && m.Authority != AuthorityDeterministic {
		return fmt.Errorf("unknown authority %q", m.Authority)
	}
	if m.Status != "" && m.Status != StatusUnclassified && m.Status != StatusEstablished && m.Status != StatusProposed && m.Status != StatusStale && m.Status != StatusConflicted && m.Status != StatusRejected && m.Status != StatusSuperseded {
		return fmt.Errorf("unknown status %q", m.Status)
	}
	if m.Kind != "" && m.Kind != KindObservation && m.Kind != KindFact && m.Kind != KindConstraint && m.Kind != KindCorrection && m.Kind != KindRejectedInterpretation {
		return fmt.Errorf("unknown kind %q", m.Kind)
	}

	canonicalID := StableMemoryIdentity(m.RepositoryName, m.Title)
	if m.StableID != "" && m.StableID != canonicalID {
		return fmt.Errorf("provided StableID %q conflicts with canonical identity %q", m.StableID, canonicalID)
	}

	o := m.Origin
	if o == "" {
		o = OriginLegacy
	}
	a := m.Authority
	if a == "" {
		a = AuthorityNone
	}
	s := m.Status
	if s == "" {
		s = StatusUnclassified
	}

	if s == StatusEstablished && a != AuthorityBotanist && a != AuthorityDeterministic {
		return fmt.Errorf("established status requires botanist or deterministic authority, got %q", a)
	}

	if o == OriginMycorrhizal && s == StatusEstablished && a != AuthorityBotanist {
		return fmt.Errorf("mycorrhizal origin requires botanist authority to be established, got %q", a)
	}

	if o == OriginSubstrate && a == AuthorityDeterministic && s == StatusEstablished {
		if m.SourceIdentity == "" {
			return fmt.Errorf("deterministic substrate establishment requires source identity")
		}
		if m.ContentIdentity == "" && m.RevisionIdentity == "" {
			return fmt.Errorf("deterministic substrate establishment requires content or revision identity")
		}
	}

	return nil
}

// StableMemoryIdentity returns a deterministic hex identifier for a memory
// based on its repository name and title. This identity is stable across backends.
func StableMemoryIdentity(repositoryName, title string) string {
	sum := sha256.Sum256([]byte(repositoryName + "\x00" + title))
	return hex.EncodeToString(sum[:])
}

type IndexStore interface {
	Close() error
	DeleteFile(ctx context.Context, repositoryName string, path string) error
	DeleteSymbolsForFile(ctx context.Context, repositoryName string, filePath string) error
	GetFile(ctx context.Context, repositoryName string, path string) (FileRecord, bool, error)
	ListFilePaths(ctx context.Context, repositoryName string) ([]string, error)
	SearchSymbols(ctx context.Context, repositoryName string, query string, limit int) ([]Symbol, error)
	UpsertFile(ctx context.Context, file FileRecord) error
	UpsertSymbols(ctx context.Context, symbols []Symbol) error
}

type MemoryBackend interface {
	DeleteMemory(ctx context.Context, repositoryName string, title string) error
	ListMemories(ctx context.Context, repositoryName string, category string, limit int) ([]Memory, error)
	SearchMemories(ctx context.Context, repositoryName string, query string, category string, limit int) ([]Memory, error)
	StoreMemory(ctx context.Context, memory Memory) error
}

type MemoryConfig struct {
	Backend           string
	SQLitePath        string
	PineconeAPIKey    string
	PineconeBaseURL   string
	PineconeDimension int
	WeaviateAPIKey    string
	WeaviateBaseURL   string
	// RemoteCleartextAck records that the operator has explicitly acknowledged
	// that remote memory backends transmit memory fields to a third-party
	// service without encryption. Required for the pinecone/weaviate backends.
	RemoteCleartextAck bool
}

type SQLiteIndexStore struct {
	db     *sql.DB
	cipher *heartwood.Cipher
}

func OpenSQLiteIndexStore(ctx context.Context, dbPath string, cipher *heartwood.Cipher) (*SQLiteIndexStore, error) {
	if cipher == nil {
		return nil, fmt.Errorf("cipher is required")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open SQLite index: %w", err)
	}

	store := &SQLiteIndexStore{db: db, cipher: cipher}
	if err := store.initSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *SQLiteIndexStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteIndexStore) initSchema(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS files (
	repositoryName TEXT NOT NULL,
	path TEXT NOT NULL,
	hash TEXT NOT NULL,
	lastModified TEXT NOT NULL,
	PRIMARY KEY (repositoryName, path)
);

CREATE VIRTUAL TABLE IF NOT EXISTS symbols USING fts5(
	repositoryName,
	name,
	type,
	filePath,
	lineStart UNINDEXED,
	lineEnd UNINDEXED,
	stubContent UNINDEXED
);

CREATE VIRTUAL TABLE IF NOT EXISTS memories USING fts5(
	repositoryName,
	category,
	title,
	content UNINDEXED,
	tags,
	createdAt UNINDEXED,
	sessionId UNINDEXED
);

CREATE TABLE IF NOT EXISTS memory_envelopes (
	repositoryName TEXT NOT NULL,
	title TEXT NOT NULL,
	origin TEXT NOT NULL DEFAULT 'legacy',
	authority TEXT NOT NULL DEFAULT 'none',
	status TEXT NOT NULL DEFAULT 'unclassified',
	kind TEXT NOT NULL DEFAULT '',
	provenance TEXT NOT NULL DEFAULT '',
	sourceClass TEXT NOT NULL DEFAULT '',
	sourceIdentity TEXT NOT NULL DEFAULT '',
	contentIdentity TEXT NOT NULL DEFAULT '',
	revisionIdentity TEXT NOT NULL DEFAULT '',
	stableId TEXT NOT NULL DEFAULT '',
	revisionMetadata TEXT NOT NULL DEFAULT '',
	supersession TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (repositoryName, title)
);`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize rhizome schema: %w", err)
	}
	return nil
}

func (s *SQLiteIndexStore) GetFile(ctx context.Context, repositoryName string, path string) (FileRecord, bool, error) {
	const query = `SELECT repositoryName, path, hash, lastModified FROM files WHERE repositoryName = ? AND path = ?`

	var file FileRecord
	var lastModified string
	err := s.db.QueryRowContext(ctx, query, repositoryName, path).Scan(&file.RepositoryName, &file.Path, &file.Hash, &lastModified)
	if err == sql.ErrNoRows {
		return FileRecord{}, false, nil
	}
	if err != nil {
		return FileRecord{}, false, fmt.Errorf("get file record: %w", err)
	}

	parsed, err := time.Parse(time.RFC3339Nano, lastModified)
	if err != nil {
		return FileRecord{}, false, fmt.Errorf("parse lastModified: %w", err)
	}
	file.LastModified = parsed

	return file, true, nil
}

func (s *SQLiteIndexStore) ListFilePaths(ctx context.Context, repositoryName string) ([]string, error) {
	const query = `SELECT path FROM files WHERE repositoryName = ?`

	rows, err := s.db.QueryContext(ctx, query, repositoryName)
	if err != nil {
		return nil, fmt.Errorf("list file paths: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scan path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file paths: %w", err)
	}
	return paths, nil
}

func (s *SQLiteIndexStore) DeleteFile(ctx context.Context, repositoryName string, path string) error {
	if err := s.DeleteSymbolsForFile(ctx, repositoryName, path); err != nil {
		return err
	}

	const statement = `DELETE FROM files WHERE repositoryName = ? AND path = ?`
	if _, err := s.db.ExecContext(ctx, statement, repositoryName, path); err != nil {
		return fmt.Errorf("delete file record: %w", err)
	}
	return nil
}

func (s *SQLiteIndexStore) UpsertFile(ctx context.Context, file FileRecord) error {
	const statement = `
INSERT INTO files (repositoryName, path, hash, lastModified)
VALUES (?, ?, ?, ?)
ON CONFLICT(repositoryName, path) DO UPDATE SET
	hash = excluded.hash,
	lastModified = excluded.lastModified`

	_, err := s.db.ExecContext(ctx, statement, file.RepositoryName, file.Path, file.Hash, file.LastModified.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert file record: %w", err)
	}
	return nil
}

func (s *SQLiteIndexStore) DeleteSymbolsForFile(ctx context.Context, repositoryName string, filePath string) error {
	const statement = `DELETE FROM symbols WHERE repositoryName = ? AND filePath = ?`

	if _, err := s.db.ExecContext(ctx, statement, repositoryName, filePath); err != nil {
		return fmt.Errorf("delete symbols for file: %w", err)
	}
	return nil
}

func (s *SQLiteIndexStore) UpsertSymbols(ctx context.Context, symbols []Symbol) error {
	if len(symbols) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin symbol upsert: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	const statement = `
INSERT INTO symbols (repositoryName, name, type, filePath, lineStart, lineEnd, stubContent)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	stmt, err := tx.PrepareContext(ctx, statement)
	if err != nil {
		return fmt.Errorf("prepare symbol insert: %w", err)
	}
	defer stmt.Close()

	for _, symbol := range symbols {
		aad := []byte("rhizome/symbols/stubContent\x00" + symbol.RepositoryName + "\x00" + symbol.FilePath + "\x00" + symbol.Name)
		encryptedStub, encryptErr := s.cipher.Encrypt(symbol.StubContent, aad)
		if encryptErr != nil {
			err = encryptErr
			return fmt.Errorf("encrypt symbol stub: %w", err)
		}
		if _, err = stmt.ExecContext(ctx, symbol.RepositoryName, symbol.Name, symbol.Type, symbol.FilePath, symbol.LineStart, symbol.LineEnd, encryptedStub); err != nil {
			return fmt.Errorf("insert symbol: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit symbol upsert: %w", err)
	}
	return nil
}

func (s *SQLiteIndexStore) SearchSymbols(ctx context.Context, repositoryName string, query string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 20
	}
	if strings.TrimSpace(query) == "" || strings.TrimSpace(query) == "*" {
		return s.listSymbols(ctx, repositoryName, limit)
	}

	const statement = `
SELECT repositoryName, name, type, filePath, lineStart, lineEnd, stubContent
FROM symbols
WHERE repositoryName = ? AND symbols MATCH ?
ORDER BY rank
LIMIT ?`

	rows, err := s.db.QueryContext(ctx, statement, repositoryName, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search symbols: %w", err)
	}
	defer rows.Close()

	return s.scanSymbolRows(rows)
}

func (s *SQLiteIndexStore) listSymbols(ctx context.Context, repositoryName string, limit int) ([]Symbol, error) {
	const statement = `
SELECT repositoryName, name, type, filePath, lineStart, lineEnd, stubContent
FROM symbols
WHERE repositoryName = ?
ORDER BY filePath, lineStart, name
LIMIT ?`

	rows, err := s.db.QueryContext(ctx, statement, repositoryName, limit)
	if err != nil {
		return nil, fmt.Errorf("list symbols: %w", err)
	}
	defer rows.Close()

	return s.scanSymbolRows(rows)
}

func (s *SQLiteIndexStore) scanSymbolRows(rows *sql.Rows) ([]Symbol, error) {
	symbols := make([]Symbol, 0)
	for rows.Next() {
		var symbol Symbol
		var encryptedStub string
		if err := rows.Scan(&symbol.RepositoryName, &symbol.Name, &symbol.Type, &symbol.FilePath, &symbol.LineStart, &symbol.LineEnd, &encryptedStub); err != nil {
			return nil, fmt.Errorf("scan symbol: %w", err)
		}
		var err error
		aad := []byte("rhizome/symbols/stubContent\x00" + symbol.RepositoryName + "\x00" + symbol.FilePath + "\x00" + symbol.Name)
		symbol.StubContent, err = s.cipher.Decrypt(encryptedStub, aad, heartwood.LegacyCiphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt symbol stub: %w", err)
		}
		symbols = append(symbols, symbol)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate symbols: %w", err)
	}

	return symbols, nil
}

func (s *SQLiteIndexStore) StoreMemory(ctx context.Context, memory Memory) error {
	if memory.StableID == "" {
		memory.StableID = StableMemoryIdentity(memory.RepositoryName, memory.Title)
	}
	if err := memory.Validate(); err != nil {
		return fmt.Errorf("invalid memory envelope: %w", err)
	}
	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = time.Now().UTC()
	}

	aad := []byte("rhizome/memories/content\x00" + memory.RepositoryName + "\x00" + memory.Title)
	encryptedContent, err := s.cipher.Encrypt(memory.Content, aad)
	if err != nil {
		return fmt.Errorf("encrypt memory content: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin memory store: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Delete the old FTS row if present (upsert on FTS5 is delete+insert).
	if _, err = tx.ExecContext(ctx,
		`DELETE FROM memories WHERE repositoryName = ? AND title = ?`,
		memory.RepositoryName, memory.Title,
	); err != nil {
		return fmt.Errorf("delete old memory fts row: %w", err)
	}

	const insertMemory = `
INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	if _, err = tx.ExecContext(ctx, insertMemory,
		memory.RepositoryName, memory.Category, memory.Title, encryptedContent,
		memory.Tags, memory.CreatedAt.UTC().Format(time.RFC3339Nano), memory.SessionID,
	); err != nil {
		return fmt.Errorf("insert memory fts row: %w", err)
	}

	const upsertEnvelope = `
INSERT INTO memory_envelopes
	(repositoryName, title, origin, authority, status, kind, provenance,
	sourceClass, sourceIdentity, contentIdentity, revisionIdentity,
	stableId, revisionMetadata, supersession)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repositoryName, title) DO UPDATE SET
	origin = excluded.origin,
	authority = excluded.authority,
	status = excluded.status,
	kind = excluded.kind,
	provenance = excluded.provenance,
	sourceClass = excluded.sourceClass,
	sourceIdentity = excluded.sourceIdentity,
	contentIdentity = excluded.contentIdentity,
	revisionIdentity = excluded.revisionIdentity,
	stableId = excluded.stableId,
	revisionMetadata = excluded.revisionMetadata,
	supersession = excluded.supersession`

	origin := string(memory.Origin)
	if origin == "" {
		origin = string(OriginLegacy)
	}
	authority := string(memory.Authority)
	if authority == "" {
		authority = string(AuthorityNone)
	}
	status := string(memory.Status)
	if status == "" {
		status = string(StatusUnclassified)
	}

	if _, err = tx.ExecContext(ctx, upsertEnvelope,
		memory.RepositoryName, memory.Title,
		origin, authority, status,
		string(memory.Kind), memory.Provenance,
		memory.SourceClass, memory.SourceIdentity, memory.ContentIdentity,
		memory.RevisionIdentity, memory.StableID, memory.RevisionMetadata,
		memory.Supersession,
	); err != nil {
		return fmt.Errorf("upsert memory envelope: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit memory store: %w", err)
	}
	return nil
}

func (s *SQLiteIndexStore) SearchMemories(ctx context.Context, repositoryName string, query string, category string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 20
	}
	trimmedQuery := strings.TrimSpace(query)
	if trimmedQuery == "" || trimmedQuery == "*" {
		return s.ListMemories(ctx, repositoryName, category, limit)
	}

	statement := `
SELECT m.repositoryName, m.category, m.title, m.content, m.tags, m.createdAt, m.sessionId,
	COALESCE(e.origin, 'legacy'), COALESCE(e.authority, 'none'), COALESCE(e.status, 'unclassified'),
	COALESCE(e.kind, ''), COALESCE(e.provenance, ''), COALESCE(e.sourceClass, ''),
	COALESCE(e.sourceIdentity, ''), COALESCE(e.contentIdentity, ''), COALESCE(e.revisionIdentity, ''),
	COALESCE(e.stableId, ''), COALESCE(e.revisionMetadata, ''), COALESCE(e.supersession, '')
FROM memories m
LEFT JOIN memory_envelopes e ON e.repositoryName = m.repositoryName AND e.title = m.title
WHERE m.repositoryName = ? AND memories MATCH ?`
	args := []any{repositoryName, trimmedQuery}
	if strings.TrimSpace(category) != "" {
		statement += ` AND m.category = ?`
		args = append(args, category)
	}
	statement += `
ORDER BY rank
LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("search memories: %w", err)
	}
	defer rows.Close()

	return s.scanMemoryRows(rows)
}

func (s *SQLiteIndexStore) ListMemories(ctx context.Context, repositoryName string, category string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 20
	}

	statement := `
SELECT m.repositoryName, m.category, m.title, m.content, m.tags, m.createdAt, m.sessionId,
	COALESCE(e.origin, 'legacy'), COALESCE(e.authority, 'none'), COALESCE(e.status, 'unclassified'),
	COALESCE(e.kind, ''), COALESCE(e.provenance, ''), COALESCE(e.sourceClass, ''),
	COALESCE(e.sourceIdentity, ''), COALESCE(e.contentIdentity, ''), COALESCE(e.revisionIdentity, ''),
	COALESCE(e.stableId, ''), COALESCE(e.revisionMetadata, ''), COALESCE(e.supersession, '')
FROM memories m
LEFT JOIN memory_envelopes e ON e.repositoryName = m.repositoryName AND e.title = m.title
WHERE m.repositoryName = ?`
	args := []any{repositoryName}
	if strings.TrimSpace(category) != "" {
		statement += ` AND m.category = ?`
		args = append(args, category)
	}
	statement += `
ORDER BY m.createdAt DESC
LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	return s.scanMemoryRows(rows)
}

func (s *SQLiteIndexStore) DeleteMemory(ctx context.Context, repositoryName string, title string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete memory tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	const statementMem = `DELETE FROM memories WHERE repositoryName = ? AND title = ?`
	if _, err = tx.ExecContext(ctx, statementMem, repositoryName, title); err != nil {
		return fmt.Errorf("delete memory fts row: %w", err)
	}

	const statementEnv = `DELETE FROM memory_envelopes WHERE repositoryName = ? AND title = ?`
	if _, err = tx.ExecContext(ctx, statementEnv, repositoryName, title); err != nil {
		return fmt.Errorf("delete memory envelope sidecar: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit delete memory tx: %w", err)
	}
	return nil
}

func (s *SQLiteIndexStore) scanMemoryRows(rows *sql.Rows) ([]Memory, error) {
	memories := make([]Memory, 0)
	for rows.Next() {
		var memory Memory
		var encryptedContent string
		var createdAt string
		var origin, authority, status, kind, provenance string
		var sourceClass, sourceIdentity, contentIdentity, revisionIdentity string
		var stableID, revisionMetadata, supersession string
		if err := rows.Scan(
			&memory.RepositoryName, &memory.Category, &memory.Title, &encryptedContent,
			&memory.Tags, &createdAt, &memory.SessionID,
			&origin, &authority, &status, &kind, &provenance,
			&sourceClass, &sourceIdentity, &contentIdentity, &revisionIdentity,
			&stableID, &revisionMetadata, &supersession,
		); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		var err error
		aad := []byte("rhizome/memories/content\x00" + memory.RepositoryName + "\x00" + memory.Title)
		memory.Content, err = s.cipher.Decrypt(encryptedContent, aad, heartwood.LegacyCiphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt memory content: %w", err)
		}
		memory.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse memory createdAt: %w", err)
		}
		memory.Origin = Origin(origin)
		memory.Authority = Authority(authority)
		memory.Status = Status(status)
		memory.Kind = Kind(kind)
		memory.Provenance = provenance
		memory.SourceClass = sourceClass
		memory.SourceIdentity = sourceIdentity
		memory.ContentIdentity = contentIdentity
		memory.RevisionIdentity = revisionIdentity
		canonicalID := StableMemoryIdentity(memory.RepositoryName, memory.Title)
		if stableID != "" && stableID != canonicalID {
			return nil, fmt.Errorf("corrupted memory envelope for %q/%q: stored stableId %q conflicts with canonical identity %q", memory.RepositoryName, memory.Title, stableID, canonicalID)
		}
		memory.StableID = canonicalID

		memory.RevisionMetadata = revisionMetadata
		memory.Supersession = supersession

		// Fail closed: reject any memory with an invalid envelope combination
		if err := memory.Validate(); err != nil {
			return nil, fmt.Errorf("corrupted memory envelope for %q/%q: %w", memory.RepositoryName, memory.Title, err)
		}
		memories = append(memories, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories: %w", err)
	}

	return memories, nil
}

func LoadMemoryConfig() (MemoryConfig, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("TENDRIL_MEMORY_BACKEND")))
	if backend == "" {
		backend = "sqlite"
	}

	sqlitePath := strings.TrimSpace(os.Getenv("TENDRIL_MEMORY_SQLITE_PATH"))
	if sqlitePath == "" {
		sqlitePath = filepath.Join(".", ".tendril", "rhizome.db")
	}

	dimension := 8
	if configured := strings.TrimSpace(os.Getenv("TENDRIL_PINECONE_DIMENSION")); configured != "" {
		if _, err := fmt.Sscanf(configured, "%d", &dimension); err != nil || dimension <= 0 {
			return MemoryConfig{}, fmt.Errorf("invalid TENDRIL_PINECONE_DIMENSION: %q", configured)
		}
	}

	remoteAck := false
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TENDRIL_MEMORY_REMOTE_CLEARTEXT_ACK"))) {
	case "true", "1", "yes", "on":
		remoteAck = true
	}

	return MemoryConfig{
		Backend:            backend,
		SQLitePath:         sqlitePath,
		PineconeAPIKey:     os.Getenv("TENDRIL_PINECONE_API_KEY"),
		PineconeBaseURL:    strings.TrimRight(os.Getenv("TENDRIL_PINECONE_BASE_URL"), "/"),
		PineconeDimension:  dimension,
		WeaviateAPIKey:     os.Getenv("TENDRIL_WEAVIATE_API_KEY"),
		WeaviateBaseURL:    strings.TrimRight(os.Getenv("TENDRIL_WEAVIATE_BASE_URL"), "/"),
		RemoteCleartextAck: remoteAck,
	}, nil
}

func OpenMemoryBackend(ctx context.Context, config MemoryConfig, cipher *heartwood.Cipher) (MemoryBackend, error) {
	switch strings.ToLower(strings.TrimSpace(config.Backend)) {
	case "", "sqlite":
		if cipher == nil {
			return nil, fmt.Errorf("cipher is required")
		}
		if err := os.MkdirAll(filepath.Dir(config.SQLitePath), 0o755); err != nil {
			return nil, fmt.Errorf("create memory database directory: %w", err)
		}
		return OpenSQLiteIndexStore(ctx, config.SQLitePath, cipher)
	case "pinecone":
		if !config.RemoteCleartextAck {
			return nil, errRemoteCleartextNotAcknowledged("pinecone")
		}
		return NewPineconeMemoryBackend(config)
	case "weaviate":
		if !config.RemoteCleartextAck {
			return nil, errRemoteCleartextNotAcknowledged("weaviate")
		}
		return NewWeaviateMemoryBackend(config)
	default:
		return nil, fmt.Errorf("unsupported memory backend %q", config.Backend)
	}
}

func errRemoteCleartextNotAcknowledged(backend string) error {
	return fmt.Errorf(
		"memory backend %q sends memory titles, content, and tags unencrypted to a third-party service; "+
			"set TENDRIL_MEMORY_REMOTE_CLEARTEXT_ACK=true to acknowledge and proceed, or use the default sqlite backend",
		backend,
	)
}

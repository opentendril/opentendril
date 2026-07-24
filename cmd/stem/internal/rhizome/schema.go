package rhizome

import (
	"context"
	"database/sql"
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

type Memory struct {
	RepositoryName string    `json:"repositoryName"`
	Category       string    `json:"category"`
	Title          string    `json:"title"`
	Content        string    `json:"content"`
	Tags           string    `json:"tags"`
	CreatedAt      time.Time `json:"createdAt"`
	SessionID      string    `json:"sessionId"`
}

type IndexStore interface {
	Close() error
	DeleteSymbolsForFile(ctx context.Context, repositoryName string, filePath string) error
	GetFile(ctx context.Context, repositoryName string, path string) (FileRecord, bool, error)
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
	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = time.Now().UTC()
	}

	aad := []byte("rhizome/memories/content\x00" + memory.RepositoryName + "\x00" + memory.Title)
	encryptedContent, err := s.cipher.Encrypt(memory.Content, aad)
	if err != nil {
		return fmt.Errorf("encrypt memory content: %w", err)
	}

	const statement = `
INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	if _, err := s.db.ExecContext(ctx, statement, memory.RepositoryName, memory.Category, memory.Title, encryptedContent, memory.Tags, memory.CreatedAt.UTC().Format(time.RFC3339Nano), memory.SessionID); err != nil {
		return fmt.Errorf("store memory: %w", err)
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
SELECT repositoryName, category, title, content, tags, createdAt, sessionId
FROM memories
WHERE repositoryName = ? AND memories MATCH ?`
	args := []any{repositoryName, trimmedQuery}
	if strings.TrimSpace(category) != "" {
		statement += ` AND category = ?`
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
SELECT repositoryName, category, title, content, tags, createdAt, sessionId
FROM memories
WHERE repositoryName = ?`
	args := []any{repositoryName}
	if strings.TrimSpace(category) != "" {
		statement += ` AND category = ?`
		args = append(args, category)
	}
	statement += `
ORDER BY createdAt DESC
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
	const statement = `DELETE FROM memories WHERE repositoryName = ? AND title = ?`
	if _, err := s.db.ExecContext(ctx, statement, repositoryName, title); err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	return nil
}

func (s *SQLiteIndexStore) scanMemoryRows(rows *sql.Rows) ([]Memory, error) {
	memories := make([]Memory, 0)
	for rows.Next() {
		var memory Memory
		var encryptedContent string
		var createdAt string
		if err := rows.Scan(&memory.RepositoryName, &memory.Category, &memory.Title, &encryptedContent, &memory.Tags, &createdAt, &memory.SessionID); err != nil {
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

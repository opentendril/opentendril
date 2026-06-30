package dreamer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

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

type IndexStore interface {
	Close() error
	DeleteSymbolsForFile(ctx context.Context, repositoryName string, filePath string) error
	GetFile(ctx context.Context, repositoryName string, path string) (FileRecord, bool, error)
	SearchSymbols(ctx context.Context, repositoryName string, query string, limit int) ([]Symbol, error)
	UpsertFile(ctx context.Context, file FileRecord) error
	UpsertSymbols(ctx context.Context, symbols []Symbol) error
}

type SQLiteIndexStore struct {
	db        *sql.DB
	encryptor *Encryptor
}

func OpenSQLiteIndexStore(ctx context.Context, dbPath string, encryptor *Encryptor) (*SQLiteIndexStore, error) {
	if encryptor == nil {
		return nil, fmt.Errorf("encryptor is required")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open SQLite index: %w", err)
	}

	store := &SQLiteIndexStore{db: db, encryptor: encryptor}
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
);`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize dreamer schema: %w", err)
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
		encryptedStub, encryptErr := s.encryptor.EncryptString(symbol.StubContent)
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
		symbol.StubContent, err = s.encryptor.DecryptString(encryptedStub)
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

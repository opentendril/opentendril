package rhizome

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/heartwood"
)

func TestSQLiteStoreEncryptsStubsAndSearchesSymbols(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rhizome.db")
	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	const secretStub = "func ProprietarySecret() string"
	err := store.UpsertSymbols(ctx, []Symbol{{
		RepositoryName: "owner/repo",
		Name:           "ProprietarySecret",
		Type:           "function",
		FilePath:       "secret.go",
		LineStart:      3,
		LineEnd:        3,
		StubContent:    secretStub,
	}})
	if err != nil {
		t.Fatalf("UpsertSymbols returned error: %v", err)
	}

	rawDatabase, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if strings.Contains(string(rawDatabase), secretStub) {
		t.Fatalf("database contains plaintext stub")
	}

	results, err := store.SearchSymbols(ctx, "owner/repo", "ProprietarySecret", 10)
	if err != nil {
		t.Fatalf("SearchSymbols returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("result count mismatch: got %d want 1", len(results))
	}
	if results[0].StubContent != secretStub {
		t.Fatalf("decrypted stub mismatch: got %q want %q", results[0].StubContent, secretStub)
	}
}

func TestScanRepositorySkipsUnchangedFilesAndUpdatesChangedFiles(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "rhizome.db")
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.Mkdir(repoRoot, 0o755); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	sourcePath := filepath.Join(repoRoot, "worker.go")
	if err := os.WriteFile(sourcePath, []byte("package repo\n\nfunc First() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	stats, err := ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{GoParser{}})
	if err != nil {
		t.Fatalf("ScanRepository returned error: %v", err)
	}
	if stats.FilesParsed != 1 || stats.FilesSkipped != 0 || stats.SymbolsStored != 2 {
		t.Fatalf("unexpected first scan stats: %+v", stats)
	}

	stats, err = ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{GoParser{}})
	if err != nil {
		t.Fatalf("second ScanRepository returned error: %v", err)
	}
	if stats.FilesParsed != 0 || stats.FilesSkipped != 1 {
		t.Fatalf("unexpected unchanged scan stats: %+v", stats)
	}

	if err := os.WriteFile(sourcePath, []byte("package repo\n\nfunc Second() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile update returned error: %v", err)
	}
	stats, err = ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{GoParser{}})
	if err != nil {
		t.Fatalf("third ScanRepository returned error: %v", err)
	}
	if stats.FilesParsed != 1 || stats.SymbolsStored != 2 {
		t.Fatalf("unexpected changed scan stats: %+v", stats)
	}

	results, err := store.SearchSymbols(ctx, "owner/repo", "Second", 10)
	if err != nil {
		t.Fatalf("SearchSymbols returned error: %v", err)
	}
	if len(results) != 1 || results[0].Name != "Second" {
		t.Fatalf("expected updated symbol, got %+v", results)
	}
}

func TestScanRepositoryPurgesDeletedFiles(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "rhizome.db")
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.Mkdir(repoRoot, 0o755); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	fileA := filepath.Join(repoRoot, "a.go")
	fileB := filepath.Join(repoRoot, "b.go")
	if err := os.WriteFile(fileA, []byte("package repo\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile A returned error: %v", err)
	}
	if err := os.WriteFile(fileB, []byte("package repo\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile B returned error: %v", err)
	}

	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	// Initial scan
	stats, err := ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{GoParser{}})
	if err != nil {
		t.Fatalf("Initial ScanRepository returned error: %v", err)
	}
	if stats.FilesParsed != 2 || stats.FilesPurged != 0 {
		t.Fatalf("unexpected initial scan stats: %+v", stats)
	}

	// Delete B
	if err := os.Remove(fileB); err != nil {
		t.Fatalf("Remove B returned error: %v", err)
	}

	// Second scan
	stats, err = ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{GoParser{}})
	if err != nil {
		t.Fatalf("Second ScanRepository returned error: %v", err)
	}
	if stats.FilesParsed != 0 || stats.FilesSkipped != 1 || stats.FilesPurged != 1 {
		t.Fatalf("unexpected second scan stats: %+v", stats)
	}

	// Verify B is purged
	_, found, err := store.GetFile(ctx, "owner/repo", "b.go")
	if err != nil {
		t.Fatalf("GetFile B returned error: %v", err)
	}
	if found {
		t.Fatalf("expected b.go to be purged, but it was found")
	}

	results, err := store.SearchSymbols(ctx, "owner/repo", "B", 10)
	if err != nil {
		t.Fatalf("SearchSymbols B returned error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected b.go symbols to be purged, got %d results", len(results))
	}

	// Verify A is untouched
	resultsA, err := store.SearchSymbols(ctx, "owner/repo", "A", 10)
	if err != nil {
		t.Fatalf("SearchSymbols A returned error: %v", err)
	}
	if len(resultsA) == 0 {
		t.Fatalf("expected a.go symbols to be present, got 0 results")
	}
}

type failingParser struct {
	failOnPath string
}

func (f failingParser) Supports(path string) bool {
	return true
}

func (f failingParser) Parse(path string, content []byte) ([]Symbol, error) {
	if path == f.failOnPath {
		return nil, fmt.Errorf("synthetic parse error for %s", path)
	}
	safeName := strings.ReplaceAll(filepath.Base(path), ".", "_")
	return []Symbol{{Name: "Parsed_" + safeName, Type: "test", StubContent: "stub"}}, nil
}

func TestScanRepositoryFailSoftAndComposition(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "rhizome.db")
	repoRoot := filepath.Join(tempDir, "repo")
	if err := os.Mkdir(repoRoot, 0o755); err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	fileA := filepath.Join(repoRoot, "a.txt")
	fileB := filepath.Join(repoRoot, "b.txt")
	if err := os.WriteFile(fileA, []byte("valid content"), 0o644); err != nil {
		t.Fatalf("WriteFile A returned error: %v", err)
	}
	if err := os.WriteFile(fileB, []byte("failing content"), 0o644); err != nil {
		t.Fatalf("WriteFile B returned error: %v", err)
	}

	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	parser := failingParser{failOnPath: "b.txt"}

	// First scan: A passes, B fails
	stats, err := ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{parser})
	if err != nil {
		t.Fatalf("First ScanRepository returned error: %v", err)
	}
	if stats.FilesParsed != 1 || stats.FilesFailed != 1 || stats.FilesPurged != 0 {
		t.Fatalf("unexpected first scan stats: %+v", stats)
	}

	// Verify A was stored
	_, found, _ := store.GetFile(ctx, "owner/repo", "a.txt")
	if !found {
		t.Fatalf("expected a.txt to be recorded")
	}

	// Verify B was NOT stored
	_, found, _ = store.GetFile(ctx, "owner/repo", "b.txt")
	if found {
		t.Fatalf("expected b.txt hash to NOT be recorded after parse failure")
	}

	// Second scan: A skipped, B fails again (retried)
	stats, err = ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{parser})
	if err != nil {
		t.Fatalf("Second ScanRepository returned error: %v", err)
	}
	if stats.FilesSkipped != 1 || stats.FilesFailed != 1 || stats.FilesPurged != 0 {
		t.Fatalf("unexpected second scan stats: %+v (expected B to be retried and fail again)", stats)
	}

	// Now write some valid symbols for B so we can test composition (purge protection)
	successParser := failingParser{failOnPath: "none"}
	stats, err = ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{successParser})
	if err != nil {
		t.Fatalf("Third ScanRepository returned error: %v", err)
	}
	if stats.FilesSkipped != 1 || stats.FilesParsed != 1 || stats.FilesPurged != 0 {
		t.Fatalf("unexpected third scan stats: %+v", stats)
	}
	_, found, _ = store.GetFile(ctx, "owner/repo", "b.txt")
	if !found {
		t.Fatalf("expected b.txt to be recorded now")
	}

	// Make B change on disk so it gets re-read, and switch back to failing parser
	time.Sleep(10 * time.Millisecond) // Ensure modified time changes slightly or content hash changes
	if err := os.WriteFile(fileB, []byte("failing content again"), 0o644); err != nil {
		t.Fatalf("WriteFile B returned error: %v", err)
	}
	stats, err = ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{parser})
	if err != nil {
		t.Fatalf("Fourth ScanRepository returned error: %v", err)
	}
	if stats.FilesSkipped != 1 || stats.FilesFailed != 1 || stats.FilesPurged != 0 {
		t.Fatalf("unexpected fourth scan stats: %+v", stats)
	}

	// B failed to parse this time, but it still exists on disk, so old symbols must NOT be purged.
	results, err := store.SearchSymbols(ctx, "owner/repo", "Parsed_b_txt", 10)
	if err != nil {
		t.Fatalf("SearchSymbols B returned error: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected b.txt's previous symbols to survive the failed parse, but they were lost")
	}
}

func TestGenerateRepoMapListsDecryptedSymbols(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rhizome.db")
	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	err := store.UpsertSymbols(ctx, []Symbol{{
		RepositoryName: "owner/repo",
		Name:           "Dream",
		Type:           "function",
		FilePath:       "rhizome.go",
		LineStart:      7,
		LineEnd:        7,
		StubContent:    "func Dream()",
	}})
	if err != nil {
		t.Fatalf("UpsertSymbols returned error: %v", err)
	}

	repoMap, err := GenerateRepoMap(ctx, store, "owner/repo", "", 10)
	if err != nil {
		t.Fatalf("GenerateRepoMap returned error: %v", err)
	}
	if !strings.Contains(repoMap, "func Dream()") {
		t.Fatalf("repomap missing decrypted stub: %s", repoMap)
	}
}

func TestStoreAndSearchMemory(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rhizome.db")
	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	const secretContent = "Prefer repository-local abstractions for long running context."
	err := store.StoreMemory(ctx, Memory{
		RepositoryName: "owner/repo",
		Category:       "Decisions",
		Title:          "Rhizome memory encryption",
		Content:        secretContent,
		Tags:           "rhizome,context",
		CreatedAt:      time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
		SessionID:      "session-1",
	})
	if err != nil {
		t.Fatalf("StoreMemory returned error: %v", err)
	}

	rawDatabase, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if strings.Contains(string(rawDatabase), secretContent) {
		t.Fatalf("database contains plaintext memory content")
	}

	results, err := store.SearchMemories(ctx, "owner/repo", "encryption", "", 10)
	if err != nil {
		t.Fatalf("SearchMemories returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("result count mismatch: got %d want 1", len(results))
	}
	if results[0].Content != secretContent {
		t.Fatalf("decrypted content mismatch: got %q want %q", results[0].Content, secretContent)
	}
}

func TestSearchMemoryByCategory(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rhizome.db")
	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	memories := []Memory{
		{
			RepositoryName: "owner/repo",
			Category:       "Decisions",
			Title:          "Shared keyword sqlite",
			Content:        "Use SQLite for local project memory.",
			CreatedAt:      time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
		},
		{
			RepositoryName: "owner/repo",
			Category:       "Errors",
			Title:          "Shared keyword fts",
			Content:        "FTS special characters need care.",
			CreatedAt:      time.Date(2026, 7, 5, 11, 0, 0, 0, time.UTC),
		},
	}
	for _, memory := range memories {
		if err := store.StoreMemory(ctx, memory); err != nil {
			t.Fatalf("StoreMemory returned error: %v", err)
		}
	}

	results, err := store.SearchMemories(ctx, "owner/repo", "keyword", "Errors", 10)
	if err != nil {
		t.Fatalf("SearchMemories returned error: %v", err)
	}
	if len(results) != 1 || results[0].Category != "Errors" {
		t.Fatalf("expected only Errors memory, got %+v", results)
	}
}

func TestDeleteMemory(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rhizome.db")
	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	title := "Temporary deletion target"
	err := store.StoreMemory(ctx, Memory{
		RepositoryName: "owner/repo",
		Category:       "Patterns",
		Title:          title,
		Content:        "Delete this memory.",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		CreatedAt:      time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("StoreMemory returned error: %v", err)
	}

	// 1. Delete memory
	if err := store.DeleteMemory(ctx, "owner/repo", title); err != nil {
		t.Fatalf("DeleteMemory returned error: %v", err)
	}

	// 2. Verify sidecar metadata is gone
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM memory_envelopes WHERE repositoryName = ? AND title = ?`, "owner/repo", title).Scan(&count); err != nil {
		t.Fatalf("Query sidecar: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected sidecar to be deleted, found %d", count)
	}

	// 3. Subsequently introduced legacy row
	cipher, _ := heartwood.NewCipher(heartwood.Material{Key: []byte("0123456789abcdef0123456789abcdef")})
	aad := []byte("rhizome/memories/content\x00owner/repo\x00" + title)
	enc, _ := cipher.Encrypt("content", aad)
	_, err = store.db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId) VALUES ('owner/repo', 'Patterns', ?, ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, title, enc)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	// 4. Reads as legacy/none/unclassified
	results, err := store.SearchMemories(ctx, "owner/repo", "Temporary", "", 10)
	if err != nil {
		t.Fatalf("SearchMemories returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Origin != OriginLegacy || results[0].Authority != AuthorityNone || results[0].Status != StatusUnclassified {
		t.Fatalf("expected legacy/none/unclassified, got %s/%s/%s", results[0].Origin, results[0].Authority, results[0].Status)
	}
}

func TestGenerateMemoryMap(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rhizome.db")
	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	memories := []Memory{
		{
			RepositoryName: "owner/repo",
			Category:       "Decisions",
			Title:          "Choose encrypted SQLite",
			Content:        "Local memories stay in rhizome.db.",
			CreatedAt:      time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
		},
		{
			RepositoryName: "owner/repo",
			Category:       "Patterns",
			Title:          "Prefer narrow interfaces",
			Content:        "Backends implement MemoryBackend.",
			CreatedAt:      time.Date(2026, 7, 5, 11, 0, 0, 0, time.UTC),
		},
	}
	for _, memory := range memories {
		if err := store.StoreMemory(ctx, memory); err != nil {
			t.Fatalf("StoreMemory returned error: %v", err)
		}
	}

	memoryMap, err := GenerateMemoryMap(ctx, store, "owner/repo", "*", 10)
	if err != nil {
		t.Fatalf("GenerateMemoryMap returned error: %v", err)
	}
	for _, expected := range []string{"## Decisions", "## Patterns", "Choose encrypted SQLite", "Prefer narrow interfaces"} {
		if !strings.Contains(memoryMap, expected) {
			t.Fatalf("memory map missing %q: %s", expected, memoryMap)
		}
	}
}

func openTestStore(t *testing.T, ctx context.Context, dbPath string) *SQLiteIndexStore {
	t.Helper()

	cipher, err := heartwood.NewCipher(heartwood.Material{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatalf("NewCipher returned error: %v", err)
	}
	store, err := OpenSQLiteIndexStore(ctx, dbPath, cipher)
	if err != nil {
		t.Fatalf("OpenSQLiteIndexStore returned error: %v", err)
	}
	return store
}

func TestOpenMemoryBackend_Gate(t *testing.T) {
	ctx := context.Background()
	cipher, err := heartwood.NewCipher(heartwood.Material{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatalf("NewCipher returned error: %v", err)
	}

	// pinecone without ack
	config := MemoryConfig{Backend: "pinecone", RemoteCleartextAck: false}
	backend, err := OpenMemoryBackend(ctx, config, cipher)
	if err == nil {
		t.Fatalf("expected error for pinecone without ack")
	}
	if !strings.Contains(err.Error(), "acknowledge") {
		t.Fatalf("expected error to mention acknowledge, got: %v", err)
	}
	if backend != nil {
		t.Fatalf("expected nil backend")
	}

	// weaviate without ack
	config = MemoryConfig{Backend: "weaviate", RemoteCleartextAck: false}
	backend, err = OpenMemoryBackend(ctx, config, cipher)
	if err == nil {
		t.Fatalf("expected error for weaviate without ack")
	}
	if !strings.Contains(err.Error(), "acknowledge") {
		t.Fatalf("expected error to mention acknowledge, got: %v", err)
	}
	if backend != nil {
		t.Fatalf("expected nil backend")
	}

	// pinecone with ack, no base url -> pinecone's own error
	config = MemoryConfig{Backend: "pinecone", RemoteCleartextAck: true}
	_, err = OpenMemoryBackend(ctx, config, cipher)
	if err == nil {
		t.Fatalf("expected pinecone config error")
	}
	if !strings.Contains(err.Error(), "TENDRIL_PINECONE_BASE_URL") {
		t.Fatalf("expected error from Pinecone constructor, got: %v", err)
	}

	// weaviate with ack, no base url -> weaviate's own error
	config = MemoryConfig{Backend: "weaviate", RemoteCleartextAck: true}
	_, err = OpenMemoryBackend(ctx, config, cipher)
	if err == nil {
		t.Fatalf("expected weaviate config error")
	}
	if !strings.Contains(err.Error(), "TENDRIL_WEAVIATE_BASE_URL") {
		t.Fatalf("expected error from Weaviate constructor, got: %v", err)
	}
}

func TestLoadMemoryConfig_RemoteAck(t *testing.T) {
	t.Setenv("TENDRIL_MEMORY_REMOTE_CLEARTEXT_ACK", "true")
	config, err := LoadMemoryConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !config.RemoteCleartextAck {
		t.Fatalf("expected RemoteCleartextAck to be true")
	}

	t.Setenv("TENDRIL_MEMORY_REMOTE_CLEARTEXT_ACK", "")
	config, err = LoadMemoryConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if config.RemoteCleartextAck {
		t.Fatalf("expected RemoteCleartextAck to be false")
	}
}

func TestSQLiteMemoryEnvelope(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rhizome.db")

	// Seed a legacy database (only the FTS5 table, no sidecar) before the
	// current store code runs its additive migration.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("Open sqlite: %v", err)
	}
	const legacySchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS memories USING fts5(
	repositoryName, category, title, content UNINDEXED, tags, createdAt UNINDEXED, sessionId UNINDEXED
);
`
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatalf("Create legacy schema: %v", err)
	}
	cipher, _ := heartwood.NewCipher(heartwood.Material{Key: []byte("0123456789abcdef0123456789abcdef")})
	aad1 := []byte("rhizome/memories/content\x00owner/repo\x00Legacy Memory")
	c1, _ := cipher.Encrypt("content", aad1)
	aad2 := []byte("rhizome/memories/content\x00owner/repo\x00Legacy Duplicate")
	c2, _ := cipher.Encrypt("content1", aad2)
	c3, _ := cipher.Encrypt("content2", aad2)
	_, err = db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId)
	VALUES ('owner/repo', 'Test', 'Legacy Memory', ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, c1)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	_, _ = db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId)
	VALUES ('owner/repo', 'Test', 'Legacy Duplicate', ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, c2)
	_, _ = db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId)
	VALUES ('owner/repo', 'Test', 'Legacy Duplicate', ?, 'tag', '2026-07-05T11:00:00Z', 'session-1')`, c3)
	db.Close()

	// Open with current code: additive migration creates memory_envelopes sidecar.
	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	// Legacy row survives sidecar creation and reads as legacy/none/unclassified.
	results, err := store.SearchMemories(ctx, "owner/repo", "Legacy Memory", "", 10)
	if err != nil {
		t.Fatalf("Search legacy memory: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 legacy memory, got %d", len(results))
	}
	if results[0].Origin != OriginLegacy || results[0].Authority != AuthorityNone || results[0].Status != StatusUnclassified {
		t.Fatalf("Legacy fallback failed: %+v", results[0])
	}

	// Repeated writes converge to one FTS row (upsert via delete+insert).
	err = store.StoreMemory(ctx, Memory{
		RepositoryName: "owner/repo",
		Title:          "New Write",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
	})
	if err != nil {
		t.Fatalf("StoreMemory first write: %v", err)
	}
	err = store.StoreMemory(ctx, Memory{
		RepositoryName: "owner/repo",
		Title:          "New Write",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
	})
	if err != nil {
		t.Fatalf("StoreMemory second write: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM memories WHERE title = 'New Write'`).Scan(&count); err != nil {
		t.Fatalf("QueryRow count: %v", err)
	}
	if count != 1 {
		t.Fatalf("Expected 1 row for New Write, got %d", count)
	}

	// Pre-existing legacy duplicates (not written by this code) are preserved.
	if err := store.db.QueryRow(`SELECT count(*) FROM memories WHERE title = 'Legacy Duplicate'`).Scan(&count); err != nil {
		t.Fatalf("QueryRow duplicate count: %v", err)
	}
	if count != 2 {
		t.Fatalf("Expected 2 rows for Legacy Duplicate, got %d", count)
	}

	// Full envelope round-trips through store + search.
	fullMem := Memory{
		RepositoryName:   "owner/repo",
		Title:            "Full Memory",
		Origin:           OriginSubstrate,
		Authority:        AuthorityDeterministic,
		Status:           StatusEstablished,
		Kind:             KindFact,
		Provenance:       "some-provenance",
		SourceClass:      "some-class",
		SourceIdentity:   "some-id",
		ContentIdentity:  "content-id",
		RevisionIdentity: "rev-id",
		RevisionMetadata: "meta",
		Supersession:     "super",
	}
	fullMem.StableID = StableMemoryIdentity(fullMem.RepositoryName, fullMem.Title)
	if err := store.StoreMemory(ctx, fullMem); err != nil {
		t.Fatalf("Store full memory: %v", err)
	}
	res, err := store.SearchMemories(ctx, "owner/repo", "Full Memory", "", 10)
	if err != nil || len(res) != 1 {
		t.Fatalf("Search full memory: err=%v len=%d", err, len(res))
	}
	got := res[0]
	if got.Origin != fullMem.Origin || got.Authority != fullMem.Authority ||
		got.Status != fullMem.Status || got.Kind != fullMem.Kind ||
		got.Provenance != fullMem.Provenance || got.SourceClass != fullMem.SourceClass ||
		got.SourceIdentity != fullMem.SourceIdentity ||
		got.ContentIdentity != fullMem.ContentIdentity ||
		got.RevisionIdentity != fullMem.RevisionIdentity ||
		got.RevisionMetadata != fullMem.RevisionMetadata ||
		got.Supersession != fullMem.Supersession {
		t.Fatalf("Envelope round-trip mismatch:\nwant: %+v\ngot:  %+v", fullMem, got)
	}
	if got.StableID != fullMem.StableID {
		t.Fatalf("StableID mismatch: want canonical %q, got %q", fullMem.StableID, got.StableID)
	}

	// StableMemoryIdentity must be non-empty and deterministic.
	sid := StableMemoryIdentity("owner/repo", "Full Memory")
	if sid == "" {
		t.Fatal("StableMemoryIdentity returned empty string")
	}
	if sid != StableMemoryIdentity("owner/repo", "Full Memory") {
		t.Fatal("StableMemoryIdentity is not deterministic")
	}

	// Invalid envelope combinations are rejected before persistence.
	invalidMem := Memory{
		RepositoryName: "owner/repo",
		Title:          "Invalid Memory",
		Origin:         OriginMycorrhizal,
		Authority:      AuthorityNone,
		Status:         StatusEstablished,
	}
	if err := store.StoreMemory(ctx, invalidMem); err == nil {
		t.Fatal("Expected error for invalid envelope, got nil")
	}

	// Corrupted status value causes fail-closed on read.
	if _, err := store.db.Exec(`UPDATE memory_envelopes SET status = 'weird' WHERE title = 'Full Memory'`); err != nil {
		t.Fatalf("Corrupt status: %v", err)
	}
	_, err = store.SearchMemories(ctx, "owner/repo", "Full Memory", "", 10)
	if err == nil {
		t.Fatal("Expected error for corrupted status, got nil")
	}

	// legacy / invalid-authority / unclassified sidecar fails closed
	c4, _ := cipher.Encrypt("content", []byte("rhizome/memories/content\x00owner/repo\x00BadAuth"))
	store.db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId) VALUES ('owner/repo', 'Test', 'BadAuth', ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, c4)
	store.db.Exec(`INSERT INTO memory_envelopes (repositoryName, title, origin, authority, status) VALUES ('owner/repo', 'BadAuth', 'legacy', 'invalid-auth', 'unclassified')`)
	_, err = store.SearchMemories(ctx, "owner/repo", "BadAuth", "", 10)
	if err == nil {
		t.Fatal("Expected error for legacy / invalid-authority / unclassified sidecar, got nil")
	} else if !strings.Contains(err.Error(), "unknown authority") {
		t.Fatalf("Expected unknown authority error, got %v", err)
	}

	// legacy / none / unclassified / invalid-kind sidecar fails closed
	c5, _ := cipher.Encrypt("content", []byte("rhizome/memories/content\x00owner/repo\x00BadKind"))
	store.db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId) VALUES ('owner/repo', 'Test', 'BadKind', ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, c5)
	store.db.Exec(`INSERT INTO memory_envelopes (repositoryName, title, origin, authority, status, kind) VALUES ('owner/repo', 'BadKind', 'legacy', 'none', 'unclassified', 'bad-kind')`)
	_, err = store.SearchMemories(ctx, "owner/repo", "BadKind", "", 10)
	if err == nil {
		t.Fatal("Expected error for legacy / none / unclassified / invalid-kind sidecar, got nil")
	} else if !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("Expected unknown kind error, got %v", err)
	}

	// corrupt non-empty stored StableID fails closed
	c6, _ := cipher.Encrypt("content", []byte("rhizome/memories/content\x00owner/repo\x00BadStableID"))
	store.db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId) VALUES ('owner/repo', 'Test', 'BadStableID', ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, c6)
	store.db.Exec(`INSERT INTO memory_envelopes (repositoryName, title, origin, authority, status, stableId) VALUES ('owner/repo', 'BadStableID', 'legacy', 'none', 'unclassified', 'wrong-id')`)
	_, err = store.SearchMemories(ctx, "owner/repo", "BadStableID", "", 10)
	if err == nil {
		t.Fatal("Expected error for corrupt non-empty stored StableID, got nil")
	} else if !strings.Contains(err.Error(), "conflicts with canonical identity") {
		t.Fatalf("Expected canonical identity conflict error, got %v", err)
	}

	// empty StableID on a genuine legacy row is normalized to the canonical deterministic StableID
	c7, _ := cipher.Encrypt("content", []byte("rhizome/memories/content\x00owner/repo\x00EmptyStableID"))
	store.db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId) VALUES ('owner/repo', 'Test', 'EmptyStableID', ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, c7)
	store.db.Exec(`INSERT INTO memory_envelopes (repositoryName, title, origin, authority, status, stableId) VALUES ('owner/repo', 'EmptyStableID', 'legacy', 'none', 'unclassified', '')`)
	resEmpty, err := store.SearchMemories(ctx, "owner/repo", "EmptyStableID", "", 10)
	if err != nil {
		t.Fatalf("Unexpected error for empty StableID row: %v", err)
	}
	if len(resEmpty) != 1 || resEmpty[0].StableID != StableMemoryIdentity("owner/repo", "EmptyStableID") {
		t.Fatalf("Expected empty StableID to be canonicalized, got: %+v", resEmpty)
	}
}

func TestMemoryEnvelopeValidation(t *testing.T) {
	cases := []struct {
		name    string
		memory  Memory
		wantErr string
	}{
		{
			name:    "unknown origin rejected before write",
			memory:  Memory{Origin: "weird-origin", Authority: AuthorityNone, Status: StatusUnclassified},
			wantErr: "unknown origin",
		},
		{
			name:    "unknown authority rejected before write",
			memory:  Memory{Origin: OriginLegacy, Authority: "weird-authority", Status: StatusUnclassified},
			wantErr: "unknown authority",
		},
		{
			name:    "unknown status rejected before write",
			memory:  Memory{Origin: OriginLegacy, Authority: AuthorityNone, Status: "weird-status"},
			wantErr: "unknown status",
		},
		{
			name:    "unknown kind rejected before write",
			memory:  Memory{Origin: OriginLegacy, Authority: AuthorityNone, Status: StatusUnclassified, Kind: "weird-kind"},
			wantErr: "unknown kind",
		},
		{
			name:    "legacy none established invalid",
			memory:  Memory{Origin: OriginLegacy, Authority: AuthorityNone, Status: StatusEstablished},
			wantErr: "established status requires botanist or deterministic authority",
		},
		{
			name:    "botanist none established invalid",
			memory:  Memory{Origin: OriginBotanist, Authority: AuthorityNone, Status: StatusEstablished},
			wantErr: "established status requires botanist or deterministic authority",
		},
		{
			name:    "substrate none established invalid",
			memory:  Memory{Origin: OriginSubstrate, Authority: AuthorityNone, Status: StatusEstablished},
			wantErr: "established status requires botanist or deterministic authority",
		},
		{
			name:    "mycorrhizal none established invalid",
			memory:  Memory{Origin: OriginMycorrhizal, Authority: AuthorityNone, Status: StatusEstablished},
			wantErr: "established status requires botanist or deterministic authority",
		},
		{
			name:    "botanist plus botanist established valid",
			memory:  Memory{Origin: OriginBotanist, Authority: AuthorityBotanist, Status: StatusEstablished},
			wantErr: "",
		},
		{
			name:    "Mycorrhizal proposal valid",
			memory:  Memory{Origin: OriginMycorrhizal, Authority: AuthorityNone, Status: StatusProposed},
			wantErr: "",
		},
		{
			name:    "Mycorrhizal plus Botanist authority established valid",
			memory:  Memory{Origin: OriginMycorrhizal, Authority: AuthorityBotanist, Status: StatusEstablished},
			wantErr: "",
		},
		{
			name:    "Mycorrhizal plus deterministic established invalid",
			memory:  Memory{Origin: OriginMycorrhizal, Authority: AuthorityDeterministic, Status: StatusEstablished},
			wantErr: "mycorrhizal origin requires botanist authority to be established",
		},
		{
			name:    "Substrate deterministic established without evidence invalid",
			memory:  Memory{Origin: OriginSubstrate, Authority: AuthorityDeterministic, Status: StatusEstablished},
			wantErr: "deterministic substrate establishment requires source identity",
		},
		{
			name:    "Substrate deterministic established with evidence valid",
			memory:  Memory{Origin: OriginSubstrate, Authority: AuthorityDeterministic, Status: StatusEstablished, SourceIdentity: "src", ContentIdentity: "cid"},
			wantErr: "",
		},
		{
			name:    "mismatched StableID cannot persist",
			memory:  Memory{RepositoryName: "owner/repo", Title: "test", StableID: "wrong-id", Origin: OriginLegacy, Authority: AuthorityNone, Status: StatusUnclassified},
			wantErr: "conflicts with canonical identity",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.memory.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
				}
			}
		})
	}
}

func TestStableMemoryIdentityCompatibility(t *testing.T) {
	if StableMemoryIdentity("repo", "Rule") == StableMemoryIdentity(" repo ", " Rule ") {
		t.Fatal("expected distinct raw identity inputs to remain distinct for \"Rule\" != \" Rule \" and \"repo\" != \" repo \"")
	}
	if StableMemoryIdentity("owner/repo", "Rule") == StableMemoryIdentity("owner/repo", " Rule ") {
		t.Fatal("expected distinct raw identity inputs to remain distinct for \"Rule\" != \" Rule \"")
	}
	if StableMemoryIdentity("repo", "Rule") == StableMemoryIdentity(" repo ", "Rule") {
		t.Fatal("expected distinct raw identity inputs to remain distinct for \"repo\" != \" repo \"")
	}
}

func TestSQLiteGetMemory(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rhizome.db")
	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	// 1. exact miss returns found=false
	_, found, err := store.GetMemory(ctx, "owner/repo", "NonExistent")
	if err != nil {
		t.Fatalf("unexpected error for exact miss: %v", err)
	}
	if found {
		t.Fatal("expected found=false for exact miss")
	}

	mem := Memory{
		RepositoryName: "owner/repo",
		Title:          "ExactTitle",
		Content:        "Content",
		Origin:         OriginBotanist,
		Authority:      AuthorityBotanist,
		Status:         StatusEstablished,
		CreatedAt:      time.Now().UTC(),
	}
	if err := store.StoreMemory(ctx, mem); err != nil {
		t.Fatalf("StoreMemory returned error: %v", err)
	}

	// 2. exact hit returns found=true
	// 3. exact repository/title matching is used
	got, found, err := store.GetMemory(ctx, "owner/repo", "ExactTitle")
	if err != nil {
		t.Fatalf("unexpected error for exact hit: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for exact hit")
	}
	if got.Title != "ExactTitle" || got.RepositoryName != "owner/repo" {
		t.Fatalf("expected exact repository/title match, got %q / %q", got.RepositoryName, got.Title)
	}

	_, found, _ = store.GetMemory(ctx, "owner/repo", " ExactTitle")
	if found {
		t.Fatal("expected found=false for fuzzy title match")
	}
	_, found, _ = store.GetMemory(ctx, " owner/repo", "ExactTitle")
	if found {
		t.Fatal("expected found=false for fuzzy repo match")
	}

	// 4. duplicate legacy FTS rows with the exact same repository/title fail closed
	cipher, _ := heartwood.NewCipher(heartwood.Material{Key: []byte("0123456789abcdef0123456789abcdef")})
	cDup, _ := cipher.Encrypt("content", []byte("rhizome/memories/content\x00owner/repo\x00DupLegacy"))
	store.db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId) VALUES ('owner/repo', 'Test', 'DupLegacy', ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, cDup)
	store.db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId) VALUES ('owner/repo', 'Test', 'DupLegacy', ?, 'tag', '2026-07-05T11:00:00Z', 'session-1')`, cDup)

	_, _, err = store.GetMemory(ctx, "owner/repo", "DupLegacy")
	if err == nil {
		t.Fatal("expected duplicate legacy rows to fail closed, got nil error")
	} else if !strings.Contains(err.Error(), "duplicate exact memories") {
		t.Fatalf("expected duplicate error, got: %v", err)
	}

	// 5. malformed envelope metadata fails closed
	// Use INSERT OR REPLACE to force-overwrite the valid envelope already stored.
	store.db.Exec(`INSERT OR REPLACE INTO memory_envelopes (repositoryName, title, origin, authority, status) VALUES ('owner/repo', 'ExactTitle', 'weird', 'weird', 'weird')`)
	_, _, err = store.GetMemory(ctx, "owner/repo", "ExactTitle")
	if err == nil {
		t.Fatal("expected malformed envelope to fail closed, got nil error")
	} else if !strings.Contains(err.Error(), "unknown origin") {
		t.Fatalf("expected unknown origin error, got: %v", err)
	}

	// 6. conflicting stored StableID fails closed
	cConflict, _ := cipher.Encrypt("content", []byte("rhizome/memories/content\x00owner/repo\x00ConflictID"))
	store.db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId) VALUES ('owner/repo', 'Test', 'ConflictID', ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, cConflict)
	store.db.Exec(`INSERT INTO memory_envelopes (repositoryName, title, origin, authority, status, stableId) VALUES ('owner/repo', 'ConflictID', 'legacy', 'none', 'unclassified', 'bad-id')`)
	_, _, err = store.GetMemory(ctx, "owner/repo", "ConflictID")
	if err == nil {
		t.Fatal("expected conflicting StableID to fail closed, got nil error")
	} else if !strings.Contains(err.Error(), "conflicts with canonical identity") {
		t.Fatalf("expected identity conflict error, got: %v", err)
	}

	// 7. a normal legacy row without sidecar metadata still returns legacy/none/unclassified
	cLegacy, _ := cipher.Encrypt("content", []byte("rhizome/memories/content\x00owner/repo\x00LegacyOnly"))
	store.db.Exec(`INSERT INTO memories (repositoryName, category, title, content, tags, createdAt, sessionId) VALUES ('owner/repo', 'Test', 'LegacyOnly', ?, 'tag', '2026-07-05T10:00:00Z', 'session-1')`, cLegacy)
	gotLegacy, foundLegacy, err := store.GetMemory(ctx, "owner/repo", "LegacyOnly")
	if err != nil {
		t.Fatalf("unexpected error for normal legacy row: %v", err)
	}
	if !foundLegacy {
		t.Fatal("expected found=true for legacy row")
	}
	if gotLegacy.Origin != OriginLegacy || gotLegacy.Authority != AuthorityNone || gotLegacy.Status != StatusUnclassified {
		t.Fatalf("expected legacy/none/unclassified, got %q / %q / %q", gotLegacy.Origin, gotLegacy.Authority, gotLegacy.Status)
	}
}

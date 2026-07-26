package rhizome

import (
	"context"
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

	err := store.StoreMemory(ctx, Memory{
		RepositoryName: "owner/repo",
		Category:       "Patterns",
		Title:          "Temporary deletion target",
		Content:        "Delete this memory.",
		CreatedAt:      time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("StoreMemory returned error: %v", err)
	}
	if err := store.DeleteMemory(ctx, "owner/repo", "Temporary deletion target"); err != nil {
		t.Fatalf("DeleteMemory returned error: %v", err)
	}

	results, err := store.SearchMemories(ctx, "owner/repo", "Temporary", "", 10)
	if err != nil {
		t.Fatalf("SearchMemories returned error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected deleted memory to be absent, got %+v", results)
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

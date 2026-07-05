package rhizome

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	encryptor, err := NewEncryptor([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("NewEncryptor returned error: %v", err)
	}
	store, err := OpenSQLiteIndexStore(ctx, dbPath, encryptor)
	if err != nil {
		t.Fatalf("OpenSQLiteIndexStore returned error: %v", err)
	}
	return store
}

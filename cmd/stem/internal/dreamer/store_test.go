package dreamer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteStoreEncryptsStubsAndSearchesSymbols(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dreamer.db")
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
	dbPath := filepath.Join(tempDir, "dreamer.db")
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
	if stats.FilesParsed != 1 || stats.FilesSkipped != 0 || stats.SymbolsStored != 1 {
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
	if stats.FilesParsed != 1 || stats.SymbolsStored != 1 {
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
	dbPath := filepath.Join(t.TempDir(), "dreamer.db")
	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	err := store.UpsertSymbols(ctx, []Symbol{{
		RepositoryName: "owner/repo",
		Name:           "Dream",
		Type:           "function",
		FilePath:       "dreamer.go",
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

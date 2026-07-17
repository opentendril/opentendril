package conductor

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/rhizome"
)

// Runtime state OpenTendril writes into a substrate while working on it. These
// belong to the tool, not to the repository being worked on.
//
// The names live here, next to the code that creates them, because the commit
// path has to skip exactly these files and a second copy of the list would
// drift away from this one. Nothing else under .tendril is covered on purpose:
// a repository may legitimately track its own .tendril files, and an agent
// asked to edit one must still be able to.
const (
	tendrilStateDirectory = ".tendril"
	rhizomeIndexKeyFile   = "rhizome.key"
	rhizomeIndexDatabase  = "rhizome.db"
	repositoryMapFile     = "repomap.md"
)

// generatedRuntimeArtifacts lists, relative to the substrate root, everything
// GenerateRepoMap and its callers leave behind.
func generatedRuntimeArtifacts() []string {
	return []string{
		filepath.ToSlash(filepath.Join(tendrilStateDirectory, rhizomeIndexKeyFile)),
		filepath.ToSlash(filepath.Join(tendrilStateDirectory, rhizomeIndexDatabase)),
		filepath.ToSlash(filepath.Join(tendrilStateDirectory, "genome", repositoryMapFile)),
	}
}

// isGeneratedRuntimeArtifact reports whether a substrate-relative path is
// something OpenTendril wrote for itself.
//
// SQLite keeps its write-ahead log and shared-memory file beside the database
// under names derived from it, so the database is matched by prefix rather than
// equality — otherwise `rhizome.db-wal` would be committed as the agent's work.
func isGeneratedRuntimeArtifact(path string) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if normalized == "" {
		return false
	}
	databasePath := filepath.ToSlash(filepath.Join(tendrilStateDirectory, rhizomeIndexDatabase))
	if strings.HasPrefix(normalized, databasePath) {
		return true
	}
	for _, artifact := range generatedRuntimeArtifacts() {
		if normalized == artifact {
			return true
		}
	}
	return false
}

// GenerateRepoMap initializes the Rhizome context engine, incrementally scans
// the provided repository mount path, and returns a markdown-formatted map
// of the repository's semantic signatures.
func GenerateRepoMap(ctx context.Context, mountPath string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	tendrilDir := filepath.Join(mountPath, tendrilStateDirectory)
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		return "", fmt.Errorf("create .tendril dir: %w", err)
	}

	keyPath := filepath.Join(tendrilDir, rhizomeIndexKeyFile)
	key, err := getOrCreateIndexKey(keyPath)
	if err != nil {
		return "", fmt.Errorf("resolve index key: %w", err)
	}

	encryptor, err := rhizome.NewEncryptor(key)
	if err != nil {
		return "", fmt.Errorf("initialize encryptor: %w", err)
	}

	dbPath := filepath.Join(tendrilDir, rhizomeIndexDatabase)
	store, err := rhizome.OpenSQLiteIndexStore(ctx, dbPath, encryptor)
	if err != nil {
		return "", fmt.Errorf("open index store: %w", err)
	}
	defer store.Close()

	absoluteMountPath, err := filepath.Abs(mountPath)
	if err != nil {
		absoluteMountPath = mountPath
	}
	repositoryName := filepath.Base(absoluteMountPath)
	if repositoryName == "." || repositoryName == "" {
		repositoryName = "workspace"
	}

	// DefaultParsers gives Go the go/ast parser, non-Go files the in-process
	// pure-Go tree-sitter engine (rhizome.TreeSitterParser), and regex as the
	// final fallback — no container, no docker. Scanner hash-skip keeps
	// re-scans incremental per file.
	parsers := rhizome.DefaultParsers()
	if _, err := rhizome.ScanRepository(ctx, mountPath, repositoryName, store, parsers); err != nil {
		return "", fmt.Errorf("scan repository: %w", err)
	}

	return rhizome.GenerateRepoMap(ctx, store, repositoryName, "*", 2000)
}

// GenerateMemoryMap initializes the Rhizome memory backend for the provided
// repository mount path and returns a markdown-formatted project memory map.
func GenerateMemoryMap(ctx context.Context, mountPath string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	tendrilDir := filepath.Join(mountPath, tendrilStateDirectory)
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		return "", fmt.Errorf("create .tendril dir: %w", err)
	}

	keyPath := filepath.Join(tendrilDir, rhizomeIndexKeyFile)
	key, err := getOrCreateIndexKey(keyPath)
	if err != nil {
		return "", fmt.Errorf("resolve index key: %w", err)
	}

	encryptor, err := rhizome.NewEncryptor(key)
	if err != nil {
		return "", fmt.Errorf("initialize encryptor: %w", err)
	}

	dbPath := filepath.Join(tendrilDir, rhizomeIndexDatabase)
	store, err := rhizome.OpenSQLiteIndexStore(ctx, dbPath, encryptor)
	if err != nil {
		return "", fmt.Errorf("open index store: %w", err)
	}
	defer store.Close()

	absoluteMountPath, err := filepath.Abs(mountPath)
	if err != nil {
		absoluteMountPath = mountPath
	}
	repositoryName := filepath.Base(absoluteMountPath)
	if repositoryName == "." || repositoryName == "" {
		repositoryName = "workspace"
	}

	memoryMap, err := rhizome.GenerateMemoryMap(ctx, store, repositoryName, "*", 2000)
	if err != nil {
		return "", err
	}
	if memoryMap == "" {
		return "", nil
	}
	return memoryMap, nil
}

func getOrCreateIndexKey(keyPath string) ([]byte, error) {
	if envKey := os.Getenv("OPEN_TENDRIL_INDEX_KEY"); envKey != "" {
		if len(envKey) >= 32 {
			return []byte(envKey[:32]), nil
		}
		// Pad to 32 bytes if shorter
		padded := make([]byte, 32)
		copy(padded, envKey)
		return padded, nil
	}

	if content, err := os.ReadFile(keyPath); err == nil && len(content) == 32 {
		return content, nil
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate random key: %w", err)
	}

	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("save generated key: %w", err)
	}

	return key, nil
}

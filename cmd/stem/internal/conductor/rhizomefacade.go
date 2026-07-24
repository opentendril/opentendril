package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/heartwood"
	"github.com/opentendril/opentendril/cmd/stem/internal/rhizome"
)

// Runtime state OpenTendril writes into a substrate while working on it. These
// belong to the tool, not to the repository being worked on.
//
// The names live here, next to the code that creates them, because the commit
// path has to skip exactly these files and a second copy of the list would
// drift away from this one. Nothing else under .tendril is covered on purpose:
// a repository may legitimately track its own .tendril files, and a Sprout
// asked to edit one must still be able to.
const (
	tendrilStateDirectory = ".tendril"
	rhizomeIndexKeyFile   = "rhizome.key"
	rhizomeIndexDatabase  = "rhizome.db"
	repositoryMapFile     = "repomap.md"
	memoryMapFile         = "memorymap.md"
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
// equality — otherwise `rhizome.db-wal` would be committed as the Sprout's work.
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
	material, err := heartwood.ResolveKey(keyPath)
	if err != nil {
		return "", fmt.Errorf("resolve index key: %w", err)
	}

	cipher, err := heartwood.NewCipher(material)
	if err != nil {
		return "", fmt.Errorf("initialize cipher: %w", err)
	}

	dbPath := filepath.Join(tendrilDir, rhizomeIndexDatabase)
	store, err := rhizome.OpenSQLiteIndexStore(ctx, dbPath, cipher)
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
	material, err := heartwood.ResolveKey(keyPath)
	if err != nil {
		return "", fmt.Errorf("resolve index key: %w", err)
	}

	cipher, err := heartwood.NewCipher(material)
	if err != nil {
		return "", fmt.Errorf("initialize cipher: %w", err)
	}

	dbPath := filepath.Join(tendrilDir, rhizomeIndexDatabase)
	store, err := rhizome.OpenSQLiteIndexStore(ctx, dbPath, cipher)
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

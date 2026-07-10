package conductor

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opentendril/core/cmd/stem/internal/rhizome"
)

// GenerateRepoMap initializes the Rhizome context engine, incrementally scans
// the provided repository mount path, and returns a markdown-formatted map
// of the repository's semantic signatures.
func GenerateRepoMap(ctx context.Context, mountPath string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	tendrilDir := filepath.Join(mountPath, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		return "", fmt.Errorf("create .tendril dir: %w", err)
	}

	keyPath := filepath.Join(tendrilDir, "rhizome.key")
	key, err := getOrCreateIndexKey(keyPath)
	if err != nil {
		return "", fmt.Errorf("resolve index key: %w", err)
	}

	encryptor, err := rhizome.NewEncryptor(key)
	if err != nil {
		return "", fmt.Errorf("initialize encryptor: %w", err)
	}

	dbPath := filepath.Join(tendrilDir, "rhizome.db")
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

	if _, err := rhizome.ScanRepository(ctx, mountPath, repositoryName, store, nil); err != nil {
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

	tendrilDir := filepath.Join(mountPath, ".tendril")
	if err := os.MkdirAll(tendrilDir, 0o755); err != nil {
		return "", fmt.Errorf("create .tendril dir: %w", err)
	}

	keyPath := filepath.Join(tendrilDir, "rhizome.key")
	key, err := getOrCreateIndexKey(keyPath)
	if err != nil {
		return "", fmt.Errorf("resolve index key: %w", err)
	}

	encryptor, err := rhizome.NewEncryptor(key)
	if err != nil {
		return "", fmt.Errorf("initialize encryptor: %w", err)
	}

	dbPath := filepath.Join(tendrilDir, "rhizome.db")
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

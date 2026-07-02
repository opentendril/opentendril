package dreamer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ScanStats struct {
	FilesParsed   int
	FilesSkipped  int
	SymbolsStored int
}

func ScanRepository(ctx context.Context, root string, repositoryName string, store IndexStore, parsers []Parser) (ScanStats, error) {
	if store == nil {
		return ScanStats{}, fmt.Errorf("index store is required")
	}
	if strings.TrimSpace(repositoryName) == "" {
		return ScanStats{}, fmt.Errorf("repositoryName is required")
	}
	if len(parsers) == 0 {
		parsers = DefaultParsers()
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return ScanStats{}, fmt.Errorf("resolve repository root: %w", err)
	}

	var stats ScanStats
	err = filepath.WalkDir(absoluteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absoluteRoot {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		relativePath, err := filepath.Rel(absoluteRoot, path)
		if err != nil {
			return err
		}
		relativePath = filepath.ToSlash(filepath.Clean(relativePath))

		if shouldSkipPath(relativePath, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}

		parser := parserForPath(relativePath, parsers)
		if parser == nil {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash := hashContent(content)

		existing, found, err := store.GetFile(ctx, repositoryName, relativePath)
		if err != nil {
			return err
		}
		if found && existing.Hash == hash {
			stats.FilesSkipped++
			return nil
		}

		parsed, err := parser.Parse(relativePath, content)
		if err != nil {
			return err
		}
		for index := range parsed {
			parsed[index].RepositoryName = repositoryName
			parsed[index].FilePath = relativePath
		}

		if err := store.DeleteSymbolsForFile(ctx, repositoryName, relativePath); err != nil {
			return err
		}
		if err := store.UpsertSymbols(ctx, parsed); err != nil {
			return err
		}
		if err := store.UpsertFile(ctx, FileRecord{
			RepositoryName: repositoryName,
			Path:           relativePath,
			Hash:           hash,
			LastModified:   fileModTime(info),
		}); err != nil {
			return err
		}

		stats.FilesParsed++
		stats.SymbolsStored += len(parsed)
		return nil
	})
	if err != nil {
		return ScanStats{}, fmt.Errorf("scan repository: %w", err)
	}

	return stats, nil
}

func shouldSkipPath(path string, isDir bool) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if normalized == "" {
		return true
	}

	for _, segment := range strings.Split(normalized, "/") {
		switch strings.ToLower(segment) {
		case ".git", "node_modules", ".tendrilignore", "venv", ".venv", "vendor", "dist", "build", "__pycache__":
			return true
		}
	}

	if !isDir && strings.EqualFold(filepath.Base(normalized), ".tendrilignore") {
		return true
	}
	return false
}

func parserForPath(path string, parsers []Parser) Parser {
	for _, parser := range parsers {
		if parser.Supports(path) {
			return parser
		}
	}
	return nil
}

func hashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func fileModTime(info fs.FileInfo) time.Time {
	if info == nil {
		return time.Now().UTC()
	}
	return info.ModTime().UTC()
}

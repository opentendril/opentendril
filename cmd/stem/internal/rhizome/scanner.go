package rhizome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ScanStats struct {
	FilesParsed   int
	FilesSkipped  int
	FilesFailed   int
	FilesPurged   int
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
	seen := make(map[string]bool)
	err = filepath.WalkDir(absoluteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// One unreadable directory must not wither the whole map. Rootless
			// Docker's containerd overlay workdirs are mode-restricted from
			// outside the daemon's user namespace; hitting one used to fail
			// the scan before any source file was parsed.
			if path == absoluteRoot {
				return walkErr
			}
			if isScanPermissionDenied(walkErr) {
				if entry == nil || entry.IsDir() {
					return filepath.SkipDir
				}
				stats.FilesFailed++
				return nil
			}
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

		seen[relativePath] = true

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
			log.Printf("rhizome: skipping %s: parse error: %v", relativePath, err)
			stats.FilesFailed++
			return nil
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

	recordedPaths, err := store.ListFilePaths(ctx, repositoryName)
	if err != nil {
		return ScanStats{}, fmt.Errorf("list recorded paths for purge: %w", err)
	}
	for _, path := range recordedPaths {
		if !seen[path] {
			if err := store.DeleteFile(ctx, repositoryName, path); err != nil {
				return ScanStats{}, fmt.Errorf("purge deleted file %s: %w", path, err)
			}
			stats.FilesPurged++
		}
	}

	return stats, nil
}

func shouldSkipPath(path string, isDir bool) bool {
	normalized := filepath.ToSlash(strings.TrimSpace(path))
	if normalized == "" {
		return true
	}

	segments := strings.Split(normalized, "/")
	if isContainerRuntimePath(segments) {
		return true
	}
	for _, segment := range segments {
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

// isContainerRuntimePath reports whether a substrate-relative path is Docker
// or containerd storage rather than repository source. A governed Stem home
// sits next to the rootless daemon's data-root; walking into it is how a
// repo-map scan used to fail on overlay workdirs the Stem user cannot read.
func isContainerRuntimePath(segments []string) bool {
	joined := strings.ToLower(strings.Join(segments, "/"))
	if strings.Contains(joined, ".local/share/docker") {
		return true
	}
	if strings.Contains(joined, "io.containerd.snapshotter") {
		return true
	}
	var hasContainerd, hasSnapshots bool
	for _, segment := range segments {
		switch strings.ToLower(segment) {
		case "containerd":
			hasContainerd = true
		case "snapshots":
			hasSnapshots = true
		case "overlay2":
			return true
		}
	}
	return hasContainerd && hasSnapshots
}

func isScanPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) || errors.Is(err, os.ErrPermission) {
		return true
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return os.IsPermission(pathErr.Err) || errors.Is(pathErr.Err, os.ErrPermission)
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

package orchestrator

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FindPlasmidSource locates a plasmid markdown file by name.
func FindPlasmidSource(root, name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("missing plasmid name")
	}

	if filepath.IsAbs(trimmed) {
		if info, err := os.Stat(trimmed); err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(trimmed), ".md") {
			return trimmed, nil
		}
	}

	directCandidates := []string{
		filepath.Join(root, trimmed),
		filepath.Join(root, ".tendril", "genotypes", trimmed),
		filepath.Join(root, ".tendril", "genotypes", "plasmids", trimmed),
	}
	if filepath.Ext(trimmed) == "" {
		directCandidates = append(directCandidates,
			filepath.Join(root, trimmed+".md"),
			filepath.Join(root, ".tendril", "genotypes", trimmed+".md"),
			filepath.Join(root, ".tendril", "genotypes", "plasmids", trimmed+".md"),
		)
	}

	for _, candidate := range directCandidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(candidate), ".md") {
			return candidate, nil
		}
	}

	searchRoots := []string{
		filepath.Join(root, ".tendril", "genotypes", "plasmids"),
		filepath.Join(root, ".tendril", "genotypes"),
	}
	var matches []string
	targetBase := strings.TrimSuffix(filepath.Base(trimmed), filepath.Ext(trimmed))

	for _, searchRoot := range searchRoots {
		if info, err := os.Stat(searchRoot); err != nil || !info.IsDir() {
			continue
		}

		_ = filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
				return nil
			}

			base := strings.TrimSuffix(d.Name(), filepath.Ext(d.Name()))
			rel, relErr := filepath.Rel(searchRoot, path)
			if relErr != nil {
				rel = path
			}

			if strings.EqualFold(d.Name(), filepath.Base(trimmed)) ||
				strings.EqualFold(base, targetBase) ||
				strings.EqualFold(rel, trimmed) ||
				strings.EqualFold(strings.TrimSuffix(rel, filepath.Ext(rel)), strings.TrimSuffix(trimmed, filepath.Ext(trimmed))) {
				matches = append(matches, path)
			}

			return nil
		})
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("plasmid %q not found in .tendril/genotypes", trimmed)
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("plasmid %q is ambiguous; matches: %s", trimmed, strings.Join(matches, ", "))
	}
}

// InjectPlasmidIntoGenome copies a plasmid from its source into the active genome directory.
func InjectPlasmidIntoGenome(root, name string) (string, string, bool, error) {
	sourcePath, err := FindPlasmidSource(root, name)
	if err != nil {
		return "", "", false, err
	}

	destDir := filepath.Join(root, ".tendril", "genome")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", "", false, fmt.Errorf("create genome directory: %w", err)
	}

	destPath := filepath.Join(destDir, filepath.Base(sourcePath))
	if samePath(sourcePath, destPath) {
		return sourcePath, destPath, true, nil
	}

	if err := CopyMarkdownFile(sourcePath, destPath); err != nil {
		return "", "", false, err
	}

	return sourcePath, destPath, false, nil
}

// CopyMarkdownFile copies a file from src to dst.
func CopyMarkdownFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat plasmid source: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("plasmid source is a directory: %s", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create plasmid destination directory: %w", err)
	}
	_ = os.Remove(dst)

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open plasmid source: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create plasmid destination: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy plasmid: %w", err)
	}

	return nil
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}

	aAbs, err := filepath.Abs(a)
	if err != nil {
		aAbs = filepath.Clean(a)
	}
	bAbs, err := filepath.Abs(b)
	if err != nil {
		bAbs = filepath.Clean(b)
	}

	return filepath.Clean(aAbs) == filepath.Clean(bAbs)
}

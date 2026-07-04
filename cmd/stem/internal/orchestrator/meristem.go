package orchestrator

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/opentendril/core/data/genotypes"
)

type genotypeDefinition struct {
	Name                     string   `json:"name"`
	System                   bool     `json:"system,omitempty"`
	Instructions             string   `json:"instructions"`
	Plasmids                 []string `json:"plasmids,omitempty"`
	DenyPlasmids             []string `json:"denyPlasmids,omitempty"`
	RequirePlasmidSignatures bool     `json:"requirePlasmidSignatures,omitempty"`
}

// EnsureBuiltinGenotypes creates missing built-in genotypes from embedded JSON files.
func EnsureBuiltinGenotypes(root string) error {
	root = repoRoot(root)
	genotypeDir := filepath.Join(root, ".tendril", "genotypes")

	return fs.WalkDir(genotypes.FS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			return nil
		}

		genotypePath := filepath.Join(genotypeDir, filepath.Base(path))
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))

		if info, err := os.Stat(genotypePath); err == nil {
			if info.IsDir() {
				return fmt.Errorf("%s genotype path is a directory: %s", name, genotypePath)
			}
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s genotype: %w", name, err)
		}

		if err := os.MkdirAll(filepath.Dir(genotypePath), 0o755); err != nil {
			return fmt.Errorf("create %s genotype directory: %w", name, err)
		}

		payload, err := genotypes.FS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s genotype: %w", name, err)
		}

		if err := os.WriteFile(genotypePath, payload, 0o644); err != nil {
			return fmt.Errorf("write %s genotype: %w", name, err)
		}

		return nil
	})
}

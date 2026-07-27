package receptors

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"gopkg.in/yaml.v3"
)

type genotypeIndex struct {
	Genotypes []genotypeIndexEntry `yaml:"genotypes"`
}

type genotypeIndexEntry struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
}

type genotypeMetadata struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Instructions string   `json:"instructions"`
	Plasmids     []string `json:"plasmids,omitempty"`
	DenyPlasmids []string `json:"denyPlasmids,omitempty"`
}

func SyncGenotypeIndex() error {
	root := resolveRepoRoot("")
	index, err := collectGenotypeIndex(root)
	if writeErr := writeGenotypeIndex(root, index); writeErr != nil {
		return writeErr
	}
	return err
}

func collectGenotypeIndex(root string) (genotypeIndex, error) {
	var searchDirs []string

	genotypeDirs, _ := conductor.DefinitionSearchPath(root, conductor.DefinitionKindGenotypes)
	searchDirs = append(searchDirs, genotypeDirs...)

	genotypeMap := make(map[string]genotypeIndexEntry)
	var errs []error

	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("read genotypes directory %s: %w", dir, err))
			}
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				continue
			}

			filePath := filepath.Join(dir, entry.Name())
			content, err := os.ReadFile(filePath)
			if err != nil {
				errs = append(errs, fmt.Errorf("read genotype %s: %w", filePath, err))
				continue
			}

			var genotype genotypeMetadata
			if err := json.Unmarshal(content, &genotype); err != nil {
				errs = append(errs, fmt.Errorf("decode genotype %s: %w", filePath, err))
				continue
			}

			name := strings.TrimSpace(genotype.Name)
			if name == "" {
				name = strings.TrimSuffix(entry.Name(), ".json")
			}

			if _, exists := genotypeMap[name]; exists {
				continue
			}

			description := strings.TrimSpace(genotype.Description)
			if description == "" {
				description = firstNWords(genotype.Instructions, 20)
			}

			genotypeMap[name] = genotypeIndexEntry{
				Name:        name,
				Description: description,
			}
		}
	}

	index := genotypeIndex{Genotypes: make([]genotypeIndexEntry, 0, len(genotypeMap))}
	for _, entry := range genotypeMap {
		index.Genotypes = append(index.Genotypes, entry)
	}

	sort.Slice(index.Genotypes, func(i, j int) bool {
		return strings.ToLower(index.Genotypes[i].Name) < strings.ToLower(index.Genotypes[j].Name)
	})

	return index, errors.Join(errs...)
}

func loadGenotypeIndex(root string) (genotypeIndex, error) {
	indexPath := filepath.Join(root, ".tendril", "genotypes", "index.yaml")
	content, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return genotypeIndex{Genotypes: []genotypeIndexEntry{}}, nil
		}
		return genotypeIndex{}, fmt.Errorf("read genotype index: %w", err)
	}

	var index genotypeIndex
	if err := yaml.Unmarshal(content, &index); err != nil {
		return genotypeIndex{}, fmt.Errorf("decode genotype index: %w", err)
	}

	if index.Genotypes == nil {
		index.Genotypes = []genotypeIndexEntry{}
	}

	return index, nil
}

func writeGenotypeIndex(root string, index genotypeIndex) error {
	genotypesDir := filepath.Join(root, ".tendril", "genotypes")
	if err := os.MkdirAll(genotypesDir, 0o755); err != nil {
		return fmt.Errorf("create genotypes directory: %w", err)
	}

	content, err := yaml.Marshal(index)
	if err != nil {
		return fmt.Errorf("encode genotype index: %w", err)
	}

	indexPath := filepath.Join(genotypesDir, "index.yaml")
	if err := os.WriteFile(indexPath, content, 0o644); err != nil {
		return fmt.Errorf("write genotype index: %w", err)
	}

	return nil
}

func firstNWords(text string, limit int) string {
	if limit <= 0 {
		return ""
	}

	words := strings.Fields(strings.TrimSpace(text))
	if len(words) <= limit {
		return strings.Join(words, " ")
	}

	return strings.Join(words[:limit], " ")
}

func resolveRepoRoot(path string) string {
	if strings.TrimSpace(path) == "" {
		wd, err := os.Getwd()
		if err != nil {
			path = "."
		} else {
			path = wd
		}
	}

	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return path
	}

	root := strings.TrimSpace(string(output))
	if root == "" {
		return path
	}

	return root
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

func mustRel(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return target
	}
	return rel
}

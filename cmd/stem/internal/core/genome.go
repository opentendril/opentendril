package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The genome capability family. Reading the genome is
// pure filesystem work and lives here directly; reduce/evolve are *execution*
// operations owned by the orchestrator, which the Core is structurally
// forbidden from importing (see boundary_test.go). They are therefore injected
// as transport-free function ports via WithGenome — the Core stays invokable
// with zero HTTP/CLI/MCP types in scope, and zero execution internals linked.

// GenomeSeed is one Markdown seed file inside .tendril/genome/.
type GenomeSeed struct {
	// Path is the seed's path relative to the workspace root, slash-separated.
	Path string `json:"path"`
	// Content is the seed's raw Markdown content.
	Content string `json:"content"`
}

// GenomeOperations is the injection port for genome operations whose implementation
// lives outside the Core (the orchestrator's Epigenetic Chronicler). Root is
// the workspace root the genome lives under (defaults to "."). Reduce and
// Evolve may be nil, in which case the corresponding capabilities report that
// they are not wired rather than acting.
type GenomeOperations struct {
	Root   string
	Reduce func(ctx context.Context, root string) error
	Evolve func(ctx context.Context, root string) error
}

// WithGenome wires the genome operation port onto the Service and returns the
// Service for chaining.
func (s *Service) WithGenome(operations GenomeOperations) *Service {
	s.genome = operations
	return s
}

func (s *Service) genomeRoot() string {
	root := strings.TrimSpace(s.genome.Root)
	if root == "" {
		return "."
	}
	return root
}

// EpigeneticsPath returns the canonical location of the consolidated genome
// file under the Service's workspace root.
func (s *Service) epigeneticsPath() string {
	return filepath.Join(s.genomeRoot(), ".tendril", "genome", "epigenetics.md")
}

// GenomeView returns every Markdown seed in .tendril/genome/, sorted by path.
// A missing genome directory is an empty genome, not an error.
func (s *Service) GenomeView(_ context.Context) ([]GenomeSeed, error) {
	root := s.genomeRoot()
	genomeDir := filepath.Join(root, ".tendril", "genome")

	entries, err := os.ReadDir(genomeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read genome directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		files = append(files, filepath.Join(genomeDir, entry.Name()))
	}
	sort.Strings(files)

	seeds := make([]GenomeSeed, 0, len(files))
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read genome file %s: %w", path, err)
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			relPath = path
		}
		seeds = append(seeds, GenomeSeed{
			Path:    filepath.ToSlash(relPath),
			Content: string(content),
		})
	}
	return seeds, nil
}

// GenomeReduce consolidates .tendril/genome/epigenetics.md in place via the
// injected execution port and returns the file's path.
func (s *Service) GenomeReduce(ctx context.Context) (string, error) {
	if s.genome.Reduce == nil {
		return "", fmt.Errorf("genome.reduce is not wired: construct the Core with WithGenome(GenomeOperations{Reduce: …})")
	}
	if err := s.genome.Reduce(ctx, s.genomeRoot()); err != nil {
		return "", err
	}
	return s.epigeneticsPath(), nil
}

// GenomeEvolve prunes low-fitness genome material and rewrites epigenetics.md
// via the injected execution port, returning the file's path.
func (s *Service) GenomeEvolve(ctx context.Context) (string, error) {
	if s.genome.Evolve == nil {
		return "", fmt.Errorf("genome.evolve is not wired: construct the Core with WithGenome(GenomeOperations{Evolve: …})")
	}
	if err := s.genome.Evolve(ctx, s.genomeRoot()); err != nil {
		return "", err
	}
	return s.epigeneticsPath(), nil
}

// genomeCapabilities declares the genome family's registry entries, bound to
// this Service's typed methods — identical in shape to the session family in
// registry.go.
func (s *Service) genomeCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapGenomeView,
			Description: "Return every Markdown genome seed in .tendril/genome/, sorted by path.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return s.GenomeView(ctx)
			},
		},
		{
			Name:        CapGenomeReduce,
			Description: "Deduplicate, compress, and merge the epigenetic rules in .tendril/genome/epigenetics.md in place.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				path, err := s.GenomeReduce(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{"path": path, "reduced": true}, nil
			},
		},
		{
			Name:        CapGenomeEvolve,
			Description: "Prune low-fitness genome material and rewrite .tendril/genome/epigenetics.md.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				path, err := s.GenomeEvolve(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{"path": path, "evolved": true}, nil
			},
		},
	}
}

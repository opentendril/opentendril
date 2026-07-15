package core

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// The plasmid capability family. Listing plasmids is
// pure filesystem work and lives here directly; injection copies a plasmid
// into the active genome via the conductor, which the Core is structurally
// forbidden from importing (see boundary_test.go). Injection is therefore an
// injected transport-free function port via WithPlasmid — identical in shape
// to the GenomeOps port that proved this template (PR).
//
// plasmid.sign is deliberately NOT governed: it signs arbitrary files with
// the node's private signing key, and projecting it onto the REST/MCP
// surfaces would hand network callers a signing authority the CLI-local
// operation never had (security posture). It remains a CLI-only,
// ungoverned command.

// PlasmidInjection describes the outcome of copying one plasmid into the
// active genome. Source and Dest are workspace-root-relative, slash-separated
// paths.
type PlasmidInjection struct {
	// Source is the plasmid Markdown file the injection copied from.
	Source string `json:"source"`
	// Dest is the genome path the plasmid now lives at.
	Dest string `json:"dest"`
	// AlreadyActive is true when the plasmid was already in the genome and no
	// copy was performed.
	AlreadyActive bool `json:"alreadyActive"`
}

// PlasmidInjectInput identifies the plasmid to inject into the active genome.
type PlasmidInjectInput struct {
	Name string `json:"name"`
}

// PlasmidOps is the injection port for plasmid operations whose
// implementation lives outside the Core (the conductor's plasmid machinery).
// Root is the workspace root plasmids live under (defaults to "."). Inject
// may be nil, in which case the capability reports that it is not wired
// rather than acting.
type PlasmidOps struct {
	Root string
	// Inject copies the named plasmid into root's active genome and returns
	// the source/destination paths (absolute or root-relative — the Core
	// normalizes them) plus whether it was already active.
	Inject func(ctx context.Context, root, name string) (PlasmidInjection, error)
}

// WithPlasmid wires the plasmid operation port onto the Service and returns
// the Service for chaining.
func (s *Service) WithPlasmid(ops PlasmidOps) *Service {
	s.plasmid = ops
	return s
}

func (s *Service) plasmidRoot() string {
	root := strings.TrimSpace(s.plasmid.Root)
	if root == "" {
		return "."
	}
	return root
}

// PlasmidList returns the workspace's available plasmid Markdown files as
// root-relative, slash-separated paths, sorted. Plasmids canonically live in
// .tendril/genotypes/plasmids/; when that directory holds none, the wider
// .tendril/genotypes/ tree is scanned as a fallback (preserving the historic
// `tendril plasmid list` behavior). A missing directory is an empty list, not
// an error.
func (s *Service) PlasmidList(_ context.Context) ([]string, error) {
	root := s.plasmidRoot()
	plasmidRoot := filepath.Join(root, ".tendril", "genotypes", "plasmids")
	genotypeRoot := filepath.Join(root, ".tendril", "genotypes")

	files := collectMarkdownFiles(plasmidRoot)
	if len(files) == 0 {
		files = collectMarkdownFiles(genotypeRoot)
	}

	paths := make([]string, 0, len(files))
	for _, path := range files {
		paths = append(paths, relToRoot(root, path))
	}
	sort.Strings(paths)
	return paths, nil
}

// PlasmidInject copies the named plasmid into the active genome via the
// injected execution port, returning the normalized injection outcome.
func (s *Service) PlasmidInject(ctx context.Context, in PlasmidInjectInput) (PlasmidInjection, error) {
	if s.plasmid.Inject == nil {
		return PlasmidInjection{}, fmt.Errorf("plasmid.inject is not wired: construct the Core with WithPlasmid(PlasmidOps{Inject: …})")
	}
	if strings.TrimSpace(in.Name) == "" {
		return PlasmidInjection{}, fmt.Errorf("plasmid name is required")
	}

	root := s.plasmidRoot()
	result, err := s.plasmid.Inject(ctx, root, in.Name)
	if err != nil {
		return PlasmidInjection{}, err
	}
	result.Source = relToRoot(root, result.Source)
	result.Dest = relToRoot(root, result.Dest)
	return result, nil
}

// collectMarkdownFiles walks a directory tree collecting every Markdown file.
// A missing or unreadable root yields an empty slice.
func collectMarkdownFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files
}

// relToRoot renders a path relative to the workspace root, slash-separated,
// falling back to the input when it cannot be relativized.
func relToRoot(root, path string) string {
	if strings.TrimSpace(path) == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// plasmidCapabilities declares the plasmid family's registry entries, bound
// to this Service's typed methods — identical in shape to the session and
// genome families.
func (s *Service) plasmidCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapPlasmidList,
			Description: "List the available plasmid Markdown files under .tendril/genotypes/, sorted by path.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return s.PlasmidList(ctx)
			},
		},
		{
			Name:        CapPlasmidInject,
			Description: "Inject a modular plasmid rule file (e.g. go-rules, react-style) into the active project genome.",
			InputSchema: schemaObject(map[string]any{
				"name": stringProp("The plasmid name to inject into the active genome."),
			}, []string{"name"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in PlasmidInjectInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.PlasmidInject(ctx, in)
			},
		},
	}
}

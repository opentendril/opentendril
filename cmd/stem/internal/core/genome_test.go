package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func newGenomeService(t *testing.T, operations core.GenomeOperations) *core.Service {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return core.NewService(manager).WithGenome(operations)
}

func writeGenomeSeed(t *testing.T, root, name, content string) {
	t.Helper()
	genomeDir := filepath.Join(root, ".tendril", "genome")
	if err := os.MkdirAll(genomeDir, 0o755); err != nil {
		t.Fatalf("mkdir genome dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(genomeDir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write seed %s: %v", name, err)
	}
}

func TestGenomeViewReadsSeedsSorted(t *testing.T) {
	root := t.TempDir()
	writeGenomeSeed(t, root, "zeta.md", "# zeta rules")
	writeGenomeSeed(t, root, "alpha.md", "# alpha rules")
	writeGenomeSeed(t, root, "notes.txt", "not markdown, must be ignored")

	svc := newGenomeService(t, core.GenomeOperations{Root: root})
	seeds, err := svc.GenomeView(context.Background())
	if err != nil {
		t.Fatalf("GenomeView: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("seeds = %d, want 2 (non-markdown ignored)", len(seeds))
	}
	if seeds[0].Path != ".tendril/genome/alpha.md" || seeds[1].Path != ".tendril/genome/zeta.md" {
		t.Fatalf("seeds not sorted by path: %q, %q", seeds[0].Path, seeds[1].Path)
	}
	if seeds[0].Content != "# alpha rules" {
		t.Fatalf("seed content = %q", seeds[0].Content)
	}
}

func TestGenomeViewMissingDirIsEmptyGenome(t *testing.T) {
	svc := newGenomeService(t, core.GenomeOperations{Root: t.TempDir()})
	seeds, err := svc.GenomeView(context.Background())
	if err != nil {
		t.Fatalf("GenomeView on missing dir: %v", err)
	}
	if len(seeds) != 0 {
		t.Fatalf("seeds = %d, want 0", len(seeds))
	}
}

func TestGenomeReduceRunsInjectedPortWithRoot(t *testing.T) {
	root := t.TempDir()
	var gotRoot string
	svc := newGenomeService(t, core.GenomeOperations{
		Root:   root,
		Reduce: func(_ context.Context, r string) error { gotRoot = r; return nil },
	})

	path, err := svc.GenomeReduce(context.Background())
	if err != nil {
		t.Fatalf("GenomeReduce: %v", err)
	}
	if gotRoot != root {
		t.Fatalf("reduce port received root %q, want %q", gotRoot, root)
	}
	if path != filepath.Join(root, ".tendril", "genome", "epigenetics.md") {
		t.Fatalf("epigenetics path = %q", path)
	}
}

func TestGenomeReduceUnwiredFailsLoudly(t *testing.T) {
	svc := newGenomeService(t, core.GenomeOperations{Root: t.TempDir()})
	if _, err := svc.GenomeReduce(context.Background()); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("expected loud not-wired error, got %v", err)
	}
	if _, err := svc.GenomeEvolve(context.Background()); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("expected loud not-wired error for evolve, got %v", err)
	}
}

func TestGenomeCapabilitiesInRegistry(t *testing.T) {
	svc := newGenomeService(t, core.GenomeOperations{Root: t.TempDir()})

	declared := map[string]bool{}
	for _, capability := range svc.Capabilities() {
		declared[capability.Name] = true
	}
	for _, name := range []string{core.CapGenomeView, core.CapGenomeReduce, core.CapGenomeEvolve} {
		if !declared[name] {
			t.Errorf("registry does not declare %s", name)
		}
	}

	// Invoke path (the projection MCP/CLI use) works for the genome family.
	result, err := svc.Invoke(context.Background(), core.CapGenomeView, map[string]any{})
	if err != nil {
		t.Fatalf("Invoke(genome.view): %v", err)
	}
	if _, ok := result.([]core.GenomeSeed); !ok {
		t.Fatalf("Invoke(genome.view) = %T, want []core.GenomeSeed", result)
	}
}

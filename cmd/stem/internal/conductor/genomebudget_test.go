package conductor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGenomeFile(t *testing.T, workspace string, name string, content string) {
	t.Helper()
	genomeDir := filepath.Join(workspace, ".tendril", "genome")
	if err := os.MkdirAll(genomeDir, 0o755); err != nil {
		t.Fatalf("mkdir genome dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(genomeDir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write genome file %s: %v", name, err)
	}
}

func TestLoadGenomeContextSmallFilesPassThroughComplete(t *testing.T) {
	workspace := t.TempDir()
	writeGenomeFile(t, workspace, "naming-conventions.md", "# Naming\nUse full words.")
	writeGenomeFile(t, workspace, "providers.md", "# Providers\nLocal first.")

	context, err := loadGenomeContext(workspace)
	if err != nil {
		t.Fatalf("loadGenomeContext: %v", err)
	}
	if !strings.Contains(context, "Use full words.") || !strings.Contains(context, "Local first.") {
		t.Fatalf("small curated files must be included whole, got: %s", context)
	}
	if strings.Contains(context, "[truncated") || strings.Contains(context, "omitted for size") {
		t.Fatalf("small files must not be truncated or omitted, got: %s", context)
	}
}

func TestLoadGenomeContextNeverInlinesGeneratedMaps(t *testing.T) {
	workspace := t.TempDir()
	writeGenomeFile(t, workspace, "taxonomy.md", "# Taxonomy\nCanonical vocabulary.")
	oversized := strings.Repeat("- symbol line\n", 40000)
	writeGenomeFile(t, workspace, "repomap.md", oversized)
	writeGenomeFile(t, workspace, "memorymap.md", "# Memory\nsome map")

	context, err := loadGenomeContext(workspace)
	if err != nil {
		t.Fatalf("loadGenomeContext: %v", err)
	}
	if len(context) > genomeTotalByteBudget+1024 {
		t.Fatalf("genome context exceeds budget: %d bytes", len(context))
	}
	if !strings.Contains(context, "Canonical vocabulary.") {
		t.Fatalf("curated file must survive intact, got: %s", context)
	}
	if strings.Contains(context, "symbol line") || strings.Contains(context, "some map") {
		t.Fatalf("generated maps must never be inlined, got: %s", context)
	}
	if !strings.Contains(context, ".tendril/genome/repomap.md") || !strings.Contains(context, ".tendril/genome/memorymap.md") {
		t.Fatalf("generated maps must be named with their on-disk path, got: %s", context)
	}
}

func TestLoadGenomeContextTruncatesOversizedCuratedFile(t *testing.T) {
	workspace := t.TempDir()
	oversized := "# Learnings\n" + strings.Repeat("- lesson learned from a run\n", 1000)
	writeGenomeFile(t, workspace, "epigenetics.md", oversized)

	context, err := loadGenomeContext(workspace)
	if err != nil {
		t.Fatalf("loadGenomeContext: %v", err)
	}
	if len(context) > genomePerFileByteBudget+1024 {
		t.Fatalf("oversized curated file must be truncated, got %d bytes", len(context))
	}
	if !strings.Contains(context, "[truncated — read .tendril/genome/epigenetics.md") {
		t.Fatalf("truncation must point at the on-disk file, got tail: %s", context[len(context)-200:])
	}
}

func TestLoadGenomeContextNamesFilesPastTotalBudget(t *testing.T) {
	workspace := t.TempDir()
	big := strings.Repeat("x\n", genomePerFileByteBudget)
	writeGenomeFile(t, workspace, "a.md", big)
	writeGenomeFile(t, workspace, "b.md", big)
	writeGenomeFile(t, workspace, "c.md", "small tail file")

	context, err := loadGenomeContext(workspace)
	if err != nil {
		t.Fatalf("loadGenomeContext: %v", err)
	}
	if len(context) > genomeTotalByteBudget+1024 {
		t.Fatalf("genome context exceeds budget: %d bytes", len(context))
	}
	if !strings.Contains(context, "Additional genome files on disk") || !strings.Contains(context, ".tendril/genome/c.md") {
		t.Fatalf("files past the total budget must be named with their path, got tail: %s", context[len(context)-300:])
	}
	if strings.Contains(context, "small tail file") {
		t.Fatalf("files past the total budget must not be inlined, got: %s", context)
	}
}

func TestTruncateGenomeContentCutsOnLineBoundary(t *testing.T) {
	content := "first line\nsecond line\nthird line"
	got := truncateGenomeContent("epigenetics.md", content, 15)
	if !strings.HasPrefix(got, "first line\n") {
		t.Fatalf("truncation must keep whole lines, got: %q", got)
	}
	if strings.Contains(got, "second") {
		t.Fatalf("truncation must not keep partial lines past the budget, got: %q", got)
	}
	if !strings.Contains(got, ".tendril/genome/epigenetics.md") {
		t.Fatalf("marker must name the on-disk file, got: %q", got)
	}
}

func TestIsGeneratedGenomeFile(t *testing.T) {
	for _, name := range []string{"repomap.md", "memorymap.md", "Repomap.md"} {
		if !isGeneratedGenomeFile(name) {
			t.Fatalf("%s must be classified as generated", name)
		}
	}
	for _, name := range []string{"taxonomy-canonical.md", "epigenetics.md", "README.md"} {
		if isGeneratedGenomeFile(name) {
			t.Fatalf("%s must not be classified as generated", name)
		}
	}
}

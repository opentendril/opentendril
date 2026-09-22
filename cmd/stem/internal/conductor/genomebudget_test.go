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
	if !strings.Contains(context, ".tendril/genome/repomap.md") {
		t.Fatalf("generated maps must be named with their on-disk path, got: %s", context)
	}
	if strings.Contains(context, "memorymap.md") {
		t.Fatalf("quarantined map must not be advertised, got: %s", context)
	}
}

func TestLoadGenomeContextTruncatesOversizedCuratedFile(t *testing.T) {
	workspace := t.TempDir()
	oversized := "# Learnings\n" + strings.Repeat("- lesson learned from a run\n", 1000)
	writeGenomeFile(t, workspace, "curated-oversized.md", oversized)

	context, err := loadGenomeContext(workspace)
	if err != nil {
		t.Fatalf("loadGenomeContext: %v", err)
	}
	if len(context) > genomePerFileByteBudget+1024 {
		t.Fatalf("oversized curated file must be truncated, got %d bytes", len(context))
	}
	if !strings.Contains(context, "[truncated; read .tendril/genome/curated-oversized.md") {
		tailStart := len(context) - 200
		if tailStart < 0 {
			tailStart = 0
		}
		t.Fatalf("truncation must point at the on-disk file, got tail: %s", context[tailStart:])
	}
}

func TestLoadGenomeContextQuarantinesUnclassifiedMaterial(t *testing.T) {
	workspace := t.TempDir()
	writeGenomeFile(t, workspace, "epigenetics.md", "learned material")
	writeGenomeFile(t, workspace, "memorymap.md", "memory map")
	writeGenomeFile(t, workspace, "repomap.md", "repo map")

	context, err := loadGenomeContext(workspace)
	if err != nil {
		t.Fatalf("loadGenomeContext: %v", err)
	}

	if strings.Contains(context, "epigenetics.md") || strings.Contains(context, "learned material") {
		t.Fatalf("epigenetics.md must be quarantined entirely, got: %s", context)
	}
	if strings.Contains(context, "memorymap.md") || strings.Contains(context, "memory map") {
		t.Fatalf("memorymap.md must be quarantined entirely, got: %s", context)
	}
	if !strings.Contains(context, ".tendril/genome/repomap.md") {
		t.Fatalf("repomap.md must still be advertised, got: %s", context)
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
	got := truncateGenomeContent("curated-oversized.md", content, 15)
	if !strings.HasPrefix(got, "first line\n") {
		t.Fatalf("truncation must keep whole lines, got: %q", got)
	}
	if strings.Contains(got, "second") {
		t.Fatalf("truncation must not keep partial lines past the budget, got: %q", got)
	}
	if !strings.Contains(got, ".tendril/genome/curated-oversized.md") {
		t.Fatalf("marker must name the on-disk file, got: %q", got)
	}
}

func TestIsGeneratedGenomeFile(t *testing.T) {
	for _, name := range []string{"repomap.md", "Repomap.md"} {
		if !isGeneratedGenomeFile(name) {
			t.Fatalf("%s must be classified as generated", name)
		}
	}
	for _, name := range []string{"taxonomy-canonical.md", "README.md"} {
		if isGeneratedGenomeFile(name) {
			t.Fatalf("%s must not be classified as generated", name)
		}
	}
}

func TestLoadGenomeContextUsesRemainingTaskContextBudget(t *testing.T) {
	workspace := t.TempDir()
	writeGenomeFile(t, workspace, "curated.md", strings.Repeat("curated line\n", 1000))
	taskContext := strings.Repeat("task evidence\n", 200)
	remaining := genomeTotalByteBudget - len(taskContext)
	if remaining <= 0 {
		t.Fatalf("fixture task context unexpectedly consumes the full envelope")
	}

	withTask, err := loadGenomeContext(workspace, remaining)
	if err != nil {
		t.Fatalf("loadGenomeContext with task budget: %v", err)
	}
	if len(taskContext)+len(withTask) > genomeTotalByteBudget {
		t.Fatalf("task context and curated genome exceed shared envelope: task=%d genome=%d", len(taskContext), len(withTask))
	}

	withoutTask, err := loadGenomeContext(workspace)
	if err != nil {
		t.Fatalf("loadGenomeContext without task budget: %v", err)
	}
	if len(withoutTask) <= len(withTask) {
		t.Fatalf("no-context case did not retain the full genome allowance: without=%d with=%d", len(withoutTask), len(withTask))
	}
}

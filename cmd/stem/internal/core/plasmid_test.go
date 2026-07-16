package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

func newPlasmidService(t *testing.T, operations core.PlasmidOperations) *core.Service {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return core.NewService(manager).WithPlasmid(operations)
}

func writePlasmidFile(t *testing.T, root string, parts ...string) {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("# plasmid"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestPlasmidListPrefersPlasmidsDirSorted(t *testing.T) {
	root := t.TempDir()
	writePlasmidFile(t, root, ".tendril", "genotypes", "plasmids", "zeta-rules.md")
	writePlasmidFile(t, root, ".tendril", "genotypes", "plasmids", "alpha-rules.md")
	writePlasmidFile(t, root, ".tendril", "genotypes", "plasmids", "notes.txt")
	// A Markdown file outside plasmids/ must NOT appear once plasmids/ has any.
	writePlasmidFile(t, root, ".tendril", "genotypes", "outside.md")

	svc := newPlasmidService(t, core.PlasmidOperations{Root: root})
	paths, err := svc.PlasmidList(context.Background())
	if err != nil {
		t.Fatalf("PlasmidList: %v", err)
	}
	want := []string{
		".tendril/genotypes/plasmids/alpha-rules.md",
		".tendril/genotypes/plasmids/zeta-rules.md",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
}

func TestPlasmidListFallsBackToGenotypesTree(t *testing.T) {
	root := t.TempDir()
	writePlasmidFile(t, root, ".tendril", "genotypes", "go-rules.md")

	svc := newPlasmidService(t, core.PlasmidOperations{Root: root})
	paths, err := svc.PlasmidList(context.Background())
	if err != nil {
		t.Fatalf("PlasmidList: %v", err)
	}
	if len(paths) != 1 || paths[0] != ".tendril/genotypes/go-rules.md" {
		t.Fatalf("paths = %v, want the genotypes fallback entry", paths)
	}
}

func TestPlasmidListMissingDirIsEmpty(t *testing.T) {
	svc := newPlasmidService(t, core.PlasmidOperations{Root: t.TempDir()})
	paths, err := svc.PlasmidList(context.Background())
	if err != nil {
		t.Fatalf("PlasmidList on missing dir: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("paths = %v, want empty", paths)
	}
}

func TestPlasmidInjectRunsPortAndNormalizesPaths(t *testing.T) {
	root := t.TempDir()
	var gotRoot, gotName string
	svc := newPlasmidService(t, core.PlasmidOperations{
		Root: root,
		Inject: func(_ context.Context, r, name string) (core.PlasmidInjection, error) {
			gotRoot, gotName = r, name
			return core.PlasmidInjection{
				Source: filepath.Join(r, ".tendril", "genotypes", "plasmids", "go-rules.md"),
				Dest:   filepath.Join(r, ".tendril", "genome", "go-rules.md"),
			}, nil
		},
	})

	result, err := svc.PlasmidInject(context.Background(), core.PlasmidInjectInput{Name: "go-rules"})
	if err != nil {
		t.Fatalf("PlasmidInject: %v", err)
	}
	if gotRoot != root || gotName != "go-rules" {
		t.Fatalf("port received (%q, %q), want (%q, %q)", gotRoot, gotName, root, "go-rules")
	}
	if result.Source != ".tendril/genotypes/plasmids/go-rules.md" {
		t.Fatalf("source = %q, want root-relative slash path", result.Source)
	}
	if result.Dest != ".tendril/genome/go-rules.md" {
		t.Fatalf("dest = %q, want root-relative slash path", result.Dest)
	}
	if result.AlreadyActive {
		t.Fatal("alreadyActive = true, want false")
	}
}

func TestPlasmidInjectUnwiredFailsLoudly(t *testing.T) {
	svc := newPlasmidService(t, core.PlasmidOperations{Root: t.TempDir()})
	if _, err := svc.PlasmidInject(context.Background(), core.PlasmidInjectInput{Name: "go-rules"}); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("expected loud not-wired error, got %v", err)
	}
}

func TestPlasmidInjectRequiresName(t *testing.T) {
	svc := newPlasmidService(t, core.PlasmidOperations{
		Root: t.TempDir(),
		Inject: func(context.Context, string, string) (core.PlasmidInjection, error) {
			return core.PlasmidInjection{}, nil
		},
	})
	if _, err := svc.PlasmidInject(context.Background(), core.PlasmidInjectInput{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected name-required error, got %v", err)
	}
}

func TestPlasmidCapabilitiesInRegistry(t *testing.T) {
	svc := newPlasmidService(t, core.PlasmidOperations{Root: t.TempDir()})

	declared := map[string]bool{}
	for _, capability := range svc.Capabilities() {
		declared[capability.Name] = true
	}
	for _, name := range []string{core.CapPlasmidList, core.CapPlasmidInject} {
		if !declared[name] {
			t.Errorf("registry does not declare %s", name)
		}
	}

	// Invoke path (the projection MCP/CLI use) works for the plasmid family.
	result, err := svc.Invoke(context.Background(), core.CapPlasmidList, map[string]any{})
	if err != nil {
		t.Fatalf("Invoke(plasmid.list): %v", err)
	}
	if _, ok := result.([]string); !ok {
		t.Fatalf("Invoke(plasmid.list) = %T, want []string", result)
	}
}

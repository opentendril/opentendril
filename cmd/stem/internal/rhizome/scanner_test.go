package rhizome

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldSkipPathSkipsContainerRuntimeTrees(t *testing.T) {
	cases := []struct {
		path string
		skip bool
	}{
		{path: "cmd/stem/main.go", skip: false},
		{path: ".local/share/docker/containerd/daemon/io.containerd.snapshotter.v1.overlayfs/snapshots/10/work/work", skip: true},
		{path: "io.containerd.snapshotter.v1.overlayfs/snapshots/10/work/work", skip: true},
		{path: "var/lib/docker/overlay2/abcd/work", skip: true},
		{path: "vendor/pkg/file.go", skip: true},
		{path: "src/containerd/main.go", skip: false},
	}
	for _, tc := range cases {
		got := shouldSkipPath(tc.path, true)
		if got != tc.skip {
			t.Errorf("shouldSkipPath(%q) = %v, want %v", tc.path, got, tc.skip)
		}
	}
}

// The observed wither: WalkDir opens a containerd overlay workdir that the
// Stem user cannot read, and the scan used to return that error as a hard
// failure before any source file was parsed.
func TestScanRepositorySkipsUnreadableContainerdSnapshotLayout(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "rhizome.db")
	repoRoot := filepath.Join(tempDir, "workspace")

	sourceDir := filepath.Join(repoRoot, "src")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "visible.go"), []byte("package src\n\nfunc Visible() {}\n"), 0o644); err != nil {
		t.Fatalf("write visible.go: %v", err)
	}

	snapshotWork := filepath.Join(repoRoot, ".local", "share", "docker", "containerd", "daemon",
		"io.containerd.snapshotter.v1.overlayfs", "snapshots", "10", "work", "work")
	if err := os.MkdirAll(snapshotWork, 0o755); err != nil {
		t.Fatalf("mkdir snapshot work: %v", err)
	}
	if err := os.WriteFile(filepath.Join(snapshotWork, "hidden.go"), []byte("package hidden\n"), 0o644); err != nil {
		t.Fatalf("write hidden.go: %v", err)
	}
	if err := os.Chmod(snapshotWork, 0); err != nil {
		t.Fatalf("chmod snapshot work: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(snapshotWork, 0o755)
	})

	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	stats, err := ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{GoParser{}})
	if err != nil {
		t.Fatalf("ScanRepository returned error on the containerd snapshot layout: %v", err)
	}
	if stats.FilesParsed < 1 {
		t.Fatalf("expected to parse visible.go, got stats %+v", stats)
	}

	results, err := store.SearchSymbols(ctx, "owner/repo", "Visible", 10)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected Visible to be indexed; the scan skipped the readable source")
	}
}

func TestScanRepositorySkipsUnreadableSiblingDirectory(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "rhizome.db")
	repoRoot := filepath.Join(tempDir, "repo")

	if err := os.MkdirAll(filepath.Join(repoRoot, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "src", "ok.go"), []byte("package src\n\nfunc Ok() {}\n"), 0o644); err != nil {
		t.Fatalf("write ok.go: %v", err)
	}

	opaque := filepath.Join(repoRoot, "opaque")
	if err := os.MkdirAll(opaque, 0o755); err != nil {
		t.Fatalf("mkdir opaque: %v", err)
	}
	if err := os.Chmod(opaque, 0); err != nil {
		t.Fatalf("chmod opaque: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(opaque, 0o755)
	})

	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	stats, err := ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{GoParser{}})
	if err != nil {
		t.Fatalf("ScanRepository failed on an unreadable sibling directory: %v", err)
	}
	if stats.FilesParsed < 1 {
		t.Fatalf("expected to parse ok.go, got stats %+v", stats)
	}
}

func TestScanRepositoryStillFailsWhenRootIsUnreadable(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "rhizome.db")
	repoRoot := filepath.Join(tempDir, "sealed")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(repoRoot, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(repoRoot, 0o755)
	})
	if _, err := os.ReadDir(repoRoot); err == nil {
		t.Skip("process can read a 000-mode directory; cannot simulate an unreadable workspace root")
	}

	store := openTestStore(t, ctx, dbPath)
	defer store.Close()

	_, err := ScanRepository(ctx, repoRoot, "owner/repo", store, []Parser{GoParser{}})
	if err == nil {
		t.Fatal("ScanRepository succeeded against an unreadable workspace root")
	}
	if !strings.Contains(err.Error(), "scan repository") {
		t.Fatalf("error = %v, want it wrapped as scan repository", err)
	}
}

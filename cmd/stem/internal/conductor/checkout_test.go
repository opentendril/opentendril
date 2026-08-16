package conductor

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCheckoutPlan(t *testing.T) {
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", "/managed-root")

	cases := []struct {
		name       string
		spec       CheckoutSpec
		wantDir    string
		persistent bool
		wantErr    bool
	}{
		{name: "empty is ephemeral", spec: CheckoutSpec{}, wantDir: "", persistent: false},
		{name: "explicit ephemeral", spec: CheckoutSpec{Mode: "ephemeral"}, wantDir: "", persistent: false},
		{name: "managed under Tendril root", spec: CheckoutSpec{Mode: "managed"}, wantDir: "/managed-root/sub", persistent: true},
		{name: "path explicit", spec: CheckoutSpec{Mode: "path", Path: "/srv/checkouts/x"}, wantDir: "/srv/checkouts/x", persistent: true},
		{name: "path requires a path", spec: CheckoutSpec{Mode: "path"}, wantErr: true},
		{name: "unknown mode errors", spec: CheckoutSpec{Mode: "wormhole"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := resolveCheckoutPlan("sub", tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %+v", tc.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if plan.dir != tc.wantDir || plan.persistent != tc.persistent {
				t.Fatalf("got {dir:%q persistent:%v}, want {dir:%q persistent:%v}", plan.dir, plan.persistent, tc.wantDir, tc.persistent)
			}
		})
	}
}

func TestManagedCheckoutDirSanitizesName(t *testing.T) {
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", "/root")
	if got := managedCheckoutDir("my/weird:name"); got != filepath.Join("/root", "my-weird-name") {
		t.Fatalf("managedCheckoutDir sanitization = %q", got)
	}
}

// TestCloneNamedForeignSubstrateCheckoutModes exercises ephemeral vs managed
// checkout against a local source repo (no network).
func TestCloneNamedForeignSubstrateCheckoutModes(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		if _, err := runGitCommand(ctx, src, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	mustGit("init")
	mustGit("config", "user.email", "t@example.com")
	mustGit("config", "user.name", "Tester")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit("add", "-A")
	mustGit("commit", "-m", "init")

	t.Run("ephemeral is temporary", func(t *testing.T) {
		path, persistent, err := cloneNamedForeignSubstrate("eph", src, "", ResolvedCredential{})
		if err != nil {
			t.Fatalf("clone failed: %v", err)
		}
		defer os.RemoveAll(path)
		if persistent {
			t.Fatalf("ephemeral checkout should not be persistent")
		}
		if !strings.HasPrefix(path, os.TempDir()) {
			t.Fatalf("ephemeral path %q should be under temp dir", path)
		}
		if !isGitRepo(path) {
			t.Fatalf("expected a git repo at %q", path)
		}
	})

	t.Run("managed persists and is reused", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", root)
		cred := ResolvedCredential{Checkout: CheckoutSpec{Mode: "managed"}}

		path, persistent, err := cloneNamedForeignSubstrate("repo", src, "", cred)
		if err != nil {
			t.Fatalf("managed clone failed: %v", err)
		}
		if !persistent {
			t.Fatalf("managed checkout should be persistent")
		}
		if path != filepath.Join(root, "repo") {
			t.Fatalf("managed path = %q, want %q", path, filepath.Join(root, "repo"))
		}
		if !isGitRepo(path) {
			t.Fatalf("expected a git repo at %q", path)
		}

		// Second run reuses the same dir (refresh, not re-clone) without error.
		path2, persistent2, err := cloneNamedForeignSubstrate("repo", src, "", cred)
		if err != nil {
			t.Fatalf("managed reuse failed: %v", err)
		}
		if path2 != path || !persistent2 {
			t.Fatalf("reuse should return the same persistent path, got %q persistent=%v", path2, persistent2)
		}
	})
}

// TestRefreshRefusesToDiscardOperatorWork: a path-mode checkout is the
// operator's own working copy. Refreshing it hard-resets, which would silently
// delete uncommitted work — so it is refused, while a Tendril-owned managed
// checkout still refreshes as documented.
func TestRefreshRefusesToDiscardOperatorWork(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "ambient@example.com"},
		{"config", "user.name", "Ambient Tester"},
		{"checkout", "-b", "trunk"},
		{"commit", "--allow-empty", "-m", "initial"},
	} {
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "precious.txt"), []byte("hours of work\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Not Tendril-owned (checkout mode "path"): refused, and the file survives.
	err := refreshExistingCheckout(repo, "trunk", nil, false)
	if err == nil {
		t.Fatal("a dirty operator checkout was refreshed — this discards their uncommitted work")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %v, want it to name the uncommitted changes", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "precious.txt")); statErr != nil {
		t.Fatalf("the operator's work was destroyed despite the refusal: %v", statErr)
	}

	// A clean operator checkout is not blocked by this guard (it fails later
	// for want of a remote, which is a different, honest failure).
	if err := os.Remove(filepath.Join(repo, "precious.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := refreshExistingCheckout(repo, "trunk", nil, false); err != nil && strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("a clean checkout was refused as dirty: %v", err)
	}
}

// TestManagedCheckoutIsTendrilOwned pins the distinction the refusal depends
// on: managed directories are Tendril's, path directories are the operator's.
func TestManagedCheckoutIsTendrilOwned(t *testing.T) {
	managed, err := resolveCheckoutPlan("substrate", CheckoutSpec{Mode: "managed"})
	if err != nil {
		t.Fatalf("managed: %v", err)
	}
	if !managed.tendrilOwned {
		t.Error("managed checkout not marked Tendril-owned — it would then refuse to self-heal")
	}

	pathMode, err := resolveCheckoutPlan("substrate", CheckoutSpec{Mode: "path", Path: t.TempDir()})
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if pathMode.tendrilOwned {
		t.Error("path checkout marked Tendril-owned — a hard reset would then discard the operator's work")
	}
}

func TestRepoRootDoesNotEscapeManagedCheckoutIntoParentGitRepo(t *testing.T) {
	parent := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
		{"commit", "--allow-empty", "-q", "-m", "root"},
	} {
		if _, err := runGitCommand(ctx, parent, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	managedRoot := filepath.Join(parent, ".tendril", "substrates")
	checkout := filepath.Join(managedRoot, "demo")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatalf("mkdir checkout: %v", err)
	}
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", managedRoot)

	got := repoRoot(checkout)
	if got != checkout {
		t.Fatalf("repoRoot(managed checkout) = %q, want the checkout itself, not parent git toplevel %q", got, parent)
	}

	// The empty placeholder is not a checkout, even though git-rev-parse
	// would walk into the parent. Resolving it as ready is how chat mounted
	// an empty /app.
	_, err := ResolveSubstrateWorkspace("demo", &SubstrateSpec{Checkout: CheckoutSpec{Mode: "managed"}})
	if !errors.Is(err, ErrWorkspaceAbsent) {
		t.Fatalf("ResolveSubstrateWorkspace(empty managed under parent git) = %v, want ErrWorkspaceAbsent", err)
	}
}

func TestResolveSubstrateWorkspaceTreatsEmptyManagedDirAsAbsent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", root)
	placeholder := filepath.Join(root, "opentendril")
	if err := os.MkdirAll(placeholder, 0o755); err != nil {
		t.Fatalf("mkdir placeholder: %v", err)
	}

	spec := &SubstrateSpec{URL: "https://example.com/opentendril.git", Checkout: CheckoutSpec{Mode: "managed"}}
	_, err := ResolveSubstrateWorkspace("opentendril", spec)
	if !errors.Is(err, ErrWorkspaceAbsent) {
		t.Fatalf("empty managed dir resolved: %v, want ErrWorkspaceAbsent", err)
	}

	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Tester"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if _, err := runGitCommand(ctx, placeholder, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	got, err := ResolveSubstrateWorkspace("opentendril", spec)
	if err != nil {
		t.Fatalf("populated managed checkout: %v", err)
	}
	if got != placeholder {
		t.Fatalf("resolved path = %q, want %q", got, placeholder)
	}
}

func TestIsUnsuitableImplicitWorkspaceDetectsStemHomeLayout(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local", "share", "docker"), 0o755); err != nil {
		t.Fatalf("mkdir docker data-root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".tendril"), 0o755); err != nil {
		t.Fatalf("mkdir control plane: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".tendril", "api-key"), []byte("test-key\n"), 0o600); err != nil {
		t.Fatalf("write api-key: %v", err)
	}

	if !isUnsuitableImplicitWorkspace(home) {
		t.Fatal("Stem home with rootless Docker data-root and control plane was treated as a Substrate")
	}

	repo := t.TempDir()
	if _, err := runGitCommand(context.Background(), repo, "init", "-q"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if isUnsuitableImplicitWorkspace(repo) {
		t.Fatal("a plain git checkout was treated as the Stem working directory")
	}
}

func TestMaterializeManagedCheckoutsFailureTolerance(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		if _, err := runGitCommand(ctx, src, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	mustGit("init")
	mustGit("config", "user.email", "t@example.com")
	mustGit("config", "user.name", "Tester")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit("add", "-A")
	mustGit("commit", "-m", "init")

	root := t.TempDir()
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", root)

	config := &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"valid": {
				URL:      src,
				Checkout: CheckoutSpec{Mode: "managed"},
			},
			"broken": {
				URL:      "http://127.0.0.1:9999/invalid.git", // deliberately broken URL
				Checkout: CheckoutSpec{Mode: "managed"},
			},
		},
	}

	// This should not panic or return error, despite "broken" failing to clone.
	MaterializeManagedCheckouts(ctx, config)

	// Valid should resolve
	validSpec := config.Substrates["valid"]
	validPath, err := ResolveSubstrateWorkspace("valid", &validSpec)
	if err != nil {
		t.Errorf("valid substrate failed to resolve: %v", err)
	}
	if validPath != filepath.Join(root, "valid") {
		t.Errorf("valid path = %q", validPath)
	}

	// Broken should return ErrWorkspaceAbsent (mapped to 409 Conflict at transport)
	brokenSpec := config.Substrates["broken"]
	_, errBroken := ResolveSubstrateWorkspace("broken", &brokenSpec)
	if !errors.Is(errBroken, ErrWorkspaceAbsent) {
		t.Errorf("broken substrate got err %v, want ErrWorkspaceAbsent", errBroken)
	}
}

func TestMaterializeManagedCheckoutsLogging(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		if _, err := runGitCommand(ctx, src, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	mustGit("init")
	mustGit("config", "user.email", "t@example.com")
	mustGit("config", "user.name", "Tester")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit("add", "-A")
	mustGit("commit", "-m", "init")

	root := t.TempDir()
	t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", root)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	config0 := &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"unmanaged": {URL: src, Checkout: CheckoutSpec{Mode: "path", Path: "/tmp/foo"}},
		},
	}
	buf.Reset()
	MaterializeManagedCheckouts(ctx, config0)
	out0 := buf.String()
	if !strings.Contains(out0, "0 managed Substrates") {
		t.Errorf("expected 0 managed Substrates line, got: %s", out0)
	}
	if strings.Contains(out0, "Materializing managed Substrate") {
		t.Errorf("expected no per-Substrate lines, got: %s", out0)
	}

	config1 := &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"valid1":    {URL: src, Checkout: CheckoutSpec{Mode: "managed"}},
			"broken":    {URL: "http://127.0.0.1:9999/invalid.git", Checkout: CheckoutSpec{Mode: "managed"}},
			"unmanaged": {URL: src, Checkout: CheckoutSpec{Mode: "path", Path: "/tmp/foo"}},
		},
	}

	buf.Reset()
	MaterializeManagedCheckouts(ctx, config1)
	out1 := buf.String()

	if !strings.Contains(out1, "Materializing 2 managed Substrates") {
		t.Errorf("expected count line for 2 managed Substrates, got: %s", out1)
	}

	if !strings.Contains(out1, "Materializing managed Substrate \"valid1\"") {
		t.Errorf("expected before-work line for valid1, got: %s", out1)
	}
	if !strings.Contains(out1, "Substrate \"valid1\" cloned") {
		t.Errorf("expected cloned line for valid1, got: %s", out1)
	}
	if strings.Contains(out1, "Substrate \"valid1\" refreshed") {
		t.Errorf("expected cloned, not refreshed, for valid1, got: %s", out1)
	}

	if !strings.Contains(out1, "⚠️ Managed checkout materialization for substrate \"broken\" failed") {
		t.Errorf("expected failure line for broken, got: %s", out1)
	}

	buf.Reset()
	MaterializeManagedCheckouts(ctx, config1)
	out2 := buf.String()

	if !strings.Contains(out2, "Substrate \"valid1\" refreshed") {
		t.Errorf("expected refreshed line for valid1 on second run, got: %s", out2)
	}
	if strings.Contains(out2, "Substrate \"valid1\" cloned") {
		t.Errorf("expected refreshed, not cloned, for valid1 on second run, got: %s", out2)
	}
}

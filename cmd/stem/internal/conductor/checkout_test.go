package conductor

import (
	"context"
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

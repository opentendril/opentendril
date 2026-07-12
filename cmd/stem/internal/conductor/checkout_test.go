package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCheckoutPlan(t *testing.T) {
	t.Setenv("OPENTENDRIL_MANAGED_CHECKOUT_ROOT", "/managed-root")

	cases := []struct {
		name       string
		spec       CheckoutSpec
		wantDir    string
		persistent bool
		wantErr    bool
	}{
		{name: "empty is ephemeral", spec: CheckoutSpec{}, wantDir: "", persistent: false},
		{name: "explicit ephemeral", spec: CheckoutSpec{Mode: "ephemeral"}, wantDir: "", persistent: false},
		{name: "managed under OT root", spec: CheckoutSpec{Mode: "managed"}, wantDir: "/managed-root/sub", persistent: true},
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
	t.Setenv("OPENTENDRIL_MANAGED_CHECKOUT_ROOT", "/root")
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
		t.Setenv("OPENTENDRIL_MANAGED_CHECKOUT_ROOT", root)
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

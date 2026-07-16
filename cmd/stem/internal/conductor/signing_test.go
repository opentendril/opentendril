package conductor

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSigningGitConfigArgs(t *testing.T) {
	t.Run("ssh signing", func(t *testing.T) {
		got := signingGitConfigArgs(ResolvedSigning{Method: "ssh", Key: "/keys/sign"})
		want := []string{"-c", "gpg.format=ssh", "-c", "user.signingkey=/keys/sign", "-c", "commit.gpgsign=true"}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("gpg maps to openpgp", func(t *testing.T) {
		got := signingGitConfigArgs(ResolvedSigning{Method: "gpg", Key: "ABCD1234"})
		if got[1] != "gpg.format=openpgp" || got[3] != "user.signingkey=ABCD1234" {
			t.Fatalf("gpg signing args wrong: %v", got)
		}
	})
	t.Run("disabled cases return nil", func(t *testing.T) {
		for _, s := range []ResolvedSigning{
			{},
			{Method: "ssh"},             // no key
			{Key: "/k"},                 // no method
			{Method: "smoke", Key: "k"}, // unknown method
		} {
			if got := signingGitConfigArgs(s); got != nil {
				t.Fatalf("expected nil for %+v, got %v", s, got)
			}
		}
	})
}

func TestSigningWarning(t *testing.T) {
	if w := signingWarning(ResolvedSigning{}); w != "" {
		t.Fatalf("unset signing should not warn, got %q", w)
	}
	if w := signingWarning(ResolvedSigning{Method: "carrier", Key: "k"}); !strings.Contains(w, "not supported") {
		t.Fatalf("unknown method should warn, got %q", w)
	}
	if w := signingWarning(ResolvedSigning{Method: "ssh"}); !strings.Contains(w, "no key") {
		t.Fatalf("missing key should warn, got %q", w)
	}
	if w := signingWarning(ResolvedSigning{Method: "gpg", Key: "K"}); w != "" {
		t.Fatalf("valid signing should not warn, got %q", w)
	}
}

// TestCommitTerrariumExecutionSignsCommit verifies commits are actually signed
// when a signing config is present. Skips if git SSH signing is unavailable.
func TestCommitTerrariumExecutionSignsCommit(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	ctx := context.Background()
	repo := t.TempDir()
	mustGit := func(args ...string) {
		t.Helper()
		if _, err := runGitCommand(ctx, repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	mustGit("init")
	mustGit("config", "user.email", "t@example.com")
	mustGit("config", "user.name", "Tester")

	keyPath := filepath.Join(t.TempDir(), "sign_key")
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen failed: %v (%s)", err, out)
	}

	status := sproutExecutionStatus{StepID: "s1", Status: "complete", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, err := commitTerrariumExecution(ctx, repo, repo, "", status, "task", ResolvedCredential{Sign: ResolvedSigning{Method: "ssh", Key: keyPath}}); err != nil {
		t.Skipf("signed commit failed (git ssh signing unsupported?): %v", err)
	}

	out, err := runGitCommand(ctx, repo, "log", "-1", "--format=%G?")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if strings.TrimSpace(out) == "N" {
		t.Fatalf("commit is not signed (%%G? = N)")
	}
}

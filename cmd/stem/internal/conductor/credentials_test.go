package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// authTokenFromEnv returns the token the credential helper will serve (from the
// TENDRIL_GIT_TOKEN entry) and asserts the token appears nowhere else in the env —
// notably not in any GIT_CONFIG_* value, which would put it in reach of argv.
func authTokenFromEnv(t *testing.T, env []string) string {
	t.Helper()
	token := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, gitTokenCredentialEnvVar+"="); ok {
			token = v
		}
	}
	if token == "" {
		t.Fatalf("no %s in env %v", gitTokenCredentialEnvVar, env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, gitTokenCredentialEnvVar+"=") {
			continue
		}
		if strings.Contains(e, token) {
			t.Fatalf("token leaked outside %s: %q", gitTokenCredentialEnvVar, e)
		}
	}
	return token
}

func TestResolveSubstrateCredential(t *testing.T) {
	t.Run("pat from env", func(t *testing.T) {
		t.Setenv("PAT_ENV_A", "secret-token")
		rc, err := resolveSubstrateCredential(SubstrateSpec{Auth: AuthSpec{Method: "pat", Env: "PAT_ENV_A"}}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.Method != CredentialPAT || rc.TokenEnv != "PAT_ENV_A" || rc.TokenValue != "secret-token" {
			t.Fatalf("got %+v, want pat/PAT_ENV_A/secret-token", rc)
		}
	})

	t.Run("pat falls back to alternate GitHub var name", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "legacy-pat")
		rc, err := resolveSubstrateCredential(SubstrateSpec{Auth: AuthSpec{Env: "GITHUB_TOKEN"}}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.Method != CredentialPAT || rc.TokenValue != "legacy-pat" {
			t.Fatalf("expected GitHub PAT fallback, got %+v", rc)
		}
	})

	t.Run("ssh expands key path", func(t *testing.T) {
		home, _ := os.UserHomeDir()
		rc, err := resolveSubstrateCredential(SubstrateSpec{Auth: AuthSpec{Method: "ssh", Key: "~/.ssh/id_ot"}}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.Method != CredentialSSH || rc.SSHKeyPath != filepath.Join(home, ".ssh/id_ot") {
			t.Fatalf("got %+v, want ssh with expanded key", rc)
		}
		if rc.TokenValue != "" {
			t.Fatalf("ssh credential must not carry a token, got %q", rc.TokenValue)
		}
	})

	t.Run("none carries no material", func(t *testing.T) {
		rc, err := resolveSubstrateCredential(SubstrateSpec{Auth: AuthSpec{Method: "none"}}, nil)
		if err != nil || rc.Method != CredentialNone || rc.TokenValue != "" || rc.SSHKeyPath != "" {
			t.Fatalf("got %+v err=%v, want clean none", rc, err)
		}
	})

	t.Run("unspecified stays legacy", func(t *testing.T) {
		rc, err := resolveSubstrateCredential(SubstrateSpec{}, nil)
		if err != nil || rc.Method != CredentialUnspecified {
			t.Fatalf("got %+v err=%v, want unspecified", rc, err)
		}
	})

	t.Run("profile supplies auth when inline is empty", func(t *testing.T) {
		t.Setenv("PROFILE_PAT", "profile-token")
		profiles := map[string]CredentialProfile{
			"work": {Auth: AuthSpec{Method: "pat", Env: "PROFILE_PAT"}, Sign: SignSpec{Method: "gpg", Key: "KEY1"}},
		}
		rc, err := resolveSubstrateCredential(SubstrateSpec{Profile: "work"}, profiles)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.TokenValue != "profile-token" || rc.Sign.Method != "gpg" || rc.Sign.Key != "KEY1" {
			t.Fatalf("profile not applied: %+v", rc)
		}
	})

	t.Run("inline auth overrides profile", func(t *testing.T) {
		t.Setenv("INLINE_PAT", "inline-token")
		profiles := map[string]CredentialProfile{"work": {Auth: AuthSpec{Method: "pat", Env: "PROFILE_PAT"}}}
		rc, err := resolveSubstrateCredential(SubstrateSpec{Profile: "work", Auth: AuthSpec{Method: "pat", Env: "INLINE_PAT"}}, profiles)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.TokenEnv != "INLINE_PAT" || rc.TokenValue != "inline-token" {
			t.Fatalf("inline auth should win, got %+v", rc)
		}
	})

	t.Run("unknown profile errors", func(t *testing.T) {
		_, err := resolveSubstrateCredential(SubstrateSpec{Profile: "ghost"}, nil)
		if err == nil || !strings.Contains(err.Error(), "unknown credentials profile") {
			t.Fatalf("expected unknown-profile error, got %v", err)
		}
	})

	t.Run("unknown method errors", func(t *testing.T) {
		_, err := resolveSubstrateCredential(SubstrateSpec{Auth: AuthSpec{Method: "carrierpigeon"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "unknown auth method") {
			t.Fatalf("expected unknown-method error, got %v", err)
		}
	})

	t.Run("commit mode defaults to local", func(t *testing.T) {
		rc, err := resolveSubstrateCredential(SubstrateSpec{}, nil)
		if err != nil || rc.CommitMode != CommitModeLocal {
			t.Fatalf("got %+v err=%v, want default commit mode %q", rc, err, CommitModeLocal)
		}
	})

	t.Run("commit mode is normalized", func(t *testing.T) {
		rc, err := resolveSubstrateCredential(SubstrateSpec{Commit: "  API  "}, nil)
		if err != nil || rc.CommitMode != CommitModeAPI {
			t.Fatalf("got %+v err=%v, want normalized commit mode %q", rc, err, CommitModeAPI)
		}
	})

	t.Run("profile supplies commit mode when inline is empty", func(t *testing.T) {
		profiles := map[string]CredentialProfile{"github": {Commit: "api"}}
		rc, err := resolveSubstrateCredential(SubstrateSpec{Profile: "github"}, profiles)
		if err != nil || rc.CommitMode != CommitModeAPI {
			t.Fatalf("got %+v err=%v, want profile commit mode %q", rc, err, CommitModeAPI)
		}
	})

	t.Run("inline commit mode overrides profile", func(t *testing.T) {
		profiles := map[string]CredentialProfile{"github": {Commit: "api"}}
		rc, err := resolveSubstrateCredential(SubstrateSpec{Profile: "github", Commit: "local"}, profiles)
		if err != nil || rc.CommitMode != CommitModeLocal {
			t.Fatalf("got %+v err=%v, want inline commit mode %q to win", rc, err, CommitModeLocal)
		}
	})
}

func TestCredentialWarning(t *testing.T) {
	t.Run("pat env unset warns", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "")
		w := credentialWarning(SubstrateSpec{Auth: AuthSpec{Method: "pat", Env: "MISSING_PAT_ENV"}}, nil)
		if !strings.Contains(w, "not set") {
			t.Fatalf("expected not-set warning, got %q", w)
		}
	})

	t.Run("ssh missing key warns", func(t *testing.T) {
		w := credentialWarning(SubstrateSpec{Auth: AuthSpec{Method: "ssh", Key: "/definitely/not/here/id_x"}}, nil)
		if !strings.Contains(w, "not a readable file") {
			t.Fatalf("expected unreadable-key warning, got %q", w)
		}
	})

	t.Run("valid pat has no warning", func(t *testing.T) {
		t.Setenv("GOOD_PAT", "value")
		if w := credentialWarning(SubstrateSpec{Auth: AuthSpec{Method: "pat", Env: "GOOD_PAT"}}, nil); w != "" {
			t.Fatalf("expected no warning, got %q", w)
		}
	})

	t.Run("readable ssh key has no warning", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "id_ot")
		if err := os.WriteFile(keyPath, []byte("KEY"), 0o600); err != nil {
			t.Fatalf("write key: %v", err)
		}
		if w := credentialWarning(SubstrateSpec{Auth: AuthSpec{Method: "ssh", Key: keyPath}}, nil); w != "" {
			t.Fatalf("expected no warning, got %q", w)
		}
	})

	t.Run("api commit mode without a github app warns", func(t *testing.T) {
		t.Setenv("GOOD_PAT", "value")
		w := credentialWarning(SubstrateSpec{Auth: AuthSpec{Method: "pat", Env: "GOOD_PAT"}, Commit: "api"}, nil)
		if !strings.Contains(w, `requires auth method "app"`) {
			t.Fatalf("expected api-mode-requires-app warning, got %q", w)
		}
	})

	t.Run("api commit mode with a github app has no warning", func(t *testing.T) {
		keyPath := filepath.Join(t.TempDir(), "app.pem")
		if err := os.WriteFile(keyPath, []byte("KEY"), 0o600); err != nil {
			t.Fatalf("write key: %v", err)
		}
		w := credentialWarning(SubstrateSpec{
			Auth:   AuthSpec{Method: "app", AppID: "1", PrivateKeyPath: keyPath},
			Commit: "api",
		}, nil)
		if w != "" {
			t.Fatalf("expected no warning, got %q", w)
		}
	})
}

func TestResolvedCredentialStringRedactsToken(t *testing.T) {
	rc := ResolvedCredential{Method: CredentialPAT, TokenEnv: "X", TokenValue: "super-secret"}
	s := rc.String()
	if strings.Contains(s, "super-secret") {
		t.Fatalf("String() leaked the token: %s", s)
	}
	if !strings.Contains(s, "***") {
		t.Fatalf("String() should redact with ***, got %s", s)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome("~"); got != home {
		t.Fatalf("expandHome(~) = %q, want %q", got, home)
	}
	if got := expandHome("~/.ssh/id"); got != filepath.Join(home, ".ssh/id") {
		t.Fatalf("expandHome(~/.ssh/id) = %q", got)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Fatalf("expandHome should leave absolute paths, got %q", got)
	}
}

func TestMaterializeGitAuth(t *testing.T) {
	ctx := context.Background()

	t.Run("pat -> credential-helper env, token only in TENDRIL_GIT_TOKEN", func(t *testing.T) {
		env, err := materializeGitAuth(ctx,
			ResolvedCredential{Method: CredentialPAT, TokenEnv: "MY_PAT", TokenValue: "tok123"}, "https://github.com/o/r.git")
		if err != nil {
			t.Fatal(err)
		}
		if got := authTokenFromEnv(t, env); got != "tok123" {
			t.Fatalf("credential token = %q, want tok123", got)
		}
	})

	t.Run("ssh -> GIT_SSH_COMMAND, no token", func(t *testing.T) {
		env, err := materializeGitAuth(ctx,
			ResolvedCredential{Method: CredentialSSH, SSHKeyPath: "/keys/id_ot"}, "git@github.com:o/r.git")
		if err != nil {
			t.Fatal(err)
		}
		if len(env) != 1 || !strings.Contains(env[0], "GIT_SSH_COMMAND=") || !strings.Contains(env[0], "/keys/id_ot") {
			t.Fatalf("env = %v, want only GIT_SSH_COMMAND with key", env)
		}
		for _, e := range env {
			if strings.Contains(strings.ToUpper(e), "TOKEN") || strings.HasPrefix(e, "GIT_CONFIG") {
				t.Fatalf("ssh leaked token/credential config: %q", e)
			}
		}
	})

	t.Run("none -> anonymous", func(t *testing.T) {
		env, err := materializeGitAuth(ctx, ResolvedCredential{Method: CredentialNone}, "https://example.com/pub.git")
		if err != nil || env != nil {
			t.Fatalf("none should be anonymous, got env=%v err=%v", env, err)
		}
	})

	t.Run("unspecified github does not use ambient PAT", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "ambient")
		t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "")
		env, err := materializeGitAuth(ctx, ResolvedCredential{Method: CredentialUnspecified}, "https://github.com/o/r.git")
		if err != nil || env != nil {
			t.Fatalf("expected nil env, got env=%v err=%v", env, err)
		}
	})

	t.Run("empty explicit PAT returns error", func(t *testing.T) {
		_, err := materializeGitAuth(ctx, ResolvedCredential{Method: CredentialPAT, TokenEnv: "MY_TOKEN", TokenValue: ""}, "https://github.com/o/r.git")
		if err == nil || !strings.Contains(err.Error(), "MY_TOKEN") {
			t.Fatalf("expected error naming MY_TOKEN, got %v", err)
		}
	})

	t.Run("requireGitHubPushAuth", func(t *testing.T) {
		if err := requireGitHubPushAuth(nil, "https://github.com/o/r.git", ResolvedCredential{Method: CredentialUnspecified}); err == nil {
			t.Fatalf("expected error for empty pushEnv + github origin")
		}
		if err := requireGitHubPushAuth([]string{"ENV=1"}, "https://github.com/o/r.git", ResolvedCredential{}); err != nil {
			t.Fatalf("expected nil for non-empty pushEnv, got %v", err)
		}
		if err := requireGitHubPushAuth(nil, "https://gitlab.com/o/r.git", ResolvedCredential{}); err != nil {
			t.Fatalf("expected nil for empty pushEnv + non-github origin, got %v", err)
		}
	})

	t.Run("unspecified non-github stays anonymous", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "ambient")
		env, err := materializeGitAuth(ctx, ResolvedCredential{}, "https://gitlab.com/o/r.git")
		if err != nil || env != nil {
			t.Fatalf("non-github unspecified should stay anonymous, got env=%v", env)
		}
	})
}

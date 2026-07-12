package conductor

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodeAuthToken extracts the token from git http.extraHeader config args.
func decodeAuthToken(t *testing.T, configArgs []string) string {
	t.Helper()
	if len(configArgs) != 2 {
		t.Fatalf("want [-c, http.extraHeader=...], got %v", configArgs)
	}
	const marker = "Authorization: Basic "
	idx := strings.Index(configArgs[1], marker)
	if idx < 0 {
		t.Fatalf("no basic auth header in %q", configArgs[1])
	}
	raw, err := base64.StdEncoding.DecodeString(configArgs[1][idx+len(marker):])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 || parts[0] != "x-access-token" {
		t.Fatalf("unexpected basic auth payload %q", string(raw))
	}
	return parts[1]
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

	t.Run("pat -> Authorization header, no env, no url token", func(t *testing.T) {
		args, env, err := materializeGitAuth(ctx,
			ResolvedCredential{Method: CredentialPAT, TokenEnv: "MY_PAT", TokenValue: "tok123"}, "https://github.com/o/r.git")
		if err != nil {
			t.Fatal(err)
		}
		if env != nil {
			t.Fatalf("token auth must not use process env, got %v", env)
		}
		if got := decodeAuthToken(t, args); got != "tok123" {
			t.Fatalf("header token = %q, want tok123", got)
		}
	})

	t.Run("ssh -> GIT_SSH_COMMAND, no header, no token", func(t *testing.T) {
		args, env, err := materializeGitAuth(ctx,
			ResolvedCredential{Method: CredentialSSH, SSHKeyPath: "/keys/id_ot"}, "git@github.com:o/r.git")
		if err != nil {
			t.Fatal(err)
		}
		if args != nil {
			t.Fatalf("ssh must not set http auth args, got %v", args)
		}
		if len(env) != 1 || !strings.Contains(env[0], "GIT_SSH_COMMAND=") || !strings.Contains(env[0], "/keys/id_ot") {
			t.Fatalf("env = %v, want GIT_SSH_COMMAND with key", env)
		}
		for _, e := range env {
			if strings.Contains(strings.ToUpper(e), "TOKEN") {
				t.Fatalf("ssh leaked a token-ish env: %q", e)
			}
		}
	})

	t.Run("none -> anonymous", func(t *testing.T) {
		args, env, err := materializeGitAuth(ctx, ResolvedCredential{Method: CredentialNone}, "https://example.com/pub.git")
		if err != nil || args != nil || env != nil {
			t.Fatalf("none should be anonymous, got args=%v env=%v err=%v", args, env, err)
		}
	})

	t.Run("unspecified github uses ambient PAT", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "ambient")
		t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "")
		args, env, err := materializeGitAuth(ctx, ResolvedCredential{}, "https://github.com/o/r.git")
		if err != nil || env != nil {
			t.Fatalf("unexpected env=%v err=%v", env, err)
		}
		if got := decodeAuthToken(t, args); got != "ambient" {
			t.Fatalf("header token = %q, want ambient", got)
		}
	})

	t.Run("unspecified non-github stays anonymous", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "ambient")
		args, env, err := materializeGitAuth(ctx, ResolvedCredential{}, "https://gitlab.com/o/r.git")
		if err != nil || args != nil || env != nil {
			t.Fatalf("non-github unspecified should stay anonymous, got args=%v env=%v", args, env)
		}
	})
}

func TestBuildTerrariumEnvironmentSuppressesPAT(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "host-pat")
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "")

	if env := buildTerrariumEnvironment(); env["GITHUB_TOKEN"] != "host-pat" {
		t.Fatalf("expected PAT injected by default, got %v", env["GITHUB_TOKEN"])
	}

	env := buildTerrariumEnvironment(suppressGitHubPATEnvSentinel + "=true")
	if _, ok := env["GITHUB_TOKEN"]; ok {
		t.Fatalf("PAT must be suppressed for ssh/none substrates")
	}
	if _, ok := env["GITHUB_PERSONAL_ACCESS_TOKEN"]; ok {
		t.Fatalf("legacy PAT must be suppressed too")
	}
	if _, ok := env[suppressGitHubPATEnvSentinel]; ok {
		t.Fatalf("internal sentinel must never surface in the terrarium env")
	}
}

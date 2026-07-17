package conductor

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIdentityGitConfigArgs(t *testing.T) {
	t.Run("name and email", func(t *testing.T) {
		got := identityGitConfigArgs(ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"})
		want := []string{"-c", "user.name=OpenTendril Bot", "-c", "user.email=bot@example.com"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("name only", func(t *testing.T) {
		got := identityGitConfigArgs(ResolvedIdentity{Name: "OpenTendril Bot"})
		want := []string{"-c", "user.name=OpenTendril Bot"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("email only", func(t *testing.T) {
		got := identityGitConfigArgs(ResolvedIdentity{Email: "bot@example.com"})
		want := []string{"-c", "user.email=bot@example.com"}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("unset returns nil", func(t *testing.T) {
		for _, identity := range []ResolvedIdentity{
			{},
			{Name: "   ", Email: "\t"}, // whitespace-only counts as unset
		} {
			if got := identityGitConfigArgs(identity); got != nil {
				t.Fatalf("expected nil for %+v, got %v", identity, got)
			}
		}
	})
}

func TestTrimIdentitySpec(t *testing.T) {
	identity := IdentitySpec{Name: "  OpenTendril Bot  ", Email: " bot@example.com\n"}
	trimIdentitySpec(&identity)
	if identity.Name != "OpenTendril Bot" || identity.Email != "bot@example.com" {
		t.Fatalf("trimIdentitySpec did not normalize fields: %+v", identity)
	}
	trimIdentitySpec(nil) // must not panic
}

func TestResolveSubstrateCredentialIdentity(t *testing.T) {
	t.Run("inline identity resolves trimmed", func(t *testing.T) {
		rc, err := resolveSubstrateCredential(SubstrateSpec{Identity: IdentitySpec{Name: " OpenTendril Bot ", Email: " bot@example.com "}}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.Identity.Name != "OpenTendril Bot" || rc.Identity.Email != "bot@example.com" {
			t.Fatalf("identity not resolved/trimmed: %+v", rc.Identity)
		}
	})

	t.Run("unset identity stays empty", func(t *testing.T) {
		rc, err := resolveSubstrateCredential(SubstrateSpec{}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.Identity.Name != "" || rc.Identity.Email != "" {
			t.Fatalf("expected empty identity, got %+v", rc.Identity)
		}
	})

	t.Run("profile supplies identity when inline is empty", func(t *testing.T) {
		profiles := map[string]CredentialProfile{
			"work": {Identity: IdentitySpec{Name: "Profile Bot", Email: "profile@example.com"}},
		}
		rc, err := resolveSubstrateCredential(SubstrateSpec{Profile: "work"}, profiles)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.Identity.Name != "Profile Bot" || rc.Identity.Email != "profile@example.com" {
			t.Fatalf("profile identity not applied: %+v", rc.Identity)
		}
	})

	t.Run("inline identity overrides profile", func(t *testing.T) {
		profiles := map[string]CredentialProfile{
			"work": {Identity: IdentitySpec{Name: "Profile Bot", Email: "profile@example.com"}},
		}
		rc, err := resolveSubstrateCredential(SubstrateSpec{Profile: "work", Identity: IdentitySpec{Name: "Inline Bot", Email: "inline@example.com"}}, profiles)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rc.Identity.Name != "Inline Bot" || rc.Identity.Email != "inline@example.com" {
			t.Fatalf("inline identity should win, got %+v", rc.Identity)
		}
	})
}

// TestCommitTerrariumExecutionAppliesIdentity verifies the configured identity
// attributes both author and committer, and that an unset identity leaves the
// ambient git identity untouched.
func TestCommitTerrariumExecutionAppliesIdentity(t *testing.T) {
	newRepo := func(t *testing.T) string {
		t.Helper()
		ctx := context.Background()
		repo := t.TempDir()
		for _, args := range [][]string{
			{"init"},
			{"config", "user.email", "ambient@example.com"},
			{"config", "user.name", "Ambient Tester"},
		} {
			if _, err := runGitCommand(ctx, repo, args...); err != nil {
				t.Fatalf("git %v: %v", args, err)
			}
		}
		return repo
	}
	attribution := func(t *testing.T, repo string) string {
		t.Helper()
		out, err := runGitCommand(context.Background(), repo, "log", "-1", "--format=%an|%ae|%cn|%ce")
		if err != nil {
			t.Fatalf("git log: %v", err)
		}
		return strings.TrimSpace(out)
	}
	status := sproutExecutionStatus{StepID: "s1", Status: "complete", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}

	t.Run("configured identity attributes author and committer", func(t *testing.T) {
		repo := newRepo(t)
		credential := ResolvedCredential{Identity: ResolvedIdentity{Name: "OpenTendril Bot", Email: "bot@example.com"}}
		if _, err := commitTerrariumExecution(context.Background(), repo, repo, "", status, "task", credential); err != nil {
			t.Fatalf("commit: %v", err)
		}
		want := "OpenTendril Bot|bot@example.com|OpenTendril Bot|bot@example.com"
		if got := attribution(t, repo); got != want {
			t.Fatalf("attribution = %q, want %q", got, want)
		}
	})

	t.Run("unset identity keeps ambient git identity", func(t *testing.T) {
		repo := newRepo(t)
		if _, err := commitTerrariumExecution(context.Background(), repo, repo, "", status, "task", ResolvedCredential{}); err != nil {
			t.Fatalf("commit: %v", err)
		}
		want := "Ambient Tester|ambient@example.com|Ambient Tester|ambient@example.com"
		if got := attribution(t, repo); got != want {
			t.Fatalf("attribution = %q, want %q", got, want)
		}
	})
}

// The sprout CLI hands the orchestrator a substrate name; this is what the
// name has to buy. A named substrate with a LOCAL PATH is the case that
// regressed: the CLI used to substitute the path, leaving this lookup nothing
// to match, so the configured identity, signing, auth and readonly were all
// skipped in silence.
func TestExecutionPlanResolvesNamedSubstrateIdentityAndPath(t *testing.T) {
	localPath := t.TempDir()
	config := &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"demo": {
				Path: localPath,
				Identity: IdentitySpec{
					Name:  "OpenTendril Sprout",
					Email: "sprout@opentendril.local",
				},
			},
		},
	}

	plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "demo"}, config)
	if err != nil {
		t.Fatalf("resolveSubstrateExecutionPlan returned error: %v", err)
	}
	if !plan.named {
		t.Fatalf("plan.named = false: the name did not resolve to its spec")
	}
	if plan.credential.Identity.Name != "OpenTendril Sprout" {
		t.Fatalf("identity name = %q, want the configured identity", plan.credential.Identity.Name)
	}
	if plan.credential.Identity.Email != "sprout@opentendril.local" {
		t.Fatalf("identity email = %q, want the configured identity", plan.credential.Identity.Email)
	}
	// The plan resolves the local path itself, which is why the CLI does not
	// need to substitute one.
	if plan.hostPath != localPath {
		t.Fatalf("hostPath = %q, want the spec's path %q", plan.hostPath, localPath)
	}
}

// Handing the plan the resolved PATH instead of the name is the regression:
// nothing matches, and the configuration is silently skipped.
func TestExecutionPlanCannotResolveConfigurationFromAPath(t *testing.T) {
	localPath := t.TempDir()
	config := &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"demo": {
				Path:     localPath,
				Identity: IdentitySpec{Name: "OpenTendril Sprout", Email: "sprout@opentendril.local"},
			},
		},
	}

	plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: localPath}, config)
	if err != nil {
		t.Fatalf("resolveSubstrateExecutionPlan returned error: %v", err)
	}
	if plan.named {
		t.Fatalf("a path resolved as a named substrate; the lookup is by name")
	}
	if plan.credential.Identity.Name != "" || plan.credential.Identity.Email != "" {
		t.Fatalf("identity %+v resolved from a path: unreachable, and this test documents why the CLI must pass the name", plan.credential.Identity)
	}
}

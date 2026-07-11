package conductor

import "testing"

func TestResolveAuthTokenValueFallsBackAcrossGitHubPATNames(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "")

	t.Run("prefers the referenced variable", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "canonical")
		t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "legacy")
		if got := resolveAuthTokenValue("GITHUB_PERSONAL_ACCESS_TOKEN"); got != "legacy" {
			t.Fatalf("expected legacy, got %q", got)
		}
	})

	t.Run("legacy ref falls back to GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "canonical")
		if got := resolveAuthTokenValue("GITHUB_PERSONAL_ACCESS_TOKEN"); got != "canonical" {
			t.Fatalf("expected canonical, got %q", got)
		}
	})

	t.Run("canonical ref falls back to legacy name", func(t *testing.T) {
		t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "legacy")
		if got := resolveAuthTokenValue("GITHUB_TOKEN"); got != "legacy" {
			t.Fatalf("expected legacy, got %q", got)
		}
	})

	t.Run("non-GitHub refs never fall back", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "canonical")
		if got := resolveAuthTokenValue("MY_CUSTOM_PAT"); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})
}

func TestResolveGitHubPATPrefersCanonicalName(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "canonical")
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "legacy")

	name, value := resolveGitHubPAT()
	if name != "GITHUB_TOKEN" || value != "canonical" {
		t.Fatalf("expected GITHUB_TOKEN/canonical, got %s/%s", name, value)
	}

	t.Setenv("GITHUB_TOKEN", "")
	name, value = resolveGitHubPAT()
	if name != "GITHUB_PERSONAL_ACCESS_TOKEN" || value != "legacy" {
		t.Fatalf("expected legacy fallback, got %s/%s", name, value)
	}
}

package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeFruitRemoteCanonicalizesGitHubWithoutCredentials(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{name: "ssh", url: "git@github.com:OpenTendril/OpenTendril.git", want: "github.com/OpenTendril/OpenTendril"},
		{name: "https", url: "https://user:secret@github.com/OpenTendril/OpenTendril.git?token=hidden", want: "github.com/OpenTendril/OpenTendril"},
		{name: "other remote", url: "ssh://git@example.invalid:2222/team/repo.git", want: "example.invalid:2222/team/repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeFruitRemote(tc.url)
			if got != tc.want {
				t.Fatalf("normalizeFruitRemote(%q) = %q, want %q", tc.url, got, tc.want)
			}
			if strings.Contains(got, "secret") || strings.Contains(got, "token") || strings.Contains(got, "user@") {
				t.Fatalf("normalized remote contains credential material: %q", got)
			}
		})
	}
}

func TestCaptureFruitProvenanceUsesRepositoryEvidenceAndPrivateLocator(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test User"},
	} {
		if _, err := runGitCommand(ctx, root, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fruit\n"), 0o644); err != nil {
		t.Fatalf("write repository: %v", err)
	}
	if _, err := runGitCommand(ctx, root, "add", "README.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(ctx, root, "commit", "-m", "base"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	if _, err := runGitCommand(ctx, root, "remote", "add", "origin", "https://user:secret@github.com/example/repo.git"); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	commit, err := runGitCommand(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	created := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	provenance, err := captureFruitProvenance(ctx, root, "feature/fruit", strings.TrimSpace(commit), FruitPublicationPublished, created)
	if err != nil {
		t.Fatalf("captureFruitProvenance: %v", err)
	}
	if provenance.Repository != "github.com/example/repo" {
		t.Fatalf("repository = %q, want github.com/example/repo", provenance.Repository)
	}
	if provenance.Workspace == "" || provenance.Workspace == provenance.Repository {
		t.Fatalf("workspace locator = %q, want private canonical repository path", provenance.Workspace)
	}
	if strings.Contains(provenance.Repository, "secret") || strings.Contains(provenance.Workspace, "secret") {
		t.Fatalf("provenance contains credentials: %+v", provenance)
	}
	if provenance.Branch != "feature/fruit" || provenance.Commit != strings.TrimSpace(commit) || provenance.PublicationState != FruitPublicationPublished || !provenance.CreatedAt.Equal(created) {
		t.Fatalf("provenance = %+v", provenance)
	}
}

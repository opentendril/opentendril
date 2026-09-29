package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestGitFetchRequiresConfiguredNameAndExistingCheckout(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "init", "--initial-branch=main", repo)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	config := &conductor.SubstratesConfig{Substrates: map[string]conductor.SubstrateSpec{
		"demo": {Path: repo, URL: "https://github.com/owner/repository.git", Auth: conductor.AuthSpec{Method: "none"}},
	}}
	fetch := gitOperationsForConfig(config).Fetch
	if fetch == nil {
		t.Fatal("git.fetch Core execution port is not wired")
	}
	_, err := fetch(context.Background(), core.GitFetchSpec{Substrate: "demo"})
	var fetchErr core.GitFetchError
	if !errors.As(err, &fetchErr) || fetchErr.Category != core.GitFetchFailureRemoteIdentityMismatch {
		t.Fatalf("configured name did not resolve to existing checkout before identity check: %v", err)
	}
	for _, name := range []string{"", repo, "unknown"} {
		_, err := fetch(context.Background(), core.GitFetchSpec{Substrate: name})
		if !errors.As(err, &fetchErr) || fetchErr.Category != core.GitFetchFailureSubstrateUnavailable {
			t.Errorf("unconfigured Substrate %q error = %v, want safe unavailable category", name, err)
		}
	}
	_, err = gitOperationsForConfig(nil).Fetch(context.Background(), core.GitFetchSpec{Substrate: "demo"})
	if !errors.As(err, &fetchErr) || fetchErr.Category != core.GitFetchFailureSubstrateUnavailable {
		t.Fatalf("missing configuration error = %v, want safe unavailable category", err)
	}
}

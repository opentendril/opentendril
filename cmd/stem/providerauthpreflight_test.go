package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChatAndCLIGrowShareProviderAuthPreflight pins the Gate B claim that
// chat and CLI do not implement provider-auth validation themselves. Both
// surfaces grow through DockerOrchestrator.RunSprout, which owns the
// pre-emergence Roots probe.
func TestChatAndCLIGrowShareProviderAuthPreflight(t *testing.T) {
	cliSrc, err := os.ReadFile(filepath.Join("cmdsprout.go"))
	if err != nil {
		t.Fatalf("read CLI adapter: %v", err)
	}
	chatSrc, err := os.ReadFile(filepath.Join("cmdserve.go"))
	if err != nil {
		t.Fatalf("read chat adapter: %v", err)
	}

	if !strings.Contains(string(cliSrc), "orch.RunSprout(") {
		t.Fatal("CLI sprout adapter no longer grows through orch.RunSprout")
	}
	if !strings.Contains(string(chatSrc), "orch.RunSprout(") {
		t.Fatal("chat grow path no longer grows through orch.RunSprout")
	}

	for _, pair := range []struct {
		name string
		src  string
	}{
		{"CLI", string(cliSrc)},
		{"chat", string(chatSrc)},
	} {
		if strings.Contains(pair.src, "provider-auth-rejected") {
			t.Fatalf("%s adapter classifies provider-auth-rejected; that belongs in Stem/Conductor", pair.name)
		}
		if strings.Contains(pair.src, "ProbeAuthentication") {
			t.Fatalf("%s adapter talks to the provider; Roots owns that transport", pair.name)
		}
	}
}

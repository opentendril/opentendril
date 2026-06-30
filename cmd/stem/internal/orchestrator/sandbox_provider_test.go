package orchestrator

import (
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/sandbox"
)

func TestResolveSandboxProviderName(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		t.Setenv(sandboxProviderEnvKey, "gvisor")

		got := resolveSandboxProviderName(&DockerOrchestrator{Substrate: "docker"})
		if got != sandbox.ProviderGVisor {
			t.Fatalf("resolveSandboxProviderName() = %q, want %q", got, sandbox.ProviderGVisor)
		}
	})

	t.Run("substrate fallback", func(t *testing.T) {
		got := resolveSandboxProviderName(&DockerOrchestrator{Substrate: "gvisor"})
		if got != sandbox.ProviderGVisor {
			t.Fatalf("resolveSandboxProviderName() = %q, want %q", got, sandbox.ProviderGVisor)
		}
	})

	t.Run("defaults to docker", func(t *testing.T) {
		got := resolveSandboxProviderName(&DockerOrchestrator{})
		if got != sandbox.ProviderDocker {
			t.Fatalf("resolveSandboxProviderName() = %q, want %q", got, sandbox.ProviderDocker)
		}
	})
}

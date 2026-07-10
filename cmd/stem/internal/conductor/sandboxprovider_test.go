package conductor

import (
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/terrarium"
)

func TestResolveTerrariumProviderName(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		t.Setenv(terrariumProviderEnvKey, "gvisor")

		got := resolveTerrariumProviderName(&DockerOrchestrator{Substrate: "docker"})
		if got != terrarium.ProviderGVisor {
			t.Fatalf("resolveTerrariumProviderName() = %q, want %q", got, terrarium.ProviderGVisor)
		}
	})

	t.Run("substrate fallback", func(t *testing.T) {
		got := resolveTerrariumProviderName(&DockerOrchestrator{Substrate: "gvisor"})
		if got != terrarium.ProviderGVisor {
			t.Fatalf("resolveTerrariumProviderName() = %q, want %q", got, terrarium.ProviderGVisor)
		}
	})

	t.Run("defaults to docker", func(t *testing.T) {
		got := resolveTerrariumProviderName(&DockerOrchestrator{})
		if got != terrarium.ProviderDocker {
			t.Fatalf("resolveTerrariumProviderName() = %q, want %q", got, terrarium.ProviderDocker)
		}
	})
}

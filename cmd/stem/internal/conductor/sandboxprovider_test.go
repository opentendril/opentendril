package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

func TestResolveTerrariumProviderName(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		t.Setenv(terrariumProviderEnvKey, "gvisor")

		got := resolveTerrariumProviderName(context.Background(), &DockerOrchestrator{Substrate: "docker"})
		if got != terrarium.ProviderGVisor {
			t.Fatalf("resolveTerrariumProviderName() = %q, want %q", got, terrarium.ProviderGVisor)
		}
	})

	t.Run("substrate fallback", func(t *testing.T) {
		got := resolveTerrariumProviderName(context.Background(), &DockerOrchestrator{Substrate: "gvisor"})
		if got != terrarium.ProviderGVisor {
			t.Fatalf("resolveTerrariumProviderName() = %q, want %q", got, terrarium.ProviderGVisor)
		}
	})

	t.Run("substrate selects firecracker", func(t *testing.T) {
		got := resolveTerrariumProviderName(context.Background(), &DockerOrchestrator{Substrate: "firecracker"})
		if got != terrarium.ProviderFirecracker {
			t.Fatalf("resolveTerrariumProviderName() = %q, want %q", got, terrarium.ProviderFirecracker)
		}
	})

	t.Run("prefers gvisor when ready and nothing explicit is set", func(t *testing.T) {
		original := checkGVisorReadinessFn
		checkGVisorReadinessFn = func(context.Context) error { return nil }
		t.Cleanup(func() { checkGVisorReadinessFn = original })

		got := resolveTerrariumProviderName(context.Background(), &DockerOrchestrator{})
		if got != terrarium.ProviderGVisor {
			t.Fatalf("resolveTerrariumProviderName() = %q, want %q", got, terrarium.ProviderGVisor)
		}
	})

	t.Run("falls back to docker when gvisor is not ready", func(t *testing.T) {
		original := checkGVisorReadinessFn
		checkGVisorReadinessFn = func(context.Context) error { return errors.New("runsc not registered") }
		t.Cleanup(func() { checkGVisorReadinessFn = original })

		got := resolveTerrariumProviderName(context.Background(), &DockerOrchestrator{})
		if got != terrarium.ProviderDocker {
			t.Fatalf("resolveTerrariumProviderName() = %q, want %q", got, terrarium.ProviderDocker)
		}
	})

	t.Run("explicit docker substrate is never upgraded to gvisor", func(t *testing.T) {
		original := checkGVisorReadinessFn
		checkGVisorReadinessFn = func(context.Context) error { return nil } // gVisor IS ready
		t.Cleanup(func() { checkGVisorReadinessFn = original })

		got := resolveTerrariumProviderName(context.Background(), &DockerOrchestrator{Substrate: "docker"})
		if got != terrarium.ProviderDocker {
			t.Fatalf("resolveTerrariumProviderName() = %q, want %q (explicit substrate must not be overridden)", got, terrarium.ProviderDocker)
		}
	})
}

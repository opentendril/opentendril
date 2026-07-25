package terrarium

import (
	"context"
	"fmt"
	"os"
	"strings"
)

const (
	ProviderDocker      = "docker"
	ProviderGVisor      = "gvisor"
	ProviderFirecracker = "firecracker"
	ProviderHost        = "host"

	// EnvAllowHostExecution is the opt-in environment variable that must be
	// explicitly set to "true" for the host terrarium provider to activate.
	// The host provider runs processes with full host permissions and bypasses
	// all Terrarium isolation. It is disabled by default (default-deny).
	EnvAllowHostExecution = "TENDRIL_ALLOW_HOST_EXECUTION"
)

// ActivationObserver is notified exactly once when a security-relevant
// terrarium provider (currently: host) is activated. Callers with
// telemetry access pass one to NewProvider to audit the activation.
// The callback receives the resolved provider name. terrarium never
// imports internal/eventbus — the closure is the only new surface.
type ActivationObserver func(providerName string)

// NewProvider resolves a terrarium provider from configuration or defaults to
// Docker. Every provider is wrapped so Create rejects a TerrariumSpec the
// provider cannot honor instead of silently dropping the request.
//
// observers are notified exactly once when the host provider activates
// successfully. All other providers ignore them. Callers without access to a
// telemetry bus may omit observers entirely.
func NewProvider(ctx context.Context, name string, observers ...ActivationObserver) (TerrariumProvider, error) {
	provider, err := newBaseProvider(ctx, name, observers...)
	if err != nil {
		return nil, err
	}
	return newValidatingProvider(provider), nil
}

func newBaseProvider(ctx context.Context, name string, observers ...ActivationObserver) (TerrariumProvider, error) {
	switch normalizeProviderName(name) {
	case "", ProviderDocker:
		return NewDockerProvider(), nil
	case ProviderHost:
		if err := checkHostExecutionAllowed(name, observers...); err != nil {
			return nil, err
		}
		return NewHostProvider(), nil
	case ProviderGVisor:
		if err := CheckGVisorReadiness(ctx); err != nil {
			return nil, fmt.Errorf("gvisor provider is unavailable: %w", err)
		}
		return NewGVisorProvider(), nil
	case ProviderFirecracker:
		if err := CheckFirecrackerReadiness(ctx); err != nil {
			return nil, fmt.Errorf("firecracker provider is unavailable: %w", err)
		}
		return NewFirecrackerProvider(), nil
	default:
		return nil, fmt.Errorf("unsupported terrarium provider %q", strings.TrimSpace(name))
	}
}

// checkHostExecutionAllowed enforces the default-deny policy for the host
// terrarium provider. The host provider bypasses all Terrarium isolation and runs
// with full host-user permissions, so it must be explicitly opted into by
// setting TENDRIL_ALLOW_HOST_EXECUTION=true in the environment.
//
// On success each observer is called exactly once with the resolved provider
// name so callers that have telemetry access can publish a structured audit
// event without importing internal/eventbus from this package.
func checkHostExecutionAllowed(name string, observers ...ActivationObserver) error {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvAllowHostExecution)), "true") {
		fmt.Fprintf(os.Stderr,
			"⚠️   Host execution provider activated (%s=true): terrarium isolation bypassed — no mount sealing, no network sealing, running with full host-user permissions.\n",
			EnvAllowHostExecution,
		)
		resolved := normalizeProviderName(name)
		if resolved == "" {
			resolved = ProviderHost
		}
		for _, obs := range observers {
			if obs != nil {
				obs(resolved)
			}
		}
		return nil
	}
	return fmt.Errorf(
		"host terrarium provider is disabled by default: "+
			"set %s=true to explicitly opt in to running Tendril processes on the local host. "+
			"Note: host terrariums run with your full user permissions and bypass all isolation.",
		EnvAllowHostExecution,
	)
}

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

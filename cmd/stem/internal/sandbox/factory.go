package sandbox

import (
	"context"
	"fmt"
	"strings"
)

const (
	ProviderDocker = "docker"
	ProviderGVisor = "gvisor"
)

// NewProvider resolves a sandbox provider from configuration or defaults to Docker.
func NewProvider(ctx context.Context, name string) (SandboxProvider, error) {
	switch normalizeProviderName(name) {
	case "", ProviderDocker:
		return NewDockerProvider(), nil
	case ProviderGVisor:
		if err := CheckGVisorReadiness(ctx); err != nil {
			return nil, fmt.Errorf("gvisor provider is unavailable: %w", err)
		}
		return NewGVisorProvider(), nil
	default:
		return nil, fmt.Errorf("unsupported sandbox provider %q", strings.TrimSpace(name))
	}
}

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

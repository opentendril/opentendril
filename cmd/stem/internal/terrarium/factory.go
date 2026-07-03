package terrarium

import (
	"context"
	"fmt"
	"strings"
)

const (
	ProviderDocker      = "docker"
	ProviderGVisor      = "gvisor"
	ProviderFirecracker = "firecracker"
	ProviderHost        = "host"
)

// NewProvider resolves a terrarium provider from configuration or defaults to Docker.
func NewProvider(ctx context.Context, name string) (TerrariumProvider, error) {
	switch normalizeProviderName(name) {
	case "", ProviderDocker:
		return NewDockerProvider(), nil
	case ProviderHost:
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

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

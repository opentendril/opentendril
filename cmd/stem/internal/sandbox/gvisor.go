package sandbox

import (
	"context"
	"fmt"
	"strings"
)

// GVisorProvider launches sandboxes through Docker with the runsc runtime.
type GVisorProvider struct{}

func NewGVisorProvider() *GVisorProvider {
	return &GVisorProvider{}
}

func (p *GVisorProvider) Name() string {
	return ProviderGVisor
}

func (p *GVisorProvider) Capabilities() SandboxCapabilities {
	return defaultSandboxCapabilities()
}

func (p *GVisorProvider) Create(ctx context.Context, spec SandboxSpec) (Sandbox, error) {
	return createDockerSandbox(ctx, p, spec, "--runtime=runsc")
}

// CheckGVisorReadiness confirms that the local Docker daemon exposes the runsc runtime.
func CheckGVisorReadiness(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	stdout, stderr, err := runDockerCommand(ctx, "info", "-f", "{{.Runtimes.runsc}}")
	if err != nil {
		return fmt.Errorf("check gVisor readiness: %w", err)
	}

	value := strings.TrimSpace(stdout)
	if value == "" {
		value = strings.TrimSpace(stderr)
	}
	if value == "" || strings.EqualFold(value, "<no value>") || !strings.Contains(strings.ToLower(value), "runsc") {
		if value == "" {
			value = "no runtime information returned"
		}
		return fmt.Errorf("gVisor runtime runsc is not available on the host Docker daemon (%s)", value)
	}

	return nil
}

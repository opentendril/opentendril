package terrarium

import (
	"context"
	"fmt"
	"strings"
)

// ValidateSpecAgainstCapabilities rejects a TerrariumSpec that requests a
// feature the provider does not declare in its capabilities. Empty or
// zero-valued fields are permissive: not asking for a feature is never a
// violation. Anything a caller asked for and would not receive must fail
// loudly here — a silently dropped isolation request leaves the caller
// believing it has a seal it does not have.
func ValidateSpecAgainstCapabilities(providerName string, capabilities TerrariumCapabilities, spec TerrariumSpec) error {
	if len(spec.Mounts) > 0 && !capabilities.SupportsMounts {
		mounts := make([]string, len(spec.Mounts))
		for i, mount := range spec.Mounts {
			mounts[i] = mount.Source + " -> " + mount.Target
		}
		return fmt.Errorf(
			"terrarium provider %q does not support mounts, but the TerrariumSpec requests %d: %s",
			providerName, len(spec.Mounts), strings.Join(mounts, ", "),
		)
	}

	if spec.NetworkMode != "" && !containsNetworkMode(capabilities.SupportedNetworkModes, spec.NetworkMode) {
		return fmt.Errorf(
			"terrarium provider %q does not support network mode %q (supported: %s)",
			providerName, spec.NetworkMode, formatNetworkModes(capabilities.SupportedNetworkModes),
		)
	}

	if strings.TrimSpace(spec.Image) != "" && !capabilities.SupportsImages {
		return fmt.Errorf(
			"terrarium provider %q does not support selecting an image, but the TerrariumSpec requests %q",
			providerName, spec.Image,
		)
	}

	return nil
}

// validatingProvider wraps a TerrariumProvider so every Create checks the
// requested TerrariumSpec against the provider's declared capabilities before
// anything is launched. Providers historically ignored fields they could not
// honor, so a caller asking for a sealed network or a read-only mount could
// silently get neither. Wrapping at construction time keeps the check out of
// individual providers — a new provider cannot forget it.
type validatingProvider struct {
	inner TerrariumProvider
}

func newValidatingProvider(inner TerrariumProvider) TerrariumProvider {
	return &validatingProvider{inner: inner}
}

func (p *validatingProvider) Name() string {
	return p.inner.Name()
}

func (p *validatingProvider) Capabilities() TerrariumCapabilities {
	return p.inner.Capabilities()
}

func (p *validatingProvider) Create(ctx context.Context, spec TerrariumSpec) (Terrarium, error) {
	if err := ValidateSpecAgainstCapabilities(p.inner.Name(), p.inner.Capabilities(), spec); err != nil {
		return nil, err
	}
	return p.inner.Create(ctx, spec)
}

func containsNetworkMode(supported []NetworkMode, mode NetworkMode) bool {
	for _, candidate := range supported {
		if candidate == mode {
			return true
		}
	}
	return false
}

func formatNetworkModes(supported []NetworkMode) string {
	if len(supported) == 0 {
		return "none"
	}
	names := make([]string, len(supported))
	for i, mode := range supported {
		names[i] = string(mode)
	}
	return strings.Join(names, ", ")
}

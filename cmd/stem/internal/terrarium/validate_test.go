package terrarium

import (
	"context"
	"strings"
	"testing"
)

func TestValidateSpecAgainstCapabilities(t *testing.T) {
	tests := []struct {
		name         string
		providerName string
		capabilities TerrariumCapabilities
		spec         TerrariumSpec
		wantErr      []string
	}{
		{
			name:         "mounts rejected when provider lacks mount support",
			providerName: ProviderHost,
			capabilities: NewHostProvider().Capabilities(),
			spec: TerrariumSpec{
				Mounts: []MountSpec{
					{Source: "/tmp/workspace", Target: "/app", ReadOnly: true},
				},
			},
			wantErr: []string{"host", "does not support mounts", "/tmp/workspace -> /app"},
		},
		{
			name:         "network mode rejected outside supported set",
			providerName: ProviderHost,
			capabilities: NewHostProvider().Capabilities(),
			spec: TerrariumSpec{
				NetworkMode: NetworkModeNone,
			},
			wantErr: []string{"host", "network mode \"none\"", "supported: host"},
		},
		{
			name:         "image rejected when provider lacks image support",
			providerName: ProviderFirecracker,
			capabilities: NewFirecrackerProvider().Capabilities(),
			spec: TerrariumSpec{
				Image: "opentendril-go-verifier:latest",
			},
			wantErr: []string{"firecracker", "does not support selecting an image", "opentendril-go-verifier:latest"},
		},
		{
			name:         "verifier-shaped spec rejected by firecracker capabilities",
			providerName: ProviderFirecracker,
			capabilities: NewFirecrackerProvider().Capabilities(),
			spec: TerrariumSpec{
				Image:       "opentendril-go-verifier:latest",
				WorkingDir:  "/app",
				NetworkMode: NetworkModeNone,
				Mounts: []MountSpec{
					{Source: "/tmp/workspace", Target: "/app", ReadOnly: true},
				},
			},
			wantErr: []string{"firecracker", "does not support mounts"},
		},
		{
			name:         "zero-valued spec is permissive",
			providerName: ProviderHost,
			capabilities: NewHostProvider().Capabilities(),
			spec:         TerrariumSpec{Command: []string{"echo", "hello"}},
		},
		{
			name:         "empty network mode is permissive even with no supported modes",
			providerName: ProviderFirecracker,
			capabilities: TerrariumCapabilities{},
			spec:         TerrariumSpec{WorkingDir: "/root"},
		},
		{
			name:         "valid docker spec passes unchanged",
			providerName: ProviderDocker,
			capabilities: NewDockerProvider().Capabilities(),
			spec: TerrariumSpec{
				Image:       "opentendril-go:latest",
				WorkingDir:  "/app",
				NetworkMode: NetworkModeBridge,
				Mounts: []MountSpec{
					{Source: "/tmp/workspace", Target: "/app"},
					{Source: "/tmp/cache", Target: "/cache", ReadOnly: true},
				},
			},
		},
		{
			name:         "unknown network mode rejected even by docker",
			providerName: ProviderDocker,
			capabilities: NewDockerProvider().Capabilities(),
			spec: TerrariumSpec{
				Image:       "opentendril-go:latest",
				NetworkMode: NetworkMode("vpn"),
			},
			wantErr: []string{"docker", "network mode \"vpn\"", "supported: bridge, host, none"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSpecAgainstCapabilities(tt.providerName, tt.capabilities, tt.spec)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("ValidateSpecAgainstCapabilities returned error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ValidateSpecAgainstCapabilities returned nil, want error")
			}
			for _, needle := range tt.wantErr {
				if !strings.Contains(err.Error(), needle) {
					t.Fatalf("error = %q, want it to contain %q", err, needle)
				}
			}
		})
	}
}

// recordingProvider counts Create calls so tests can prove the validating
// decorator rejects an unsupported TerrariumSpec before the provider runs.
type recordingProvider struct {
	createCalls int
}

func (p *recordingProvider) Name() string {
	return "recording"
}

func (p *recordingProvider) Capabilities() TerrariumCapabilities {
	return TerrariumCapabilities{}
}

func (p *recordingProvider) Create(ctx context.Context, spec TerrariumSpec) (Terrarium, error) {
	p.createCalls++
	return nil, nil
}

func TestValidatingProviderInterceptsCreate(t *testing.T) {
	recording := &recordingProvider{}
	provider := newValidatingProvider(recording)

	if provider.Name() != "recording" {
		t.Fatalf("Name() = %q, want recording", provider.Name())
	}

	_, err := provider.Create(context.Background(), TerrariumSpec{
		Mounts: []MountSpec{{Source: "/tmp/workspace", Target: "/app"}},
	})
	if err == nil {
		t.Fatal("Create returned nil error for unsupported mounts, want error")
	}
	if recording.createCalls != 0 {
		t.Fatalf("provider Create ran %d time(s) despite the violation, want 0", recording.createCalls)
	}

	if _, err := provider.Create(context.Background(), TerrariumSpec{}); err != nil {
		t.Fatalf("Create returned error for a permissive zero-valued spec: %v", err)
	}
	if recording.createCalls != 1 {
		t.Fatalf("provider Create ran %d time(s) for a valid spec, want 1", recording.createCalls)
	}
}

func TestNewProviderEnforcesCapabilitiesAtCreate(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "true")

	provider, err := NewProvider(context.Background(), ProviderHost)
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}

	_, err = provider.Create(context.Background(), TerrariumSpec{
		Command:     []string{"echo", "hello"},
		NetworkMode: NetworkModeNone,
	})
	if err == nil {
		t.Fatal("Create returned nil error for host provider with network mode none, want error")
	}
	for _, needle := range []string{"host", "network mode \"none\""} {
		if !strings.Contains(err.Error(), needle) {
			t.Fatalf("error = %q, want it to contain %q", err, needle)
		}
	}
}

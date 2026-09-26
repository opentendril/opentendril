package conductor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"github.com/opentendril/opentendril/roots/llm"
)

// successfulTerrariumProvider stands in for a provider the existing factory
// has already selected. Create succeeds without launching a runtime, and
// Name returns the selected value unchanged.
type successfulTerrariumProvider struct {
	name      string
	createErr error
}

func (p *successfulTerrariumProvider) Name() string { return p.name }

func (p *successfulTerrariumProvider) Capabilities() terrarium.TerrariumCapabilities {
	return terrarium.TerrariumCapabilities{
		SupportsMounts: true,
		SupportsImages: true,
		SupportedNetworkModes: []terrarium.NetworkMode{
			terrarium.NetworkModeNone,
			terrarium.NetworkModeHost,
			terrarium.NetworkModeBridge,
		},
	}
}

func (p *successfulTerrariumProvider) Create(context.Context, terrarium.TerrariumSpec) (terrarium.Terrarium, error) {
	if p.createErr != nil {
		return nil, p.createErr
	}
	return &stubTerrarium{}, nil
}

func stubSproutBeforeTerrarium(t *testing.T, mount string) {
	t.Helper()
	originalPreflight := runSproutPreflightChecksFn
	originalShadow := createShadowWorktreeFn
	originalRepoMap := generateRepoMapFn
	originalEnsure := ensureSproutImageFn
	originalNewSprout := newSproutFn
	originalProvider := terrariumNewProviderFn
	originalProbe := probeProviderAuthFn
	t.Cleanup(func() {
		runSproutPreflightChecksFn = originalPreflight
		createShadowWorktreeFn = originalShadow
		generateRepoMapFn = originalRepoMap
		ensureSproutImageFn = originalEnsure
		newSproutFn = originalNewSprout
		terrariumNewProviderFn = originalProvider
		probeProviderAuthFn = originalProbe
	})
	runSproutPreflightChecksFn = func(context.Context, *llm.Client) error { return nil }
	createShadowWorktreeFn = func(string, string) (string, error) { return mount, nil }
	generateRepoMapFn = func(context.Context, string) (string, error) { return "", nil }
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	probeProviderAuthFn = func(context.Context, *llm.Client) error { return nil }
	newSproutFn = func(context.Context, string, string, string, llmCaller, toolSession, *eventbus.Bus, string, string, string) (sproutRunner, error) {
		return &mockSproutRunner{response: "done"}, nil
	}
}

func TestStartTerrariumSessionReportsProviderThatCreatedIt(t *testing.T) {
	original := terrariumNewProviderFn
	t.Cleanup(func() { terrariumNewProviderFn = original })

	for _, name := range []string{
		terrarium.ProviderDocker,
		terrarium.ProviderGVisor,
		terrarium.ProviderFirecracker,
		terrarium.ProviderHost,
	} {
		t.Run(name, func(t *testing.T) {
			var requested string
			terrariumNewProviderFn = func(_ context.Context, got string, _ ...terrarium.ActivationObserver) (terrarium.TerrariumProvider, error) {
				requested = got
				return &successfulTerrariumProvider{name: got}, nil
			}
			session, err := startTerrariumSession(context.Background(), name, "test-image", t.TempDir(), false, []string{"true"}, nil, time.Minute)
			if err != nil {
				t.Fatalf("startTerrariumSession: %v", err)
			}
			if requested != name {
				t.Fatalf("factory received %q, want %q", requested, name)
			}
			if got := observedTerrariumProvider(session); got != name {
				t.Fatalf("observed provider = %q, want %q", got, name)
			}
		})
	}
}

func TestStartTerrariumSessionOmitsProviderWhenCreateFails(t *testing.T) {
	original := terrariumNewProviderFn
	t.Cleanup(func() { terrariumNewProviderFn = original })
	terrariumNewProviderFn = func(context.Context, string, ...terrarium.ActivationObserver) (terrarium.TerrariumProvider, error) {
		return &successfulTerrariumProvider{name: terrarium.ProviderDocker, createErr: errors.New("create failed")}, nil
	}
	session, err := startTerrariumSession(context.Background(), terrarium.ProviderDocker, "test-image", t.TempDir(), false, []string{"true"}, nil, time.Minute)
	if err == nil {
		t.Fatal("expected terrarium creation to fail")
	}
	if session != nil {
		t.Fatalf("failed creation returned a session: %#v", session)
	}
	if got := observedTerrariumProvider(session); got != "" {
		t.Fatalf("failed creation observed %q", got)
	}
}

func TestRunSproutRecordsActualTerrariumProvider(t *testing.T) {
	for _, name := range []string{terrarium.ProviderDocker, terrarium.ProviderGVisor, terrarium.ProviderFirecracker} {
		t.Run(name, func(t *testing.T) {
			mount := t.TempDir()
			stubSproutBeforeTerrarium(t, mount)
			t.Setenv(terrariumProviderEnvKey, name)

			var requested string
			var observed []string
			var sproutStarted bool
			terrariumNewProviderFn = func(ctx context.Context, got string, observers ...terrarium.ActivationObserver) (terrarium.TerrariumProvider, error) {
				requested = got
				if name == terrarium.ProviderDocker {
					provider, err := terrarium.NewProvider(ctx, got, observers...)
					if err != nil {
						return nil, err
					}
					if provider.Name() != terrarium.ProviderDocker {
						t.Fatalf("docker factory name = %q", provider.Name())
					}
					return &successfulTerrariumProvider{name: provider.Name()}, nil
				}
				return &successfulTerrariumProvider{name: got}, nil
			}
			newSproutFn = func(context.Context, string, string, string, llmCaller, toolSession, *eventbus.Bus, string, string, string) (sproutRunner, error) {
				sproutStarted = true
				if len(observed) != 1 || observed[0] != name {
					t.Fatalf("provider before sprout turn = %v, want [%s]", observed, name)
				}
				t.Setenv(terrariumProviderEnvKey, terrarium.ProviderHost)
				return &mockSproutRunner{response: "done"}, nil
			}

			orch := NewDockerOrchestrator()
			orch.Substrate = mount
			orch.StepID = "boundary-step"
			orch.DisableMergeBack = true
			orch.OnTerrariumCreated = func(providerName string) {
				if sproutStarted {
					t.Fatal("terrarium provider was reported after the sprout turn started")
				}
				observed = append(observed, providerName)
			}

			report, err := orch.RunSprout(context.Background(), "test prompt")
			if err != nil {
				t.Fatalf("RunSprout: %v", err)
			}
			if requested != name {
				t.Fatalf("selected provider = %q, want %q", requested, name)
			}
			if len(observed) != 1 || observed[0] != name {
				t.Fatalf("observed providers = %v, want [%s]", observed, name)
			}
			if report.TerrariumProvider != name {
				t.Fatalf("terminal report provider = %q, want %q", report.TerrariumProvider, name)
			}
		})
	}
}

func TestRunSproutRecordsHostProviderAfterExplicitActivation(t *testing.T) {
	mount := t.TempDir()
	stubSproutBeforeTerrarium(t, mount)
	t.Setenv(terrarium.EnvAllowHostExecution, "true")
	t.Setenv(terrariumProviderEnvKey, terrarium.ProviderHost)

	bus := eventbus.New()
	var audits int
	bus.Subscribe(eventbus.EventHostExecutionActivated, func(eventbus.Event) { audits++ })

	var observed []string
	terrariumNewProviderFn = func(ctx context.Context, name string, observers ...terrarium.ActivationObserver) (terrarium.TerrariumProvider, error) {
		provider, err := terrarium.NewProvider(ctx, name, observers...)
		if err != nil {
			return nil, err
		}
		return &successfulTerrariumProvider{name: provider.Name()}, nil
	}

	orch := NewDockerOrchestrator()
	orch.Substrate = mount
	orch.StepID = "host-boundary"
	orch.DisableMergeBack = true
	orch.EventBus = bus
	orch.OnTerrariumCreated = func(providerName string) {
		observed = append(observed, providerName)
	}

	report, err := orch.RunSprout(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}
	if audits != 1 {
		t.Fatalf("host activation events = %d, want 1", audits)
	}
	if len(observed) != 1 || observed[0] != terrarium.ProviderHost {
		t.Fatalf("observed providers = %v, want [host]", observed)
	}
	if report.TerrariumProvider != terrarium.ProviderHost {
		t.Fatalf("terminal report provider = %q, want host", report.TerrariumProvider)
	}
}

func TestRunSproutDoesNotRecordProviderWhenTerrariumCreateFails(t *testing.T) {
	mount := t.TempDir()
	stubSproutBeforeTerrarium(t, mount)
	t.Setenv(terrariumProviderEnvKey, terrarium.ProviderDocker)

	var observed []string
	terrariumNewProviderFn = func(context.Context, string, ...terrarium.ActivationObserver) (terrarium.TerrariumProvider, error) {
		return &successfulTerrariumProvider{name: terrarium.ProviderDocker, createErr: errors.New("create failed")}, nil
	}
	newSproutFn = func(context.Context, string, string, string, llmCaller, toolSession, *eventbus.Bus, string, string, string) (sproutRunner, error) {
		t.Fatal("sprout turn started after terrarium creation failed")
		return nil, errors.New("unreachable")
	}

	orch := NewDockerOrchestrator()
	orch.Substrate = mount
	orch.StepID = "create-failed"
	orch.DisableMergeBack = true
	orch.OnTerrariumCreated = func(providerName string) {
		observed = append(observed, providerName)
	}

	report, err := orch.RunSprout(context.Background(), "test prompt")
	if err == nil {
		t.Fatal("expected terrarium creation to fail")
	}
	if len(observed) != 0 {
		t.Fatalf("creation failure recorded %v", observed)
	}
	if report.TerrariumProvider != "" {
		t.Fatalf("terminal report provider = %q, want absent", report.TerrariumProvider)
	}
}

func TestRunSproutHostActivationDoesNotFabricateProviderWhenCreateFails(t *testing.T) {
	mount := t.TempDir()
	stubSproutBeforeTerrarium(t, mount)
	t.Setenv(terrarium.EnvAllowHostExecution, "true")
	t.Setenv(terrariumProviderEnvKey, terrarium.ProviderHost)

	bus := eventbus.New()
	var audits int
	bus.Subscribe(eventbus.EventHostExecutionActivated, func(eventbus.Event) { audits++ })

	var observed []string
	orch := NewDockerOrchestrator()
	orch.Substrate = mount
	orch.StepID = "host-create-failed"
	orch.DisableMergeBack = true
	orch.EventBus = bus
	orch.OnTerrariumCreated = func(providerName string) {
		observed = append(observed, providerName)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	report, err := orch.RunSprout(ctx, "test prompt")
	if err == nil {
		t.Fatal("expected host terrarium creation to fail")
	}
	if audits != 1 {
		t.Fatalf("host activation events = %d, want 1", audits)
	}
	if len(observed) != 0 {
		t.Fatalf("failed host creation recorded %v", observed)
	}
	if report.TerrariumProvider != "" {
		t.Fatalf("terminal report provider = %q, want absent", report.TerrariumProvider)
	}
}

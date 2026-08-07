package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// chatCapture records what an endpoint was actually asked for. Selection tests
// assert against this rather than against the spec alone: a spec whose Provider
// field reads "google" proves only that a struct field was assigned, and the
// question this whole seam exists to answer is which provider was billed for
// which model.
type chatCapture struct {
	path       string
	authHeader string
	model      string
}

// captureChatEndpoint stands up an OpenAI-shaped chat endpoint that records one
// request and answers it. hits counts every request that reached it, including
// ones the test expects never to arrive.
func captureChatEndpoint(t *testing.T, hits *int64, captured chan<- chatCapture) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(hits, 1)

		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode chat request: %v", err)
		}
		select {
		case captured <- chatCapture{path: r.URL.Path, authHeader: r.Header.Get("Authorization"), model: body.Model}:
		default:
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// A provider the operator named must be the provider that receives the request.
// The assertion runs all the way to the wire on purpose: with keys present for
// three providers and none of them pinned to a model, selection used to build
// the spec from whichever model it liked best across all of them, and every
// field of that spec was self-consistent afterwards.
func TestConfiguredProviderReceivesTheRequest(t *testing.T) {
	chdirWithoutTendrilConfig(t)
	clearProviderKeys(t)
	clearTierModelEnv(t, "google")

	var googleHits, strayHits int64
	captured := make(chan chatCapture, 1)
	google := captureChatEndpoint(t, &googleHits, captured)
	stray := captureChatEndpoint(t, &strayHits, make(chan chatCapture, 1))

	// Keys for three providers, so selection has somewhere to wander to, and
	// the two it must not choose answer on a server that counts arrivals.
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("GOOGLE_BASE_URL", google.URL)
	t.Setenv("ANTHROPIC_BASE_URL", stray.URL)
	t.Setenv("OPENAI_BASE_URL", stray.URL)
	t.Setenv("DEFAULT_LLM_PROVIDER", "google")

	spec := ResolveTierProviderSpec(TierPremium)
	if spec.ResolutionErr != nil {
		t.Fatalf("spec.ResolutionErr = %v, want nil", spec.ResolutionErr)
	}
	if spec.Provider != "google" {
		t.Fatalf("spec.Provider = %q, want google", spec.Provider)
	}
	if spec.Model != "gemini-3.1-pro" {
		t.Fatalf("spec.Model = %q, want gemini-3.1-pro (the best google model under a premium ceiling)", spec.Model)
	}

	if _, err := NewClient(spec).CallPrompt(context.Background(), "system", "user"); err != nil {
		t.Fatalf("CallPrompt failed: %v", err)
	}

	if got := atomic.LoadInt64(&strayHits); got != 0 {
		t.Fatalf("%d requests reached a provider the operator did not configure", got)
	}
	if got := atomic.LoadInt64(&googleHits); got != 1 {
		t.Fatalf("google endpoint received %d requests, want 1", got)
	}

	request := <-captured
	if request.model != spec.Model {
		t.Fatalf("request model = %q, want %q (the resolved model must be the one on the wire)", request.model, spec.Model)
	}
	if request.authHeader != "Bearer google-key" {
		t.Fatalf("request Authorization = %q, want the google key", request.authHeader)
	}
	if request.path != "/chat/completions" {
		t.Fatalf("request path = %q, want /chat/completions", request.path)
	}
}

// A configured provider that serves nothing usable must fail, naming the
// provider and what was missing. Resolving elsewhere is the defect: the run
// then succeeds, bills a provider nobody chose, and no record can say which.
func TestConfiguredProviderWithNoUsableModelFailsLoudly(t *testing.T) {
	chdirWithoutTendrilConfig(t)
	clearProviderKeys(t)
	clearTierModelEnv(t, "grok")

	var strayHits int64
	stray := captureChatEndpoint(t, &strayHits, make(chan chatCapture, 1))

	// grok serves one model and it is premium-tier, so a cheapest ceiling
	// leaves it with nothing — while anthropic, whose key is present, has a
	// cheapest-tier model waiting.
	t.Setenv("GROK_API_KEY", "grok-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("GROK_BASE_URL", stray.URL)
	t.Setenv("ANTHROPIC_BASE_URL", stray.URL)
	t.Setenv("DEFAULT_LLM_PROVIDER", "grok")

	spec := ResolveTierProviderSpec(TierCheapest)
	if spec.ResolutionErr == nil {
		t.Fatalf("resolution succeeded with provider %q model %q; want a failure naming grok", spec.Provider, spec.Model)
	}
	if !errors.Is(spec.ResolutionErr, ErrNoModelAvailable) {
		t.Fatalf("ResolutionErr = %v, want it to wrap ErrNoModelAvailable", spec.ResolutionErr)
	}
	message := spec.ResolutionErr.Error()
	if !strings.Contains(message, "grok") {
		t.Fatalf("ResolutionErr = %q, want it to name the configured provider", message)
	}
	if !strings.Contains(message, "cheapest") {
		t.Fatalf("ResolutionErr = %q, want it to name what was missing", message)
	}
	if spec.Model != "" {
		t.Fatalf("spec.Model = %q, want empty on a failed resolution", spec.Model)
	}

	// The failure has to survive as far as a request. A spec that carried an
	// error but still called would be a silent success, which is what this
	// replaces.
	_, err := NewClient(spec).CallPrompt(context.Background(), "system", "user")
	if !errors.Is(err, ErrNoModelAvailable) {
		t.Fatalf("CallPrompt error = %v, want the resolution failure", err)
	}
	if got := atomic.LoadInt64(&strayHits); got != 0 {
		t.Fatalf("%d requests were sent despite a failed resolution", got)
	}
}

// A relaxation for an autonomous run may raise the cost ceiling, and as a last
// resort may drop the tool requirement. It may not drop the provider: doing so
// would reintroduce the same silent hop this seam exists to close, through the
// one path that runs without a Botanist watching.
func TestAutonomousToolFallbackStaysOnTheConfiguredProvider(t *testing.T) {
	chdirWithoutTendrilConfig(t)
	clearProviderKeys(t)
	clearTierModelEnv(t, "local")
	withLocalInference(t)

	// Under a cheapest ceiling the only local model is llama3.2, which does not
	// drive tools — so RequiresToolUse matches nothing under the ceiling and a
	// relaxation fires. anthropic, whose key is present, has a cheapest-tier
	// model that DOES drive tools, and is exactly where an unconstrained
	// fallback would go.
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")

	spec := ResolveAgentTierProviderSpec(TierCheapest)
	if spec.ResolutionErr != nil {
		t.Fatalf("spec.ResolutionErr = %v, want nil", spec.ResolutionErr)
	}
	if spec.Provider != "local" {
		t.Fatalf("spec.Provider = %q, want local (a relaxation must never leave the chosen provider)", spec.Provider)
	}
	// The relaxation that fires is the ceiling rising, not the tool requirement
	// falling — so the answer is the provider's tool-capable model rather than
	// its cheapest one. Which of the two gives way is asserted in its own test.
	if spec.Model != "qwen3.5:9b" {
		t.Fatalf("spec.Model = %q, want qwen3.5:9b", spec.Model)
	}
}

// Nothing configured still resolves: a lone API key names its provider, and the
// best model that provider serves under the ceiling is the answer. A detected
// provider is a starting point rather than an instruction, so it does not
// constrain selection the way a configured one does.
func TestUnconfiguredProviderStillDefaults(t *testing.T) {
	chdirWithoutTendrilConfig(t)
	clearProviderKeys(t)
	clearTierModelEnv(t, "openai")
	t.Setenv("DEFAULT_LLM_PROVIDER", "")
	t.Setenv("OPENAI_API_KEY", "openai-key")

	spec := ResolveTierProviderSpec(TierPremium)
	if spec.ResolutionErr != nil {
		t.Fatalf("spec.ResolutionErr = %v, want nil", spec.ResolutionErr)
	}
	if spec.Provider != "openai" || spec.Model != "gpt-5.6-terra" {
		t.Fatalf("spec = %s/%s, want openai/gpt-5.6-terra", spec.Provider, spec.Model)
	}

	autonomousSpec := ResolveAgentTierProviderSpec(TierPremium)
	if autonomousSpec.ResolutionErr != nil {
		t.Fatalf("autonomous spec.ResolutionErr = %v, want nil", autonomousSpec.ResolutionErr)
	}
	if autonomousSpec.Provider != "openai" {
		t.Fatalf("autonomous spec.Provider = %q, want openai", autonomousSpec.Provider)
	}
}

// The local provider is offered only where a local inference endpoint has been
// declared. It used to be seeded unconditionally, which made a local model a
// candidate — and, under a ceiling that preferred cheap models, the preferred
// candidate — on every installation that has never run one.
func TestLocalProviderRequiresADeclaredEndpoint(t *testing.T) {
	t.Run("absent without an endpoint", func(t *testing.T) {
		chdirWithoutTendrilConfig(t)
		clearProviderKeys(t)

		for _, provider := range AvailableProviders() {
			if provider == "local" {
				t.Fatalf("AvailableProviders() = %v, want no local entry when no endpoint is declared", AvailableProviders())
			}
		}
	})

	t.Run("present once an endpoint is declared", func(t *testing.T) {
		chdirWithoutTendrilConfig(t)
		clearProviderKeys(t)
		withLocalInference(t)

		found := false
		for _, provider := range AvailableProviders() {
			if provider == "local" {
				found = true
			}
		}
		if !found {
			t.Fatalf("AvailableProviders() = %v, want a local entry once LOCAL_INFERENCE_URL is set", AvailableProviders())
		}
	})

	t.Run("present once config declares the provider", func(t *testing.T) {
		clearProviderKeys(t)
		chdirWithTendrilConfig(t, `
llm:
  providers:
    local:
      base-url: http://127.0.0.1:11434/v1
`)

		if !LocalProviderAvailable() {
			t.Fatalf("LocalProviderAvailable() = false, want true when config declares a local provider")
		}
	})

	// The consequence that matters: an autonomous run on a machine with one
	// hosted key must not be handed a local model it can never reach.
	t.Run("selection never lands on local without an endpoint", func(t *testing.T) {
		chdirWithoutTendrilConfig(t)
		clearProviderKeys(t)
		clearTierModelEnv(t, "anthropic")
		t.Setenv("DEFAULT_LLM_PROVIDER", "")
		t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

		spec := ResolveAgentTierProviderSpec(TierCheapest)
		if spec.ResolutionErr != nil {
			t.Fatalf("spec.ResolutionErr = %v, want nil", spec.ResolutionErr)
		}
		if spec.Provider != "anthropic" || spec.Model != "claude-haiku-4-5" {
			t.Fatalf("spec = %s/%s, want anthropic/claude-haiku-4-5", spec.Provider, spec.Model)
		}
	})
}

// MaxCostTier is a ceiling and the sort must agree with it: the ceiling decides
// what is admissible, and the best admissible model is the answer. Sorting the
// other way made asking for premium a way of getting the cheapest model on the
// shelf, so the tier a caller chose could never be the tier it got.
func TestMaxCostTierIsACeilingTheSortRespects(t *testing.T) {
	chdirWithoutTendrilConfig(t)
	clearProviderKeys(t)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	wanted := map[ModelTier]string{
		TierPremium:  "claude-opus-4-8",
		TierStandard: "claude-sonnet-5",
		TierCheapest: "claude-haiku-4-5",
	}
	for tier, want := range wanted {
		model, err := SelectBestModel(Capabilities{MaxCostTier: tier})
		if err != nil {
			t.Fatalf("SelectBestModel(%s) failed: %v", tier, err)
		}
		if model.Name != want {
			t.Fatalf("SelectBestModel(%s) = %q, want %q", tier, model.Name, want)
		}
		if costTierRank(model.CostTier) > costTierRank(tier) {
			t.Fatalf("SelectBestModel(%s) returned %s-tier %q, which is above the ceiling", tier, model.CostTier, model.Name)
		}
	}
}

// An autonomous run has two constraints that are not equal, and the order they
// are given up in decides whether the run does anything at all. A model that
// cannot drive tools does not do cheap work — it returns prose or an empty
// completion and the sprout matures having achieved nothing. A model above the
// intended tier still does the work.
//
// A local-only installation is where this bites: its one tool-capable model is
// standard-tier, so any cheaper ceiling has nothing to offer under it. The
// ceiling must give way, not the tool requirement.
func TestAgentTierRaisesTheCeilingBeforeGivingUpToolCapability(t *testing.T) {
	chdirWithoutTendrilConfig(t)
	clearProviderKeys(t)
	withLocalInference(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	clearTierModelEnv(t, "local")

	spec := ResolveAgentTierProviderSpec(TierCheapest)
	if spec.ResolutionErr != nil {
		t.Fatalf("spec.ResolutionErr = %v, want nil", spec.ResolutionErr)
	}
	if spec.Provider != "local" {
		t.Fatalf("spec.Provider = %q, want local", spec.Provider)
	}
	if spec.Model != "qwen3.5:9b" {
		t.Fatalf("spec.Model = %q, want qwen3.5:9b — the ceiling should rise, not the tool requirement fall", spec.Model)
	}

	// Asserted against the registry rather than against the name, so this keeps
	// meaning what it says if the local entries are ever re-tiered.
	var drivesTools bool
	var found bool
	for _, model := range FallbackModels {
		if model.Provider == spec.Provider && model.Name == spec.Model {
			found, drivesTools = true, model.DrivesTools
		}
	}
	if !found {
		t.Fatalf("resolved model %q is not in the registry", spec.Model)
	}
	if !drivesTools {
		t.Fatalf("resolved model %q cannot drive tools; an autonomous run on it does nothing", spec.Model)
	}

	// Resolution that does not require tools is deliberately unchanged: only an
	// autonomous run carries the requirement that forces the ceiling up.
	plain := ResolveTierProviderSpec(TierCheapest)
	if plain.Model != "llama3.2" {
		t.Fatalf("ResolveTierProviderSpec(cheapest) = %q, want llama3.2 — the ceiling still binds where tools are not required", plain.Model)
	}
}

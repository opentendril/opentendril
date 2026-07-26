package conductor

import (
	"context"
	"testing"

	"github.com/opentendril/opentendril/roots/llm"
)

// TestAssessTaskComplexityUsesAssessorSeam proves AssessTaskComplexity goes
// through the injectable newAssessorClientFn seam rather than a concrete
// roots/llm client, so the classification path can be exercised without a
// real network call.
func TestAssessTaskComplexityUsesAssessorSeam(t *testing.T) {
	original := newAssessorClientFn
	t.Cleanup(func() { newAssessorClientFn = original })

	fake := &fakeLLM{response: `{"tier":"standard"}`}
	newAssessorClientFn = func() llmCaller { return fake }

	tier, err := AssessTaskComplexity(context.Background(), "  implement the widget  ")
	if err != nil {
		t.Fatalf("AssessTaskComplexity returned error: %v", err)
	}
	if tier != llm.TierStandard {
		t.Fatalf("tier = %q, want %q", tier, llm.TierStandard)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 LLM call via the seam, got %d", len(fake.calls))
	}
	if got := fake.calls[0][1].Content; got != "implement the widget" {
		t.Fatalf("user prompt = %q, want trimmed transcript", got)
	}
}

// TestAssessTaskComplexityPropagatesSeamError proves a failure from the
// injected client surfaces as an error rather than being swallowed.
func TestAssessTaskComplexityPropagatesSeamError(t *testing.T) {
	original := newAssessorClientFn
	t.Cleanup(func() { newAssessorClientFn = original })

	newAssessorClientFn = func() llmCaller { return &stubBranchingClient{err: context.DeadlineExceeded} }

	if _, err := AssessTaskComplexity(context.Background(), "transcript"); err == nil {
		t.Fatal("expected AssessTaskComplexity to return an error when the seam fails")
	}
}

// TestRouteTaskUsesDynamicRouterSeam proves RouteTask's dynamic-router branch
// calls through the injectable newRouterClientFn seam rather than a concrete
// roots/llm client. The environment is set so the internal router is neither
// bypassed (a provider name that carries no strict model configuration) nor
// starved of routable options (two distinct local models in the registry).
func TestRouteTaskUsesDynamicRouterSeam(t *testing.T) {
	original := newRouterClientFn
	t.Cleanup(func() { newRouterClientFn = original })

	t.Setenv("DEFAULT_LLM_PROVIDER", "stub-router-test-provider")
	t.Setenv("DEFAULT_MODEL_NAME", "")

	fake := &fakeLLM{response: `{"provider":"local","model":"model-b"}`}
	newRouterClientFn = func() llmCaller { return fake }

	registry := []llm.ModelDefinition{
		{Provider: "local", Name: "model-a"},
		{Provider: "local", Name: "model-b"},
	}

	got, err := RouteTask(context.Background(), "task transcript", llm.Capabilities{}, registry)
	if err != nil {
		t.Fatalf("RouteTask returned error: %v", err)
	}

	want := llm.RouteSelection{Provider: "local", Model: "model-b"}
	if got != want {
		t.Fatalf("RouteTask = %+v, want %+v", got, want)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 LLM call via the router seam, got %d", len(fake.calls))
	}
}

func TestParseRouterResponse(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantProv  string
		wantModel string
		wantErr   bool
	}{
		{name: "valid", text: `{"provider":"grok","model":"grok-beta"}`, wantProv: "grok", wantModel: "grok-beta"},
		{name: "whitespace", text: "\n {\"provider\":\"openai\",\"model\":\"gpt-4o\"}\n", wantProv: "openai", wantModel: "gpt-4o"},
		{name: "malformed", text: `{"provider":`, wantErr: true},
		{name: "missing provider", text: `{"model":"gpt-4o"}`, wantErr: true},
		{name: "missing model", text: `{"provider":"openai"}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRouterResponse(tt.text)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRouterResponse(%q) returned nil error", tt.text)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRouterResponse(%q) returned error: %v", tt.text, err)
			}
			if got.Provider != tt.wantProv {
				t.Fatalf("provider = %q, want %q", got.Provider, tt.wantProv)
			}
			if got.Model != tt.wantModel {
				t.Fatalf("model = %q, want %q", got.Model, tt.wantModel)
			}
		})
	}
}

func TestParseAssessorResponse(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    llm.ModelTier
		wantErr bool
	}{
		{name: "premium", text: `{"tier":"premium"}`, want: llm.TierPremium},
		{name: "standard", text: `{"tier":"standard"}`, want: llm.TierStandard},
		{name: "cheapest", text: `{"tier":"cheapest"}`, want: llm.TierCheapest},
		{name: "whitespace", text: "\n\t {\"tier\":\"standard\"} \n", want: llm.TierStandard},
		{name: "malformed", text: `{"tier":`, wantErr: true},
		{name: "invalid tier", text: `{"tier":"expensive"}`, wantErr: true},
		{name: "missing tier", text: `{}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAssessorResponse(tt.text)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseAssessorResponse(%q) returned nil error", tt.text)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAssessorResponse(%q) returned error: %v", tt.text, err)
			}
			if got != tt.want {
				t.Fatalf("parseAssessorResponse(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

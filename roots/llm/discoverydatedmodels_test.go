package llm

import "testing"

// registryEntry returns a known static registry entry, failing the test if the
// registry no longer carries it. Reading the expectation from the registry
// rather than restating it keeps these tests measuring the lookup rather than a
// copy of the table that can drift away from it.
func registryEntry(t *testing.T, provider, name string) ModelDefinition {
	t.Helper()
	for _, model := range FallbackModels {
		if model.Provider == provider && model.Name == name {
			return model
		}
	}
	t.Fatalf("registry no longer carries %s/%s; this test's premise is gone", provider, name)
	return ModelDefinition{}
}

// TestEnrichKeepsRegistryKnowledgeForDatedIdentifiers is the defect this file
// exists for. Anthropic serves claude-haiku-4-5 under the dated identifier
// claude-haiku-4-5-20251001. The exact-match lookup missed, the model fell
// through to name inference — which cannot infer tool capability — and arrived
// declared unable to drive tools. It was the only model at the cheapest tier, so
// nothing tool-capable survived that ceiling and every autonomous run silently
// escalated to the most expensive model the provider serves.
//
// Mutation target: remove the undated-base-name lookup → the dated identifier
// loses DrivesTools and this test fails.
func TestEnrichKeepsRegistryKnowledgeForDatedIdentifiers(t *testing.T) {
	known := registryEntry(t, "anthropic", "claude-haiku-4-5")
	if !known.DrivesTools {
		t.Fatal("the registry no longer declares claude-haiku-4-5 tool-capable; this test's premise is gone")
	}

	got := enrichModelDefinition("anthropic", "claude-haiku-4-5-20251001")

	if !got.DrivesTools {
		t.Error("a dated identifier lost DrivesTools; it empties the cheapest tier of tool-capable models")
	}
	if got.CostTier != known.CostTier {
		t.Errorf("CostTier = %q, want %q from the registry", got.CostTier, known.CostTier)
	}
	if got.ContextSize != known.ContextSize {
		t.Errorf("ContextSize = %d, want %d from the registry", got.ContextSize, known.ContextSize)
	}
}

// TestEnrichReportsTheIdentifierTheProviderServes pins that inheriting the
// registry's knowledge does not rewrite the model's name. The dated identifier
// is what goes on the wire and what a run record must report; substituting the
// undated alias would make every run misreport which model served it — trading
// one silent wrong answer for another.
//
// Mutation target: return the registry entry unchanged (dropping known.Name =
// name) → the undated alias is reported and this test fails.
func TestEnrichReportsTheIdentifierTheProviderServes(t *testing.T) {
	got := enrichModelDefinition("anthropic", "claude-haiku-4-5-20251001")

	if got.Name != "claude-haiku-4-5-20251001" {
		t.Errorf("Name = %q, want the identifier the provider serves", got.Name)
	}
}

// TestEnrichLeavesExactMatchesAlone pins that the new path is a fallback. An
// identifier already in the registry must still be found by the exact match,
// unchanged.
//
// Mutation target: strip the date before the exact-match loop rather than after
// it → an undated name still resolves, so this test alone would not catch it;
// it is here to prove the addition disturbed nothing, not as the primary pin.
func TestEnrichLeavesExactMatchesAlone(t *testing.T) {
	known := registryEntry(t, "anthropic", "claude-opus-4-8")

	got := enrichModelDefinition("anthropic", "claude-opus-4-8")

	if got.Name != known.Name || got.DrivesTools != known.DrivesTools || got.CostTier != known.CostTier {
		t.Errorf("exact match returned %+v, want the registry entry %+v", got, known)
	}
}

// TestUndatedModelNameOnlyStripsAReleaseDate guards the boundary. The suffix is
// eight digits and anchored, so a version segment or a name that merely ends in
// digits keeps its identity — stripping those would collapse distinct models
// onto one registry entry, which is a worse failure than the one being fixed.
//
// Mutation target: relax the pattern to `-\d+$` → gpt-4-32k is unaffected but
// grok-3 style names are not; the cases below cover both directions.
func TestUndatedModelNameOnlyStripsAReleaseDate(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5"},
		{"claude-opus-4-5-20251101", "claude-opus-4-5"},
		{"claude-opus-4-8", "claude-opus-4-8"},
		{"gpt-4-32k", "gpt-4-32k"},
		{"grok-3", "grok-3"},
		{"llama3.2", "llama3.2"},
		{"model-2025100", "model-2025100"},     // seven digits: not a date
		{"model-202510011", "model-202510011"}, // nine digits: not a date
	} {
		if got := undatedModelName(tc.in); got != tc.want {
			t.Errorf("undatedModelName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestEnrichStillInfersForUnknownModels pins that an identifier the registry has
// never heard of keeps falling through to name inference. The base-name lookup
// must not swallow the unknown case.
//
// Mutation target: return a zero ModelDefinition when no registry entry matches
// → an unknown model loses its inferred tier and this test fails.
func TestEnrichStillInfersForUnknownModels(t *testing.T) {
	got := enrichModelDefinition("anthropic", "claude-imaginary-9-9-20990101")

	if got.Name != "claude-imaginary-9-9-20990101" {
		t.Errorf("Name = %q, want the identifier as served", got.Name)
	}
	if got.CostTier == "" {
		t.Error("an unknown model lost its inferred cost tier")
	}
}

package rhizome

import (
	"strings"
	"testing"
)

func TestWeaviateDeterministicUUID(t *testing.T) {
	id := deterministicUUID("owner/repo", "test memory")
	expected := "3777ddf4-ad6e-ce6f-043c-4e93ad6adb94"
	if id != expected {
		t.Fatalf("Expected deterministic object identity %q, got %q", expected, id)
	}
}

func TestWeaviateLegacyFallback(t *testing.T) {
	legacyMeta := map[string]any{
		"repositoryName": "owner/repo",
		"title":          "Legacy",
	}
	got, err := memoryFromMetadata(legacyMeta)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if got.Origin != OriginLegacy || got.Authority != AuthorityNone || got.Status != StatusUnclassified {
		t.Fatalf("Legacy fallback failed: %+v", got)
	}
}

func TestWeaviateMalformedFailsClosed(t *testing.T) {
	malformedMeta := map[string]any{
		"repositoryName": "owner/repo",
		"title":          "Malformed",
		"origin":         "botanist",
		"authority":      "botanist",
		"status":         "weird-status",
	}
	_, err := memoryFromMetadata(malformedMeta)
	if err == nil {
		t.Fatalf("Expected error for malformed status, got nil")
	}
	if !strings.Contains(err.Error(), "invalid memory status") {
		t.Fatalf("Expected invalid status error, got %v", err)
	}
}

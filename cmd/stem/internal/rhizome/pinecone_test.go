package rhizome

import (
	"strings"
	"testing"
	"time"
)

func TestPineconeEnvelopeRoundTrip(t *testing.T) {
	fullMem := Memory{
		RepositoryName:   "owner/repo",
		Title:            "Full Memory",
		Origin:           OriginSubstrate,
		Authority:        AuthorityDeterministic,
		Status:           StatusEstablished,
		Kind:             KindFact,
		Provenance:       "some-provenance",
		SourceClass:      "some-class",
		SourceIdentity:   "some-id",
		ContentIdentity:  "content-id",
		RevisionIdentity: "rev-id",
		StableID:         StableMemoryIdentity("owner/repo", "Full Memory"),
		RevisionMetadata: "meta",
		Supersession:     "super",
		CreatedAt:        time.Now().UTC(),
	}

	meta := pineconeMemoryMetadata(fullMem)

	got, err := memoryFromMetadata(meta)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if got.Origin != fullMem.Origin || got.Authority != fullMem.Authority || got.Status != fullMem.Status || got.Kind != fullMem.Kind ||
		got.Provenance != fullMem.Provenance || got.SourceClass != fullMem.SourceClass || got.SourceIdentity != fullMem.SourceIdentity ||
		got.ContentIdentity != fullMem.ContentIdentity || got.RevisionIdentity != fullMem.RevisionIdentity ||
		got.RevisionMetadata != fullMem.RevisionMetadata || got.Supersession != fullMem.Supersession {
		t.Fatalf("Envelope round-trip mismatch. want: %+v got: %+v", fullMem, got)
	}
	if got.StableID != StableMemoryIdentity(fullMem.RepositoryName, fullMem.Title) {
		t.Fatalf("StableID mismatch. want canonical, got %q", got.StableID)
	}
}

func TestPineconeLegacyFallback(t *testing.T) {
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

func TestPineconeStableIDLegacyNormalization(t *testing.T) {
	// Legacy metadata with no stableId must receive the canonical StableID.
	meta := map[string]any{
		"repositoryName": "owner/repo",
		"title":          "Legacy Title",
	}
	got, err := memoryFromMetadata(meta)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	canonical := StableMemoryIdentity("owner/repo", "Legacy Title")
	if got.StableID != canonical {
		t.Fatalf("Expected canonical StableID %q, got %q", canonical, got.StableID)
	}
}

func TestPineconeStableIDMatchingAccepted(t *testing.T) {
	// Stored stableId that matches the canonical value must be accepted.
	canonical := StableMemoryIdentity("owner/repo", "Match Title")
	meta := map[string]any{
		"repositoryName": "owner/repo",
		"title":          "Match Title",
		"stableId":       canonical,
	}
	got, err := memoryFromMetadata(meta)
	if err != nil {
		t.Fatalf("Unexpected error for matching StableID: %v", err)
	}
	if got.StableID != canonical {
		t.Fatalf("Expected canonical StableID %q, got %q", canonical, got.StableID)
	}
}

func TestPineconeStableIDConflictFailsClosed(t *testing.T) {
	// A stored stableId that conflicts with the canonical value must return
	// an envelope corruption error and must not be silently overwritten.
	meta := map[string]any{
		"repositoryName": "owner/repo",
		"title":          "Conflict Title",
		"stableId":       "wrong-id",
	}
	_, err := memoryFromMetadata(meta)
	if err == nil {
		t.Fatal("Expected corruption error for conflicting stableId, got nil")
	}
	if !strings.Contains(err.Error(), "corrupted memory envelope") {
		t.Fatalf("Expected 'corrupted memory envelope' in error, got: %v", err)
	}
}

func TestPineconeMalformedFailsClosed(t *testing.T) {
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
	if !strings.Contains(err.Error(), "unknown status") {
		t.Fatalf("Expected invalid status error, got %v", err)
	}
}

func TestPineconeStableIdentity(t *testing.T) {
	id := memoryID("owner/repo", "test memory")
	expected := "owner/repo::3777ddf4ad6ece6f043c4e93"
	if id != expected {
		t.Fatalf("Expected memory ID %q, got %q", expected, id)
	}
}

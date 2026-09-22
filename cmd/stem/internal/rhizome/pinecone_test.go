package rhizome

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestPineconeGetMemory(t *testing.T) {
	ctx := context.Background()

	respondWith := func(payload any) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(payload)
		}
	}

	t.Run("missing vector returns found=false", func(t *testing.T) {
		var capturedPath string
		var capturedBody []byte
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
			capturedBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"vectors": map[string]any{}})
		}))
		defer ts.Close()
		backend, err := NewPineconeMemoryBackend(MemoryConfig{
			PineconeAPIKey:    "test-key",
			PineconeBaseURL:   ts.URL,
			PineconeDimension: 8,
		})
		if err != nil {
			t.Fatalf("NewPineconeMemoryBackend: %v", err)
		}

		_, found, err := backend.GetMemory(ctx, "owner/repo", "Missing")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if found {
			t.Fatal("expected found=false for missing vector")
		}

		// 1. exact fetch uses memoryID
		if capturedPath != "/vectors/fetch" {
			t.Fatalf("expected /vectors/fetch, got %q", capturedPath)
		}
		var reqBody map[string]any
		_ = json.Unmarshal(capturedBody, &reqBody)
		ids, _ := reqBody["ids"].([]any)
		if len(ids) != 1 || ids[0] != memoryID("owner/repo", "Missing") {
			t.Fatalf("expected ids=[%q], got %v", memoryID("owner/repo", "Missing"), ids)
		}
	})

	t.Run("exact hit returns expected Memory", func(t *testing.T) {
		canonicalID := StableMemoryIdentity("owner/repo", "Exact")
		validMeta := map[string]any{
			"repositoryName": "owner/repo",
			"title":          "Exact",
			"origin":         "botanist",
			"authority":      "botanist",
			"status":         "established",
			"stableId":       canonicalID,
		}
		ts := httptest.NewServer(http.HandlerFunc(respondWith(map[string]any{
			"vectors": map[string]any{
				memoryID("owner/repo", "Exact"): map[string]any{"metadata": validMeta},
			},
		})))
		defer ts.Close()
		backend, _ := NewPineconeMemoryBackend(MemoryConfig{
			PineconeAPIKey:    "test-key",
			PineconeBaseURL:   ts.URL,
			PineconeDimension: 8,
		})

		got, found, err := backend.GetMemory(ctx, "owner/repo", "Exact")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !found {
			t.Fatal("expected found=true for exact hit")
		}
		if got.Title != "Exact" || got.RepositoryName != "owner/repo" {
			t.Fatalf("expected exact match, got %q/%q", got.RepositoryName, got.Title)
		}
	})

	t.Run("repository mismatch fails closed", func(t *testing.T) {
		mismatchMeta := map[string]any{
			"repositoryName": "other/repo",
			"title":          "Exact",
			"origin":         "botanist",
			"authority":      "botanist",
			"status":         "established",
			"stableId":       StableMemoryIdentity("other/repo", "Exact"),
		}
		ts := httptest.NewServer(http.HandlerFunc(respondWith(map[string]any{
			"vectors": map[string]any{
				memoryID("owner/repo", "Exact"): map[string]any{"metadata": mismatchMeta},
			},
		})))
		defer ts.Close()
		backend, _ := NewPineconeMemoryBackend(MemoryConfig{
			PineconeAPIKey:    "test-key",
			PineconeBaseURL:   ts.URL,
			PineconeDimension: 8,
		})

		_, _, err := backend.GetMemory(ctx, "owner/repo", "Exact")
		if err == nil {
			t.Fatal("expected repository mismatch to fail closed")
		}
		if !strings.Contains(err.Error(), "exact memory identity mismatch") {
			t.Fatalf("expected identity mismatch error, got: %v", err)
		}
	})

	t.Run("title mismatch fails closed", func(t *testing.T) {
		mismatchMeta := map[string]any{
			"repositoryName": "owner/repo",
			"title":          "Different",
			"origin":         "botanist",
			"authority":      "botanist",
			"status":         "established",
			"stableId":       StableMemoryIdentity("owner/repo", "Different"),
		}
		ts := httptest.NewServer(http.HandlerFunc(respondWith(map[string]any{
			"vectors": map[string]any{
				memoryID("owner/repo", "Exact"): map[string]any{"metadata": mismatchMeta},
			},
		})))
		defer ts.Close()
		backend, _ := NewPineconeMemoryBackend(MemoryConfig{
			PineconeAPIKey:    "test-key",
			PineconeBaseURL:   ts.URL,
			PineconeDimension: 8,
		})

		_, _, err := backend.GetMemory(ctx, "owner/repo", "Exact")
		if err == nil {
			t.Fatal("expected title mismatch to fail closed")
		}
		if !strings.Contains(err.Error(), "exact memory identity mismatch") {
			t.Fatalf("expected identity mismatch error, got: %v", err)
		}
	})

	t.Run("malformed envelope fails closed", func(t *testing.T) {
		malformedMeta := map[string]any{
			"repositoryName": "owner/repo",
			"title":          "Exact",
			"origin":         "botanist",
			"authority":      "botanist",
			"status":         "weird",
		}
		ts := httptest.NewServer(http.HandlerFunc(respondWith(map[string]any{
			"vectors": map[string]any{
				memoryID("owner/repo", "Exact"): map[string]any{"metadata": malformedMeta},
			},
		})))
		defer ts.Close()
		backend, _ := NewPineconeMemoryBackend(MemoryConfig{
			PineconeAPIKey:    "test-key",
			PineconeBaseURL:   ts.URL,
			PineconeDimension: 8,
		})

		_, _, err := backend.GetMemory(ctx, "owner/repo", "Exact")
		if err == nil {
			t.Fatal("expected malformed envelope to fail closed")
		}
		if !strings.Contains(err.Error(), "unknown status") {
			t.Fatalf("expected unknown status error, got: %v", err)
		}
	})

	t.Run("conflicting StableID fails closed", func(t *testing.T) {
		conflictMeta := map[string]any{
			"repositoryName": "owner/repo",
			"title":          "Exact",
			"origin":         "botanist",
			"authority":      "botanist",
			"status":         "established",
			"stableId":       "bad-id",
		}
		ts := httptest.NewServer(http.HandlerFunc(respondWith(map[string]any{
			"vectors": map[string]any{
				memoryID("owner/repo", "Exact"): map[string]any{"metadata": conflictMeta},
			},
		})))
		defer ts.Close()
		backend, _ := NewPineconeMemoryBackend(MemoryConfig{
			PineconeAPIKey:    "test-key",
			PineconeBaseURL:   ts.URL,
			PineconeDimension: 8,
		})

		_, _, err := backend.GetMemory(ctx, "owner/repo", "Exact")
		if err == nil {
			t.Fatal("expected conflicting StableID to fail closed")
		}
		if !strings.Contains(err.Error(), "conflicts with canonical identity") {
			t.Fatalf("expected identity conflict error, got: %v", err)
		}
	})
}

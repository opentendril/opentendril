package rhizome

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestWeaviateStableIDLegacyNormalization(t *testing.T) {
	// Legacy properties with no stableId must receive the canonical StableID.
	props := map[string]any{
		"repositoryName": "owner/repo",
		"title":          "Legacy Title",
	}
	got, err := memoryFromMetadata(props)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	canonical := StableMemoryIdentity("owner/repo", "Legacy Title")
	if got.StableID != canonical {
		t.Fatalf("Expected canonical StableID %q, got %q", canonical, got.StableID)
	}
}

func TestWeaviateStableIDConflictFailsClosed(t *testing.T) {
	// A stored stableId that conflicts with the canonical value must return
	// an envelope corruption error and must not be silently overwritten.
	props := map[string]any{
		"repositoryName": "owner/repo",
		"title":          "Conflict Title",
		"stableId":       "wrong-id",
	}
	_, err := memoryFromMetadata(props)
	if err == nil {
		t.Fatal("Expected corruption error for conflicting stableId, got nil")
	}
	if !strings.Contains(err.Error(), "corrupted memory envelope") {
		t.Fatalf("Expected 'corrupted memory envelope' in error, got: %v", err)
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
	if !strings.Contains(err.Error(), "unknown status") {
		t.Fatalf("Expected invalid status error, got %v", err)
	}
}

func TestWeaviateWriteSideEnvelopeSerialization(t *testing.T) {
	ctx := context.Background()
	var capturedPayload map[string]any

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/objects" && r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&capturedPayload); err != nil {
				t.Errorf("failed to decode request body: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	config := MemoryConfig{
		Backend:            "weaviate",
		WeaviateBaseURL:    ts.URL,
		RemoteCleartextAck: true,
	}
	backend, err := NewWeaviateMemoryBackend(config)
	if err != nil {
		t.Fatalf("NewWeaviateMemoryBackend failed: %v", err)
	}

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
		RevisionMetadata: "meta",
		Supersession:     "super",
	}

	if err := backend.StoreMemory(ctx, fullMem); err != nil {
		t.Fatalf("StoreMemory failed: %v", err)
	}

	if capturedPayload == nil {
		t.Fatal("expected payload to be captured")
	}

	props, ok := capturedPayload["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties in payload, got %v", capturedPayload)
	}

	expectedStableID := StableMemoryIdentity(fullMem.RepositoryName, fullMem.Title)
	if props["stableId"] != expectedStableID {
		t.Errorf("expected stableId %q, got %v", expectedStableID, props["stableId"])
	}

	// Verify complete semantic envelope round-trips
	roundTripped, err := memoryFromMetadata(props)
	if err != nil {
		t.Fatalf("memoryFromMetadata failed: %v", err)
	}

	if roundTripped.Origin != fullMem.Origin ||
		roundTripped.Authority != fullMem.Authority ||
		roundTripped.Status != fullMem.Status ||
		roundTripped.Kind != fullMem.Kind ||
		roundTripped.Provenance != fullMem.Provenance ||
		roundTripped.SourceClass != fullMem.SourceClass ||
		roundTripped.SourceIdentity != fullMem.SourceIdentity ||
		roundTripped.ContentIdentity != fullMem.ContentIdentity ||
		roundTripped.RevisionIdentity != fullMem.RevisionIdentity ||
		roundTripped.StableID != expectedStableID ||
		roundTripped.RevisionMetadata != fullMem.RevisionMetadata ||
		roundTripped.Supersession != fullMem.Supersession {
		t.Errorf("round tripped memory envelope mismatch:\nwant: %+v\ngot:  %+v", fullMem, roundTripped)
	}
}

func TestWeaviateGetMemory(t *testing.T) {
	ctx := context.Background()

	newBackend := func(t *testing.T, handler http.HandlerFunc) *WeaviateMemoryBackend {
		t.Helper()
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)
		b, err := NewWeaviateMemoryBackend(MemoryConfig{
			WeaviateBaseURL:    ts.URL,
			RemoteCleartextAck: true,
		})
		if err != nil {
			t.Fatalf("NewWeaviateMemoryBackend: %v", err)
		}
		return b
	}

	t.Run("exact lookup uses deterministicUUID", func(t *testing.T) {
		expectedID := deterministicUUID("owner/repo", "Target")
		var capturedPath string
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			capturedPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			props := map[string]any{
				"repositoryName": "owner/repo",
				"title":          "Target",
				"stableId":       StableMemoryIdentity("owner/repo", "Target"),
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"properties": props})
		})

		_, found, err := backend.GetMemory(ctx, "owner/repo", "Target")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !found {
			t.Fatal("expected found=true")
		}
		expectedPath := "/v1/objects/" + weaviateMemoryClass + "/" + expectedID
		if capturedPath != expectedPath {
			t.Fatalf("expected request path %q, got %q", expectedPath, capturedPath)
		}
	})

	t.Run("HTTP 404 returns found=false", func(t *testing.T) {
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})

		_, found, err := backend.GetMemory(ctx, "owner/repo", "Missing")
		if err != nil {
			t.Fatalf("unexpected error on 404: %v", err)
		}
		if found {
			t.Fatal("expected found=false on 404")
		}
	})

	t.Run("repository mismatch fails closed", func(t *testing.T) {
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			props := map[string]any{
				"repositoryName": "other/repo",
				"title":          "Target",
				"stableId":       StableMemoryIdentity("other/repo", "Target"),
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"properties": props})
		})

		_, _, err := backend.GetMemory(ctx, "owner/repo", "Target")
		if err == nil {
			t.Fatal("expected repository mismatch to fail closed")
		}
		if !strings.Contains(err.Error(), "exact memory identity mismatch") {
			t.Fatalf("expected identity mismatch error, got: %v", err)
		}
	})

	t.Run("title mismatch fails closed", func(t *testing.T) {
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			props := map[string]any{
				"repositoryName": "owner/repo",
				"title":          "Different",
				"stableId":       StableMemoryIdentity("owner/repo", "Different"),
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"properties": props})
		})

		_, _, err := backend.GetMemory(ctx, "owner/repo", "Target")
		if err == nil {
			t.Fatal("expected title mismatch to fail closed")
		}
		if !strings.Contains(err.Error(), "exact memory identity mismatch") {
			t.Fatalf("expected identity mismatch error, got: %v", err)
		}
	})

	t.Run("malformed envelope fails closed", func(t *testing.T) {
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			props := map[string]any{
				"repositoryName": "owner/repo",
				"title":          "Target",
				"status":         "weird",
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"properties": props})
		})

		_, _, err := backend.GetMemory(ctx, "owner/repo", "Target")
		if err == nil {
			t.Fatal("expected malformed envelope to fail closed")
		}
		if !strings.Contains(err.Error(), "unknown status") {
			t.Fatalf("expected unknown status error, got: %v", err)
		}
	})

	t.Run("conflicting StableID fails closed", func(t *testing.T) {
		backend := newBackend(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			props := map[string]any{
				"repositoryName": "owner/repo",
				"title":          "Target",
				"stableId":       "bad-id",
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"properties": props})
		})

		_, _, err := backend.GetMemory(ctx, "owner/repo", "Target")
		if err == nil {
			t.Fatal("expected conflicting StableID to fail closed")
		}
		if !strings.Contains(err.Error(), "conflicts with canonical identity") {
			t.Fatalf("expected identity conflict error, got: %v", err)
		}
	})
}

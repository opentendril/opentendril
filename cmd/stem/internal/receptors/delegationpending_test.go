package receptors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestDelegationPendingHandler_RoundTripAnd404(t *testing.T) {
	store := core.NewPendingConfirmationStore()
	handler := NewDelegationPendingHandler(store)
	mux := http.NewServeMux()
	handler.Register(mux, nil) // no auth for this test

	// Create a pending confirmation
	grant := core.DelegationGrant{
		Pollen:             "test-pollen",
		OperationClasses:   []string{"test-op"},
		Substrates:         []string{"test-sub"},
		ConfirmAboveImpact: core.DelegationImpactHigh,
	}
	record := store.Create("test-pollen", "test-op", "test-sub", "High", grant, time.Hour)

	// List it
	req := httptest.NewRequest(http.MethodGet, "/v1/delegation/pending", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("List failed: %v", rec.Code)
	}

	var list []pendingResponse
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("Decode list: %v", err)
	}
	if len(list) != 1 || list[0].ID != record.ID {
		t.Fatalf("Expected 1 pending confirmation with ID %s, got %+v", record.ID, list)
	}

	// Approve it
	reqApprove := httptest.NewRequest(http.MethodPost, "/v1/delegation/pending/"+record.ID+"/approve", nil)
	recApprove := httptest.NewRecorder()
	mux.ServeHTTP(recApprove, reqApprove)

	if recApprove.Code != http.StatusOK {
		t.Fatalf("Approve failed: %d - %s", recApprove.Code, recApprove.Body.String())
	}

	var appResp map[string]string
	if err := json.NewDecoder(recApprove.Body).Decode(&appResp); err != nil {
		t.Fatalf("Decode approve: %v", err)
	}
	if appResp["status"] != "approved" || appResp["id"] != record.ID {
		t.Fatalf("Unexpected approve response: %+v", appResp)
	}

	// List again, should be empty (since it's not open anymore)
	req2 := httptest.NewRequest(http.MethodGet, "/v1/delegation/pending", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	var list2 []pendingResponse
	if err := json.NewDecoder(rec2.Body).Decode(&list2); err != nil {
		t.Fatalf("Decode list2: %v", err)
	}
	if len(list2) != 0 {
		t.Fatalf("Expected 0 pending confirmations, got %d", len(list2))
	}

	// Test 404
	req404 := httptest.NewRequest(http.MethodPost, "/v1/delegation/pending/unknown-id/deny", nil)
	rec404 := httptest.NewRecorder()
	mux.ServeHTTP(rec404, req404)

	if rec404.Code != http.StatusNotFound {
		t.Fatalf("Expected 404 for unknown ID, got %d", rec404.Code)
	}
}

// Integration proof test
func TestDelegationPendingHandler_IntegrationProof(t *testing.T) {
	store := core.NewPendingConfirmationStore()
	handler := NewDelegationPendingHandler(store)
	mux := http.NewServeMux()
	handler.Register(mux, nil)

	grant := core.DelegationGrant{
		Pollen:             "int-pollen",
		OperationClasses:   []string{"int-op"},
		Substrates:         []string{"int-sub"},
		ConfirmAboveImpact: core.DelegationImpactMedium,
	}

	authorizer := core.NewDelegationAuthorizer([]core.DelegationGrant{grant}).WithPendingStore(store, time.Hour)

	reqAuth := core.DelegationRequest{
		Pollen:         "int-pollen",
		OperationClass: "int-op",
		Substrate:      "int-sub",
		Impact:         core.DelegationImpactHigh,
	}

	// First Authorize call, should be PendingConfirmation: true
	decision1 := authorizer.Authorize(reqAuth)
	if decision1.Authorized {
		t.Fatal("Expected not authorized initially")
	}
	if !decision1.PendingConfirmation {
		t.Fatal("Expected pending confirmation to be true")
	}

	// Find the created pending confirmation ID
	var pendingID string
	records := store.List()
	if len(records) != 1 {
		t.Fatalf("Expected 1 pending record, got %d", len(records))
	}
	pendingID = records[0].ID

	// Approve it via HTTP endpoint
	reqApprove := httptest.NewRequest(http.MethodPost, "/v1/delegation/pending/"+pendingID+"/approve", nil)
	recApprove := httptest.NewRecorder()
	mux.ServeHTTP(recApprove, reqApprove)

	if recApprove.Code != http.StatusOK {
		t.Fatalf("Approve via HTTP failed: %d", recApprove.Code)
	}

	// Call Authorize again with the same request
	decision2 := authorizer.Authorize(reqAuth)
	if !decision2.Authorized {
		t.Fatal("Expected authorized after approval via HTTP")
	}
}

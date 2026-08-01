package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/mesh"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// TestBotanistLaneRefusesPollinatorLaneBearers pins the isolation between the
// Botanist lane and the Pollinator lane.
//
// Go's http.ServeMux exposes no route enumeration, so this test is a table of
// known Botanist-lane routes. It catches a wrong-lane edit to an existing route.
// It does not catch a newly added route registered on the wrong lane — the table
// simply won't mention it.
func TestBotanistLaneRefusesPollinatorLaneBearers(t *testing.T) {
	dir := t.TempDir()

	// Setup a real StemSigner
	stemSigner, err := core.LoadOrCreateStemSigner(dir)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	// Issue a real Pollinator credential
	credSecret, _, err := core.IssuePollinatorCredential(dir, "tester", "")
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	creds, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}

	// Mint a real access token
	token, err := stemSigner.MintAccessToken("tester", 0, core.AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	apiKey := "test-api-key"
	adminKey := "test-admin-key"

	bus := eventbus.New()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}

	pendingStore := core.NewPendingConfirmationStore()
	delegationGate := &receptors.DelegationGate{
		Pollinators: creds,
		Signer:      stemSigner,
		Authorizer:  core.NewDelegationAuthorizer(nil).WithPendingStore(pendingStore, time.Hour),
		Bus:         bus,
	}

	deps := serveDependencies{
		APIKey:                apiKey,
		PollinatorCredentials: creds,
		StemSigner:            stemSigner,
		Networked:             false, // Loopback so credentials are valid everywhere on Pollinator lane
		DelegationGate:        delegationGate,
		EventBus:              bus,
		Sessions:              manager,
		History:               nil,
		CoreService:           core.NewService(manager),
		HealthMonitor:         newDefaultHealthMonitor(bus, time.Hour),
		TendrilDir:            dir,
		MeshServer:            mesh.NewServer(dir),
		PendingStore:          pendingStore,
		AdminKey:              adminKey,
	}

	// -------------------------------------------------------------------------
	// Reached-Spy: withAPIKeyAuth's own behaviour, in isolation.
	//
	// This calls the middleware directly, not through the mux, so it pins what
	// withAPIKeyAuth does with a Pollinator bearer and nothing about how routes
	// are registered. A route moved to the wrong lane leaves this green — the
	// route table below is what catches that.
	// -------------------------------------------------------------------------
	reached := false
	spyHandler := withAPIKeyAuth(adminKey, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	bearers := []struct {
		name   string
		bearer string
	}{
		{"Pollinator credential", "Bearer " + credSecret},
		{"Access token", "Bearer " + token},
	}

	for _, bearerCase := range bearers {
		t.Run(bearerCase.name+"_Spy", func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodGet, "/dummy", nil)
			req.Header.Set("Authorization", bearerCase.bearer)
			rec := httptest.NewRecorder()
			spyHandler(rec, req)

			if reached {
				t.Fatalf("Botanist-lane handler was entered by %s", bearerCase.name)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("spy status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}

	// -------------------------------------------------------------------------
	// Route Registrations: Verify the mux wire-up.
	// -------------------------------------------------------------------------
	mux := buildServeMux(deps)

	botanistRoutes := []struct {
		method string
		path   string
		wantOK int // The specific status returned to a valid AdminKey
	}{
		// 500 because the mesh.NewServer fixture lacks a workspace private key.
		{"POST", "/v1/mesh/admin/issue-token", http.StatusInternalServerError},
		{"GET", "/v1/delegation/pending", http.StatusOK},
		// 404 because the confirmation ID "123" does not exist in the store.
		{"POST", "/v1/delegation/pending/123/approve", http.StatusNotFound},
		{"POST", "/v1/delegation/pending/123/deny", http.StatusNotFound},
	}

	// A route on the Pollinator lane wrapped by guardedAuth. We assert 403 (Forbidden)
	// instead of 200. This is because guardedAuth uses DelegationGate.Middleware,
	// which denies EVERY request that resolves to a non-empty Pollen with
	// "this endpoint exposes no delegable operation-class".
	// The 403 proves successful resolution (an unresolvable credential would yield 401).
	pollinatorRoute := struct {
		method string
		path   string
	}{"GET", "/v1/config/genotypes"}

	for _, bearerCase := range bearers {
		t.Run(bearerCase.name+"_Routes", func(t *testing.T) {
			// Assertion 3: Positive control on the credential.
			// We assert 403 explicitly; this proves resolution rather than acceptance.
			req := httptest.NewRequest(pollinatorRoute.method, pollinatorRoute.path, nil)
			req.Header.Set("Authorization", bearerCase.bearer)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("Assertion 3 failed: expected 403 Forbidden to prove successful Pollen resolution, got %d", rec.Code)
			}

			// For each Botanist route, assert 1 (refusal) and 2 (positive control on lane)
			for _, route := range botanistRoutes {
				t.Run(route.path, func(t *testing.T) {
					// Assertion 1: Refusal (401 from withAPIKeyAuth).
					req := httptest.NewRequest(route.method, route.path, nil)
					req.Header.Set("Authorization", bearerCase.bearer)
					rec := httptest.NewRecorder()
					mux.ServeHTTP(rec, req)
					if rec.Code != http.StatusUnauthorized {
						t.Fatalf("Assertion 1 failed: %s %s did not refuse %s (got %d)", route.method, route.path, bearerCase.name, rec.Code)
					}

					// Assertion 2: Positive control on the lane with AdminKey.
					// Assert the exact expected status code to prove the route is real.
					reqAdmin := httptest.NewRequest(route.method, route.path, nil)
					reqAdmin.Header.Set("Authorization", "Bearer "+adminKey)
					recAdmin := httptest.NewRecorder()
					mux.ServeHTTP(recAdmin, reqAdmin)
					if recAdmin.Code != route.wantOK {
						t.Fatalf("Assertion 2 failed: %s %s returned %d to AdminKey, want %d", route.method, route.path, recAdmin.Code, route.wantOK)
					}
				})
			}
		})
	}

	// The property the lane separation exists for, stated as an effect rather
	// than a status code: a confirmation held against a Pollen must still be
	// held after that Pollen's own bearers have tried to release it.
	//
	// The assertions above observe which layer refused. This one observes
	// whether anything moved, so it still means something if both the lane and
	// the gate are removed — the case where the status codes stop differing.
	t.Run("a Pollinator cannot release a confirmation held against it", func(t *testing.T) {
		held := pendingStore.Create(
			"tester", "git.push", "myrepo", core.DelegationImpactHigh,
			core.DelegationGrant{Pollen: "tester", OperationClasses: []string{"git.push"}},
			time.Hour,
		)

		for _, bearerCase := range bearers {
			for _, verb := range []string{"approve", "deny"} {
				req := httptest.NewRequest(http.MethodPost, "/v1/delegation/pending/"+held.ID+"/"+verb, nil)
				req.Header.Set("Authorization", bearerCase.bearer)
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				// Errorf, not Fatalf: the effect check below is the one that
				// still means something when the status codes stop differing,
				// so it must run even after this fails.
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("%s reached %s on a real held confirmation (got %d)", bearerCase.name, verb, rec.Code)
				}
			}
		}

		// Approve succeeds only on a record that is still open, so it fails here
		// if any attempt above approved or denied the confirmation.
		if err := pendingStore.Approve(held.ID); err != nil {
			t.Fatalf("confirmation held against Pollen %q no longer open after Pollinator attempts: %v", held.Pollen, err)
		}
	})
}

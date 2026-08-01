package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
)

func TestDelegationPendingEndpointsRequireAuth(t *testing.T) {
	store := core.NewPendingConfirmationStore()
	handler := receptors.NewDelegationPendingHandler(store)
	mux := http.NewServeMux()

	apiKey := "secret-botanist-key"
	for _, route := range handler.Routes() {
		// Mock the exact wrapping used in cmdserve.go
		mux.HandleFunc(route.Pattern, withAPIKeyAuth(apiKey, route.Handler))
	}

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/delegation/pending"},
		{http.MethodPost, "/v1/delegation/pending/some-id/approve"},
		{http.MethodPost, "/v1/delegation/pending/some-id/deny"},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			cases := []struct {
				name   string
				header string
				want   int
			}{
				{"missing header", "", http.StatusUnauthorized},
				{"wrong key", "Bearer wrong-key", http.StatusUnauthorized},
				// Note: for correct key, we might get 200 or 404 (for missing id), but not 401
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					req := httptest.NewRequest(ep.method, ep.path, nil)
					if tc.header != "" {
						req.Header.Set("Authorization", tc.header)
					}
					rec := httptest.NewRecorder()
					mux.ServeHTTP(rec, req)

					if rec.Code != tc.want {
						t.Fatalf("status = %d, want %d", rec.Code, tc.want)
					}
				})
			}
		})
	}
}

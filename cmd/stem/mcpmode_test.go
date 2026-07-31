package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMCPModeSelection(t *testing.T) {
	callerUID := os.Getuid()
	otherUID := callerUID + 1

	tests := []struct {
		name           string
		healthOwner    *int
		healthDelay    time.Duration
		hasCredential  bool
		override       bool
		expectedMode   mcpMode
		expectedMsgStr string
	}{
		{
			name:          "owner present, differs from caller, credential configured -> forward",
			healthOwner:   &otherUID,
			hasCredential: true,
			expectedMode:  mcpModeForward,
		},
		{
			name:           "owner present, differs from caller, no credential -> refuse",
			healthOwner:    &otherUID,
			hasCredential:  false,
			expectedMode:   mcpModeRefuse,
			expectedMsgStr: "tendril pollinator issue",
		},
		{
			name:          "owner present, equals caller -> in-process",
			healthOwner:   &callerUID,
			hasCredential: true, // or false, doesn't matter
			expectedMode:  mcpModeInProcess,
		},
		{
			name:           "answers, no owner reported -> in-process",
			healthOwner:    nil,
			hasCredential:  true,
			expectedMode:   mcpModeInProcess,
			expectedMsgStr: "⚠️ A Stem is serving on this address, but its ownership could not be established. Assuming this is a local environment and starting an in-process Stem beside it. To forward requests to the running Stem, it must be governed (have an owner configured).",
		},
		{
			name:          "override forces in-process despite differing owner and credential",
			healthOwner:   &otherUID,
			hasCredential: true,
			override:      true,
			expectedMode:  mcpModeInProcess,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.healthDelay > 0 {
					// dwell: delay the health response past the probe bound so detection times out.
					time.Sleep(tt.healthDelay)
				}
				report := struct {
					Owner *int `json:"owner,omitempty"`
				}{
					Owner: tt.healthOwner,
				}
				json.NewEncoder(w).Encode(report)
			}))
			defer server.Close()

			host := strings.TrimPrefix(server.URL, "http://")
			hostPart, portPart := "", ""
			parts := strings.Split(host, ":")
			if len(parts) == 2 {
				hostPart = parts[0]
				portPart = parts[1]
			}
			os.Setenv("TERROIR_HOST", hostPart)
			os.Setenv("PORT", portPart)
			defer os.Unsetenv("TERROIR_HOST")
			defer os.Unsetenv("PORT")

			if tt.override {
				os.Setenv("TENDRIL_MCP_IN_PROCESS", "1")
				defer os.Unsetenv("TENDRIL_MCP_IN_PROCESS")
			}

			decision := detectMCPMode(context.Background(), tt.hasCredential)
			if decision.Mode != tt.expectedMode {
				t.Errorf("expected mode %v, got %v", tt.expectedMode, decision.Mode)
			}
			if tt.expectedMsgStr != "" && !strings.Contains(decision.Message, tt.expectedMsgStr) {
				t.Errorf("expected message to contain %q, got %q", tt.expectedMsgStr, decision.Message)
			}

			if tt.expectedMode == mcpModeRefuse {
				if !strings.Contains(decision.Message, envMCPCredential) {
					t.Errorf("expected refusal to name variable %s, got message: %s", envMCPCredential, decision.Message)
				}
			}
		})
	}
}

func TestMCPMode_NothingAnswers(t *testing.T) {
	os.Setenv("TERROIR_HOST", "127.0.0.1")
	os.Setenv("PORT", "65534") // unreachable port
	defer os.Unsetenv("TERROIR_HOST")
	defer os.Unsetenv("PORT")

	decision := detectMCPMode(context.Background(), true)
	if decision.Mode != mcpModeInProcess {
		t.Errorf("expected in-process mode when nothing answers, got %v", decision.Mode)
	}
}

func TestMCPMode_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// dwell: delay the health response past the probe bound so detection times out.
		time.Sleep(3 * time.Second)
	}))
	defer server.Close()

	parts := strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")
	os.Setenv("TERROIR_HOST", parts[0])
	os.Setenv("PORT", parts[1])
	defer os.Unsetenv("TERROIR_HOST")
	defer os.Unsetenv("PORT")

	start := time.Now()
	decision := detectMCPMode(context.Background(), true)
	elapsed := time.Since(start)

	if decision.Mode != mcpModeInProcess {
		t.Errorf("expected timeout to result in in-process mode, got %v", decision.Mode)
	}
	if elapsed > 2500*time.Millisecond {
		t.Errorf("probe delayed startup beyond bound, took %v", elapsed)
	}
}

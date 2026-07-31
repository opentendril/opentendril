package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMCPForwarder(t *testing.T) {
	var (
		mintCount     int
		v1Count       int
		v1ReturnBytes []byte
		v1Received    []byte
		testPollen    = "pollen-123"
	)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pollinator/token", func(w http.ResponseWriter, r *http.Request) {
		mintCount++
		auth := r.Header.Get("Authorization")
		if auth != "Bearer valid-root" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":     "fake-token",
			"pollen":    testPollen,
			"expiresAt": time.Now().Add(2 * time.Minute),
		})
	})

	mux.HandleFunc("POST /v1", func(w http.ResponseWriter, r *http.Request) {
		v1Count++
		body, _ := io.ReadAll(r.Body)
		v1Received = body

		if v1ReturnBytes != nil {
			w.Write(v1ReturnBytes)
		} else {
			w.Write(body)
		}
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	t.Run("Frame preserved unaltered", func(t *testing.T) {
		mintCount = 0
		v1Count = 0
		v1ReturnBytes = nil
		v1Received = nil

		f := NewMCPForwarder("valid-root")
		f.BaseURL = server.URL

		req := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		resp := f.Forward(req)

		if !bytes.Equal(req, v1Received) {
			t.Errorf("Forwarded body altered.\nGot : %s\nWant: %s", v1Received, req)
		}
		if !bytes.Equal(req, resp) {
			t.Errorf("Returned body altered.\nGot : %s\nWant: %s", resp, req)
		}
	})

	t.Run("Re-mint before expiry", func(t *testing.T) {
		mintCount = 0
		v1Count = 0
		v1ReturnBytes = nil

		f := NewMCPForwarder("valid-root")
		f.BaseURL = server.URL

		// Set token to expire in 30 seconds
		f.token = "old-token"
		f.expiresAt = time.Now().Add(30 * time.Second)

		req := []byte(`{"jsonrpc":"2.0","id":1}`)
		f.Forward(req)

		if mintCount != 1 {
			t.Errorf("Expected 1 mint due to impending expiry, got %d", mintCount)
		}
		if v1Count != 1 {
			t.Errorf("Expected 1 forward call, got %d", v1Count)
		}
	})

	t.Run("One 401 causes exactly one re-mint and retry", func(t *testing.T) {
		mintCount = 0
		v1Calls := 0

		f := NewMCPForwarder("valid-root")
		f.token = "initial-token"
		f.expiresAt = time.Now().Add(1 * time.Hour) // won't naturally expire

		mux2 := http.NewServeMux()
		mux2.HandleFunc("POST /v1/pollinator/token", func(w http.ResponseWriter, r *http.Request) {
			mintCount++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"token":     "fake-token",
				"pollen":    testPollen,
				"expiresAt": time.Now().Add(2 * time.Minute),
			})
		})
		mux2.HandleFunc("POST /v1", func(w http.ResponseWriter, r *http.Request) {
			v1Calls++
			if v1Calls == 1 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized) // First time 401
				return
			}
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"ok"}`)) // Second time success
		})
		srv2 := httptest.NewServer(mux2)
		defer srv2.Close()

		f.BaseURL = srv2.URL

		req := []byte(`{"jsonrpc":"2.0","id":1}`)
		resp := f.Forward(req)

		if mintCount != 1 {
			t.Errorf("Expected exactly 1 re-mint, got %d", mintCount)
		}
		if v1Calls != 2 {
			t.Errorf("Expected exactly 2 v1 calls (initial + retry), got %d", v1Calls)
		}

		var respMap map[string]interface{}
		json.Unmarshal(resp, &respMap)
		if respMap["result"] != "ok" {
			t.Errorf("Expected successful retry, got: %s", string(resp))
		}
	})

	t.Run("Two consecutive 401s do not loop", func(t *testing.T) {
		mintCount = 0
		v1Calls := 0

		f := NewMCPForwarder("valid-root")
		f.token = "initial-token"
		f.expiresAt = time.Now().Add(1 * time.Hour)

		mux2 := http.NewServeMux()
		mux2.HandleFunc("POST /v1/pollinator/token", func(w http.ResponseWriter, r *http.Request) {
			mintCount++
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"token":     "fake-token",
				"expiresAt": time.Now().Add(2 * time.Minute),
			})
		})
		mux2.HandleFunc("POST /v1", func(w http.ResponseWriter, r *http.Request) {
			v1Calls++
			http.Error(w, "Unauthorized", http.StatusUnauthorized) // Always 401
		})
		srv2 := httptest.NewServer(mux2)
		defer srv2.Close()

		f.BaseURL = srv2.URL

		req := []byte(`{"jsonrpc":"2.0","id":2}`)

		done := make(chan struct{})
		var resp []byte
		go func() {
			resp = f.Forward(req)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Test timed out: infinite loop detected on consecutive 401s")
		}

		if mintCount != 1 {
			t.Errorf("Expected exactly 1 re-mint, got %d", mintCount)
		}
		if v1Calls != 2 {
			t.Errorf("Expected exactly 2 v1 calls (initial + retry), got %d", v1Calls)
		}

		if !strings.Contains(string(resp), "credential refused after retry") {
			t.Errorf("Expected auth error in JSON response, got %s", string(resp))
		}
	})

	t.Run("Unreachable Stem message", func(t *testing.T) {
		f := NewMCPForwarder("valid-root")
		f.BaseURL = "http://127.0.0.1:0" // Unreachable

		req := []byte(`{"jsonrpc":"2.0","id":3}`)
		resp := f.Forward(req)

		if !strings.Contains(string(resp), "no Stem is answering") {
			t.Errorf("Expected 'no Stem is answering' in response, got %s", string(resp))
		}
	})

	t.Run("Refused credential message", func(t *testing.T) {
		f := NewMCPForwarder("invalid-root")
		f.BaseURL = server.URL

		req := []byte(`{"jsonrpc":"2.0","id":4}`)
		resp := f.Forward(req)

		if !strings.Contains(string(resp), "the Stem refused you") {
			t.Errorf("Expected 'the Stem refused you' in response, got %s", string(resp))
		}
	})

	t.Run("Every failure path returns valid protocol frame carrying ID", func(t *testing.T) {
		f := NewMCPForwarder("invalid-root")
		f.BaseURL = server.URL

		req := []byte(`{"jsonrpc":"2.0","id":5}`)
		resp := f.Forward(req)

		var frame struct {
			JSONRPC string      `json:"jsonrpc"`
			ID      interface{} `json:"id"`
			Error   *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(resp, &frame); err != nil {
			t.Fatalf("Response is not valid JSON: %v. Resp: %s", err, string(resp))
		}
		if frame.ID != float64(5) {
			t.Errorf("Expected ID=5, got %v", frame.ID)
		}
		if frame.Error == nil {
			t.Errorf("Expected error frame, got success")
		}
	})

	t.Run("No secret material appears in any output stream", func(t *testing.T) {
		f := NewMCPForwarder("super-secret-root")
		f.BaseURL = server.URL
		f.token = "super-secret-token"
		f.expiresAt = time.Now().Add(1 * time.Hour)

		mux2 := http.NewServeMux()
		mux2.HandleFunc("POST /v1", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Internal Server Error", 500)
		})
		srv2 := httptest.NewServer(mux2)
		defer srv2.Close()
		f.BaseURL = srv2.URL

		oldStdout := os.Stdout
		oldStderr := os.Stderr
		rOut, wOut, _ := os.Pipe()
		rErr, wErr, _ := os.Pipe()
		os.Stdout = wOut
		os.Stderr = wErr

		req := []byte(`{"jsonrpc":"2.0","id":6}`)
		resp := f.Forward(req)

		wOut.Close()
		wErr.Close()
		os.Stdout = oldStdout
		os.Stderr = oldStderr

		outBytes, _ := io.ReadAll(rOut)
		errBytes, _ := io.ReadAll(rErr)

		allOutput := string(resp) + string(outBytes) + string(errBytes)
		if strings.Contains(allOutput, "super-secret-root") {
			t.Errorf("Secret root leaked in output")
		}
		if strings.Contains(allOutput, "super-secret-token") {
			t.Errorf("Secret token leaked in output")
		}
	})

	t.Run("Large frame round-trips", func(t *testing.T) {
		f := NewMCPForwarder("valid-root")
		f.BaseURL = server.URL

		largeStr := strings.Repeat("a", 1024*1024*5) // 5MB
		req := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"large","params":"%s"}`, largeStr))

		v1ReturnBytes = req // Return the exact same large frame

		resp := f.Forward(req)
		if len(resp) != len(req) {
			t.Errorf("Expected length %d, got %d", len(req), len(resp))
		}
	})
}

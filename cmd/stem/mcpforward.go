package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type minimalMCPRequest struct {
	ID interface{} `json:"id"`
}

type MCPForwarder struct {
	BaseURL    string
	RootCred   string
	HTTPClient *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func NewMCPForwarder(rootCred string) *MCPForwarder {
	host := strings.TrimSpace(os.Getenv(EnvTerroirHost))
	if host == "" {
		host = "127.0.0.1"
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := net.JoinHostPort(host, port)

	return &MCPForwarder{
		BaseURL:    "http://" + addr,
		RootCred:   rootCred,
		HTTPClient: &http.Client{Timeout: 15 * time.Minute},
	}
}

func (f *MCPForwarder) mintToken() (string, time.Time, error) {
	req, err := http.NewRequest(http.MethodPost, f.BaseURL+"/v1/pollinator/token", nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build mint request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.RootCred)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("no Stem is answering: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", time.Time{}, fmt.Errorf("the Stem refused you: invalid Pollinator credential")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("mint failed with status %d: %s", resp.StatusCode, string(body))
	}

	var mintResp struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mintResp); err != nil {
		return "", time.Time{}, fmt.Errorf("decode mint response: %w", err)
	}

	return mintResp.Token, mintResp.ExpiresAt, nil
}

func (f *MCPForwarder) ensureToken() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Renew if less than 1 minute before expiry
	if f.token != "" && time.Now().Add(1*time.Minute).Before(f.expiresAt) {
		return f.token, nil
	}

	token, expiresAt, err := f.mintToken()
	if err != nil {
		return "", err
	}

	f.token = token
	f.expiresAt = expiresAt
	return f.token, nil
}

func (f *MCPForwarder) Forward(reqBytes []byte) []byte {
	var minimal minimalMCPRequest
	_ = json.Unmarshal(reqBytes, &minimal)
	reqID := minimal.ID

	token, err := f.ensureToken()
	if err != nil {
		return f.formatError(reqID, -32000, err.Error())
	}

	respBytes, is401, err := f.doRequest(reqBytes, token)
	if err != nil {
		return f.formatError(reqID, -32000, err.Error())
	}

	if is401 {
		// Exactly one retry
		f.mu.Lock()
		f.token = "" // force re-mint
		f.mu.Unlock()

		token, err = f.ensureToken()
		if err != nil {
			return f.formatError(reqID, -32000, err.Error())
		}

		respBytes, is401, err = f.doRequest(reqBytes, token)
		if err != nil {
			return f.formatError(reqID, -32000, err.Error())
		}
		if is401 {
			return f.formatError(reqID, -32000, "the Stem refused you: credential refused after retry")
		}
	}

	return respBytes
}

func (f *MCPForwarder) doRequest(reqBytes []byte, token string) ([]byte, bool, error) {
	req, err := http.NewRequest(http.MethodPost, f.BaseURL+"/v1", bytes.NewReader(reqBytes))
	if err != nil {
		return nil, false, fmt.Errorf("build forward request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("no Stem is answering: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, true, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var minimal minimalMCPRequest
		_ = json.Unmarshal(reqBytes, &minimal)
		return f.formatError(minimal.ID, -32000, strings.TrimSpace(string(body))), false, nil
	}

	return body, false, nil
}

func (f *MCPForwarder) formatError(id interface{}, code int, message string) []byte {
	type errFrame struct {
		JSONRPC string      `json:"jsonrpc"`
		ID      interface{} `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	frame := errFrame{
		JSONRPC: "2.0",
		ID:      id,
	}
	frame.Error.Code = code
	frame.Error.Message = message
	b, _ := json.Marshal(frame)
	return b
}

package mcpclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type minimalMCPRequest struct {
	ID interface{} `json:"id"`
}

// Forwarder mints a short-lived Pollinator access token from a durable root
// and forwards raw MCP frames to the Stem. It owns transport only: no
// capability, grant, or Stem-construction logic.
type Forwarder struct {
	BaseURL    string
	RootCred   string
	HTTPClient *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// forwardingTimeout is generous because a forwarded frame may be a long-running invocation.
// It is deliberately not linked to the access-token lifetime.
const forwardingTimeout = 15 * time.Minute

// NewForwarder builds a client pointed at the resolved Stem address.
func NewForwarder(rootCred string) *Forwarder {
	addr := ResolveStemAddress("")

	return &Forwarder{
		BaseURL:    "http://" + addr,
		RootCred:   rootCred,
		HTTPClient: &http.Client{Timeout: forwardingTimeout},
	}
}

func (f *Forwarder) mintToken() (string, time.Time, error) {
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

func (f *Forwarder) ensureToken() (string, error) {
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

// Forward sends one MCP request frame to the Stem and returns the response
// bytes, or a protocol-shaped error frame. A 401 causes exactly one remint
// and one retry.
func (f *Forwarder) Forward(reqBytes []byte) []byte {
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

func (f *Forwarder) doRequest(reqBytes []byte, token string) ([]byte, bool, error) {
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

func (f *Forwarder) formatError(id interface{}, code int, message string) []byte {
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

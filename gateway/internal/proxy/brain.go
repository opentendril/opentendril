// Package proxy implements the HTTP client that calls the Python brain's
// POST /v1/chat endpoint with the Unified Message Object.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// BrainClient talks to the Python Tendril brain over HTTP.
type BrainClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewBrainClient creates a client pointing at the brain service.
func NewBrainClient(baseURL string) *BrainClient {
	return &BrainClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 120 * time.Second, // LLM calls can be slow
		},
	}
}

// chatRequest matches the Python brain's ChatRequest pydantic model.
type chatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	Provider  string `json:"provider"`
}

// chatResponse matches the Python brain's response format.
type chatResponse struct {
	Response string `json:"response"`
	Provider string `json:"provider"`
}

// Chat sends a message to the brain and returns the response text.
func (b *BrainClient) Chat(sessionID, message, provider string) (string, error) {
	reqBody := chatRequest{
		SessionID: sessionID,
		Message:   message,
		Provider:  provider,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := b.BaseURL + "/v1/chat"
	log.Printf("→ POST %s (session=%s, %d bytes)", url, sessionID, len(body))

	resp, err := b.HTTPClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("brain request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading brain response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("brain returned %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("unmarshal brain response: %w", err)
	}

	return chatResp.Response, nil
}

// Health checks if the brain is reachable.
func (b *BrainClient) Health() error {
	resp, err := b.HTTPClient.Get(b.BaseURL + "/health")
	if err != nil {
		return fmt.Errorf("brain health check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("brain unhealthy: status %d", resp.StatusCode)
	}
	return nil
}

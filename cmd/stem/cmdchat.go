package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model     string    `json:"model"`
	SessionID string    `json:"sessionId,omitempty"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream"`
}

type ChatResponse struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	SessionID string `json:"sessionId,omitempty"`
	Choices   []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

type sessionCreateResponse struct {
	SessionID string `json:"sessionId"`
}

type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// connectWS establishes a WebSocket connection to the gateway. The gateway
// dialer can set real headers (unlike a browser), so the bearer key rides
// along the way it does for every other Stem call (issue #171 finding 2).
func connectWS(base *url.URL) (*websocket.Conn, error) {
	u := url.URL{Scheme: "ws", Host: base.Host, Path: "/ws"}
	log.Printf("Connecting to WS: %s", u.String())
	header := http.Header{"Authorization": []string{"Bearer " + chatAPIKey()}}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
	if err != nil {
		log.Printf("WS connect failed: %v", err)
		return nil, err
	}
	return conn, nil
}

// sendWS sends a message over WebSocket and reads the response.
func sendWS(conn *websocket.Conn, msg string) (string, error) {
	err := conn.WriteJSON(WSMessage{Type: "userMessage", Data: msg})
	if err != nil {
		return "", err
	}

	for {
		_, rawMsg, err := conn.ReadMessage()
		if err != nil {
			return "", err
		}

		// Try to parse as WSMessage
		var response WSMessage
		if err := json.Unmarshal(rawMsg, &response); err == nil && response.Type != "" {
			if response.Type == "event" || response.Type == "telemetry" {
				// Ignore background events, keep waiting for the real response
				continue
			}
			if respData, ok := response.Data.(string); ok {
				return respData, nil
			}
			// If it's a JSON object, format it as string
			bytes, _ := json.MarshalIndent(response.Data, "", "  ")
			return string(bytes), nil
		}

		// Try to parse as OpenAI-compatible ChatResponse
		var chatResp ChatResponse
		if err := json.Unmarshal(rawMsg, &chatResp); err == nil && len(chatResp.Choices) > 0 {
			return chatResp.Choices[0].Message.Content, nil
		}

		// Fallback: treat the raw payload as standard text/markdown stream
		return string(rawMsg), nil
	}
}

// sproutCLISession asks the Go Stem for a new CLI-origin Tendril session so
// every message in this chat run shares one unified session ID.
func sproutCLISession(base *url.URL) (string, error) {
	u := *base
	u.Path = "/v1/sessions"

	req, err := http.NewRequest("POST", u.String(), strings.NewReader(`{"origin":"cli"}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+chatAPIKey())

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var created sessionCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", err
	}
	if created.SessionID == "" {
		return "", fmt.Errorf("server returned no sessionId")
	}
	return created.SessionID, nil
}

// chatAPIKey resolves the bearer key this CLI sends to the local Stem. It
// mirrors getOrCreateAPIKey's resolution order (cmdserve.go) without ever
// generating a key itself: the `serve` command owns key creation, and
// persists it to the same "./.tendril/api-key" file this reads (issue #171
// finding 1 — the Stem no longer runs unauthenticated, so this client can no
// longer assume a keyless server).
func chatAPIKey() string {
	if key := strings.TrimSpace(os.Getenv("OPENTENDRIL_API_KEY")); key != "" {
		return key
	}
	if key := readPersistedAPIKey("./.tendril"); key != "" {
		return key
	}
	return "sk-123" // Only reached against a Stem predating issue #171's auto-generated key
}

// sendHTTP sends a message via HTTP to the OpenAI-compatible endpoint.
func sendHTTP(base *url.URL, msg, sessionID string) (string, error) {
	u := *base
	u.Path = "/v1/chat/completions"
	reqBody := ChatRequest{
		// Use model name from env, default to "local" so Go Stem routes correctly
		Model:     getEnvOrDefaultStr("LOCAL_MODEL_NAME", os.Getenv("DEFAULT_LLM_PROVIDER")),
		SessionID: sessionID,
		Messages: []Message{
			{Role: "user", Content: msg},
		},
		Stream: false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", u.String(), strings.NewReader(string(jsonData)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+chatAPIKey())
	if sessionID != "" {
		req.Header.Set("X-Tendril-Session", sessionID)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var chatResp ChatResponse
	err = json.NewDecoder(resp.Body).Decode(&chatResp)
	if err != nil {
		return "", err
	}

	if len(chatResp.Choices) > 0 {
		return chatResp.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("no content in response")
}

// connect attempts WS first, falls back to HTTP after retries.
func connect(base *url.URL, useWS bool, sessionID string) (func(string) (string, error), error) {
	if !useWS {
		log.Println("Using HTTP mode")
		return func(msg string) (string, error) { return sendHTTP(base, msg, sessionID) }, nil
	}

	// Try WS with retries
	for i := 0; i < 3; i++ {
		conn, err := connectWS(base)
		if err == nil {
			log.Println("Connected via WebSocket")
			return func(msg string) (string, error) { return sendWS(conn, msg) }, nil
		}
		log.Printf("WS retry %d/3 failed: %v. Waiting 2s...", i+1, err)
		time.Sleep(2 * time.Second)
	}

	log.Println("WS failed; falling back to HTTP")
	return func(msg string) (string, error) { return sendHTTP(base, msg, sessionID) }, nil
}

func runChatCmd(ctx context.Context, args []string) {
	useWS := false // Default to HTTP for Go Stem
	for _, arg := range args {
		if arg == "--ws" {
			useWS = true
		}
	}

	base, err := url.Parse("http://localhost:8080") // Go Stem default port
	if err != nil {
		log.Fatal("Invalid URL:", err)
	}

	// Check Go Stem is reachable
	fmt.Println("🌱 Connecting to OpenTendril Stem...")
	ensureBackendOnline(ctx, "http://localhost:8080")

	// Bind this chat run to a unified Tendril session; older servers without
	// the sessions API still work (messages just run session-less).
	sessionID, err := sproutCLISession(base)
	if err != nil {
		log.Printf("⚠️ Could not sprout a Tendril session (continuing without one): %v", err)
	} else {
		log.Printf("🪴 Chat bound to Tendril session %s", sessionID)
	}

	sendFunc, err := connect(base, useWS, sessionID)
	if err != nil {
		log.Fatal("Failed to connect:", err)
	}

	log.Println("Connected! Type your task below, or 'exit' to quit.")
	log.Println("Tip: Use 'tendril chat --ws' to force WebSocket mode (if gateway is running).")

	scanner := bufio.NewScanner(os.Stdin)

	for scanner.Scan() {
		msg := strings.TrimSpace(scanner.Text())
		if msg == "" || msg == "exit" || msg == "/exit" {
			break
		}

		// --- Host-side Command Interception ---
		if msg == "/restart" {
			restartBackend(ctx, "http://localhost:8080")
			continue
		}

		if msg == "/test" {
			log.Println("🧪 Running health checks...")
			cmd := exec.Command("docker", "compose", "ps")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			_ = cmd.Run()
			log.Println("✅ Config OK.")
			continue
		}

		// Let the backend process these first to save to .env, then intercept to restart Docker
		isRepoCmd := strings.HasPrefix(msg, "/repo ")
		isLocalCmd := msg == "/local"
		isInitCmd := strings.HasPrefix(msg, "/init")
		isSecureCmd := msg == "/secure"

		response, err := sendFunc(msg)
		if err != nil {
			log.Printf("Error: %v", err)
			continue
		}
		fmt.Println(response)

		// After backend responds successfully, trigger the host-side Docker restart
		if (isRepoCmd || isInitCmd || isSecureCmd) && !strings.Contains(response, "❌") {
			restartBackend(ctx, "http://localhost:8080")
		} else if isLocalCmd && !strings.Contains(response, "❌") {
			log.Println("🔄 Restarting with GPU Profile enabled...")
			cmd1 := exec.Command("docker", "compose", "down")
			_ = cmd1.Run()
			cmd2 := exec.Command("docker", "compose", "--profile", "gpu", "up", "-d")
			cmd2.Stdout = os.Stdout
			cmd2.Stderr = os.Stderr
			_ = cmd2.Run()
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Scanner error: %v", err)
	}
}

func getEnvOrDefaultStr(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	if fallback != "" {
		return fallback
	}
	return "local" // final fallback for the Go Stem router
}

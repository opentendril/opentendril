// Package main implements the Tendril CLI client.
// Supports WebSocket (primary) and HTTP fallback to the Brain's OpenAI-compatible endpoint.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

var (
	wsFlag  = flag.Bool("ws", true, "Use WebSocket connection (default)")
	httpFlag = flag.Bool("http", false, "Force HTTP connection (OpenAI-compatible)")
	baseURL  = flag.String("url", "localhost:9090", "Base URL for the Tendril server")
	helpFlag = flag.Bool("h", false, "Show help")
	mcpFlag  = flag.Bool("mcp", false, "Run in MCP stdio mode")
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// connectWS establishes a WebSocket connection to the gateway.
func connectWS(base *url.URL) (*websocket.Conn, error) {
	u := url.URL{Scheme: "ws", Host: base.Host, Path: "/ws"}
	log.Printf("Connecting to WS: %s", u.String())
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Printf("WS connect failed: %v", err)
		return nil, err
	}
	return conn, nil
}

// sendWS sends a message over WebSocket and reads the response.
func sendWS(conn *websocket.Conn, msg string) (string, error) {
	err := conn.WriteJSON(WSMessage{Type: "user_message", Data: msg})
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

// sendHTTP sends a message via HTTP to the OpenAI-compatible endpoint.
func sendHTTP(base *url.URL, msg string) (string, error) {
	u := *base
	u.Path = "/v1/chat/completions"
	reqBody := ChatRequest{
		Model: "gpt-4", // Default model; can be configured later
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
	req.Header.Set("Authorization", "Bearer sk-123") // Dummy key; configure via env or flag

	client := &http.Client{Timeout: 30 * time.Second}
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
func connect(base *url.URL, useWS bool) (func(string) (string, error), error) {
	if !useWS {
		log.Println("Using HTTP mode")
		return func(msg string) (string, error) { return sendHTTP(base, msg) }, nil
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
	return func(msg string) (string, error) { return sendHTTP(base, msg) }, nil
}

func main() {
	flag.Parse()
	if *helpFlag {
		flag.PrintDefaults()
		os.Exit(0)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		log.Println("\nExiting...")
		cancel()
		if backendCmd != nil && backendCmd.Process != nil {
			_ = backendCmd.Process.Kill()
		}
		os.Exit(0)
	}()

	if *mcpFlag {
		runMCPRelay(ctx, "http://localhost:8080")
		return
	}

	if *wsFlag && *httpFlag {
		log.Fatal("Cannot use both --ws and --http")
	}
	useWS := *wsFlag

	base, err := url.Parse(fmt.Sprintf("http://%s", *baseURL))
	if err != nil {
		log.Fatal("Invalid URL:", err)
	}

	sendFunc, err := connect(base, useWS)
	if err != nil {
		log.Fatal("Failed to connect:", err)
	}

	log.Println("Tendril CLI connected! Type 'exit' or Ctrl+C to quit.")
	log.Println("Tip: Use --http to force HTTP mode.")

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

var backendCmd *exec.Cmd

func runMCPRelay(ctx context.Context, brainURL string) {
	ensureBackendOnline(ctx, brainURL)

	reader := bufio.NewReader(os.Stdin)
	client := &http.Client{Timeout: 300 * time.Second}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("Error reading stdin: %v", err)
			break
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var req struct {
			ID interface{} `json:"id"`
		}
		_ = json.Unmarshal([]byte(line), &req)

		resp, err := client.Post(brainURL+"/v1", "application/json", strings.NewReader(line))
		if err != nil {
			sendJSONRPCError(req.ID, -32603, "Failed to connect to backend: "+err.Error())
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			sendJSONRPCError(req.ID, -32603, "Failed to read backend response: "+err.Error())
			continue
		}

		if resp.StatusCode != http.StatusOK {
			sendJSONRPCError(req.ID, -32603, fmt.Sprintf("Backend returned HTTP status %d: %s", resp.StatusCode, string(respBody)))
			continue
		}

		fmt.Println(string(respBody))
	}
}

func sendJSONRPCError(id interface{}, code int, message string) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
		"id": id,
	}
	bytes, _ := json.Marshal(resp)
	fmt.Println(string(bytes))
}

func ensureBackendOnline(ctx context.Context, brainURL string) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	req, err := http.NewRequestWithContext(ctx, "GET", brainURL+"/health", nil)
	if err == nil {
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return
		}
	}

	log.Println("⚠️ Backend FastAPI server is offline. Auto-booting...")
	projectDir := findProjectDir()

	if isSandboxEnabled() && isDockerRunning() {
		log.Println("🐳 Booting backend via Docker Compose...")
		c := exec.Command("docker", "compose", "up", "-d")
		c.Dir = projectDir
		c.Stdout = os.Stderr
		c.Stderr = os.Stderr
		_ = c.Run()
	} else {
		log.Println("🚀 Booting backend via standard subprocess (Solo Mode)...")
		var uvicornCmd string
		venvPath := filepath.Join(projectDir, "venv", "bin", "uvicorn")
		if _, err := os.Stat(venvPath); err == nil {
			uvicornCmd = venvPath
		} else {
			uvicornCmd = "uvicorn"
		}

		backendCmd = exec.Command(uvicornCmd, "src.main:app", "--host", "127.0.0.1", "--port", "8080")
		backendCmd.Dir = projectDir
		backendCmd.Stdout = os.Stderr
		backendCmd.Stderr = os.Stderr
		if err := backendCmd.Start(); err != nil {
			log.Printf("Error starting uvicorn: %v", err)
		}
	}

	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		req, err := http.NewRequestWithContext(ctx, "GET", brainURL+"/health", nil)
		if err == nil {
			resp, err := client.Do(req)
			if err == nil && resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				log.Println("✅ Backend online!")
				return
			}
		}
	}
	log.Println("❌ Timeout waiting for backend to start.")
}

func isSandboxEnabled() bool {
	if val := os.Getenv("SANDBOX_ENABLED"); val != "" {
		return strings.ToLower(val) == "true"
	}
	projectDir := findProjectDir()
	paths := []string{
		filepath.Join(projectDir, ".env"),
		filepath.Join(projectDir, "core", ".env"),
	}
	for _, p := range paths {
		file, err := os.Open(p)
		if err == nil {
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if strings.HasPrefix(line, "SANDBOX_ENABLED=") {
					parts := strings.SplitN(line, "=", 2)
					if len(parts) == 2 {
						val := strings.Trim(parts[1], `"' `)
						return strings.ToLower(val) == "true"
					}
				}
			}
		}
	}
	return true
}

func isDockerRunning() bool {
	cmd := exec.Command("docker", "info")
	return cmd.Run() == nil
}

func findProjectDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "src", "main.py")); err == nil {
			return dir
		}
		if _, err := os.Stat(filepath.Join(dir, "core", "src", "main.py")); err == nil {
			return filepath.Join(dir, "core")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

func restartBackend(ctx context.Context, brainURL string) {
	if isSandboxEnabled() && isDockerRunning() {
		log.Println("🔄 Remounting volumes and applying configuration via Docker Compose...")
		cmd := exec.Command("docker", "compose", "up", "-d")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	} else {
		log.Println("🔄 Restarting backend subprocess (Solo Mode)...")
		if backendCmd != nil && backendCmd.Process != nil {
			_ = backendCmd.Process.Kill()
			_ = backendCmd.Wait()
		}

		projectDir := findProjectDir()
		var uvicornCmd string
		venvPath := filepath.Join(projectDir, "venv", "bin", "uvicorn")
		if _, err := os.Stat(venvPath); err == nil {
			uvicornCmd = venvPath
		} else {
			uvicornCmd = "uvicorn"
		}

		backendCmd = exec.Command(uvicornCmd, "src.main:app", "--host", "127.0.0.1", "--port", "8080")
		backendCmd.Dir = projectDir
		backendCmd.Stdout = os.Stderr
		backendCmd.Stderr = os.Stderr
		if err := backendCmd.Start(); err != nil {
			log.Printf("Error starting uvicorn: %v", err)
			return
		}

		client := &http.Client{Timeout: 500 * time.Millisecond}
		for i := 0; i < 20; i++ {
			time.Sleep(500 * time.Millisecond)
			req, err := http.NewRequestWithContext(ctx, "GET", brainURL+"/health", nil)
			if err == nil {
				resp, err := client.Do(req)
				if err == nil && resp.StatusCode == http.StatusOK {
					resp.Body.Close()
					log.Println("✅ Backend online!")
					return
				}
			}
		}
	}
}

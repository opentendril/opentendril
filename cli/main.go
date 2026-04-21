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
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

var (
	wsFlag  = flag.Bool("ws", true, "Use WebSocket connection (default)")
	httpFlag = flag.Bool("http", false, "Force HTTP connection (OpenAI-compatible)")
	baseURL  = flag.String("url", "localhost:8080", "Base URL for the Tendril server")
	helpFlag = flag.Bool("h", false, "Show help")
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

	var response WSMessage
	err = conn.ReadJSON(&response)
	if err != nil {
		return "", err
	}

	if respData, ok := response.Data.(string); ok {
		return respData, nil
	}
	return "", fmt.Errorf("invalid WS response")
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
	_, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		log.Println("\nExiting...")
		cancel()
	}()

	for scanner.Scan() {
		msg := strings.TrimSpace(scanner.Text())
		if msg == "" || msg == "exit" || msg == "/exit" {
			break
		}

		// --- Host-side Command Interception ---
		if msg == "/restart" {
			log.Println("🔄 Restarting Tendril containers...")
			cmd := exec.Command("docker", "compose", "restart")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			_ = cmd.Run()
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

		response, err := sendFunc(msg)
		if err != nil {
			log.Printf("Error: %v", err)
			continue
		}
		fmt.Println(response)

		// After backend responds successfully, trigger the host-side Docker restart
		if isRepoCmd && !strings.Contains(response, "❌") {
			log.Println("🔄 Remounting volumes (this may take a few seconds)...")
			cmd := exec.Command("docker", "compose", "up", "-d")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			_ = cmd.Run()
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

// tendril-cli — Interactive terminal client for the Tendril AI orchestrator.
//
// Connects via WebSocket to the Tendril Chat Gateway.
// Streams responses word-by-word like Codex CLI / Gemini CLI.
//
// Usage:
//
//	tendril-cli                          # Connect to localhost:9090
//	tendril-cli --url ws://host:9090/ws  # Connect to remote gateway
//	tendril-cli --provider anthropic     # Force a specific LLM provider
//	tendril-cli --session my-project     # Persistent named session
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gorilla/websocket"
)

// Message types (matching gateway protocol)
type IncomingMessage struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	Provider  string `json:"provider,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type OutgoingMessage struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	RunID   string `json:"run_id,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ANSI colors
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorDim    = "\033[2m"
	colorBold   = "\033[1m"
)

func main() {
	url := flag.String("url", "ws://localhost:9090/ws", "Gateway WebSocket URL")
	provider := flag.String("provider", "default", "LLM provider (grok, anthropic, openai)")
	session := flag.String("session", "cli-default", "Session ID for conversation persistence")
	flag.Parse()

	fmt.Printf("%s%s🌱 Tendril CLI v0.1.0%s\n", colorBold, colorGreen, colorReset)
	fmt.Printf("%sConnecting to %s...%s\n", colorDim, *url, colorReset)

	conn, _, err := websocket.DefaultDialer.Dial(*url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s❌ Connection failed: %v%s\n", colorRed, err, colorReset)
		fmt.Fprintf(os.Stderr, "\nMake sure the gateway is running:\n")
		fmt.Fprintf(os.Stderr, "  docker compose up gateway\n")
		os.Exit(1)
	}
	defer conn.Close()

	// Wait for connected message
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Fatalf("Failed to read connection confirmation: %v", err)
	}
	var connectMsg OutgoingMessage
	json.Unmarshal(msg, &connectMsg)
	if connectMsg.Type == "connected" {
		fmt.Printf("%s✅ Connected (session: %s, provider: %s)%s\n",
			colorGreen, *session, *provider, colorReset)
	}

	fmt.Printf("%sType your message and press Enter. Ctrl+C to exit.%s\n\n", colorDim, colorReset)

	// Handle Ctrl+C gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Printf("\n%s👋 Goodbye!%s\n", colorCyan, colorReset)
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		os.Exit(0)
	}()

	// Response reader goroutine
	responseCh := make(chan OutgoingMessage, 100)
	go func() {
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					return
				}
				fmt.Fprintf(os.Stderr, "\n%s⚠️  Connection lost: %v%s\n", colorYellow, err, colorReset)
				os.Exit(1)
			}
			var msg OutgoingMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			responseCh <- msg
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)

	for {
		// Prompt
		fmt.Printf("%s%syou › %s", colorBold, colorCyan, colorReset)

		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// Handle special commands
		if input == "/quit" || input == "/exit" {
			fmt.Printf("%s👋 Goodbye!%s\n", colorCyan, colorReset)
			break
		}
		if input == "/clear" {
			fmt.Print("\033[H\033[2J") // Clear screen
			continue
		}
		if strings.HasPrefix(input, "/provider ") {
			*provider = strings.TrimPrefix(input, "/provider ")
			fmt.Printf("%sSwitched to provider: %s%s\n", colorDim, *provider, colorReset)
			continue
		}
		if input == "/help" {
			fmt.Printf("%sCommands:%s\n", colorDim, colorReset)
			fmt.Printf("  /provider <name>  Switch LLM provider\n")
			fmt.Printf("  /clear            Clear screen\n")
			fmt.Printf("  /quit             Exit\n\n")
			continue
		}

		// Send message
		outMsg := IncomingMessage{
			Type:      "message",
			Content:   input,
			Provider:  *provider,
			SessionID: *session,
		}
		data, _ := json.Marshal(outMsg)
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			fmt.Fprintf(os.Stderr, "%s❌ Send failed: %v%s\n", colorRed, err, colorReset)
			break
		}

		// Read streaming response
		fmt.Printf("%s%stendril › %s", colorBold, colorGreen, colorReset)
		streaming := true
		for streaming {
			select {
			case msg := <-responseCh:
				switch msg.Type {
				case "stream.start":
					// Response is starting
				case "stream.token":
					fmt.Print(msg.Content)
				case "stream.end":
					fmt.Println() // Newline after response
					fmt.Println()
					streaming = false
				case "error":
					fmt.Printf("\n%s❌ %s%s\n\n", colorRed, msg.Error, colorReset)
					streaming = false
				}
			}
		}
	}
}

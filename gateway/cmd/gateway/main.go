// Tendril Chat Gateway — WebSocket entry point for all chat channels.
//
// This is the public-facing service that replaces SSE streaming.
// Clients (Web UI, CLI, Slack, Discord) connect via WebSocket.
// Messages are normalized and proxied to the Python brain.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/opentendril/gateway/internal/proxy"
	"github.com/opentendril/gateway/internal/server"
)

func main() {
	port := getEnv("GATEWAY_PORT", "9090")
	brainURL := getEnv("BRAIN_URL", "http://localhost:8080")

	log.Printf("🌱 Tendril Gateway starting on :%s", port)
	log.Printf("🧠 Brain endpoint: %s", brainURL)

	brain := proxy.NewBrainClient(brainURL)
	hub := server.NewHub()
	handler := &server.Handler{Hub: hub, Brain: brain}

	// WebSocket endpoint
	http.HandleFunc("/ws", handler.ServeWS)

	// Health check
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := brain.Health()
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status":"unhealthy","error":"%s"}`, err.Error())
			return
		}
		fmt.Fprintf(w, `{"status":"healthy","connections":%d,"sessions":%d}`,
			hub.ClientCount(), hub.SessionCount())
	})

	// Status page
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Tendril Chat Gateway v0.1.0\nWebSocket: ws://localhost:%s/ws\nHealth:    http://localhost:%s/health\n", port, port)
	})

	log.Printf("✅ Gateway ready. WebSocket at ws://0.0.0.0:%s/ws", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Gateway failed: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

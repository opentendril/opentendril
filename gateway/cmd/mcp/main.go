package main

import (
	"log"
	"os"

	"github.com/opentendril/gateway/internal/mcp"
	"github.com/opentendril/gateway/internal/proxy"
)

func main() {
	// MCP stdio requires clean stdout for JSON-RPC. We must redirect logs to stderr.
	log.SetOutput(os.Stderr)

	brainURL := getEnv("BRAIN_URL", "http://localhost:8080")
	log.Printf("🌱 Tendril MCP Gateway starting")
	log.Printf("🧠 Brain endpoint: %s", brainURL)

	brain := proxy.NewBrainClient(brainURL)
	server := mcp.NewServer(brain)

	server.Start()
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

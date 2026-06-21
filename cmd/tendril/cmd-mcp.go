package main

import (
	"context"

	"github.com/opentendril/sprout/internal/mcp"
	"github.com/opentendril/sprout/internal/proxy"
)

func runMCPCmd(args []string) {
	// Start the MCP JSON-RPC stdio server
	// We want to ensure the backend is online first.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	brainURL := "http://localhost:8080"
	ensureBackendOnline(ctx, brainURL)

	brainClient := proxy.NewBrainClient(brainURL)
	server := mcp.NewServer(brainClient)
	server.Start()
}

package main

import (

	"github.com/opentendril/core/cmd/stem/internal/mcp"
	"github.com/opentendril/core/cmd/stem/internal/proxy"
)

func runMCPCmd(args []string) {
	// Start the MCP JSON-RPC stdio server
	// We want to ensure the backend is online first.


	brainURL := "http://localhost:8080"
	brainClient := proxy.NewBrainClient(brainURL)
	server := mcp.NewServer(brainClient)
	server.Start()
}

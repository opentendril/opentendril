package main

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/opentendril/core/cmd/stem/internal/api"
)

func runMCPCmd(ctx context.Context, args []string) {
	fmt.Fprintln(os.Stderr, "🚀 OpenTendril MCP Stdio Server initializing...")

	handler := api.NewMCPHandler()
	scanner := bufio.NewScanner(os.Stdin)

	// Increase buffer size to handle large MCP schemas
	const maxCapacity = 1024 * 1024 * 5 // 5MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	fmt.Fprintln(os.Stderr, "🟢 OpenTendril MCP Server ready. Listening on stdio.")

	for scanner.Scan() {
		reqBytes := scanner.Bytes()
		if len(reqBytes) == 0 {
			continue
		}

		respBytes := handler.ProcessMCPMessage(reqBytes)
		
		if len(respBytes) > 0 {
			// Write response exactly as one line to stdout
			fmt.Fprintln(os.Stdout, string(respBytes))
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ MCP Stdio Error: %v\n", err)
	}

	fmt.Fprintln(os.Stderr, "🛑 OpenTendril MCP Stdio Server exiting.")
}

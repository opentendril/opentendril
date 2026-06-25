// Package main implements the Unified Tendril CLI client.
// Commands:
//   tendril chat  - Start the interactive chat interface
//   tendril mcp   - Start the MCP JSON-RPC stdio server
//   tendril init  - Run the Developer Onboarding Wizard

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
)

func main() {
	// Load .env file if it exists
	_ = godotenv.Load()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		cancel()
		if backendCmd != nil && backendCmd.Process != nil {
			_ = backendCmd.Process.Kill()
		}
		os.Exit(0)
	}()

	switch os.Args[1] {
	case "chat":
		runChatCmd(ctx, os.Args[2:])
	case "mcp":
		runMCPCmd(ctx, os.Args[2:])
	case "init":
		runInitCmd(os.Args[2:])
	case "serve":
		runServeCmd(ctx, os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Tendril CLI - The Unified OpenTendril Tool")
	fmt.Println("\nUsage:")
	fmt.Println("  tendril <command> [arguments]")
	fmt.Println("\nCommands:")
	fmt.Println("  chat    Start the interactive chat interface")
	fmt.Println("  mcp     Start the MCP JSON-RPC stdio server")
	fmt.Println("  init    Run the Developer Onboarding Wizard")
	fmt.Println("  serve   Start the Go Stem Orchestrator API")
}

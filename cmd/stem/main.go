// Package main implements the Unified Tendril CLI client.
// Commands:
//   tendril chat  - Start the interactive chat interface
//   tendril mesh  - Manage mesh grafting keys and tokens
//   tendril mcp   - Start the MCP JSON-RPC stdio server
//   tendril init  - Run the Developer Onboarding Wizard
//   tendril serve - Start the Go Stem Orchestrator API
//   tendril adapt - Mine recent git history into the genome
//   tendril genome - Inspect, reduce, or evolve the active genome
//   tendril plasmid - List or inject modular genome seeds
//   tendril repomap - Generate the active repository map
//   tendril sequence - Run or list YAML task sequences

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
	case "adapt":
		runAdaptCmd(ctx, os.Args[2:])
	case "genome":
		runGenomeCmd(ctx, os.Args[2:])
	case "plasmid":
		runPlasmidCmd(os.Args[2:])
	case "repomap":
		runRepoMapCmd(os.Args[2:])
	case "sequence":
		runSequenceCmd(ctx, os.Args[2:])
	case "mesh":
		runMeshCmd(ctx, os.Args[2:])
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
	fmt.Println("  adapt   Mine recent git history into .tendril/genome/epigenetics.md")
	fmt.Println("  genome  Inspect, reduce, or evolve the active genome seeds")
	fmt.Println("  plasmid Manage modular genome plasmids")
	fmt.Println("  repomap Generate the active repository map")
	fmt.Println("  sequence Run or list YAML task sequences")
	fmt.Println("  mesh    Manage mesh grafting keys and tokens")
	fmt.Println("  mcp     Start the MCP JSON-RPC stdio server")
	fmt.Println("  init    Run the Developer Onboarding Wizard")
	fmt.Println("  serve   Start the Go Stem Orchestrator API")
}

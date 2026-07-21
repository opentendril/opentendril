// Package main implements the Unified Tendril CLI client.
// Commands:
//   tendril chat  - Start the interactive chat interface
//   tendril setup - Bootstrap configuration for a new Substrate workspace
//   tendril mesh  - Manage mesh grafting keys and tokens
//   tendril mcp   - Start the MCP JSON-RPC stdio server
//   tendril init  - Run the Developer Onboarding Wizard
//   tendril serve - Start the Go Stem Orchestrator API or MCP stdio bridge
//   tendril adapt - Mine recent git history into the genome
//   tendril genome - Inspect, reduce, or evolve the active genome
//   tendril plasmid - Sign, list, or inject modular genome seeds
//   tendril memory - Store, search, and export project memory
//   tendril repomap - Generate the active repository map
//   tendril sequence - Run or list YAML task sequences
//   tendril sprout - Delegate a one-shot task to an autonomous Tendril
//   tendril passthrough - Run one bounded command in a network-sealed terrarium
//   tendril git   - Commit a substrate's workspace under its configured identity
//   tendril terrarium - Manage execution terrarium environments
//   tendril health - Run infrastructure health diagnostics
//   tendril llm   - Inspect and test the configured local LLM provider
//   tendril assess - Probe local hardware and judge which local models fit

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/opentendril/opentendril/roots/llm"
)

func main() {
	// Load .env file if it exists
	_ = godotenv.Load()
	llm.StartModelDiscovery()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		// First signal: cancel the context and let the running command unwind
		// its deferred cleanup (the conductor restores the host pre-flight stash
		// through a context.WithoutCancel cleanup context, so it survives this
		// cancellation). Exiting immediately here would skip those defers and
		// strand the user's stashed working tree. main() returns normally once
		// the command finishes, so no os.Exit is needed on the graceful path.
		cancel()
		if backendCmd != nil && backendCmd.Process != nil {
			_ = backendCmd.Process.Kill()
		}
		// Bound the graceful window: force quit on a second signal or after a
		// short grace period, so a command that ignores ctx can't hang the
		// process. On the graceful path main() returns first and the process
		// exits before this fires.
		select {
		case <-c:
		case <-time.After(10 * time.Second):
		}
		os.Exit(130)
	}()

	switch os.Args[1] {
	case "chat":
		runChatCmd(ctx, os.Args[2:])
	case "phytomer", "session": // "session" is the legacy alias for "phytomer"
		runSessionCmd(ctx, os.Args[2:])
	case "setup":
		runSetupCmd(os.Args[2:])
	case "adapt":
		runAdaptCmd(ctx, os.Args[2:])
	case "genome":
		runGenomeCmd(ctx, os.Args[2:])
	case "plasmid":
		runPlasmidCmd(ctx, os.Args[2:])
	case "memory":
		runMemoryCmd(ctx, os.Args[2:])
	case "repomap":
		runRepoMapCmd(os.Args[2:])
	case "sequence":
		runSequenceCmd(ctx, os.Args[2:])
	case "verdict":
		runVerdictCmd(os.Args[2:])
	case "sprout":
		runSproutCmd(ctx, os.Args[2:])
	case "passthrough":
		runPassthroughCmd(ctx, os.Args[2:])
	case "git":
		runGitCmd(ctx, os.Args[2:])
	case "terrarium":
		runTerrariumCmd(ctx, os.Args[2:])
	case "health":
		runHealthCmd(ctx, os.Args[2:])
	case "llm":
		runLLMCmd(ctx, os.Args[2:])
	case "assess":
		runAssessCmd(ctx, os.Args[2:])
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
	fmt.Println("  phytomer Manage Phytomers (create/list/get/update/delete/history); alias: session")
	fmt.Println("  setup   Bootstrap Substrate workspace configuration")
	fmt.Println("  adapt   Mine recent git history into .tendril/genome/epigenetics.md")
	fmt.Println("  genome  Inspect, reduce, or evolve the active genome seeds")
	fmt.Println("  plasmid   Sign and verify Plasmid integrity")
	fmt.Println("  memory  Store, search, and export project memory")
	fmt.Println("  repomap Generate the active repository map")
	fmt.Println("  sequence Run or list YAML task sequences")
	fmt.Println("  verdict Judge a completed test run, skip-aware")
	fmt.Println("  sprout  Delegate a one-shot task to an autonomous Tendril in a terrarium")
	fmt.Println("  passthrough Run one bounded command in a network-sealed terrarium")
	fmt.Println("  git     Commit a substrate's workspace under its configured commit identity")
	fmt.Println("  terrarium Manage execution terrarium environments")
	fmt.Println("  health  Run infrastructure health diagnostics")
	fmt.Println("  llm     List or test the configured local LLM provider")
	fmt.Println("  assess  Probe local hardware and judge which local models fit")
	fmt.Println("  mesh    Manage mesh grafting keys and tokens")
	fmt.Println("  mcp     Start the MCP JSON-RPC stdio server")
	fmt.Println("  init    Run the Developer Onboarding Wizard")
	fmt.Println("  serve   Start the Go Stem Orchestrator API or MCP stdio bridge")
}

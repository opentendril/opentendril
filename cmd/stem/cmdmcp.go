package main

import (
	"bufio"
	"context"
	"fmt"
	"os"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/historydb"
	"github.com/opentendril/core/cmd/stem/internal/receptors"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

func runMCPCmd(ctx context.Context, args []string) {
	fmt.Fprintln(os.Stderr, "🚀 OpenTendril MCP Stdio Server initializing...")

	handler := receptors.NewMCPHandler()

	// Unified Interface Layer: bind this stdio server process to one Tendril
	// session so MCP interactions share state with the CLI and REST surfaces.
	history, err := historydb.OpenFromEnv(ctx, resolveRepoRoot(""))
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ History database unavailable: %v (continuing without persistence)\n", err)
		history = nil
	}
	var sessionStore session.Store
	if history != nil {
		sessionStore = history
		defer history.Close()
	}
	if manager, err := session.NewManager(ctx, sessionStore); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Session manager unavailable: %v (continuing without sessions)\n", err)
	} else if sess, err := manager.Sprout(ctx, session.OriginMCP, session.Preferences{}); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to sprout MCP session: %v (continuing without sessions)\n", err)
	} else {
		coreSvc := core.NewService(manager).
			WithGenome(genomeOperations(resolveRepoRoot(""))).
			WithPlasmid(plasmidOperations(resolveRepoRoot(""))).
			WithMesh(meshOperations()).
			// The stdio server has no event bus: nil keeps its sequence runs
			// telemetry-silent, exactly as before.
			WithSequence(serveSequenceOperations(resolveRepoRoot(""), nil)).
			WithSprout(sproutOperations(history)).
			WithPassthrough(passthroughOperations())
		handler = handler.WithSessions(manager, history).WithDefaultSession(sess.ID).WithCore(coreSvc)
		fmt.Fprintf(os.Stderr, "🪴 MCP interactions bound to Tendril session %s\n", sess.ID)
	}

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

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/historydb"
	"github.com/opentendril/core/cmd/stem/internal/receptors"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// envDelegationSubject names the environment variable that binds this MCP
// stdio server process to one delegation subject at startup. The subject is a
// property of the trusted MCP connection — never declared per-invocation in
// tool arguments — so every delegated-class invocation on this connection is
// authorized as that one subject against the active grants. Unset means no
// subject is bound and every delegated capability is denied over MCP
// (deny-closed).
const envDelegationSubject = "OPENTENDRIL_DELEGATION_SUBJECT"

func runMCPCmd(ctx context.Context, args []string) {
	fmt.Fprintln(os.Stderr, "🚀 OpenTendril MCP Stdio Server initializing...")

	tendrilDir := "./.tendril"
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

	// Delegation audit lane: the same EventBus + history.db sink wiring the
	// REST server uses, so every delegation decision made on this surface is
	// audited to history.db. The deferred Shutdown drains the sink before the
	// deferred history.Close above releases the database.
	bus := eventbus.New()
	if history != nil {
		bus.AttachSink(history, 0)
	}
	defer bus.Shutdown()

	if manager, err := session.NewManager(ctx, sessionStore); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Session manager unavailable: %v (continuing without sessions)\n", err)
	} else if sess, err := manager.Sprout(ctx, session.OriginMCP, session.Preferences{}); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to sprout MCP session: %v (continuing without sessions)\n", err)
	} else {
		coreSvc := core.NewService(manager).
			WithGenome(genomeOperations(resolveRepoRoot(""))).
			WithPlasmid(plasmidOperations(resolveRepoRoot(""))).
			WithMesh(meshOperations()).
			// Sequence runs on this stdio server stay telemetry-silent (a nil
			// bus), exactly as before; the EventBus above carries only the
			// delegation audit lane.
			WithSequence(serveSequenceOperations(resolveRepoRoot(""), nil)).
			WithSprout(sproutOperations(history)).
			WithPassthrough(passthroughOperations()).
			WithGit(gitOperations())
		handler = handler.WithSessions(manager, history).WithDefaultSession(sess.ID).WithCore(coreSvc)
		fmt.Fprintf(os.Stderr, "🪴 MCP interactions bound to Tendril session %s\n", sess.ID)
	}

	// Delegated-execution control plane (mirroring the REST server): grants
	// load from the Stem's own .tendril/grants.yaml — never from a Substrate
	// checkout. With zero grants every delegated invocation is denied and all
	// non-delegated behavior is untouched; a malformed grants file degrades
	// the same way, never open.
	delegationGrants, grantsErr := core.LoadDelegationGrants(tendrilDir)
	if grantsErr != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to load delegation grants: %v (delegation disabled — every delegated invocation is denied)\n", grantsErr)
		delegationGrants = nil
	}
	if len(delegationGrants) > 0 {
		fmt.Fprintf(os.Stderr, "🔏 Delegation enabled: %d grant(s) loaded from %s\n", len(delegationGrants), filepath.Join(tendrilDir, core.DelegationGrantsFilename))
	} else {
		fmt.Fprintln(os.Stderr, "🔏 No delegation grants configured: every delegated invocation is denied (secure default)")
	}
	delegationGate := &receptors.DelegationGate{
		Authorizer: core.NewDelegationAuthorizer(delegationGrants),
		Bus:        bus,
	}

	// The delegation subject is bound once, at startup, as a property of this
	// trusted stdio connection — a tool argument can never self-declare it.
	delegationSubject := strings.TrimSpace(os.Getenv(envDelegationSubject))
	if delegationSubject != "" {
		fmt.Fprintf(os.Stderr, "🔏 Delegation subject %q bound from %s: delegated capabilities are authorized against the loaded grants\n", delegationSubject, envDelegationSubject)
	} else {
		fmt.Fprintf(os.Stderr, "🔏 No delegation subject bound (%s is unset): delegated capabilities are denied over MCP (deny-closed)\n", envDelegationSubject)
	}
	handler = handler.WithDelegation(delegationGate, delegationSubject)

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

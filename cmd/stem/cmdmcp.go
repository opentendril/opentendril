package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
	"github.com/opentendril/opentendril/internal/mcpclient"
	"strings"
)

// envPollen names the environment variable that binds this MCP
// stdio server process to one Pollen at startup. The pollen is a
// property of the trusted MCP connection — never declared per-invocation in
// tool arguments — so every delegated-class invocation on this connection is
// authorized as that one Pollen against the active grants. Unset means no
// pollen is bound and every delegated capability is denied over MCP
// (deny-closed).
const envPollen = "TENDRIL_POLLEN"

func runMCPCmd(ctx context.Context, args []string) {
	fmt.Fprintln(os.Stderr, "🚀 OpenTendril MCP Stdio Server initializing...")

	// Intake the durable root credential if configured (consumed in slice 2).
	// A malformed or over-permissive credential file is a startup failure, not
	// a silent downgrade, so a user trying to configure one is told why it failed.
	rootCred, credErr := mcpclient.LoadCredential()
	if credErr != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", credErr)
		os.Exit(1)
	}

	decision := detectMCPMode(ctx, rootCred != "")

	if decision.Message != "" {
		fmt.Fprintf(os.Stderr, "%s\n", decision.Message)
	}

	if decision.Mode == mcpModeRefuse {
		os.Exit(1)
	}

	var forwarder *mcpclient.Forwarder
	var handler *receptors.MCPHandler

	if decision.Mode == mcpModeForward {
		forwarder = mcpclient.NewForwarder(rootCred)

		// Nothing below is constructed, so nothing below is announced. The Stem
		// being forwarded to is the control plane for this connection: it loads
		// its own grants, derives the Pollen from the presented credential and
		// records the decision in its own audit lane. A second control plane
		// built here would govern none of the forwarded frames, and describing
		// one would assert an authorization this surface does not perform.
		fmt.Fprintf(os.Stderr, "🔏 Delegation is governed by the Stem at %s: a presented credential derives its Pollen there. Local grants and %s do not affect this connection.\n",
			mcpclient.ResolveStemAddress(""), envPollen)
	} else {
		var release func()
		handler, release = newInProcessMCPHandler(ctx)
		defer release()
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

		var respBytes []byte
		if forwarder != nil {
			respBytes = forwarder.Forward(reqBytes)
		} else {
			respBytes = handler.ProcessMCPMessage(reqBytes)
		}

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

// newInProcessMCPHandler builds the Stem this surface serves from when no
// governed Stem is being forwarded to, and returns the handler with a release
// function for the resources it opened.
//
// It exists as a function so that forwarding mode can decline to call it. When
// this surface forwards, every frame goes to the governed Stem and the handler
// built here is never consulted — so constructing it would open a history store
// and an event bus for nothing, read a control plane that governs nothing, and
// print several startup lines describing an authorization that is not the one in
// force.
//
// Release order is the reason this returns a function rather than leaving the
// caller to close two things: the bus must drain its sink BEFORE the history
// store is released, or queued audit records are written to a closed database.
func newInProcessMCPHandler(ctx context.Context) (*receptors.MCPHandler, func()) {
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
	}

	// Delegation audit lane: the same EventBus + history.db sink wiring the
	// REST server uses, so every delegation decision made on this surface is
	// audited to history.db.
	bus := eventbus.New()
	if history != nil {
		bus.AttachSink(history, 0, "historydb")
	}

	// Shutdown drains the sink before Close releases the database. The order is
	// load-bearing, which is why it is expressed here rather than left to two
	// deferred statements whose relative order a later edit could reverse.
	release := func() {
		bus.Shutdown()
		if history != nil {
			history.Close()
		}
	}

	if manager, err := session.NewManager(ctx, sessionStore); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Session manager unavailable: %v (continuing without sessions)\n", err)
	} else if sess, err := manager.Initiate(ctx, session.OriginMCP, session.Preferences{}); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to initiate MCP session: %v (continuing without sessions)\n", err)
	} else {
		coreSvc := core.NewService(manager).
			WithGenome(genomeOperations(resolveRepoRoot(""))).
			WithPlasmid(plasmidOperations(resolveRepoRoot(""))).
			WithMesh(meshOperations()).
			// Sequence runs on this stdio server stay telemetry-silent (a nil
			// bus), exactly as before; the EventBus above carries only the
			// delegation audit lane.
			WithSequence(serveSequenceOperations(resolveRepoRoot(""), nil)).
			WithSprout(sproutOperations(history, nil)).
			WithStoma(stomaOperations()).
			WithGit(gitOperations())
		handler = handler.WithSessions(manager, history).WithDefaultSession(sess.ID).WithCore(coreSvc)
		fmt.Fprintf(os.Stderr, "🪴 MCP interactions bound to Tendril session %s\n", sess.ID)
	}

	// Delegated-execution control plane (mirroring the REST server): grants
	// load from the Stem's own .tendril/grants.yaml — never from a Substrate
	// checkout. An editor starts this server in whatever repository it has open,
	// so the working directory is incidental here and cannot be the anchor; a
	// cloned repository must not be able to widen what its own Pollen may do.
	// With zero grants every delegated invocation is denied and all
	// non-delegated behavior is untouched; a malformed grants file degrades
	// the same way, never open.
	grantsDir, grantsDirErr := resolveGrantsDir()
	if grantsDirErr != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Delegation control plane could not be located: %v (delegation disabled — every delegated invocation is denied)\n", grantsDirErr)
		grantsDir = ""
	} else {
		warnIfWorkingDirectoryGrantsIgnored(os.Stderr, grantsDir)
	}

	var delegationGrants []core.DelegationGrant
	var grantsErr error
	if grantsDir != "" {
		delegationGrants, grantsErr = core.LoadDelegationGrants(grantsDir)
	}
	if grantsErr != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Failed to load delegation grants: %v (delegation disabled — every delegated invocation is denied)\n", grantsErr)
		delegationGrants = nil
	}
	if len(delegationGrants) > 0 {
		fmt.Fprintf(os.Stderr, "🔏 Delegation enabled: %d grant(s) loaded from %s\n", len(delegationGrants), filepath.Join(grantsDir, core.DelegationGrantsFilename))
	} else {
		fmt.Fprintln(os.Stderr, "🔏 No delegation grants configured: every delegated invocation is denied (secure default)")
	}
	// Issued credentials are what let a caller PROVE a Pollen rather than
	// declare one. A malformed store is fatal rather than empty: degrading to
	// "no credentials" would silently return every caller to the declared-Pollen
	// path, which is the weaker tier.
	pollinatorCredentials, credentialsErr := core.LoadPollinatorCredentials(tendrilDir)
	if credentialsErr != nil {
		fmt.Fprintf(os.Stderr, "❌ Pollinator credentials could not be read: %v\n", credentialsErr)
		os.Exit(1)
	}
	delegationGate := &receptors.DelegationGate{
		Pollinators: pollinatorCredentials,
		Authorizer:  core.NewDelegationAuthorizer(delegationGrants),
		Bus:         bus,
	}
	if len(pollinatorCredentials) > 0 {
		active := 0
		for _, credential := range pollinatorCredentials {
			if credential.Active() {
				active++
			}
		}
		fmt.Fprintf(os.Stderr, "🔏 %d Pollinator credential(s) loaded (%d active): a presented credential DERIVES its Pollen; the header claim is ignored for those callers\n", len(pollinatorCredentials), active)
	}

	// The Pollen is bound once, at startup, as a property of this
	// trusted stdio connection — a tool argument can never self-declare it.
	pollen := strings.TrimSpace(os.Getenv(envPollen))
	if pollen != "" {
		fmt.Fprintf(os.Stderr, "🔏 Pollen %q bound from %s: delegated capabilities are authorized against the loaded grants\n", pollen, envPollen)
	} else {
		fmt.Fprintf(os.Stderr, "🔏 No Pollen bound (%s is unset): delegated capabilities are denied over MCP (deny-closed)\n", envPollen)
	}
	handler = handler.WithDelegation(delegationGate, pollen)

	return handler, release
}

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/opentendril/opentendril/internal/mcpclient"
)

// Credential-path environment names stay available to package main so
// hardiness path discovery and existing tests keep a single spelling.
const envMCPCredential = mcpclient.EnvMCPCredential
const envPollinatorCredential = mcpclient.EnvPollinatorCredential

type mcpMode int

const (
	mcpModeInProcess mcpMode = iota
	mcpModeForward
	mcpModeRefuse
)

type mcpModeDecision struct {
	Mode    mcpMode
	Message string
}

func detectMCPMode(ctx context.Context, hasCredential bool) mcpModeDecision {
	if os.Getenv("TENDRIL_MCP_IN_PROCESS") == "1" {
		return mcpModeDecision{Mode: mcpModeInProcess}
	}

	// Owner establishment is the extracted client probe, shared with hardiness.
	probe := mcpclient.ProbeOwner(ctx)
	if !probe.Reached {
		return mcpModeDecision{Mode: mcpModeInProcess}
	}
	report := probe

	if report.Owner == nil {
		return mcpModeDecision{
			Mode:    mcpModeInProcess,
			Message: "⚠️ A Stem is serving on this address, but its ownership could not be established. Assuming this is a local environment and starting an in-process Stem beside it. To forward requests to the running Stem, it must be governed (have an owner configured).",
		}
	}

	callerUID := os.Getuid()
	if *report.Owner == callerUID {
		return mcpModeDecision{Mode: mcpModeInProcess}
	}

	if hasCredential {
		return mcpModeDecision{Mode: mcpModeForward}
	}

	return mcpModeDecision{
		Mode:    mcpModeRefuse,
		Message: fmt.Sprintf("❌ Stem is owned by another user (uid %d). To connect, ask the operator to issue a credential:\n\n    sudo -u tendril -i tendril pollinator issue --pollen <name> --note \"<where>\"\n\nand save it to the file named by %s.", *report.Owner, envMCPCredential),
	}
}

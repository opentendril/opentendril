package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

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

// stemOwnerProbe is what the unauthenticated health surface reports about who
// owns the Stem answering at an address.
//
// Reached distinguishes "nothing answered" from "something answered but did not
// publish an owner". Those are different conditions with different remedies, and
// collapsing them would make a caller unable to tell an absent Stem from an
// ungoverned one.
type stemOwnerProbe struct {
	// Address is what was probed, reported so a caller can name it.
	Address string
	// Reached is true when a Stem answered and its report decoded.
	Reached bool
	// Owner is the published owner, nil when none was published.
	Owner *int
}

// probeStemOwner asks the resolved address who owns the Stem there.
//
// Shared by mode selection and by hardiness so the two cannot drift into
// disagreeing about whether another principal is serving on this host.
func probeStemOwner(ctx context.Context) stemOwnerProbe {
	probe := stemOwnerProbe{Address: resolveStemAddress("")}

	client := &http.Client{
		Timeout: 2 * time.Second, // probe carries its own 2-second bound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+probe.Address+"/health", nil)
	if err != nil {
		return probe
	}

	resp, err := client.Do(req)
	if err != nil {
		return probe
	}
	defer resp.Body.Close()

	var report struct {
		Owner *int `json:"owner,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return probe
	}

	probe.Reached = true
	probe.Owner = report.Owner
	return probe
}

func detectMCPMode(ctx context.Context, hasCredential bool) mcpModeDecision {
	if os.Getenv("TENDRIL_MCP_IN_PROCESS") == "1" {
		return mcpModeDecision{Mode: mcpModeInProcess}
	}

	probe := probeStemOwner(ctx)
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

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

// detectMCPMode probes the Stem's health surface to determine if another principal owns it.
func detectMCPMode(ctx context.Context, hasCredential bool) mcpModeDecision {
	if os.Getenv("TENDRIL_MCP_IN_PROCESS") == "1" {
		return mcpModeDecision{Mode: mcpModeInProcess}
	}

	addr := resolveStemAddress()
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+addr+"/health", nil)
	if err != nil {
		return mcpModeDecision{Mode: mcpModeInProcess}
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Nothing answers -> in-process
		return mcpModeDecision{Mode: mcpModeInProcess}
	}
	defer resp.Body.Close()

	var report struct {
		Owner *int `json:"owner,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return mcpModeDecision{Mode: mcpModeInProcess}
	}

	if report.Owner == nil {
		// Answers, no owner reported -> in-process, say so loudly
		return mcpModeDecision{
			Mode:    mcpModeInProcess,
			Message: "⚠️ A Stem is serving at " + addr + " but did not report an owner. Assuming in-process mode. If you upgraded the Stem recently, please restart it.",
		}
	}

	callerUID := os.Getuid()
	if *report.Owner == callerUID && false { // BYPASSED FOR TESTING
		// Owner present, equals caller -> in-process
		return mcpModeDecision{Mode: mcpModeInProcess}
	}

	// Owner present, differs from caller
	if hasCredential {
		return mcpModeDecision{Mode: mcpModeForward}
	}

	// No credential
	return mcpModeDecision{
		Mode:    mcpModeRefuse,
		Message: fmt.Sprintf("❌ Stem is owned by another user (uid %d). To connect, ask the operator to issue a credential:\n\n    sudo -u tendril -i tendril pollinator issue --pollen <name> --note \"<where>\"\n\nand save it to the file named by %s.", *report.Owner, envMCPCredential),
	}

	// Owner present, differs from caller
	if hasCredential {
		return mcpModeDecision{Mode: mcpModeForward}
	}

	// No credential
	return mcpModeDecision{
		Mode:    mcpModeRefuse,
		Message: fmt.Sprintf("❌ Stem is owned by another user (uid %d). To connect, ask the operator to issue a credential:\n\n    sudo -u tendril -i tendril pollinator issue --pollen <name> --note \"<where>\"\n\nand save it to the file named by %s.", *report.Owner, envMCPCredential),
	}
}

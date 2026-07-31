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

func detectMCPMode(ctx context.Context, hasCredential bool) mcpModeDecision {
	if os.Getenv("TENDRIL_MCP_IN_PROCESS") == "1" {
		return mcpModeDecision{Mode: mcpModeInProcess}
	}

	addr := resolveStemAddress("")
	client := &http.Client{
		Timeout: 2 * time.Second, // probe carries its own 2-second bound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", nil)
	if err != nil {
		return mcpModeDecision{Mode: mcpModeInProcess}
	}

	resp, err := client.Do(req)
	if err != nil {
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
		return mcpModeDecision{Mode: mcpModeInProcess, Message: "⚠️ A Stem is serving"}
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

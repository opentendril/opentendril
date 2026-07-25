package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
)

// runDelegationCmd is the CLI adapter for managing pending delegation confirmations.
// It interacts with the running Stem daemon's REST API.
func runDelegationCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printDelegationUsage()
		return
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "pending":
		runDelegationPendingCmd(ctx, args[1:])
	case "approve":
		runDelegationApproveCmd(ctx, args[1:])
	case "deny":
		runDelegationDenyCmd(ctx, args[1:])
	case "-h", "--help", "help":
		printDelegationUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown delegation command: %s\n", args[0])
		printDelegationUsage()
		os.Exit(1)
	}
}

func printDelegationUsage() {
	fmt.Println("Usage: tendril delegation <pending|approve|deny> [id]")
	fmt.Println("  pending         List pending delegation confirmations")
	fmt.Println("  approve <id>    Approve a pending confirmation")
	fmt.Println("  deny <id>       Deny a pending confirmation")
}

func newDelegationRequest(ctx context.Context, method, path string) (*http.Request, error) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	url := fmt.Sprintf("http://localhost:%s%s", port, path)
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	if key := strings.TrimSpace(os.Getenv(EnvBotanistKey)); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req, nil
}

type pendingResponse struct {
	ID             string `json:"id"`
	Pollen         string `json:"pollen"`
	OperationClass string `json:"operationClass"`
	Substrate      string `json:"substrate"`
	Impact         string `json:"impact"`
	CreatedAt      string `json:"createdAt"`
	ExpiresAt      string `json:"expiresAt"`
}

func runDelegationPendingCmd(ctx context.Context, args []string) {
	req, err := newDelegationRequest(ctx, http.MethodGet, "/v1/delegation/pending")
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to build request: %v\n", err)
		os.Exit(1)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Stem daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "Please ensure the OpenTendril daemon is running (`tendril serve`).")
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "❌ Stem daemon returned error (status %d)\n", resp.StatusCode)
		os.Exit(1)
	}

	var results []pendingResponse
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to decode daemon response: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No pending confirmations.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tPOLLEN\tOPERATION\tSUBSTRATE\tIMPACT\tEXPIRES AT")
	for _, r := range results {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Pollen, r.OperationClass, r.Substrate, r.Impact, r.ExpiresAt)
	}
	w.Flush()
}

func runDelegationApproveCmd(ctx context.Context, args []string) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(os.Stderr, "missing pending confirmation id. Usage: tendril delegation approve <id>")
		printDelegationUsage()
		os.Exit(1)
	}
	id := strings.TrimSpace(args[0])

	req, err := newDelegationRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/delegation/pending/%s/approve", id))
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to build request: %v\n", err)
		os.Exit(1)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Stem daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "Please ensure the OpenTendril daemon is running (`tendril serve`).")
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg := "error"
		if resp.StatusCode == http.StatusNotFound {
			msg = "not found"
		} else if resp.StatusCode == http.StatusConflict {
			msg = "already resolved or expired"
		}
		fmt.Fprintf(os.Stderr, "❌ Failed to approve %s: %s (status %d)\n", id, msg, resp.StatusCode)
		os.Exit(1)
	}

	fmt.Printf("✅ Approved %s\n", id)
}

func runDelegationDenyCmd(ctx context.Context, args []string) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(os.Stderr, "missing pending confirmation id. Usage: tendril delegation deny <id>")
		printDelegationUsage()
		os.Exit(1)
	}
	id := strings.TrimSpace(args[0])

	req, err := newDelegationRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/delegation/pending/%s/deny", id))
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to build request: %v\n", err)
		os.Exit(1)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to connect to Stem daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "Please ensure the OpenTendril daemon is running (`tendril serve`).")
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg := "error"
		if resp.StatusCode == http.StatusNotFound {
			msg = "not found"
		} else if resp.StatusCode == http.StatusConflict {
			msg = "already resolved or expired"
		}
		fmt.Fprintf(os.Stderr, "❌ Failed to deny %s: %s (status %d)\n", id, msg, resp.StatusCode)
		os.Exit(1)
	}

	fmt.Printf("✅ Denied %s\n", id)
}

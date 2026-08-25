package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// runDelegationCmd is the CLI adapter for Botanist-side delegation control.
// Pending confirmation approve/deny talks to the running Stem daemon.
// Durable grant mutation talks to the Stem control-plane grants file through
// Core; adapters do not own grant policy.
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
	case "grant":
		runDelegationGrantCmd(args[1:])
	case "grants":
		runDelegationGrantsCmd(args[1:])
	case "revoke":
		runDelegationRevokeCmd(args[1:])
	case "-h", "--help", "help":
		printDelegationUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown delegation command: %s\n", args[0])
		printDelegationUsage()
		os.Exit(1)
	}
}

func printDelegationUsage() {
	fmt.Println("Usage: tendril delegation <pending|approve|deny|grant|grants|revoke> [args]")
	fmt.Println("  pending         List pending delegation confirmations")
	fmt.Println("  approve <id>    Approve a pending confirmation")
	fmt.Println("  deny <id>       Deny a pending confirmation")
	fmt.Println("  grant           Add named operation classes to an existing Pollen/Substrate grant")
	fmt.Println("  grants          Show current control-plane grants")
	fmt.Println("  revoke          Remove named operation classes from an existing grant")
	fmt.Println()
	fmt.Println("  grant  --pollen <pollen> --substrate <name> --operation <class> [--operation <class> ...]")
	fmt.Println("  grants [--pollen <pollen>] [--substrate <name>]")
	fmt.Println("  revoke --pollen <pollen> --substrate <name> --operation <class> [--operation <class> ...]")
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

type delegationGrantFlags struct {
	pollen     string
	substrate  string
	operations []string
	help       bool
}

func parseDelegationGrantFlags(args []string) (delegationGrantFlags, error) {
	var flags delegationGrantFlags
	need := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("flag %s requires a value", name)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help", "help":
			flags.help = true
			return flags, nil
		case "--pollen":
			value, err := need(&i, "--pollen")
			if err != nil {
				return flags, err
			}
			flags.pollen = value
		case "--substrate":
			value, err := need(&i, "--substrate")
			if err != nil {
				return flags, err
			}
			flags.substrate = value
		case "--operation":
			value, err := need(&i, "--operation")
			if err != nil {
				return flags, err
			}
			flags.operations = append(flags.operations, value)
		case "--dir", "--grants", "--grants-file", "--grants-path":
			return flags, fmt.Errorf("delegation grant mutation does not accept a grants file path; grants are read only from the Stem control plane")
		default:
			return flags, fmt.Errorf("unknown argument %q", args[i])
		}
	}
	return flags, nil
}

func requireDelegationMutationFlags(flags delegationGrantFlags) error {
	if strings.TrimSpace(flags.pollen) == "" {
		return fmt.Errorf("missing --pollen")
	}
	if strings.TrimSpace(flags.substrate) == "" {
		return fmt.Errorf("missing --substrate")
	}
	if len(flags.operations) == 0 {
		return fmt.Errorf("missing --operation")
	}
	return nil
}

func resolveDelegationControlPlane() (string, error) {
	tendrilDir, err := resolveGrantsDir()
	if err != nil {
		return "", fmt.Errorf("resolve delegation control plane: %w", err)
	}
	warnIfWorkingDirectoryGrantsIgnored(os.Stderr, tendrilDir)
	return tendrilDir, nil
}

func refuseDeclaredPollenGrantMutation() error {
	if pollen := strings.TrimSpace(os.Getenv(envPollenCLI)); pollen != "" {
		return fmt.Errorf("grant mutation is a Botanist control-plane action and is not available under a declared Pollen (%s=%s)", envPollenCLI, pollen)
	}
	return nil
}

func runDelegationGrantCmd(args []string) {
	flags, err := parseDelegationGrantFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		printDelegationUsage()
		os.Exit(1)
	}
	if flags.help {
		printDelegationUsage()
		return
	}
	if err := requireDelegationMutationFlags(flags); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v. Usage: tendril delegation grant --pollen <pollen> --substrate <name> --operation <class> [--operation <class> ...]\n", err)
		os.Exit(1)
	}
	if err := refuseDeclaredPollenGrantMutation(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	tendrilDir, err := resolveDelegationControlPlane()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if err := core.AddGrantOperationClasses(tendrilDir, flags.pollen, flags.substrate, flags.operations); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Granted %s to pollen %q on substrate %q\n", strings.Join(uniquePreserveStrings(flags.operations), ", "), strings.TrimSpace(flags.pollen), strings.TrimSpace(flags.substrate))
	printMatchingGrants(tendrilDir, flags.pollen, flags.substrate)
	fmt.Fprintf(os.Stderr, "The running Stem reads grants at startup; restart it before the new authority takes effect.\n")
}

func runDelegationRevokeCmd(args []string) {
	flags, err := parseDelegationGrantFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		printDelegationUsage()
		os.Exit(1)
	}
	if flags.help {
		printDelegationUsage()
		return
	}
	if err := requireDelegationMutationFlags(flags); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v. Usage: tendril delegation revoke --pollen <pollen> --substrate <name> --operation <class> [--operation <class> ...]\n", err)
		os.Exit(1)
	}
	if err := refuseDeclaredPollenGrantMutation(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	tendrilDir, err := resolveDelegationControlPlane()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if err := core.RevokeGrantOperationClasses(tendrilDir, flags.pollen, flags.substrate, flags.operations); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Revoked %s from pollen %q on substrate %q\n", strings.Join(uniquePreserveStrings(flags.operations), ", "), strings.TrimSpace(flags.pollen), strings.TrimSpace(flags.substrate))
	printMatchingGrants(tendrilDir, flags.pollen, flags.substrate)
	fmt.Fprintf(os.Stderr, "The running Stem reads grants at startup; restart it before the new authority takes effect.\n")
}

func runDelegationGrantsCmd(args []string) {
	flags, err := parseDelegationGrantFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		printDelegationUsage()
		os.Exit(1)
	}
	if flags.help {
		printDelegationUsage()
		return
	}
	if len(flags.operations) > 0 {
		fmt.Fprintln(os.Stderr, "❌ tendril delegation grants does not accept --operation; it only inspects current grants")
		os.Exit(1)
	}
	tendrilDir, err := resolveDelegationControlPlane()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	printMatchingGrants(tendrilDir, flags.pollen, flags.substrate)
}

func printMatchingGrants(tendrilDir, pollen, substrate string) {
	grants, err := core.LoadDelegationGrants(tendrilDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Delegation grants could not be loaded from %s: %v\n", filepath.Join(tendrilDir, core.DelegationGrantsFilename), err)
		os.Exit(1)
	}
	pollen = strings.TrimSpace(pollen)
	substrate = strings.TrimSpace(substrate)
	matched := make([]core.DelegationGrant, 0, len(grants))
	for _, grant := range grants {
		if pollen != "" && grant.Pollen != pollen {
			continue
		}
		if substrate != "" && !containsExactString(grant.Substrates, substrate) {
			continue
		}
		matched = append(matched, grant)
	}
	if len(matched) == 0 {
		if len(grants) == 0 {
			fmt.Println("No delegation grants configured (secure default: every delegated invocation is denied).")
			return
		}
		fmt.Println("No grants match the requested Pollen/Substrate filter.")
		return
	}

	fmt.Printf("Control plane: %s\n", filepath.Join(tendrilDir, core.DelegationGrantsFilename))
	for _, grant := range matched {
		fmt.Printf("pollen: %s\n", grant.Pollen)
		fmt.Printf("  substrates: [%s]\n", strings.Join(grant.Substrates, ", "))
		fmt.Printf("  operationClasses: [%s]\n", strings.Join(grant.OperationClasses, ", "))
		if len(grant.Egress) > 0 {
			fmt.Printf("  egress: [%s]\n", strings.Join(grant.Egress, ", "))
		}
		if !grant.Expires.IsZero() {
			fmt.Printf("  expires: %s\n", formatGrantExpiry(grant.Expires))
		}
		if grant.ConfirmAboveImpact != "" {
			fmt.Printf("  confirmAbove: %s\n", grant.ConfirmAboveImpact)
		}
	}
}

func formatGrantExpiry(expires time.Time) string {
	utc := expires.UTC()
	if utc.Hour() == 0 && utc.Minute() == 0 && utc.Second() == 0 && utc.Nanosecond() == 0 {
		return utc.Format("2006-01-02")
	}
	return utc.Format(time.RFC3339)
}

func uniquePreserveStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func containsExactString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

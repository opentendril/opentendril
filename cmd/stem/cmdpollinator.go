package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// `tendril pollinator` — the Botanist's control over who may ask.
//
// Issuing a credential is what turns a Pollen from something a caller declares
// into something it must prove. The commands are deliberately few: issue, list,
// revoke. There is no "edit", because changing which identity a live credential
// authenticates as is exactly the confusion this removes.

func runPollinatorCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printPollinatorUsage()
		return
	}

	tendrilDir := "./.tendril"
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "issue":
		runPollinatorIssue(tendrilDir, args[1:])
	case "list":
		runPollinatorList(tendrilDir)
	case "revoke":
		runPollinatorRevoke(tendrilDir, args[1:])
	case "-h", "--help", "help":
		printPollinatorUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown pollinator command: %s\n", args[0])
		printPollinatorUsage()
		os.Exit(1)
	}
}

// parsePollinatorArgs reads --pollen and --note.
func parsePollinatorArgs(args []string) (pollen, note string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pollen":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("flag --pollen requires a value")
			}
			i++
			pollen = args[i]
		case "--note":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("flag --note requires a value")
			}
			i++
			note = args[i]
		default:
			return "", "", fmt.Errorf("unknown argument %q", args[i])
		}
	}
	return pollen, note, nil
}

func runPollinatorIssue(tendrilDir string, args []string) {
	pollen, note, err := parsePollinatorArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(pollen) == "" {
		fmt.Fprintln(os.Stderr, "❌ missing --pollen. Usage: tendril pollinator issue --pollen <name> [--note <memo>]")
		os.Exit(1)
	}

	secret, credential, err := core.IssuePollinatorCredential(tendrilDir, pollen, note)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Could not issue credential: %v\n", err)
		os.Exit(1)
	}

	// The secret is printed once and stored nowhere. Losing it means issuing
	// another and revoking this one, which is the correct cost.
	fmt.Fprintf(os.Stderr, "🔏 Issued a credential for Pollen %q\n\n", credential.Pollen)
	fmt.Println(secret)
	fmt.Fprintln(os.Stderr, "\n   This is shown ONCE and is not stored — only its digest is kept.")
	fmt.Fprintln(os.Stderr, "   Give it to that Pollinator; the Pollen is derived from it, so the")
	fmt.Fprintln(os.Stderr, "   Pollinator can no longer declare an identity of its own choosing.")
	fmt.Fprintf(os.Stderr, "   A grant is still required: %s must cover this Pollen.\n", core.DelegationGrantsFilename)
}

func runPollinatorList(tendrilDir string) {
	credentials, err := core.LoadPollinatorCredentials(tendrilDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Could not read credentials: %v\n", err)
		os.Exit(1)
	}
	if len(credentials) == 0 {
		fmt.Fprintln(os.Stderr, "No Pollinator credentials issued. Every Pollinator is therefore denied (secure default).")
		return
	}

	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "POLLEN\tSTATUS\tISSUED\tDIGEST\tNOTE")
	for _, credential := range credentials {
		status := "active"
		if !credential.Active() {
			status = "revoked " + credential.RevokedAt.Format(time.DateOnly)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s…\t%s\n",
			credential.Pollen, status, credential.IssuedAt.Format(time.DateOnly), credential.Digest[:12], credential.Note)
	}
	writer.Flush()
}

func runPollinatorRevoke(tendrilDir string, args []string) {
	pollen, _, err := parsePollinatorArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(pollen) == "" {
		fmt.Fprintln(os.Stderr, "❌ missing --pollen. Usage: tendril pollinator revoke --pollen <name>")
		os.Exit(1)
	}

	revoked, err := core.RevokePollinatorCredentials(tendrilDir, pollen)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Could not revoke: %v\n", err)
		os.Exit(1)
	}
	if revoked == 0 {
		fmt.Fprintf(os.Stderr, "No active credential for Pollen %q — nothing to revoke.\n", pollen)
		return
	}
	fmt.Fprintf(os.Stderr, "🔏 Revoked %d credential(s) for Pollen %q. The next request presenting one is denied.\n", revoked, pollen)
}

func printPollinatorUsage() {
	fmt.Println("Usage: tendril pollinator <issue|list|revoke> [flags]")
	fmt.Println()
	fmt.Println("issue --pollen <name> [--note <memo>]")
	fmt.Println("  Mints a credential that authenticates AS that Pollen. The secret prints")
	fmt.Println("  once and is never stored — only its digest is kept, so a leaked store")
	fmt.Println("  cannot be replayed. A grant is still required for the Pollen to do anything.")
	fmt.Println()
	fmt.Println("list")
	fmt.Println("  Shows every credential, active and revoked, with its Pollen and digest")
	fmt.Println("  prefix. Revoked credentials are kept so the record of what existed survives.")
	fmt.Println()
	fmt.Println("revoke --pollen <name>")
	fmt.Println("  Withdraws every active credential for a Pollen, effective immediately.")
	fmt.Println()
	fmt.Println("A credential carries the Pollen, so a Pollinator presenting one cannot claim")
	fmt.Println("another identity. That is the difference between this and a declared Pollen,")
	fmt.Println("which is an audit control rather than a boundary.")
}

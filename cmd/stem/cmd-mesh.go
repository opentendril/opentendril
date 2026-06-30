package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/mesh"
)

func runMeshCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printMeshUsage()
		return
	}

	switch args[0] {
	case "keygen":
		runMeshKeygenCmd(args[1:])
	case "issue-token":
		runMeshIssueTokenCmd(ctx, args[1:])
	case "-h", "--help", "help":
		printMeshUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown mesh command: %s\n", args[0])
		printMeshUsage()
		os.Exit(1)
	}
}

func runMeshKeygenCmd(args []string) {
	fs := flag.NewFlagSet("mesh keygen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	workspace := fs.String("workspace", "", "Workspace root to store .tendril/security")
	force := fs.Bool("force", false, "Overwrite existing mesh keys")
	if err := fs.Parse(args); err != nil {
		return
	}

	root := strings.TrimSpace(*workspace)
	if root == "" {
		root = resolveRepoRoot("")
	}

	privateKeyPath, publicKeyPath := mesh.WorkspaceKeyPaths(root)
	if !*force {
		if _, err := os.Stat(privateKeyPath); err == nil {
			fmt.Fprintf(os.Stderr, "mesh keygen refused: %s already exists (use --force to overwrite)\n", privateKeyPath)
			os.Exit(1)
		}
		if _, err := os.Stat(publicKeyPath); err == nil {
			fmt.Fprintf(os.Stderr, "mesh keygen refused: %s already exists (use --force to overwrite)\n", publicKeyPath)
			os.Exit(1)
		}
	}

	pair, err := mesh.GenerateKeyPair()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mesh keygen failed: %v\n", err)
		os.Exit(1)
	}
	if err := mesh.WriteKeyPair(root, pair); err != nil {
		fmt.Fprintf(os.Stderr, "mesh keygen failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stdout, "Generated mesh keypair in %s\n", mesh.WorkspaceSecurityDir(root))
	fmt.Fprintf(os.Stdout, "Public key fingerprint: %s\n", mesh.PublicKeyFingerprint(pair.PublicKey))
}

func runMeshIssueTokenCmd(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("mesh issue-token", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	workspace := fs.String("workspace", "", "Workspace root to read .tendril/security")
	subject := fs.String("subject", "", "JWT subject")
	audience := fs.String("audience", "", "JWT audience")
	scope := fs.String("scope", "", "JWT mesh scope")
	issuer := fs.String("issuer", "", "JWT issuer")
	tokenID := fs.String("token-id", "", "JWT token identifier")
	ttl := fs.Duration("ttl", time.Hour, "Token lifetime")
	if err := fs.Parse(args); err != nil {
		return
	}

	_ = ctx
	root := strings.TrimSpace(*workspace)
	if root == "" {
		root = resolveRepoRoot("")
	}

	audiences := []string{}
	if trimmed := strings.TrimSpace(*audience); trimmed != "" {
		audiences = append(audiences, trimmed)
	}

	token, err := mesh.IssueWorkspaceToken(root, mesh.TokenOptions{
		Issuer:    strings.TrimSpace(*issuer),
		Subject:   strings.TrimSpace(*subject),
		Audience:  audiences,
		MeshScope: strings.TrimSpace(*scope),
		TokenID:   strings.TrimSpace(*tokenID),
		ExpiresIn: *ttl,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mesh issue-token failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stdout, token)
}

func printMeshUsage() {
	fmt.Println("tendril mesh - mesh grafting utilities")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  tendril mesh keygen [--workspace PATH] [--force]")
	fmt.Println("  tendril mesh issue-token [--workspace PATH] [--subject TEXT] [--audience TEXT] [--scope TEXT] [--ttl DURATION]")
}

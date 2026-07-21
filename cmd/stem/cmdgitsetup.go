package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
)

// `tendril git setup` — one command that stands up a git connection so neither
// a human nor an agent hand-assembles config. It writes a `substrates.yaml`
// credentials profile + substrate and (optionally) a `.tendril/grants.yaml`
// grant, prints the per-agent MCP block, and can check the result with
// --verify. It is flag-driven (no interactive-only path) so an agent can call
// it too, and it writes only references — env-var names and key paths — never a
// secret. Two postures mirror the connection tiers: `app` (GitHub App, commits
// signed server-side via commit: api) and `pat` (fine-grained Personal Access
// Token + a dedicated GPG signing key).

type gitSetupOptions struct {
	posture       string // "app" (default) | "pat"
	substrate     string
	repo          string // owner/repo
	appID         string
	keyPath       string
	tokenEnv      string
	signKey       string
	identityName  string
	identityEmail string
	grantSubject  string
	checkout      string // managed (default) | path | ephemeral
	dir           string // where config is written (default: cwd)
	force         bool
	verify        bool
	help          bool
}

// runGitSetup is the `tendril git setup` entry point, dispatched from
// runGitCmd before the capability lookup (setup is a local config action, not
// a governed capability).
func runGitSetup(ctx context.Context, args []string) {
	opts, err := parseGitSetupArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		printGitSetupUsage()
		os.Exit(1)
	}
	if opts.help {
		printGitSetupUsage()
		return
	}
	if opts.verify {
		if !runGitSetupVerify(opts) {
			os.Exit(1)
		}
		return
	}

	substratesPath := filepath.Join(opts.dir, "substrates.yaml")
	if err := upsertSubstrates(substratesPath, opts); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}

	if opts.grantSubject != "" {
		grantsDir := filepath.Join(opts.dir, ".tendril")
		if err := os.MkdirAll(grantsDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "❌ create %s: %v\n", grantsDir, err)
			os.Exit(1)
		}
		if err := upsertGrants(filepath.Join(grantsDir, "grants.yaml"), opts); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
	}

	printGitSetupNextSteps(opts)
}

// parseGitSetupArgs turns CLI flags into validated options, applying the secure
// defaults (posture app, checkout managed) and enforcing the per-posture
// requirements.
func parseGitSetupArgs(args []string) (gitSetupOptions, error) {
	opts := gitSetupOptions{posture: "app", checkout: "managed", dir: "."}
	need := func(i *int) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("flag %s requires a value", args[*i])
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		switch args[i] {
		case "-h", "--help", "help":
			opts.help = true
			return opts, nil
		case "--posture":
			opts.posture, err = need(&i)
		case "--substrate":
			opts.substrate, err = need(&i)
		case "--repo":
			opts.repo, err = need(&i)
		case "--app-id":
			opts.appID, err = need(&i)
		case "--key":
			opts.keyPath, err = need(&i)
		case "--token-env":
			opts.tokenEnv, err = need(&i)
		case "--sign-key":
			opts.signKey, err = need(&i)
		case "--identity-name":
			opts.identityName, err = need(&i)
		case "--identity-email":
			opts.identityEmail, err = need(&i)
		case "--grant-subject":
			opts.grantSubject, err = need(&i)
		case "--checkout":
			opts.checkout, err = need(&i)
		case "--dir":
			opts.dir, err = need(&i)
		case "--force":
			opts.force = true
		case "--verify":
			opts.verify = true
		default:
			return opts, fmt.Errorf("unknown argument %q for git setup", args[i])
		}
		if err != nil {
			return opts, err
		}
	}

	opts.posture = strings.ToLower(strings.TrimSpace(opts.posture))
	opts.checkout = strings.ToLower(strings.TrimSpace(opts.checkout))
	if opts.posture != "app" && opts.posture != "pat" {
		return opts, fmt.Errorf("--posture must be app or pat, got %q", opts.posture)
	}
	switch opts.checkout {
	case "managed", "path", "ephemeral":
	default:
		return opts, fmt.Errorf("--checkout must be managed, path, or ephemeral, got %q", opts.checkout)
	}
	if strings.TrimSpace(opts.substrate) == "" {
		return opts, fmt.Errorf("--substrate <name> is required")
	}
	// --verify only needs to locate the substrate; the write path needs the
	// full posture inputs.
	if opts.verify {
		return opts, nil
	}
	if strings.TrimSpace(opts.repo) == "" || !strings.Contains(opts.repo, "/") {
		return opts, fmt.Errorf("--repo <owner/repo> is required")
	}
	if opts.posture == "app" {
		if strings.TrimSpace(opts.appID) == "" || strings.TrimSpace(opts.keyPath) == "" {
			return opts, fmt.Errorf("posture app requires --app-id and --key <pem path>")
		}
	} else {
		if strings.TrimSpace(opts.tokenEnv) == "" {
			opts.tokenEnv = "GITHUB_TOKEN"
		}
		// The pat posture creates local, signed commits, and the delegated
		// commit path is deny-closed on identity: without a name and email a
		// commit would be refused, so setup requires them up front.
		if strings.TrimSpace(opts.signKey) == "" {
			return opts, fmt.Errorf("posture pat requires --sign-key <gpg key id>")
		}
		if strings.TrimSpace(opts.identityName) == "" || strings.TrimSpace(opts.identityEmail) == "" {
			return opts, fmt.Errorf("posture pat requires --identity-name and --identity-email (a commit without an identity is refused)")
		}
	}
	return opts, nil
}

// renderSubstratesYAML builds the connection config for the chosen posture.
// Secrets are never emitted — only env-var names and key paths.
func renderSubstratesYAML(o gitSetupOptions) string {
	profile := o.substrate + "-connection"
	var b strings.Builder
	b.WriteString("# Generated by `tendril git setup`. Secrets are referenced by env-var name\n")
	b.WriteString("# or key path, never stored here. Edit freely; re-run setup to regenerate.\n")
	b.WriteString("credentials:\n")
	fmt.Fprintf(&b, "  %s:\n", profile)
	if o.posture == "app" {
		fmt.Fprintf(&b, "    auth: { method: app, appId: %q, privateKeyPath: %q }\n", o.appID, o.keyPath)
		b.WriteString("    commit: api            # GitHub signs the commit server-side (verified)\n")
	} else {
		fmt.Fprintf(&b, "    auth: { method: pat, env: %s }\n", o.tokenEnv)
		fmt.Fprintf(&b, "    sign: { method: gpg, key: %q }\n", o.signKey)
		fmt.Fprintf(&b, "    identity: { name: %q, email: %q }\n", o.identityName, o.identityEmail)
	}
	b.WriteString("\nsubstrates:\n")
	fmt.Fprintf(&b, "  %s:\n", o.substrate)
	fmt.Fprintf(&b, "    url: https://github.com/%s\n", o.repo)
	fmt.Fprintf(&b, "    profile: %s\n", profile)
	fmt.Fprintf(&b, "    checkout: { mode: %s }\n", o.checkout)
	return b.String()
}

// renderGrantsYAML builds the control-plane grant authorising one agent subject
// to run git operations on the new substrate.
func renderGrantsYAML(o gitSetupOptions) string {
	var b strings.Builder
	b.WriteString("# Generated by `tendril git setup`. Control-plane grants: which agent\n")
	b.WriteString("# (subject) may run which git operation on which substrate. No grant = denied.\n")
	b.WriteString("grants:\n")
	fmt.Fprintf(&b, "  %s:\n", o.grantSubject)
	b.WriteString("    operationClasses: [git.commit, git.push]\n")
	fmt.Fprintf(&b, "    substrates: [%s]\n", o.substrate)
	return b.String()
}

// renderProfileValueYAML renders just the value block of a credentials profile
// (the mapping under `<name>-connection:`), so the merge path can parse it into
// a node and upsert it into an existing file.
func renderProfileValueYAML(o gitSetupOptions) string {
	var b strings.Builder
	if o.posture == "app" {
		fmt.Fprintf(&b, "auth: { method: app, appId: %q, privateKeyPath: %q }\n", o.appID, o.keyPath)
		b.WriteString("commit: api\n")
	} else {
		fmt.Fprintf(&b, "auth: { method: pat, env: %s }\n", o.tokenEnv)
		fmt.Fprintf(&b, "sign: { method: gpg, key: %q }\n", o.signKey)
		fmt.Fprintf(&b, "identity: { name: %q, email: %q }\n", o.identityName, o.identityEmail)
	}
	return b.String()
}

// renderSubstrateValueYAML renders just the value block of a substrate (the
// mapping under `<name>:`).
func renderSubstrateValueYAML(o gitSetupOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "url: https://github.com/%s\n", o.repo)
	fmt.Fprintf(&b, "profile: %s-connection\n", o.substrate)
	fmt.Fprintf(&b, "checkout: { mode: %s }\n", o.checkout)
	return b.String()
}

// printGitSetupNextSteps prints the per-agent MCP block and the follow-up
// actions a human still has to take (uploading a signing key, verifying).
func printGitSetupNextSteps(o gitSetupOptions) {
	subject := o.grantSubject
	if subject == "" {
		subject = "<agent-name>"
	}
	fmt.Println()
	fmt.Println("Give an agent access — add this to its MCP config (one block per agent,")
	fmt.Println("each with its own subject; the subject is bound here, never self-declared):")
	fmt.Println(`  { "mcpServers": { "opentendril": {`)
	fmt.Println(`    "command": "tendril", "args": ["serve", "mcp", "stdio"],`)
	fmt.Printf("    \"env\": { \"OPENTENDRIL_DELEGATION_SUBJECT\": %q }\n", subject)
	fmt.Println(`  }}}`)
	fmt.Println()
	if o.posture == "pat" {
		fmt.Println("Next: ensure the signing key is uploaded to GitHub (Settings → GPG keys)")
		fmt.Printf("      and the token env %s is set, so commits show Verified.\n", envOrDefault(o.tokenEnv, "GITHUB_TOKEN"))
	} else {
		fmt.Println("Next: ensure the GitHub App is installed on the repository (Contents: Read")
		fmt.Println("      and write). Commits are then signed by GitHub and show Verified.")
	}
	fmt.Printf("Check it:  tendril git setup --verify --substrate %s --dir %s\n", o.substrate, o.dir)
}

func envOrDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// runGitSetupVerify loads the written config, resolves the substrate's
// credential, and reports whether the authentication material is actually
// present — a side-effect-free configuration check (it never creates a commit).
// Returns true when the connection looks ready.
func runGitSetupVerify(o gitSetupOptions) bool {
	cfg, err := conductor.LoadSubstratesConfig(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ load substrates config: %v\n", err)
		return false
	}
	spec, isName := conductor.ResolveSubstrate(o.substrate, cfg)
	if !isName || spec == nil {
		fmt.Fprintf(os.Stderr, "❌ substrate %q not found (run setup first, or pass --dir)\n", o.substrate)
		return false
	}
	cred, err := conductor.ResolveSubstrateCredential(*spec, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ resolve credential: %v\n", err)
		return false
	}

	ready := true
	fmt.Printf("Connection %q:\n", o.substrate)
	fmt.Printf("  auth method:  %s\n", cred.Method)
	fmt.Printf("  commit mode:  %s\n", cred.CommitMode)
	switch cred.Method {
	case conductor.CredentialApp:
		fmt.Printf("  app id:       %s\n", cred.App.AppID)
		if _, statErr := os.Stat(cred.App.PrivateKeyPath); statErr != nil {
			fmt.Printf("  ⚠️  private key not readable at %s: %v\n", cred.App.PrivateKeyPath, statErr)
			ready = false
		} else {
			fmt.Printf("  ✅ private key present: %s\n", cred.App.PrivateKeyPath)
		}
	case conductor.CredentialPAT:
		if strings.TrimSpace(cred.TokenValue) == "" {
			fmt.Printf("  ⚠️  token env %s is not set in this environment\n", cred.TokenEnv)
			ready = false
		} else {
			fmt.Printf("  ✅ token present (from %s)\n", cred.TokenEnv)
		}
	}
	if cred.Sign.Method != "" {
		fmt.Printf("  signing:      %s (%s)\n", cred.Sign.Method, cred.Sign.Key)
	}
	if cred.Identity.Name != "" || cred.Identity.Email != "" {
		fmt.Printf("  identity:     %s <%s>\n", cred.Identity.Name, cred.Identity.Email)
	}
	if ready {
		fmt.Println("✅ Connection configured; authentication material present.")
	} else {
		fmt.Println("⚠️  Connection configured, but authentication material is missing (see above).")
	}
	return ready
}

func printGitSetupUsage() {
	fmt.Println("Usage: tendril git setup --substrate <name> --repo <owner/repo> [flags]")
	fmt.Println()
	fmt.Println("Writes a git connection (substrates.yaml) and optional grant (.tendril/grants.yaml),")
	fmt.Println("then prints the per-agent MCP config. Secrets are referenced, never stored.")
	fmt.Println()
	fmt.Println("  --posture app|pat     Connection posture (default app: GitHub App, server-signed)")
	fmt.Println("  --substrate <name>    Name for this connection/repo (required)")
	fmt.Println("  --repo <owner/repo>   The GitHub repository (required)")
	fmt.Println("  --checkout <mode>     managed (default) | path | ephemeral")
	fmt.Println("  --grant-subject <s>   Also write a grant authorising agent subject <s>")
	fmt.Println()
	fmt.Println("  posture app:  --app-id <id>  --key <pem path>")
	fmt.Println("  posture pat:  --token-env <ENV>  --sign-key <gpg id>  --identity-name <n>  --identity-email <e>")
	fmt.Println()
	fmt.Println("  --dir <path>          Where to write config (default: current directory)")
	fmt.Println("  --force               Overwrite existing config files")
	fmt.Println("  --verify              Check an existing connection's credentials (no commit is made)")
}

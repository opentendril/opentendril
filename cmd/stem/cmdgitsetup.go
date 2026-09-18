package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/substrateconfig"
)

// `tendril git setup` — one command that stands up a git connection so neither
// the Botanist nor a Pollinator hand-assembles config. It writes a
// `substrates.yaml` credentials profile + substrate, prints the per-Pollinator
// Model Context Protocol block, and can check the result with --verify. It is
// flag-driven (no interactive-only path) and writes only references — env-var
// names and key paths — never a secret. Two postures mirror the connection
// tiers: `app` (GitHub App, commits signed server-side via commit: api) and
// `pat` (fine-grained Personal Access Token + a dedicated GPG signing key).

type gitSetupOptions struct {
	posture        string // "app" (default) | "pat"
	substrate      string
	repo           string // owner/repo
	appID          string
	keyPath        string
	tokenEnv       string
	signKey        string
	identityName   string
	identityEmail  string
	grantPollen    string
	grantPollenSet bool
	checkout       string // managed (default) | path | ephemeral
	dir            string // explicit alternate config root; empty selects canonical registry
	yes            bool
	force          bool
	verify         bool
	help           bool
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
	if err := executeGitSetup(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

// executeGitSetup performs the Botanist-only setup operation after argument
// parsing. The Pollen gate is deliberately first so setup and verification do
// not read or mutate the Substrate registry for a delegated invocation.
func executeGitSetup(ctx context.Context, opts gitSetupOptions) error {
	if pollen := strings.TrimSpace(os.Getenv(envPollenCLI)); pollen != "" {
		return fmt.Errorf("git setup is Botanist-only and refuses declared Pollen %q before Substrate configuration access", pollen)
	}
	if opts.verify {
		if !runGitSetupVerify(ctx, opts) {
			return fmt.Errorf("git setup verification failed")
		}
		return nil
	}

	if !confirmGitSetupTarget(opts) {
		return fmt.Errorf("nothing written")
	}

	substratesPath, err := gitSetupSubstratePath(opts)
	if err != nil {
		return err
	}
	if err := upsertSubstrates(substratesPath, opts); err != nil {
		return err
	}

	printGitSetupNextSteps(opts)
	return nil
}

// parseGitSetupArgs turns CLI flags into validated options, applying the secure
// defaults (posture app, checkout managed) and enforcing the per-posture
// requirements.
func parseGitSetupArgs(args []string) (gitSetupOptions, error) {
	opts := gitSetupOptions{posture: "app", checkout: "managed"}
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
		case "--grant-pollen":
			opts.grantPollenSet = true
			opts.grantPollen, err = need(&i)
			if err != nil {
				return opts, unsupportedGitSetupGrantPollenError()
			}
		case "--checkout":
			opts.checkout, err = need(&i)
		case "--dir":
			opts.dir, err = need(&i)
		case "--yes":
			opts.yes = true
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
	if opts.grantPollenSet {
		return opts, unsupportedGitSetupGrantPollenError()
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

func unsupportedGitSetupGrantPollenError() error {
	return fmt.Errorf("--grant-pollen is not supported by git setup; use tendril delegation create --pollen <pollen> --substrate <name> --operation <class> to grant authority")
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

// renderGrantsYAML is retained as a fixture renderer for delegation tests. Git
// setup no longer persists its output; delegation authority is created through
// the dedicated delegation lifecycle.
func renderGrantsYAML(o gitSetupOptions) string {
	var b strings.Builder
	b.WriteString("# Generated delegation fixture\ngrants:\n")
	fmt.Fprintf(&b, "  %s:\n", o.grantPollen)
	b.WriteString("    operationClasses: [git.status, git.branch.list, git.branch, git.commit, git.push, git.pr]\n")
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

// printGitSetupNextSteps prints the per-Pollinator Model Context Protocol block and the follow-up
// actions a human still has to take (uploading a signing key, verifying).
func printGitSetupNextSteps(o gitSetupOptions) {
	fmt.Println()
	fmt.Println("Substrate configuration does not create delegation authority.")
	fmt.Println("To authorise a Pollinator, use the separate delegation lifecycle:")
	fmt.Println("  tendril delegation create --pollen <pollen> --substrate <name> --operation <class>")
	fmt.Println()
	fmt.Println("Pollinator Model Context Protocol configuration:")
	fmt.Println(`  { "mcpServers": { "opentendril": {`)
	fmt.Println(`    "command": "tendril", "args": ["serve", "mcp", "stdio"],`)
	fmt.Println(`    "env": { "TENDRIL_POLLEN": "<pollen>" }`)
	fmt.Println(`  }}}`)
	fmt.Println()
	if o.posture == "pat" {
		fmt.Println("Next: ensure the signing key is uploaded to GitHub (Settings → GPG keys)")
		fmt.Printf("      and the token env %s is set, so commits show Verified.\n", envOrDefault(o.tokenEnv, "GITHUB_TOKEN"))
		fmt.Println("      The token needs Contents: Read and write, plus Pull requests: Read")
		fmt.Println("      and write for the granted git.pr operation.")
	} else {
		fmt.Println("Next: ensure the GitHub App is installed on the repository (Contents: Read")
		fmt.Println("      and write, plus Pull requests: Read and write for the granted git.pr")
		fmt.Println("      operation). Commits are then signed by GitHub and show Verified.")
	}
	fmt.Printf("Check it:  tendril git setup --verify --substrate %s", o.substrate)
	if strings.TrimSpace(o.dir) != "" {
		fmt.Printf(" --dir %s", o.dir)
	}
	fmt.Println()
}

func envOrDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// verifySubstrateSetup is the Conductor setup-verification probe. The CLI
// adapter reports the result; tests may replace this function.
var verifySubstrateSetup = conductor.VerifySubstrateSetup

// runGitSetupVerify loads the written config, resolves the substrate's
// credential, and asks the Conductor to verify the connection. The CLI does
// not implement GitHub authentication, branch resolution, checkout-mode
// routing, or readiness policy. The check does not clone, commit, push, or
// open a pull request. Returns true when the connection is ready.
func runGitSetupVerify(ctx context.Context, o gitSetupOptions) bool {
	cfg, source, err := conductor.LoadSubstratesConfigWithSource(o.dir)
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
	if source != "" {
		fmt.Printf("  registry:     %s\n", source)
	}
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
	var managed bool
	if ready {
		verification, err := verifySubstrateSetup(ctx, *spec, cred)
		if err != nil {
			diagnostic := err.Error()
			if strings.Contains(diagnostic, "has no Git base") {
				diagnostic = strings.Replace(diagnostic,
					"Create an initial commit before using it as an OpenTendril Substrate, then rerun tendril git setup --verify",
					fmt.Sprintf("use the supported `tendril git bootstrap --substrate %s` command to create the OpenTendril Substrate base, then rerun tendril git setup --verify", o.substrate),
					1,
				)
			}
			fmt.Fprintf(os.Stderr, "❌ remote verification failed: %s\n", diagnostic)
			ready = false
		} else {
			managed = verification.Managed
			if managed {
				sha := verification.GitBase.Commit
				if len(sha) > 12 {
					sha = sha[:12]
				}
				fmt.Println("  ✅ authenticated to the configured repository")
				fmt.Printf("  ✅ Git base ready: branch %q at %s\n", verification.GitBase.Branch, sha)
				if verification.ContentsWrite {
					fmt.Println("  ✅ App installation has repository contents write permission")
				}
			} else if cred.Method == conductor.CredentialApp {
				fmt.Println("  ✅ authenticated to the configured repository")
			}
		}
	}
	if cred.Sign.Method != "" {
		fmt.Printf("  signing:      %s (%s)\n", cred.Sign.Method, cred.Sign.Key)
	}
	if cred.Identity.Name != "" || cred.Identity.Email != "" {
		fmt.Printf("  identity:     %s <%s>\n", cred.Identity.Name, cred.Identity.Email)
	}
	if ready {
		if managed {
			fmt.Println("✅ Connection configured; the repository is ready as a managed Substrate.")
		} else if cred.Method == conductor.CredentialApp {
			fmt.Println("✅ Connection configured; authenticated to the remote repository.")
		} else {
			fmt.Println("✅ Connection configured; authentication material present.")
		}
	} else {
		fmt.Println("⚠️  Connection configured, but it is not ready (see above).")
	}
	return ready
}

// confirmGitSetupTarget shows where configuration will be written and who will
// be able to read the credential it references, then asks.
//
// The canonical registry is account-global; an explicit --dir opts into an
// alternate location. A key path the running user cannot read produces a
// connection that reports success and can never work.
func confirmGitSetupTarget(opts gitSetupOptions) bool {
	destination, err := gitSetupSubstratePath(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ resolve setup destination: %v\n", err)
		return false
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		absolute = destination
	}

	fmt.Println("About to write:")
	fmt.Printf("  %s\n", absolute)
	fmt.Printf("Connection %q → %s\n", opts.substrate, opts.repo)

	if key := strings.TrimSpace(opts.keyPath); key != "" {
		if file, err := os.Open(key); err == nil {
			file.Close()
		} else {
			current := "this user"
			if who, err := user.Current(); err == nil {
				current = who.Username
			}
			fmt.Printf("\n⚠️  %s cannot read %s.\n", current, key)
			fmt.Println("    The connection would be written but could never authenticate.")
			fmt.Println("    Run this as the account that owns the key.")
		}
	}

	if opts.yes || opts.force {
		return true
	}
	if !gitSetupIsTerminal(os.Stdin) {
		fmt.Fprintln(os.Stderr, "\n❌ Not a terminal, so the destination cannot be confirmed.")
		fmt.Fprintln(os.Stderr, "   Re-run with --yes to write without confirmation.")
		return false
	}

	fmt.Print("\nProceed? (y/n): ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// isTerminal reports whether a file is an interactive terminal, so a scripted
// invocation is never blocked on a prompt.
func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

var gitSetupIsTerminal = isTerminal

func printGitSetupUsage() {
	fmt.Println("Usage: tendril git setup --substrate <name> --repo <owner/repo> [flags]")
	fmt.Println()
	fmt.Println("Writes a git connection to the canonical Substrate registry and prints the")
	fmt.Println("per-Pollinator Model Context Protocol configuration. Secrets are referenced, never stored.")
	fmt.Println()
	fmt.Println("  --posture app|pat     Connection posture (default app: GitHub App, server-signed)")
	fmt.Println("  --substrate <name>    Name for this connection/repo (required)")
	fmt.Println("  --repo <owner/repo>   The GitHub repository (required)")
	fmt.Println("  --checkout <mode>     managed (default) | path | ephemeral")
	fmt.Println("  Delegation authority is separate; use tendril delegation create")
	fmt.Println()
	fmt.Println("  posture app:  --app-id <id>  --key <pem path>")
	fmt.Println("  posture pat:  --token-env <ENV>  --sign-key <gpg id>  --identity-name <n>  --identity-email <e>")
	fmt.Println()
	fmt.Println("  --dir <path>          Explicit alternate config directory (default: ~/.tendril)")
	fmt.Println("  --yes                 Accept the destination without prompting; does not overwrite existing entries")
	fmt.Println("  --force               Overwrite existing entries and skip confirmation")
	fmt.Println("  --verify              Check the configured connection (managed checkouts include Git-base readiness; no mutation)")
}

func gitSetupSubstratePath(o gitSetupOptions) (string, error) {
	if strings.TrimSpace(o.dir) == "" {
		return substrateconfig.CanonicalPath()
	}
	return filepath.Join(o.dir, "substrates.yaml"), nil
}

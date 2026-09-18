package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/substrateconfig"
)

// runSubstrateCmd is the Botanist control-plane adapter for the persistent
// Substrate registry. It deliberately has no --dir escape hatch: lifecycle
// operations always use the ordinary canonical/default discovery.
func runSubstrateCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printSubstrateUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printSubstrateUsage()
	case "list":
		if len(args) > 1 {
			if len(args) == 2 && isHelpArg(args[1]) {
				printSubstrateUsage()
				return
			}
			fmt.Fprintf(os.Stderr, "❌ tendril substrate list does not accept arguments\n")
			printSubstrateUsage()
			os.Exit(1)
		}
		if err := executeSubstrateList(); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
	case "get":
		opts, err := parseSubstrateNameArgs("get", args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			printSubstrateUsage()
			os.Exit(1)
		}
		if opts.help {
			printSubstrateUsage()
			return
		}
		if err := executeSubstrateGet(opts.name); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
	case "add":
		opts, err := parseSubstrateAddArgs(args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			printSubstrateUsage()
			os.Exit(1)
		}
		if opts.help {
			printSubstrateUsage()
			return
		}
		if err := executeSubstrateAdd(opts.request); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
	case "update":
		opts, err := parseSubstrateUpdateArgs(args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			printSubstrateUsage()
			os.Exit(1)
		}
		if opts.help {
			printSubstrateUsage()
			return
		}
		if err := executeSubstrateUpdate(opts.request); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
	case "verify":
		opts, err := parseSubstrateNameArgs("verify", args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			printSubstrateUsage()
			os.Exit(1)
		}
		if opts.help {
			printSubstrateUsage()
			return
		}
		if err := executeSubstrateVerify(ctx, opts.name); err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown substrate command: %s\n", args[0])
		printSubstrateUsage()
		os.Exit(1)
	}
}

type substrateNameOptions struct {
	name string
	help bool
}

type substrateAddOptions struct {
	request substrateconfig.AddRequest
	help    bool
}

type substrateUpdateOptions struct {
	request substrateconfig.UpdateRequest
	help    bool
}

func parseSubstrateNameArgs(operation string, args []string) (substrateNameOptions, error) {
	if len(args) == 1 && isHelpArg(args[0]) {
		return substrateNameOptions{help: true}, nil
	}
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" || strings.HasPrefix(args[0], "-") {
		return substrateNameOptions{}, fmt.Errorf("tendril substrate %s requires <name>", operation)
	}
	return substrateNameOptions{name: strings.TrimSpace(args[0])}, nil
}

func parseSubstrateAddArgs(args []string) (substrateAddOptions, error) {
	opts := substrateAddOptions{request: substrateconfig.AddRequest{Posture: "app", Checkout: "managed"}}
	if len(args) == 1 && isHelpArg(args[0]) {
		opts.help = true
		return opts, nil
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return opts, fmt.Errorf("tendril substrate add requires <name>")
	}
	opts.request.Name = args[0]
	for index := 1; index < len(args); index++ {
		var err error
		switch args[index] {
		case "--repo":
			opts.request.Repo, err = nextSubstrateArg(args, &index)
		case "--posture":
			opts.request.Posture, err = nextSubstrateArg(args, &index)
			opts.request.PostureSet = true
		case "--app-id":
			opts.request.AppID, err = nextSubstrateArg(args, &index)
		case "--key":
			opts.request.KeyPath, err = nextSubstrateArg(args, &index)
		case "--token-env":
			opts.request.TokenEnv, err = nextSubstrateArg(args, &index)
		case "--sign-key":
			opts.request.SignKey, err = nextSubstrateArg(args, &index)
		case "--identity-name":
			opts.request.IdentityName, err = nextSubstrateArg(args, &index)
		case "--identity-email":
			opts.request.IdentityEmail, err = nextSubstrateArg(args, &index)
		case "--checkout":
			opts.request.Checkout, err = nextSubstrateArg(args, &index)
			opts.request.CheckoutSet = true
		case "--path":
			opts.request.CheckoutPath, err = nextSubstrateArg(args, &index)
			opts.request.PathSet = true
		case "--branch":
			opts.request.Branch, err = nextSubstrateArg(args, &index)
			opts.request.BranchSet = true
		case "-h", "--help", "help":
			return opts, fmt.Errorf("help must be the only argument")
		default:
			return opts, fmt.Errorf("unknown argument %q for substrate add", args[index])
		}
		if err != nil {
			return opts, err
		}
	}
	return opts, nil
}

func parseSubstrateUpdateArgs(args []string) (substrateUpdateOptions, error) {
	opts := substrateUpdateOptions{}
	if len(args) == 1 && isHelpArg(args[0]) {
		opts.help = true
		return opts, nil
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return opts, fmt.Errorf("tendril substrate update requires <name>")
	}
	opts.request.Name = args[0]
	for index := 1; index < len(args); index++ {
		var err error
		switch args[index] {
		case "--repo":
			opts.request.Repo, err = nextSubstrateArg(args, &index)
			opts.request.RepoSet = true
		case "--branch":
			opts.request.Branch, err = nextSubstrateArg(args, &index)
			opts.request.BranchSet = true
		case "--checkout":
			opts.request.Checkout, err = nextSubstrateArg(args, &index)
			opts.request.CheckoutSet = true
		case "--path":
			opts.request.CheckoutPath, err = nextSubstrateArg(args, &index)
			opts.request.PathSet = true
		case "-h", "--help", "help":
			return opts, fmt.Errorf("help must be the only argument")
		default:
			return opts, fmt.Errorf("unknown argument %q for substrate update", args[index])
		}
		if err != nil {
			return opts, err
		}
	}
	return opts, nil
}

func nextSubstrateArg(args []string, index *int) (string, error) {
	if *index+1 >= len(args) {
		return "", fmt.Errorf("flag %s requires a value", args[*index])
	}
	*index++
	return args[*index], nil
}

func isHelpArg(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "-h", "--help", "help":
		return true
	default:
		return false
	}
}

func requireBotanistSubstrate(operation string) error {
	if rawPollen := os.Getenv(envPollenCLI); rawPollen != "" {
		return fmt.Errorf("substrate %s is Botanist-only and refuses declared Pollen %q before Substrate configuration access", operation, rawPollen)
	}
	return nil
}

func executeSubstrateList() error {
	if err := requireBotanistSubstrate("list"); err != nil {
		return err
	}
	result, err := substrateconfig.LoadCanonical()
	if err != nil {
		return fmt.Errorf("load Substrate registry: %w", err)
	}
	printRegistrySource(result)
	if result.Source == "" || result.Config == nil || len(result.Config.Substrates) == 0 {
		fmt.Println("No Substrates configured.")
		return nil
	}

	names := sortedSubstrateNames(result.Config)
	for _, name := range names {
		spec := result.Config.Substrates[name]
		fmt.Printf("Substrate %q:\n", name)
		fmt.Printf("  url:            %s\n", safeStoredURL(spec.URL))
		fmt.Printf("  profile:        %s\n", spec.Profile)
		fmt.Printf("  checkout.mode:  %s\n", spec.Checkout.Mode)
	}
	return nil
}

func executeSubstrateGet(name string) error {
	if err := requireBotanistSubstrate("get"); err != nil {
		return err
	}
	result, err := substrateconfig.LoadCanonical()
	if err != nil {
		return fmt.Errorf("load Substrate registry: %w", err)
	}
	printRegistrySource(result)
	if result.Config == nil {
		return fmt.Errorf("Substrate %q not found", name)
	}
	spec, ok := result.Config.Substrates[name]
	if !ok {
		return fmt.Errorf("Substrate %q not found", name)
	}
	printStoredSubstrate(name, spec)
	if strings.TrimSpace(spec.Profile) != "" {
		profile, ok := result.Config.Credentials[spec.Profile]
		if !ok {
			fmt.Printf("  credential profile: %s (not found)\n", spec.Profile)
		} else {
			printStoredCredentialProfile(spec.Profile, profile)
		}
	}
	return nil
}

func executeSubstrateAdd(request substrateconfig.AddRequest) error {
	if err := requireBotanistSubstrate("add"); err != nil {
		return err
	}
	result, err := substrateconfig.AddCanonical(request)
	if err != nil {
		return err
	}
	fmt.Printf("Added Substrate %q to %s\n", strings.TrimSpace(request.Name), result.Destination)
	printMutationObservability(result)
	return nil
}

func executeSubstrateUpdate(request substrateconfig.UpdateRequest) error {
	if err := requireBotanistSubstrate("update"); err != nil {
		return err
	}
	result, err := substrateconfig.UpdateCanonical(request)
	if err != nil {
		return err
	}
	fmt.Printf("Updated Substrate %q in %s\n", strings.TrimSpace(request.Name), result.Destination)
	printMutationObservability(result)
	return nil
}

func executeSubstrateVerify(ctx context.Context, name string) error {
	if err := requireBotanistSubstrate("verify"); err != nil {
		return err
	}
	if !verifySubstrateConnection(ctx, name, "", substrateVerificationOperationName) {
		return fmt.Errorf("Substrate verification failed")
	}
	return nil
}

func printRegistrySource(result substrateconfig.ReadResult) {
	source := result.Source
	if source == "" {
		source = result.Target
	}
	fmt.Printf("registry: %s\n", source)
	if result.LegacyIgnored {
		fmt.Printf("legacy registry ignored: %s (canonical registry is active)\n", result.LegacyPath)
	}
}

func printMutationObservability(result substrateconfig.MutationResult) {
	switch {
	case result.ImportedLegacy:
		fmt.Printf("Imported legacy registry from %s into %s; canonical registry is now active.\n", result.Source, result.Destination)
	case result.LegacyIgnored:
		fmt.Printf("Legacy registry %s ignored because the canonical registry is active.\n", result.LegacyPath)
	}
}

func printStoredSubstrate(name string, spec conductor.SubstrateSpec) {
	fmt.Printf("Substrate %q:\n", name)
	fmt.Printf("  url:                    %s\n", safeStoredURL(spec.URL))
	fmt.Printf("  path:                   %s\n", spec.Path)
	fmt.Printf("  branch:                 %s\n", spec.Branch)
	fmt.Printf("  profile:                %s\n", spec.Profile)
	fmt.Printf("  checkout.mode:          %s\n", spec.Checkout.Mode)
	fmt.Printf("  checkout.path:          %s\n", spec.Checkout.Path)
	fmt.Printf("  commit:                 %s\n", spec.Commit)
	if spec.ProtectDefaultBranch == nil {
		fmt.Println("  protectDefaultBranch:   <unset>")
	} else {
		fmt.Printf("  protectDefaultBranch:   %t\n", *spec.ProtectDefaultBranch)
	}
	fmt.Printf("  readonly:               %t\n", spec.ReadOnly)
	fmt.Printf("  provider:               %s\n", spec.Provider)
	fmt.Printf("  command:                %q\n", spec.Command)
	fmt.Printf("  patience.growth:        %s\n", spec.Patience.Growth)
	fmt.Printf("  patience.reap:          %s\n", spec.Patience.Reap)
	fmt.Printf("  patience.scratch:       %s\n", spec.Patience.Scratch)
	if spec.Auth.Method != "" || spec.Auth.Env != "" || spec.Auth.Key != "" || spec.Auth.AppID != "" || spec.Auth.PrivateKeyPath != "" || spec.Auth.PrivateKeyEnv != "" {
		fmt.Println("  stored auth:")
		printStoredAuth(spec.Auth, "    ")
	}
	if spec.Sign.Method != "" || spec.Sign.Key != "" {
		fmt.Printf("  stored sign:            method=%s key=%s\n", spec.Sign.Method, spec.Sign.Key)
	}
	if spec.Identity.Name != "" || spec.Identity.Email != "" {
		fmt.Printf("  stored identity:        %s <%s>\n", spec.Identity.Name, spec.Identity.Email)
	}
}

func printStoredCredentialProfile(name string, profile conductor.CredentialProfile) {
	fmt.Printf("  credential profile %q:\n", name)
	printStoredAuth(profile.Auth, "    ")
	fmt.Printf("    sign.method:           %s\n", profile.Sign.Method)
	fmt.Printf("    sign.key:              %s\n", profile.Sign.Key)
	fmt.Printf("    identity.name:         %s\n", profile.Identity.Name)
	fmt.Printf("    identity.email:        %s\n", profile.Identity.Email)
	fmt.Printf("    commit:                %s\n", profile.Commit)
}

func printStoredAuth(auth conductor.AuthSpec, indent string) {
	fmt.Printf("%sauth.method:          %s\n", indent, auth.Method)
	fmt.Printf("%sauth.env:             %s\n", indent, auth.Env)
	fmt.Printf("%sauth.key:             %s\n", indent, auth.Key)
	fmt.Printf("%sauth.appId:           %s\n", indent, auth.AppID)
	fmt.Printf("%sauth.installationId:   %d\n", indent, auth.InstallationID)
	fmt.Printf("%sauth.privateKeyPath:  %s\n", indent, auth.PrivateKeyPath)
	fmt.Printf("%sauth.privateKeyEnv:   %s\n", indent, auth.PrivateKeyEnv)
	fmt.Printf("%sauth.exposeToken:     %t\n", indent, auth.ExposeToken)
}

func safeStoredURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<invalid stored URL redacted>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func printSubstrateUsage() {
	fmt.Println("Usage: tendril substrate <list|get|add|update|verify> [arguments]")
	fmt.Println()
	fmt.Println("  tendril substrate list")
	fmt.Println("  tendril substrate get <name>")
	fmt.Println("  tendril substrate add <name> --repo owner/repo [flags]")
	fmt.Println("  tendril substrate update <name> [--repo owner/repo] [--branch branch] [--checkout managed|path|ephemeral] [--path checkout-path]")
	fmt.Println("  tendril substrate verify <name>")
}

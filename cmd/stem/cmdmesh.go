package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/conductor"
	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/mesh"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// runMeshCmd hosts three kinds of subcommands. graft/promote are the CLI
// adapter for the governed substrate-grafting capability family (// slice 3): thin projections of the same transport-free core.Core the REST
// and MCP surfaces use. trait/list-accept-reject expose the governed mesh
// trait inbox. keygen/issue-token stay deliberately ungoverned,
// CLI-local key-management commands: they mint the workspace's private mesh
// keys and signed tokens, an authority that must not be projected onto the
// network surfaces (see internal/core/mesh.go).
func runMeshCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printMeshUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "keygen":
		runMeshKeygenCmd(args[1:])
		return
	case "issue-token":
		runMeshIssueTokenCmd(ctx, args[1:])
		return
	case "trait":
		runMeshTraitCmd(ctx, args[1:])
		return
	case "-h", "--help", "help":
		printMeshUsage()
		return
	}

	command, ok := lookupMeshCommand(strings.ToLower(strings.TrimSpace(args[0])))
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown mesh command: %s\n", args[0])
		printMeshUsage()
		os.Exit(1)
	}

	input, err := parseMeshArgs(command.capability, args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}

	svc, err := buildMeshCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, command.capability, input)
	if err != nil {
		switch command.capability {
		case core.CapMeshGraft:
			fmt.Fprintf(os.Stderr, "❌ Mesh graft failed: %v\n", err)
		case core.CapMeshPromote:
			fmt.Fprintf(os.Stderr, "❌ PR promotion failed: %v\n", err)
		default:
			fmt.Fprintf(os.Stderr, "❌ %s failed: %v\n", command.capability, err)
		}
		os.Exit(1)
	}

	renderMeshResult(command.capability, result)
}

// buildMeshCore constructs a Core with the mesh execution port wired. The
// session manager is an ephemeral in-memory one — mesh capabilities touch no
// session state.
func buildMeshCore(ctx context.Context) (core.Core, error) {
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		return nil, err
	}
	return core.NewService(manager).WithMesh(meshOperations()), nil
}

// meshOperations binds the mesh execution port to the conductor (substrate
// resolution), the mesh client (delegated push), and the local trait inbox
// stub — this wiring lives in the adapter layer precisely so the Core never
// imports either package (see internal/core/boundary_test.go).
func meshOperations() core.MeshOperations {
	return core.MeshOperations{
		ResolveWorkspace: func(_ context.Context, substrate string) (string, error) {
			return resolveMeshWorkspace(substrate)
		},
		DelegatePush: func(ctx context.Context, workspace, branch, commitMessage string) (string, error) {
			client := mesh.NewClientFromEnv()
			if client == nil {
				return "", fmt.Errorf("TENDRIL_GRAFT_URL and TENDRIL_GRAFT_TOKEN must be set to delegate a mesh graft")
			}
			return client.DelegatePush(ctx, workspace, branch, commitMessage)
		},
		ListPendingTraits: func(context.Context) ([]any, error) {
			records := meshTraitInbox.ListPending()
			traits := make([]any, 0, len(records))
			for _, record := range records {
				traits = append(traits, record)
			}
			return traits, nil
		},
		AcceptTrait: func(_ context.Context, traitID string) error {
			return meshTraitInbox.Accept(traitID)
		},
		RejectTrait: func(_ context.Context, traitID string) error {
			return meshTraitInbox.Reject(traitID)
		},
	}
}

// resolveMeshWorkspace maps a substrate path or named substrate key onto a
// local git workspace root (the same resolution the MCP adapter historically
// performed inline).
func resolveMeshWorkspace(substrate string) (string, error) {
	substrate = strings.TrimSpace(substrate)
	if substrate == "" {
		return resolveRepoRoot(""), nil
	}

	substratesConfig, err := conductor.LoadSubstratesConfig("")
	if err != nil {
		substratesConfig = nil
	}
	if substrateSpec, isName := conductor.ResolveSubstrate(substrate, substratesConfig); isName && substrateSpec != nil {
		if trimmedPath := strings.TrimSpace(substrateSpec.Path); trimmedPath != "" {
			if info, err := os.Stat(trimmedPath); err == nil && info.IsDir() {
				return resolveRepoRoot(trimmedPath), nil
			}
		}
	}

	if info, err := os.Stat(substrate); err == nil && info.IsDir() {
		return resolveRepoRoot(substrate), nil
	}

	return "", fmt.Errorf("substrate %q does not resolve to a local git workspace", substrate)
}

// meshCommand is one governed subcommand actually registered on the
// `tendril mesh` command tree.
type meshCommand struct {
	name       string // CLI token, e.g. "graft"
	capability string // the governed Core capability it invokes
}

// meshCommands is the CLI command tree for the governed half of
// `tendril mesh` (keygen/issue-token are ungoverned and dispatched
// separately). Like sessionCommands, this registration — NOT
// core.CapabilityNames() — is the source of truth the parity coverage test
// reads for the CLI arm.
var meshCommands = []meshCommand{
	{"graft", core.CapMeshGraft},
	{"promote", core.CapMeshPromote},
}

// meshTraitCommand is one governed subcommand actually registered under
// `tendril mesh trait`.
type meshTraitCommand struct {
	name       string
	capability string
}

// meshTraitCommands is the CLI command tree for the governed mesh trait
// inbox. Like sessionCommands, this registration — NOT core.CapabilityNames()
// — is the source of truth the parity coverage test reads for the CLI arm.
var meshTraitCommands = []meshTraitCommand{
	{"list", core.CapMeshTraitList},
	{"accept", core.CapMeshTraitAccept},
	{"reject", core.CapMeshTraitReject},
}

// lookupMeshCommand resolves a CLI subcommand token to its registered entry.
func lookupMeshCommand(sub string) (meshCommand, bool) {
	for _, command := range meshCommands {
		if command.name == sub {
			return command, true
		}
	}
	return meshCommand{}, false
}

// lookupMeshTraitCommand resolves a CLI trait subcommand token to its registered entry.
func lookupMeshTraitCommand(sub string) (meshTraitCommand, bool) {
	for _, command := range meshTraitCommands {
		if command.name == sub {
			return command, true
		}
	}
	return meshTraitCommand{}, false
}

// meshCLICapabilityNames returns the capability names the CLI has actually
// registered mesh subcommands for, sorted. Read by the parity coverage test.
func meshCLICapabilityNames() []string {
	names := make([]string, 0, len(meshCommands)+len(meshTraitCommands))
	for _, command := range meshCommands {
		names = append(names, command.capability)
	}
	for _, command := range meshTraitCommands {
		names = append(names, command.capability)
	}
	sort.Strings(names)
	return names
}

// parseMeshArgs turns CLI args into a capability input map: a positional
// substrate plus --branch/--commit-message (and --pr-number for promote);
// `--json '{...}'` is the generic escape hatch, mirroring parseSessionArgs.
func parseMeshArgs(capName string, args []string) (map[string]any, error) {
	input := map[string]any{}
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --json requires a value")
			}
			i++
			if err := json.Unmarshal([]byte(args[i]), &input); err != nil {
				return nil, fmt.Errorf("invalid --json input: %w", err)
			}
		case "--branch":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --branch requires a value")
			}
			i++
			input["branch"] = args[i]
		case "--commit-message":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --commit-message requires a value")
			}
			i++
			input["commitMessage"] = args[i]
		case "--pr-number":
			if capName != core.CapMeshPromote {
				return nil, fmt.Errorf("unknown argument %q for mesh graft", args[i])
			}
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --pr-number requires a value")
			}
			i++
			input["prNumber"] = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, fmt.Errorf("unknown argument %q for mesh %s", args[i], strings.TrimPrefix(capName, "mesh."))
			}
			positional = append(positional, args[i])
		}
	}

	if len(positional) > 1 {
		return nil, fmt.Errorf("expected exactly one substrate, got %d", len(positional))
	}
	if len(positional) == 1 {
		input["substrate"] = positional[0]
	}
	if substrate, _ := input["substrate"].(string); strings.TrimSpace(substrate) == "" {
		return nil, fmt.Errorf("missing substrate. Usage: tendril mesh %s <substrate>", strings.TrimPrefix(capName, "mesh."))
	}
	return input, nil
}

// renderMeshResult presents a capability result on the terminal, mirroring
// the mesh-graft text the MCP surface has always emitted.
func renderMeshResult(capability string, result any) {
	switch capability {
	case core.CapMeshGraft:
		delegation, _ := result.(core.MeshDelegation)
		fmt.Printf("✅ Delegated substrate %s through mesh graft. Commit %s\n", delegation.Workspace, delegation.Commit)
	case core.CapMeshPromote:
		promotion, _ := result.(core.MeshPromotion)
		if promotion.PRNumber != "" {
			fmt.Printf("✅ Promoted pull request #%s via mesh graft for %s. Commit %s\n", promotion.PRNumber, promotion.Workspace, promotion.Commit)
			return
		}
		fmt.Printf("✅ Promoted pull request via mesh graft for %s. Commit %s\n", promotion.Workspace, promotion.Commit)
	}
}

func runMeshTraitCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printMeshTraitUsage()
		return
	}

	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "-h", "--help", "help":
		printMeshTraitUsage()
		return
	}

	command, ok := lookupMeshTraitCommand(strings.ToLower(strings.TrimSpace(args[0])))
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown mesh trait command: %s\n", args[0])
		printMeshTraitUsage()
		os.Exit(1)
	}

	input, err := parseMeshTraitArgs(command.capability, args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}

	svc, err := buildMeshCore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize core: %v\n", err)
		os.Exit(1)
	}

	result, err := svc.Invoke(ctx, command.capability, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ mesh trait %s failed: %v\n", command.name, err)
		os.Exit(1)
	}

	renderMeshTraitResult(result)
}

// parseMeshTraitArgs turns CLI args into a mesh trait capability input map.
func parseMeshTraitArgs(capName string, args []string) (map[string]any, error) {
	input := map[string]any{}
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --json requires a value")
			}
			i++
			if err := json.Unmarshal([]byte(args[i]), &input); err != nil {
				return nil, fmt.Errorf("invalid --json input: %w", err)
			}
		case "--id", "--trait-id":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag %s requires a value", args[i])
			}
			i++
			input["traitId"] = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return nil, fmt.Errorf("unknown argument %q for mesh trait %s", args[i], strings.TrimPrefix(capName, "mesh.trait."))
			}
			positional = append(positional, args[i])
		}
	}

	switch capName {
	case core.CapMeshTraitList:
		if len(positional) > 0 {
			return nil, fmt.Errorf("mesh trait list does not take positional arguments")
		}
	case core.CapMeshTraitAccept, core.CapMeshTraitReject:
		if len(positional) > 1 {
			return nil, fmt.Errorf("expected exactly one trait id, got %d", len(positional))
		}
		if len(positional) == 1 {
			input["traitId"] = positional[0]
		}
		if traitID, _ := input["traitId"].(string); strings.TrimSpace(traitID) == "" {
			return nil, fmt.Errorf("missing trait id. Usage: tendril mesh trait %s <trait-id>", strings.TrimPrefix(capName, "mesh.trait."))
		}
	}

	return input, nil
}

func renderMeshTraitResult(result any) {
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode result: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(payload))
}

func printMeshTraitUsage() {
	fmt.Println("Usage: tendril mesh trait <list|accept|reject> [trait-id]")
	fmt.Println("  list            Show pending foreign traits")
	fmt.Println("  accept <id>     Accept one pending foreign trait")
	fmt.Println("  reject <id>     Reject one pending foreign trait")
	fmt.Println()
	fmt.Println("Trait commands are projections of the shared Core capability registry.")
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
	fmt.Println("tendril mesh - mesh utilities")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  tendril mesh graft <substrate> [--branch NAME] [--commit-message TEXT]")
	fmt.Println("  tendril mesh promote <substrate> [--branch NAME] [--pr-number N] [--commit-message TEXT]")
	fmt.Println("  tendril mesh trait <list|accept|reject> [trait-id]")
	fmt.Println("  tendril mesh keygen [--workspace PATH] [--force]")
	fmt.Println("  tendril mesh issue-token [--workspace PATH] [--subject TEXT] [--audience TEXT] [--scope TEXT] [--ttl DURATION]")
	fmt.Println()
	fmt.Println("graft, promote, and trait are projections of the shared Core capability registry.")
}

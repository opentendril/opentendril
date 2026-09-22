package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/opentendril/opentendril/cmd/stem/internal/heartwood"
	"github.com/opentendril/opentendril/cmd/stem/internal/rhizome"
)

func runMemoryCmd(ctx context.Context, args []string) {
	if len(args) == 0 {
		printMemoryUsage()
		return
	}

	switch args[0] {
	case "-h", "--help", "help":
		printMemoryUsage()
	case "list":
		runMemoryListCmd(ctx, args[1:])
	case "search":
		runMemorySearchCmd(ctx, args[1:])
	case "add":
		runMemoryAddCmd(ctx, args[1:])
	case "confirm":
		runMemoryConfirmCmd(ctx, args[1:])
	case "reject":
		runMemoryRejectCmd(ctx, args[1:])
	case "supersede":
		runMemorySupersedeCmd(ctx, args[1:])
	case "remove":
		runMemoryRemoveCmd(ctx, args[1:])
	case "export":
		runMemoryExportCmd(ctx, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown memory command: %s\n", args[0])
		printMemoryUsage()
		os.Exit(1)
	}
}

func runMemoryListCmd(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("memory list", flag.ExitOnError)
	category := flags.String("category", "", "Memory category")
	if err := flags.Parse(args); err != nil {
		os.Exit(1)
	}

	backend := openMemoryBackendForCLI(ctx)
	defer closeMemoryBackend(backend)

	memories, err := backend.ListMemories(ctx, currentRepositoryName(), *category, 100)
	if err != nil {
		failMemoryCmd("list memories", err)
	}
	printMemoryTable(memories)
}

func runMemorySearchCmd(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("memory search", flag.ExitOnError)
	category := flags.String("category", "", "Memory category")
	if err := flags.Parse(args); err != nil {
		os.Exit(1)
	}
	if flags.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: tendril memory search [--category=X] <query>")
		os.Exit(1)
	}

	backend := openMemoryBackendForCLI(ctx)
	defer closeMemoryBackend(backend)

	memories, err := backend.SearchMemories(ctx, currentRepositoryName(), strings.Join(flags.Args(), " "), *category, 20)
	if err != nil {
		failMemoryCmd("search memories", err)
	}
	printMemoryTable(memories)
}

// parsedAddArgs contains the pure parsed CLI inputs for add.
type parsedAddArgs struct {
	Title         string
	Category      string
	Tags          string
	Content       string
	Kind          string
	EvidencePaths []string
}

// parseAddArgs is the pure transport parsing boundary for memory add.
func parseAddArgs(args []string) (parsedAddArgs, error) {
	fs := flag.NewFlagSet("memory add", flag.ContinueOnError)
	category := fs.String("category", "General", "Memory category")
	title := fs.String("title", "", "Memory title")
	tags := fs.String("tags", "", "Comma-separated tags")
	content := fs.String("content", "", "Memory content (reads stdin when omitted)")
	kind := fs.String("kind", "", "Knowledge kind: observation (default), fact, constraint, correction, rejected-interpretation")
	evidence := &multiStringFlag{}
	fs.Var(evidence, "evidence", "Repository-relative evidence file path (may be repeated)")
	// Setting Output to io.Discard prevents flag.Parse from printing to stderr on error
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return parsedAddArgs{}, fmt.Errorf("add: parse flags: %w", err)
	}
	for _, arg := range fs.Args() {
		if strings.HasPrefix(arg, "-") {
			return parsedAddArgs{}, fmt.Errorf("add: unsupported or misplaced flag: %s", arg)
		}
	}
	if strings.TrimSpace(*title) == "" {
		return parsedAddArgs{}, fmt.Errorf("add: --title is required")
	}
	return parsedAddArgs{
		Title:         *title,
		Category:      *category,
		Tags:          *tags,
		Content:       *content,
		Kind:          *kind,
		EvidencePaths: evidence.values,
	}, nil
}

// runMemoryAddCmd stores a new Botanist-established memory.
//
// Origin, authority, and status are policy outputs; callers may not supply
// them as flags. The --kind flag selects a knowledge kind; it defaults to
// observation. The --evidence flag may be repeated to bind repository-relative
// evidence files.
func runMemoryAddCmd(ctx context.Context, args []string) {
	parsed, err := parseAddArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Usage: tendril memory add --title=X [--category=X] [--tags=X] [--content=X] [--kind=X] [--evidence=path ...]")
		os.Exit(1)
	}

	body := parsed.Content
	if body == "" {
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			failMemoryCmd("read memory content", err)
		}
		body = string(input)
	}
	if strings.TrimSpace(body) == "" {
		fmt.Fprintln(os.Stderr, "memory content is required")
		os.Exit(1)
	}

	intent := rhizome.BotanistAddIntent{
		RepositoryName: currentRepositoryName(),
		Category:       parsed.Category,
		Title:          parsed.Title,
		Content:        strings.TrimSpace(body),
		Tags:           parsed.Tags,
		Kind:           rhizome.Kind(parsed.Kind),
		EvidencePaths:  parsed.EvidencePaths,
	}
	if len(parsed.EvidencePaths) > 0 {
		intent.SubstrateRoot = currentSubstrateRoot()
	}

	backend := openMemoryBackendForCLI(ctx)
	defer closeMemoryBackend(backend)

	mem, err := rhizome.ExecuteBotanistAdd(ctx, backend, intent)
	if err != nil {
		failMemoryCmd("execute botanist add policy", err)
	}
	fmt.Printf("Memory stored: origin=%s authority=%s status=%s kind=%s\n",
		mem.Origin, mem.Authority, mem.Status, mem.Kind)
}

// parsedConfirmArgs contains the pure parsed CLI inputs for confirm.
type parsedConfirmArgs struct {
	Title string
}

// parseConfirmArgs is the pure transport parsing boundary for memory confirm.
func parseConfirmArgs(args []string) (parsedConfirmArgs, error) {
	fs := flag.NewFlagSet("memory confirm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return parsedConfirmArgs{}, fmt.Errorf("confirm: parse flags: %w", err)
	}
	for _, arg := range fs.Args() {
		if strings.HasPrefix(arg, "-") {
			return parsedConfirmArgs{}, fmt.Errorf("confirm: unsupported or misplaced flag: %s", arg)
		}
	}
	if fs.NArg() < 1 {
		return parsedConfirmArgs{}, fmt.Errorf("confirm: title is required")
	}
	return parsedConfirmArgs{Title: strings.Join(fs.Args(), " ")}, nil
}

// runMemoryConfirmCmd confirms a proposed memory, transitioning it to established
// under Botanist authority while preserving origin and all provenance fields.
func runMemoryConfirmCmd(ctx context.Context, args []string) {
	parsed, err := parseConfirmArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Usage: tendril memory confirm <title>")
		os.Exit(1)
	}

	backend := openMemoryBackendForCLI(ctx)
	defer closeMemoryBackend(backend)

	confirmed, err := rhizome.ApplyConfirm(ctx, backend, currentRepositoryName(), parsed.Title)
	if err != nil {
		failMemoryCmd("confirm memory", err)
	}
	fmt.Printf("Memory confirmed: origin=%s authority=%s status=%s\n",
		confirmed.Origin, confirmed.Authority, confirmed.Status)
}

// parsedRejectArgs contains the pure parsed CLI inputs for reject.
type parsedRejectArgs struct {
	Title string
}

// parseRejectArgs is the pure transport parsing boundary for memory reject.
func parseRejectArgs(args []string) (parsedRejectArgs, error) {
	fs := flag.NewFlagSet("memory reject", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return parsedRejectArgs{}, fmt.Errorf("reject: parse flags: %w", err)
	}
	for _, arg := range fs.Args() {
		if strings.HasPrefix(arg, "-") {
			return parsedRejectArgs{}, fmt.Errorf("reject: unsupported or misplaced flag: %s", arg)
		}
	}
	if fs.NArg() < 1 {
		return parsedRejectArgs{}, fmt.Errorf("reject: title is required")
	}
	return parsedRejectArgs{Title: strings.Join(fs.Args(), " ")}, nil
}

// runMemoryRejectCmd rejects a proposed memory, recording the Botanist decision
// while preserving the original content and origin.
func runMemoryRejectCmd(ctx context.Context, args []string) {
	parsed, err := parseRejectArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Usage: tendril memory reject <title>")
		os.Exit(1)
	}

	backend := openMemoryBackendForCLI(ctx)
	defer closeMemoryBackend(backend)

	rejected, err := rhizome.ApplyReject(ctx, backend, currentRepositoryName(), parsed.Title)
	if err != nil {
		failMemoryCmd("reject memory", err)
	}
	fmt.Printf("Memory rejected: origin=%s authority=%s status=%s\n",
		rejected.Origin, rejected.Authority, rejected.Status)
}

// parsedSupersedeArgs contains the pure parsed CLI inputs for supersede.
type parsedSupersedeArgs struct {
	OldTitle      string
	NewTitle      string
	Category      string
	Tags          string
	Content       string
	EvidencePaths []string
}

// parseSupersedeArgs is the pure transport parsing boundary for memory supersede.
func parseSupersedeArgs(args []string) (parsedSupersedeArgs, error) {
	fs := flag.NewFlagSet("memory supersede", flag.ContinueOnError)
	newTitle := fs.String("title", "", "Title for the replacement memory")
	category := fs.String("category", "", "Category for the replacement")
	tags := fs.String("tags", "", "Tags for the replacement")
	content := fs.String("content", "", "Content for the replacement")
	evidence := &multiStringFlag{}
	fs.Var(evidence, "evidence", "Repository-relative evidence file path (may be repeated)")
	fs.SetOutput(io.Discard)

	var flagArgs []string
	var leadingArgs []string
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") {
			flagArgs = args[i:]
			break
		}
		leadingArgs = append(leadingArgs, arg)
	}

	if err := fs.Parse(flagArgs); err != nil {
		return parsedSupersedeArgs{}, fmt.Errorf("supersede: parse flags: %w", err)
	}
	for _, arg := range fs.Args() {
		if strings.HasPrefix(arg, "-") {
			return parsedSupersedeArgs{}, fmt.Errorf("supersede: unsupported or misplaced flag: %s", arg)
		}
	}

	allPositionals := append(leadingArgs, fs.Args()...)

	if len(allPositionals) < 1 {
		return parsedSupersedeArgs{}, fmt.Errorf("supersede: old title is required")
	}
	if strings.TrimSpace(*newTitle) == "" {
		return parsedSupersedeArgs{}, fmt.Errorf("supersede: --title is required")
	}
	return parsedSupersedeArgs{
		OldTitle:      strings.Join(allPositionals, " "),
		NewTitle:      *newTitle,
		Category:      *category,
		Tags:          *tags,
		Content:       *content,
		EvidencePaths: evidence.values,
	}, nil
}

// runMemorySupersedeCmd creates a Botanist correction that supersedes an existing
// memory. The old record is preserved and marked superseded; the replacement is
// persisted only after the old item is successfully updated.
func runMemorySupersedeCmd(ctx context.Context, args []string) {
	parsed, err := parseSupersedeArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Usage: tendril memory supersede <old-title> --title=<new-title> [--category=X] [--tags=X] [--content=X] [--evidence=path ...]")
		os.Exit(1)
	}

	body := parsed.Content
	if body == "" {
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			failMemoryCmd("read replacement content", err)
		}
		body = string(input)
	}
	if strings.TrimSpace(body) == "" {
		fmt.Fprintln(os.Stderr, "replacement content is required")
		os.Exit(1)
	}

	intent := rhizome.SupersedeIntent{
		RepositoryName: currentRepositoryName(),
		OldTitle:       parsed.OldTitle,
		NewTitle:       parsed.NewTitle,
		Category:       parsed.Category,
		Tags:           parsed.Tags,
		Content:        strings.TrimSpace(body),
		EvidencePaths:  parsed.EvidencePaths,
	}
	if len(parsed.EvidencePaths) > 0 {
		intent.SubstrateRoot = currentSubstrateRoot()
	}

	backend := openMemoryBackendForCLI(ctx)
	defer closeMemoryBackend(backend)

	replacement, err := rhizome.ApplySupersede(ctx, backend, intent)
	if err != nil {
		failMemoryCmd("supersede memory", err)
	}
	fmt.Printf("Memory superseded: old=%q marked superseded; replacement %q origin=%s authority=%s status=%s kind=%s\n",
		parsed.OldTitle, replacement.Title, replacement.Origin, replacement.Authority, replacement.Status, replacement.Kind)
}

func runMemoryRemoveCmd(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("memory remove", flag.ExitOnError)
	if err := flags.Parse(args); err != nil {
		os.Exit(1)
	}
	if flags.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: tendril memory remove <title>")
		os.Exit(1)
	}

	backend := openMemoryBackendForCLI(ctx)
	defer closeMemoryBackend(backend)

	if err := backend.DeleteMemory(ctx, currentRepositoryName(), strings.Join(flags.Args(), " ")); err != nil {
		failMemoryCmd("remove memory", err)
	}
	fmt.Println("Memory removed.")
}

func runMemoryExportCmd(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("memory export", flag.ExitOnError)
	jsonOutput := flags.Bool("json", false, "Export JSON")
	if err := flags.Parse(args); err != nil {
		os.Exit(1)
	}

	backend := openMemoryBackendForCLI(ctx)
	defer closeMemoryBackend(backend)

	memories, err := backend.ListMemories(ctx, currentRepositoryName(), "", 1000)
	if err != nil {
		failMemoryCmd("export memories", err)
	}
	if *jsonOutput {
		encoded, err := json.MarshalIndent(memories, "", "  ")
		if err != nil {
			failMemoryCmd("encode memories", err)
		}
		fmt.Println(string(encoded))
		return
	}
	printMemoryTable(memories)
}

func openMemoryBackendForCLI(ctx context.Context) rhizome.MemoryBackend {
	config, err := rhizome.LoadMemoryConfig()
	if err != nil {
		failMemoryCmd("load memory config", err)
	}

	var cipher *heartwood.Cipher
	if config.Backend == "" || config.Backend == "sqlite" {
		material, err := heartwood.ResolveKey(filepath.Join(".", ".tendril", "rhizome.key"))
		if err != nil {
			failMemoryCmd("resolve memory key", err)
		}
		cipher, err = heartwood.NewCipher(material)
		if err != nil {
			failMemoryCmd("initialize cipher", err)
		}
	}

	backend, err := rhizome.OpenMemoryBackend(ctx, config, cipher)
	if err != nil {
		failMemoryCmd("open memory backend", err)
	}
	return backend
}

func closeMemoryBackend(backend rhizome.MemoryBackend) {
	if closer, ok := backend.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

// printMemoryTable renders a human-readable table of memories including
// lifecycle envelope fields: origin, authority, status, and kind.
func printMemoryTable(memories []rhizome.Memory) {
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "CREATED\tCATEGORY\tTITLE\tORIGIN\tAUTHORITY\tSTATUS\tKIND\tTAGS")
	for _, memory := range memories {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			memory.CreatedAt.Format("2006-01-02"),
			memory.Category,
			memory.Title,
			memory.Origin,
			memory.Authority,
			memory.Status,
			memory.Kind,
			memory.Tags,
		)
	}
	_ = writer.Flush()
}

func currentRepositoryName() string {
	root := currentSubstrateRoot()
	return filepath.Base(root)
}

// currentSubstrateRoot returns the absolute path of the Git worktree root
// (the directory containing the .git folder) by running
// "git rev-parse --show-toplevel". It resolves the same root regardless of
// which subdirectory inside the repository the command is executed from.
func currentSubstrateRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		failMemoryCmd("resolve git substrate root", fmt.Errorf("must be run inside a git repository"))
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		failMemoryCmd("resolve git substrate root", fmt.Errorf("empty root returned by git rev-parse"))
	}
	return root
}

func failMemoryCmd(action string, err error) {
	fmt.Fprintf(os.Stderr, "Failed to %s: %v\n", action, err)
	os.Exit(1)
}

func printMemoryUsage() {
	fmt.Println("Usage: tendril memory <command> [arguments]")
	fmt.Println("  list [--category=X]                        List memories")
	fmt.Println("  search <query>                             Search memories")
	fmt.Println("  add --title=X [--kind=X] [--evidence=path] Store a Botanist-established memory")
	fmt.Println("  confirm <title>                            Confirm a proposed memory")
	fmt.Println("  reject <title>                             Reject a proposed memory")
	fmt.Println("  supersede <old-title> --title=<new-title>  Supersede a memory with a correction")
	fmt.Println("  remove <title>                             Remove a memory")
	fmt.Println("  export [--json]                            Export memories")
}

// multiStringFlag is a flag.Value implementation that collects repeated
// --flag=value occurrences into a slice.
type multiStringFlag struct {
	values []string
}

func (m *multiStringFlag) String() string {
	return strings.Join(m.values, ",")
}

func (m *multiStringFlag) Set(s string) error {
	m.values = append(m.values, s)
	return nil
}

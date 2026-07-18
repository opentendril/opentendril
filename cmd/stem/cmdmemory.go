package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

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

func runMemoryAddCmd(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("memory add", flag.ExitOnError)
	category := flags.String("category", "General", "Memory category")
	title := flags.String("title", "", "Memory title")
	tags := flags.String("tags", "", "Comma-separated tags")
	content := flags.String("content", "", "Memory content")
	if err := flags.Parse(args); err != nil {
		os.Exit(1)
	}
	if strings.TrimSpace(*title) == "" {
		fmt.Fprintln(os.Stderr, "Usage: tendril memory add --category=X --title=X [--tags=X] [--content=X]")
		os.Exit(1)
	}

	body := *content
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

	backend := openMemoryBackendForCLI(ctx)
	defer closeMemoryBackend(backend)

	err := backend.StoreMemory(ctx, rhizome.Memory{
		RepositoryName: currentRepositoryName(),
		Category:       *category,
		Title:          *title,
		Content:        strings.TrimSpace(body),
		Tags:           *tags,
		CreatedAt:      time.Now().UTC(),
	})
	if err != nil {
		failMemoryCmd("store memory", err)
	}
	fmt.Println("Memory stored.")
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

	var encryptor *rhizome.Encryptor
	if config.Backend == "" || config.Backend == "sqlite" {
		key, err := getOrCreateMemoryKey(filepath.Join(".", ".tendril", "rhizome.key"))
		if err != nil {
			failMemoryCmd("resolve memory key", err)
		}
		encryptor, err = rhizome.NewEncryptor(key)
		if err != nil {
			failMemoryCmd("initialize encryptor", err)
		}
	}

	backend, err := rhizome.OpenMemoryBackend(ctx, config, encryptor)
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

func printMemoryTable(memories []rhizome.Memory) {
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "CREATED\tCATEGORY\tTITLE\tTAGS")
	for _, memory := range memories {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", memory.CreatedAt.Format("2006-01-02"), memory.Category, memory.Title, memory.Tags)
	}
	_ = writer.Flush()
}

func currentRepositoryName() string {
	wd, err := os.Getwd()
	if err != nil {
		return "workspace"
	}
	name := filepath.Base(wd)
	if name == "." || name == "" {
		return "workspace"
	}
	return name
}

func getOrCreateMemoryKey(keyPath string) ([]byte, error) {
	if envKey := os.Getenv("OPEN_TENDRIL_INDEX_KEY"); envKey != "" {
		key := make([]byte, 32)
		copy(key, []byte(envKey))
		return key, nil
	}
	if content, err := os.ReadFile(keyPath); err == nil && len(content) == 32 {
		return content, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate random key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("save generated key: %w", err)
	}
	return key, nil
}

func failMemoryCmd(action string, err error) {
	fmt.Fprintf(os.Stderr, "Failed to %s: %v\n", action, err)
	os.Exit(1)
}

func printMemoryUsage() {
	fmt.Println("Usage: tendril memory <command> [arguments]")
	fmt.Println("  list [--category=X]       List memories")
	fmt.Println("  search <query>            Search memories")
	fmt.Println("  add --title=X             Store a memory")
	fmt.Println("  remove <title>            Remove a memory")
	fmt.Println("  export [--json]           Export memories")
}

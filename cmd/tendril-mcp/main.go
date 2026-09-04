// tendril-mcp is a Pollinator-side stdio MCP bridge. It forwards frames to a
// separately owned governed Stem and never constructs one.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/internal/buildinfo"
	"github.com/opentendril/opentendril/internal/mcpclient"
	"github.com/opentendril/opentendril/internal/pollinatorconfig"
)

type bridgeOptions struct {
	connection string
	explicit   bool
}

func main() {
	if err := runCommand(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run keeps the small in-package bridge seam used by tests. Command-line
// dispatch itself lives in runCommand so --version can be tested without
// allowing configuration or network initialization first.
func run(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
	return runCommand(ctx, nil, in, out, errOut)
}

func runCommand(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 1 && args[0] == "--version" {
		_, err := fmt.Fprintf(out, "tendril-mcp %s\n", buildinfo.Version)
		return err
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printUsage(out)
		return nil
	}
	if len(args) > 0 && args[0] == "connection" {
		return runConnectionCommand(args[1:], out)
	}
	if len(args) > 0 && args[0] == "diagnose" {
		options, err := parseSelector(args[1:])
		if err != nil {
			return err
		}
		return runDiagnose(ctx, options, out)
	}
	options, err := parseSelector(args)
	if err != nil {
		return err
	}
	return runBridge(ctx, options, in, out, errOut)
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  tendril-mcp [--connection <name> | -c <name>]")
	fmt.Fprintln(out, "  tendril-mcp connection list")
	fmt.Fprintln(out, "  tendril-mcp connection show <name>")
	fmt.Fprintln(out, "  tendril-mcp connection set <name> --endpoint <url> --credential <credential>")
	fmt.Fprintln(out, "  tendril-mcp connection use <name>")
	fmt.Fprintln(out, "  tendril-mcp connection remove <name>")
	fmt.Fprintln(out, "  tendril-mcp diagnose [--connection <name> | -c <name>]")
	fmt.Fprintln(out, "  tendril-mcp --version")
}

func parseSelector(args []string) (bridgeOptions, error) {
	var options bridgeOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--connection" || arg == "-c":
			if options.explicit {
				return bridgeOptions{}, errors.New("connection selector may be specified only once")
			}
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return bridgeOptions{}, fmt.Errorf("%s requires a connection name", arg)
			}
			options.connection = strings.TrimSpace(args[i+1])
			options.explicit = true
			i++
		case strings.HasPrefix(arg, "--connection="):
			if options.explicit {
				return bridgeOptions{}, errors.New("connection selector may be specified only once")
			}
			options.connection = strings.TrimSpace(strings.TrimPrefix(arg, "--connection="))
			if options.connection == "" {
				return bridgeOptions{}, errors.New("--connection requires a connection name")
			}
			options.explicit = true
		case strings.HasPrefix(arg, "-c="):
			if options.explicit {
				return bridgeOptions{}, errors.New("connection selector may be specified only once")
			}
			options.connection = strings.TrimSpace(strings.TrimPrefix(arg, "-c="))
			if options.connection == "" {
				return bridgeOptions{}, errors.New("-c requires a connection name")
			}
			options.explicit = true
		default:
			return bridgeOptions{}, fmt.Errorf("unknown argument %q; try --help", arg)
		}
	}
	return options, nil
}

func loadSelectedConnection(options bridgeOptions) (pollinatorconfig.Selection, error) {
	configPath := pollinatorconfig.ConfigFile()
	cfg, err := pollinatorconfig.Load()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return pollinatorconfig.Selection{}, fmt.Errorf("no Pollinator connection config at %s; run 'tendril-mcp connection set <name> --endpoint <url> --credential <credential>'", configPath)
		}
		return pollinatorconfig.Selection{}, err
	}
	selection, err := cfg.Select(options.connection)
	if err != nil {
		return pollinatorconfig.Selection{}, err
	}
	return selection, nil
}

func resolveSelectedCredential(selection pollinatorconfig.Selection) (string, error) {
	credentialPath, err := pollinatorconfig.ResolveCredentialReference(selection.Connection.Credential)
	if err != nil {
		return "", err
	}
	return credentialPath, nil
}

func runBridge(ctx context.Context, options bridgeOptions, in io.Reader, out, errOut io.Writer) error {
	selection, err := loadSelectedConnection(options)
	if err != nil {
		return err
	}
	if err := mcpclient.ValidateLocalGovernedEndpoint(selection.Connection.Endpoint); err != nil {
		return fmt.Errorf("connection %q: %w", selection.Name, err)
	}
	credentialPath, err := resolveSelectedCredential(selection)
	if err != nil {
		return err
	}
	root, err := mcpclient.LoadCredentialFile(credentialPath)
	if err != nil {
		return fmt.Errorf("connection %q credential: %w", selection.Name, err)
	}
	probe := mcpclient.ProbeOwnerAt(ctx, selection.Connection.Endpoint)
	if !probe.Reached {
		return fmt.Errorf("no Stem is answering at %s", selection.Connection.Endpoint)
	}
	if probe.Owner == nil {
		return fmt.Errorf("Stem at %s answered but ownership was not established", selection.Connection.Endpoint)
	}
	if *probe.Owner == os.Getuid() {
		return fmt.Errorf("this executable is only for a separately owned governed Stem; the Stem at %s reports this process's uid", selection.Connection.Endpoint)
	}

	forwarder := mcpclient.NewForwarderAt(selection.Connection.Endpoint, root)
	if err := forwarder.Preflight(); err != nil {
		return err
	}
	fmt.Fprintf(errOut, "forwarding connection %q to the governed Stem at %s; authorization stays there\n", selection.Name, selection.Connection.Endpoint)
	return forwardFrames(in, out, forwarder)
}

func forwardFrames(in io.Reader, out io.Writer, forwarder *mcpclient.Forwarder) error {
	scanner := bufio.NewScanner(in)
	const maxCapacity = 1024 * 1024 * 5 // 5MB matches the Stem MCP stdio scanner
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		reqBytes := scanner.Bytes()
		if len(reqBytes) == 0 {
			continue
		}
		respBytes := forwarder.Forward(reqBytes)
		if len(respBytes) > 0 {
			if _, err := fmt.Fprintln(out, string(respBytes)); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func runDiagnose(ctx context.Context, options bridgeOptions, out io.Writer) error {
	configPath := pollinatorconfig.ConfigFile()
	fmt.Fprintf(out, "tendril-mcp version: %s\n", buildinfo.Version)
	fmt.Fprintf(out, "config file: %s\n", configPath)

	selection, err := loadSelectedConnection(options)
	if err != nil {
		fmt.Fprintf(out, "selected connection: unavailable\n")
		fmt.Fprintf(out, "selection source: %s\n", selectionSource(options))
		fmt.Fprintf(out, "preflight: refused (%v)\n", err)
		return errors.New("diagnose preflight failed")
	}
	fmt.Fprintf(out, "selected connection: %s\n", selection.Name)
	fmt.Fprintf(out, "selection source: %s\n", selection.Source)
	fmt.Fprintf(out, "endpoint: %s\n", selection.Connection.Endpoint)
	fmt.Fprintf(out, "credential reference: %s\n", selection.Connection.Credential)
	if err := mcpclient.ValidateLocalGovernedEndpoint(selection.Connection.Endpoint); err != nil {
		fmt.Fprintf(out, "transport posture: unsupported (%v)\n", err)
		fmt.Fprintln(out, "authentication: refused (transport posture is not supported by the current local-governed path)")
		return errors.New("diagnose preflight failed")
	}
	fmt.Fprintln(out, "transport posture: accepted")
	credentialPath, err := resolveSelectedCredential(selection)
	if err != nil {
		fmt.Fprintf(out, "preflight: refused (%v)\n", err)
		return errors.New("diagnose preflight failed")
	}
	fmt.Fprintf(out, "credential path: %s\n", credentialPath)

	root, credentialErr := mcpclient.LoadCredentialFile(credentialPath)
	if credentialErr != nil {
		fmt.Fprintf(out, "credential permission/readability: refused (%v)\n", credentialErr)
	} else {
		fmt.Fprintln(out, "credential permission/readability: accepted")
	}

	probe := mcpclient.ProbeOwnerAt(ctx, selection.Connection.Endpoint)
	if !probe.Reached {
		fmt.Fprintln(out, "Stem reachable: no")
		fmt.Fprintln(out, "reported Stem owner: unavailable")
		fmt.Fprintln(out, "same-principal refusal: not applicable")
		fmt.Fprintln(out, "authentication: refused (Stem unreachable)")
		return errors.New("diagnose preflight failed")
	}
	fmt.Fprintln(out, "Stem reachable: yes")
	if probe.Owner == nil {
		fmt.Fprintln(out, "reported Stem owner: unavailable")
		fmt.Fprintln(out, "same-principal refusal: not applicable")
		fmt.Fprintln(out, "authentication: refused (Stem ownership not established)")
		return errors.New("diagnose preflight failed")
	}
	fmt.Fprintf(out, "reported Stem owner: uid %d\n", *probe.Owner)
	if *probe.Owner == os.Getuid() {
		fmt.Fprintln(out, "same-principal refusal: yes")
		fmt.Fprintln(out, "authentication: refused (Stem has this process's uid)")
		return errors.New("diagnose preflight failed")
	}
	fmt.Fprintln(out, "same-principal refusal: no")
	if credentialErr != nil {
		fmt.Fprintln(out, "authentication: refused (credential unavailable)")
		return errors.New("diagnose preflight failed")
	}
	forwarder := mcpclient.NewForwarderAt(selection.Connection.Endpoint, root)
	if err := forwarder.Preflight(); err != nil {
		fmt.Fprintf(out, "authentication: refused (%v)\n", err)
		return errors.New("diagnose preflight failed")
	}
	fmt.Fprintln(out, "authentication: accepted")
	return nil
}

func selectionSource(options bridgeOptions) string {
	if options.explicit {
		return string(pollinatorconfig.SelectionExplicit)
	}
	return string(pollinatorconfig.SelectionDefault)
}

func runConnectionCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("connection requires list, show, set, use, or remove")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("connection list takes no arguments")
		}
		return listConnections(out)
	case "show":
		if len(args) != 2 {
			return errors.New("usage: tendril-mcp connection show <name>")
		}
		return showConnection(args[1], out)
	case "set":
		return setConnection(args[1:], out)
	case "use":
		if len(args) != 2 {
			return errors.New("usage: tendril-mcp connection use <name>")
		}
		return useConnection(args[1], out)
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: tendril-mcp connection remove <name>")
		}
		return removeConnection(args[1], out)
	default:
		return fmt.Errorf("unknown connection command %q", args[0])
	}
}

func readConfigForMutation() (pollinatorconfig.Config, error) {
	cfg, err := pollinatorconfig.Load()
	if errors.Is(err, os.ErrNotExist) {
		return pollinatorconfig.Config{Version: 1, Connections: map[string]pollinatorconfig.Connection{}}, nil
	}
	return cfg, err
}

func listConnections(out io.Writer) error {
	cfg, err := pollinatorconfig.Load()
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "No Pollinator connections configured.")
		return nil
	}
	if err != nil {
		return err
	}
	if len(cfg.Connections) == 0 {
		fmt.Fprintln(out, "No Pollinator connections configured.")
		return nil
	}
	names := make([]string, 0, len(cfg.Connections))
	for name := range cfg.Connections {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == cfg.Default {
			fmt.Fprintf(out, "* %s (default)\n", name)
		} else {
			fmt.Fprintf(out, "  %s\n", name)
		}
	}
	return nil
}

func showConnection(name string, out io.Writer) error {
	cfg, err := pollinatorconfig.Load()
	if err != nil {
		return err
	}
	if err := pollinatorconfig.ValidateName(name); err != nil {
		return err
	}
	connection, ok := cfg.Connections[name]
	if !ok {
		return fmt.Errorf("connection %q does not exist", name)
	}
	path, err := pollinatorconfig.ResolveCredentialReference(connection.Credential)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "name: %s\n", name)
	fmt.Fprintf(out, "endpoint: %s\n", connection.Endpoint)
	fmt.Fprintf(out, "credential reference: %s\n", connection.Credential)
	fmt.Fprintf(out, "resolved credential path: %s\n", path)
	return nil
}

func setConnection(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: tendril-mcp connection set <name> --endpoint <url> --credential <credential>")
	}
	name := args[0]
	if err := pollinatorconfig.ValidateName(name); err != nil {
		return fmt.Errorf("invalid connection name: %w", err)
	}
	var endpoint, credential string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--endpoint":
			if i+1 >= len(args) {
				return errors.New("--endpoint requires a URL")
			}
			endpoint = args[i+1]
			i++
		case "--credential":
			if i+1 >= len(args) {
				return errors.New("--credential requires a reference name")
			}
			credential = args[i+1]
			i++
		default:
			return fmt.Errorf("unknown connection set argument %q", args[i])
		}
	}
	normalizedEndpoint, err := pollinatorconfig.NormalizeEndpoint(endpoint)
	if err != nil {
		return err
	}
	if err := pollinatorconfig.ValidateCredentialReference(credential); err != nil {
		return err
	}
	cfg, err := readConfigForMutation()
	if err != nil {
		return err
	}
	cfg.Connections[name] = pollinatorconfig.Connection{Endpoint: normalizedEndpoint, Credential: credential}
	if err := pollinatorconfig.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "Connection %q saved.\n", name)
	return nil
}

func useConnection(name string, out io.Writer) error {
	if err := pollinatorconfig.ValidateName(name); err != nil {
		return fmt.Errorf("invalid connection name: %w", err)
	}
	cfg, err := pollinatorconfig.Load()
	if err != nil {
		return err
	}
	if _, ok := cfg.Connections[name]; !ok {
		return fmt.Errorf("connection %q does not exist", name)
	}
	cfg.Default = name
	if err := pollinatorconfig.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "Default connection set to %q.\n", name)
	return nil
}

func removeConnection(name string, out io.Writer) error {
	if err := pollinatorconfig.ValidateName(name); err != nil {
		return fmt.Errorf("invalid connection name: %w", err)
	}
	cfg, err := pollinatorconfig.Load()
	if err != nil {
		return err
	}
	if name == cfg.Default {
		return fmt.Errorf("refusing to remove default connection %q; choose another default with 'tendril-mcp connection use <name>' first", name)
	}
	if _, ok := cfg.Connections[name]; !ok {
		return fmt.Errorf("connection %q does not exist", name)
	}
	delete(cfg.Connections, name)
	if err := pollinatorconfig.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(out, "Connection %q removed.\n", name)
	return nil
}

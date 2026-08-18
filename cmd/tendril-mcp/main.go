// tendril-mcp is a Pollinator-side stdio MCP bridge. It forwards frames to a
// separately owned governed Stem and never constructs one.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/opentendril/opentendril/internal/mcpclient"
)

func main() {
	if err := run(context.Background(), os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run loads a durable Pollinator root, refuses to proceed unless a separately
// owned governed Stem accepts it, then forwards stdio MCP frames to that Stem.
func run(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
	root, err := mcpclient.LoadCredential()
	if err != nil {
		return err
	}
	if root == "" {
		return fmt.Errorf("no Pollinator credential configured: set %s or %s to a 0600 file, or set TENDRIL_POLLEN to select ~/.config/tendril/pollinators/<pollen>. The operator can issue a credential with:\n\n    sudo -u tendril -i tendril pollinator issue --pollen <name> --note %q",
			mcpclient.EnvPollinatorCredential, mcpclient.EnvMCPCredential, "<where>")
	}

	probe := mcpclient.ProbeOwner(ctx)
	if !probe.Reached {
		return fmt.Errorf("no Stem is answering at %s", probe.Address)
	}
	if probe.Owner == nil {
		return fmt.Errorf("Stem at %s answered but ownership was not established", probe.Address)
	}
	if *probe.Owner == os.Getuid() {
		return fmt.Errorf("this executable is only for a separately owned governed Stem; the Stem at %s reports this process's uid", probe.Address)
	}

	forwarder := mcpclient.NewForwarder(root)
	if err := forwarder.Preflight(); err != nil {
		return err
	}
	fmt.Fprintf(errOut, "forwarding to the governed Stem at %s; authorization stays there\n", probe.Address)

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
			fmt.Fprintln(out, string(respBytes))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

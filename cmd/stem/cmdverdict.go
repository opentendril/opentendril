package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
)

// runVerdictCmd is the CLI adapter for the shared skip-aware test judgement.
// It exists so the remote tier (GitHub Actions) and the local sealed verifier
// judge a `go test -json` run through the SAME code
// (conductor.ReportGoTestRun) instead of each tier reimplementing what
// "green" means in YAML or shell — two implementations would drift.
//
// No LLM, no daemon, no terrarium: the command reads a completed run's event
// stream plus its exit code and renders the verdict. It never executes tests
// itself, so the caller keeps full control over how the run is invoked.
func runVerdictCmd(args []string) {
	if len(args) == 0 {
		printVerdictUsage()
		os.Exit(1)
	}

	switch strings.ToLower(args[0]) {
	case "go-test":
		os.Exit(runVerdictGoTestCmd(args[1:]))
	case "-h", "--help", "help":
		printVerdictUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown verdict command: %s\n", args[0])
		printVerdictUsage()
		os.Exit(1)
	}
}

// runVerdictGoTestCmd parses the go-test subcommand's arguments, reads the
// event stream (a positional file path, or stdin when none is given), and
// returns the process exit code for main to raise.
func runVerdictGoTestCmd(args []string) int {
	exitCodeArg := ""
	stderrPath := ""
	streamPath := ""
	for i := 0; i < len(args); i++ {
		// Both --flag value and --flag=value are accepted: the inline form is
		// what shell callers reach for, and rejecting it fails a caller that
		// spelled its intent perfectly well.
		name, inlineValue, hasInline := strings.Cut(args[i], "=")

		// takeValue returns the flag's value from the inline form, or the next
		// argument when it was written separately.
		takeValue := func(flag string) (string, bool) {
			if hasInline {
				if inlineValue == "" {
					fmt.Fprintf(os.Stderr, "❌ flag %s requires a value\n", flag)
					return "", false
				}
				return inlineValue, true
			}
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "❌ flag %s requires a value\n", flag)
				return "", false
			}
			i++
			return args[i], true
		}

		switch name {
		case "--exit-code":
			value, ok := takeValue("--exit-code")
			if !ok {
				return 1
			}
			exitCodeArg = value
		case "--stderr-file":
			value, ok := takeValue("--stderr-file")
			if !ok {
				return 1
			}
			stderrPath = value
		case "-h", "--help", "help":
			printVerdictUsage()
			return 0
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "❌ unknown argument %q for verdict go-test\n", args[i])
				return 1
			}
			if streamPath != "" {
				fmt.Fprintf(os.Stderr, "❌ unexpected extra argument %q: verdict go-test reads one stream\n", args[i])
				return 1
			}
			streamPath = args[i]
		}
	}

	// The exit code is mandatory, never defaulted: assuming 0 when the caller
	// forgot to pass it would wave compile errors through — exactly the
	// fail-open judgement this command exists to prevent.
	if exitCodeArg == "" {
		fmt.Fprintln(os.Stderr, "❌ Missing required flag --exit-code (the `go test` process exit code)")
		printVerdictUsage()
		return 1
	}
	exitCode, err := strconv.Atoi(exitCodeArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ --exit-code must be an integer, got %q\n", exitCodeArg)
		return 1
	}

	var stream []byte
	if streamPath == "" {
		stream, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to read the event stream from stdin: %v\n", err)
			return 1
		}
	} else {
		stream, err = os.ReadFile(streamPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to read the event stream: %v\n", err)
			return 1
		}
	}

	capturedStderr := ""
	if stderrPath != "" {
		content, err := os.ReadFile(stderrPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Failed to read the captured stderr: %v\n", err)
			return 1
		}
		capturedStderr = string(content)
	}

	return renderGoTestVerdict(os.Stdout, os.Stderr, exitCode, string(stream), capturedStderr)
}

// renderGoTestVerdict applies the shared judgement and renders the report plus
// a one-line verdict, mirroring scoped-ci's GREEN/FAILED/BLOCKED vocabulary.
// Exit codes: 0 green, 1 failed, 2 blocked — both non-green codes stop a
// caller that only checks for non-zero, while a caller that cares can tell an
// unverified run from a red one.
func renderGoTestVerdict(out, errOut io.Writer, exitCode int, stream, capturedStderr string) int {
	report, err := conductor.ReportGoTestRun("go test -json", exitCode, stream, capturedStderr)
	fmt.Fprintln(out, report)
	switch {
	case err == nil:
		fmt.Fprintln(out, "✅ go-test verdict: GREEN — every applicable test ran and passed")
		return 0
	case errors.Is(err, conductor.ErrVerifierBlocked):
		fmt.Fprintf(errOut, "⛔ go-test verdict: BLOCKED — the run is NOT verified: %v\n", err)
		return 2
	default:
		fmt.Fprintf(errOut, "❌ go-test verdict: FAILED — %v\n", err)
		return 1
	}
}

func printVerdictUsage() {
	fmt.Println("Usage: tendril verdict go-test --exit-code <n> [--stderr-file <path>] [stream-file]")
	fmt.Println("  Judge a completed `go test -json` run with the same skip-aware verdict")
	fmt.Println("  the sealed local verifier applies: a skipped test is BLOCKED, never green.")
	fmt.Println("  The event stream is read from stream-file, or from stdin when omitted.")
	fmt.Println("    --exit-code    The `go test` process exit code (required: a non-zero")
	fmt.Println("                   exit with no fail event, e.g. a compile error, must fail)")
	fmt.Println("    --stderr-file  File holding the run's captured stderr, included in the report")
	fmt.Println("  Exit status: 0 green, 1 failed, 2 blocked (skipped tests left unverified).")
}

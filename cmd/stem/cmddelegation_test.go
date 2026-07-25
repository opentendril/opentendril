package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestDelegationCLIArguments executes the compiled stem binary in a subprocess to assert
// os.Exit(1) and stderr usage prints on missing arguments or unknown subcommands, matching
// the style used by other CLI component tests in this codebase.
func TestDelegationCLIArguments(t *testing.T) {
	if os.Getenv("TEST_DELEGATION_CLI") == "1" {
		argsStr := os.Getenv("TEST_DELEGATION_ARGS")
		var args []string
		if argsStr != "" {
			args = strings.Split(argsStr, " ")
		}
		runDelegationCmd(context.Background(), args)
		os.Exit(0)
	}

	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "no subcommand",
			args:       []string{},
			wantExit:   0,
			wantStderr: "", // Usage printed to stdout by printDelegationUsage
		},
		{
			name:       "unknown subcommand",
			args:       []string{"wut"},
			wantExit:   1,
			wantStderr: "Unknown delegation command: wut\n",
		},
		{
			name:       "approve missing id",
			args:       []string{"approve"},
			wantExit:   1,
			wantStderr: "missing pending confirmation id. Usage: tendril delegation approve <id>\n",
		},
		{
			name:       "approve empty id",
			args:       []string{"approve", "   "},
			wantExit:   1,
			wantStderr: "missing pending confirmation id. Usage: tendril delegation approve <id>\n",
		},
		{
			name:       "deny missing id",
			args:       []string{"deny"},
			wantExit:   1,
			wantStderr: "missing pending confirmation id. Usage: tendril delegation deny <id>\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestDelegationCLIArguments")
			cmd.Env = append(os.Environ(), "TEST_DELEGATION_CLI=1", "TEST_DELEGATION_ARGS="+strings.Join(tt.args, " "))
			output, err := cmd.CombinedOutput()

			exitCode := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				} else {
					t.Fatalf("unexpected error running command: %v", err)
				}
			}

			if exitCode != tt.wantExit {
				t.Errorf("exit code = %d, want %d (output: %s)", exitCode, tt.wantExit, string(output))
			}

			if tt.wantStderr != "" && !strings.Contains(string(output), tt.wantStderr) {
				t.Errorf("output = %q, want it to contain %q", string(output), tt.wantStderr)
			}
		})
	}
}

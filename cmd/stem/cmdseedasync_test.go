package main

import (
	"reflect"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestExtractSeedAsyncFlag(t *testing.T) {
	cases := []struct {
		name      string
		in        []string
		wantArgs  []string
		wantAsync bool
	}{
		{
			name:      "no flag",
			in:        []string{"--substrate", ".", "--goal", "g", "--", "go", "test"},
			wantArgs:  []string{"--substrate", ".", "--goal", "g", "--", "go", "test"},
			wantAsync: false,
		},
		{
			name:      "flag before the separator is consumed",
			in:        []string{"--substrate", ".", "--async", "--goal", "g", "--", "go", "test"},
			wantArgs:  []string{"--substrate", ".", "--goal", "g", "--", "go", "test"},
			wantAsync: true,
		},
		{
			name:      "a literal --async in the verify command is kept",
			in:        []string{"--async", "--", "mytool", "--async"},
			wantArgs:  []string{"--", "mytool", "--async"},
			wantAsync: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, async := extractSeedAsyncFlag(tc.in)
			if async != tc.wantAsync {
				t.Fatalf("async = %v, want %v", async, tc.wantAsync)
			}
			if !reflect.DeepEqual(got, tc.wantArgs) {
				t.Fatalf("args = %v, want %v", got, tc.wantArgs)
			}
		})
	}
}

func TestParseSeedArgsPreservesIdempotencyKey(t *testing.T) {
	command, ok := lookupSeedCommand("grow")
	if !ok {
		t.Fatal("seed grow command is not registered")
	}
	input, err := parseSeedArgs(command.capability, []string{
		"--substrate", "core", "--goal", "fix", "--idempotency-key", "caller-key", "--", "go", "test",
	})
	if err != nil {
		t.Fatalf("parseSeedArgs: %v", err)
	}
	if input["idempotencyKey"] != "caller-key" {
		t.Fatalf("idempotencyKey = %#v, want exact caller key", input["idempotencyKey"])
	}
}

func TestParseSeedArgsAllowsOmittedVerification(t *testing.T) {
	command, _ := lookupSeedCommand("grow")
	input, err := parseSeedArgs(command.capability, []string{"--substrate", "core", "--goal", "review candidate"})
	if err != nil {
		t.Fatalf("parseSeedArgs: %v", err)
	}
	if _, present := input["verify"]; present {
		t.Fatalf("parser synthesized verifier: %#v", input["verify"])
	}
}

func TestSeedCommandResultUsesExecutionAndVerificationFacts(t *testing.T) {
	cases := []struct {
		execution string
		verify    string
		want      bool
	}{
		{core.SeedExecutionOutcomeCompleted, core.SeedVerificationOutcomeNotRequested, true},
		{core.SeedExecutionOutcomeCompleted, core.SeedVerificationOutcomePassed, true},
		{core.SeedExecutionOutcomeCompleted, core.SeedVerificationOutcomePredicateFailed, false},
		{core.SeedExecutionOutcomeCompleted, core.SeedVerificationOutcomeConfigurationInvalid, false},
		{core.SeedExecutionOutcomeInfrastructureFailed, core.SeedVerificationOutcomeNotRequested, false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := seedCommandResultAcceptable(tc.execution, tc.verify); got != tc.want {
			t.Errorf("acceptable(%q, %q) = %v, want %v", tc.execution, tc.verify, got, tc.want)
		}
	}
}

func TestEnsureSeedOpenIdempotencyKeyGeneratesOrPreserves(t *testing.T) {
	previous := seedOpenIdempotencyKeySource
	seedOpenIdempotencyKeySource = func() (string, error) { return "generated-key", nil }
	t.Cleanup(func() { seedOpenIdempotencyKeySource = previous })

	input := map[string]any{}
	key, err := ensureSeedOpenIdempotencyKey(input)
	if err != nil || key != "generated-key" || input["idempotencyKey"] != "generated-key" {
		t.Fatalf("generated identity = %q input=%#v err=%v", key, input, err)
	}
	input = map[string]any{"idempotencyKey": "  supplied-key  "}
	key, err = ensureSeedOpenIdempotencyKey(input)
	if err != nil || key != "  supplied-key  " || input["idempotencyKey"] != "  supplied-key  " {
		t.Fatalf("supplied identity changed: key=%q input=%#v err=%v", key, input, err)
	}
}

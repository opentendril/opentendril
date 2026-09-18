package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
)

func TestParseGitBootstrapArgs(t *testing.T) {
	defaults, err := parseGitBootstrapArgs([]string{"--substrate", "garden"})
	if err != nil {
		t.Fatalf("parseGitBootstrapArgs defaults: %v", err)
	}
	if defaults.dir != "" {
		t.Fatalf("default dir = %q, want empty canonical-registry selector", defaults.dir)
	}

	opts, err := parseGitBootstrapArgs([]string{"--substrate", "garden", "--branch", "trunk", "--confirm", "--dir", "/config"})
	if err != nil {
		t.Fatalf("parseGitBootstrapArgs: %v", err)
	}
	if opts.substrate != "garden" || opts.branch != "trunk" || !opts.confirm || opts.dir != "/config" {
		t.Fatalf("options = %+v, want parsed bootstrap options", opts)
	}

	for name, args := range map[string][]string{
		"missing substrate":  {"--confirm"},
		"missing flag value": {"--substrate"},
		"unknown flag":       {"--substrate", "garden", "--force"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGitBootstrapArgs(args); err == nil {
				t.Fatal("expected argument error")
			}
		})
	}
}

func TestExecuteGitBootstrapRefusesDeclaredPollenBeforeConfigOrRemote(t *testing.T) {
	t.Setenv("TENDRIL_POLLEN", "worker")
	if err := executeGitBootstrap(context.Background(), gitBootstrapOptions{substrate: "garden", dir: "/does/not/exist", confirm: true}); err == nil || !strings.Contains(err.Error(), "Botanist-only") {
		t.Fatalf("error = %v, want Botanist-only refusal", err)
	}
}

func TestExecuteGitBootstrapOmittedDirReadsCanonicalRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	fixtureDir := writeAppVerifyFixture(t, "772211", "acme/widget", genSetupKeyPEM(t))
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "substrates.yaml"))
	if err != nil {
		t.Fatalf("read bootstrap fixture: %v", err)
	}
	canonical := filepath.Join(home, ".tendril", "substrates.yaml")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatalf("mkdir canonical registry: %v", err)
	}
	if err := os.WriteFile(canonical, raw, 0o644); err != nil {
		t.Fatalf("write canonical registry: %v", err)
	}
	t.Chdir(t.TempDir())
	setGitSetupStdin(t, "")

	var calls []gitHubCall
	startSetupVerifyServer(t, setupVerifyFakeOpts{
		appStatus: http.StatusOK, installStatus: http.StatusOK, repoStatus: http.StatusOK,
		emptyRepo: true, defaultBranch: "trunk", installToken: "ghs_BOOTSTRAP_CANONICAL_TOKEN",
	}, &calls)
	parsed, err := parseGitBootstrapArgs([]string{"--substrate", "garden"})
	if err != nil {
		t.Fatalf("parse canonical bootstrap args: %v", err)
	}
	stdout, stderr := captureVerifyOutput(t, func() {
		err := executeGitBootstrap(context.Background(), parsed)
		if err == nil || !strings.Contains(err.Error(), "declined") {
			t.Errorf("error = %v, want confirmation refusal after canonical load", err)
		}
	})
	if !strings.Contains(stdout, "acme/widget") || strings.Contains(stderr, "not found") {
		t.Fatalf("bootstrap output = %q%q, want canonical substrate resolution", stdout, stderr)
	}
}

func TestExecuteGitBootstrapDeclineDoesNotMutateRepository(t *testing.T) {
	dir := writeAppVerifyFixture(t, "772211", "acme/widget", genSetupKeyPEM(t))
	var calls []gitHubCall
	startSetupVerifyServer(t, setupVerifyFakeOpts{
		appStatus: http.StatusOK, installStatus: http.StatusOK, repoStatus: http.StatusOK,
		emptyRepo: true, defaultBranch: "trunk", installToken: "ghs_BOOTSTRAP_DECLINE_TOKEN",
	}, &calls)

	stdout, stderr := captureVerifyOutput(t, func() {
		err := executeGitBootstrap(context.Background(), gitBootstrapOptions{substrate: "garden", dir: dir})
		if err == nil || !strings.Contains(err.Error(), "declined") {
			t.Errorf("error = %v, want confirmation refusal", err)
		}
	})
	if !strings.Contains(stdout+stderr, "Explicit Botanist confirmation") && !strings.Contains(stdout+stderr, "Create this empty root commit?") {
		t.Fatalf("output = %q, want confirmation guidance", stdout+stderr)
	}
	for _, call := range calls {
		if call.Method == http.MethodGet {
			continue
		}
		if call.Method == http.MethodPost && strings.Contains(call.Path, "access_tokens") {
			continue
		}
		t.Fatalf("bootstrap decline made mutating GitHub request: %+v", call)
	}
}

func TestPrintGitBootstrapPlanIdentifiesSetupStateNotFruit(t *testing.T) {
	stdout, _ := captureVerifyOutput(t, func() {
		printGitBootstrapPlan(conductorBootstrapPlanForCLI("main"))
	})
	for _, want := range []string{"acme/widget", "main", "empty Git tree", "not Fruit"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want %q", stdout, want)
		}
	}
}

func conductorBootstrapPlanForCLI(branch string) conductor.GitBootstrapPlan {
	return conductor.GitBootstrapPlan{Owner: "acme", Repo: "widget", Branch: branch}
}

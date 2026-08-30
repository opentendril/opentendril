package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
)

func TestParseGitBootstrapArgs(t *testing.T) {
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

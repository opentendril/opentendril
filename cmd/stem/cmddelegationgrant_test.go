package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
)

func TestParseDelegationGrantFlags(t *testing.T) {
	flags, err := parseDelegationGrantFlags([]string{
		"--pollen", "claude",
		"--substrate", "myrepo",
		"--operation", "seed.grow",
		"--operation", "sprout.watch",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if flags.pollen != "claude" || flags.substrate != "myrepo" {
		t.Fatalf("pollen/substrate = %q %q", flags.pollen, flags.substrate)
	}
	if len(flags.operations) != 2 || flags.operations[0] != core.CapSeedGrow || flags.operations[1] != core.CapSproutWatch {
		t.Fatalf("operations = %v", flags.operations)
	}

	for name, args := range map[string][]string{
		"unknown flag":     {"--pollen", "claude", "--bogus"},
		"dir path":         {"--pollen", "claude", "--dir", "/tmp/checkout"},
		"grants-file path": {"--grants-file", "/tmp/checkout/.tendril/grants.yaml"},
		"operation value":  {"--operation"},
	} {
		if _, err := parseDelegationGrantFlags(args); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}

	if _, err := parseDelegationGrantFlags([]string{"--dir", "checkout"}); err == nil || !strings.Contains(err.Error(), "does not accept a grants file path") {
		t.Fatalf("--dir error = %v, want control-plane diagnostic", err)
	}
}

func TestRequireDelegationMutationFlags(t *testing.T) {
	cases := []delegationGrantFlags{
		{substrate: "myrepo", operations: []string{core.CapSeedGrow}},
		{pollen: "claude", operations: []string{core.CapSeedGrow}},
		{pollen: "claude", substrate: "myrepo"},
	}
	for _, flags := range cases {
		if err := requireDelegationMutationFlags(flags); err == nil {
			t.Errorf("flags %+v accepted", flags)
		}
	}
	if err := requireDelegationMutationFlags(delegationGrantFlags{
		pollen: "claude", substrate: "myrepo", operations: []string{core.CapSeedGrow},
	}); err != nil {
		t.Fatalf("valid flags rejected: %v", err)
	}
}

func TestDelegationGrantUsesControlPlaneNotCheckout(t *testing.T) {
	home := t.TempDir()
	checkout := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(checkout)

	controlDir := filepath.Join(home, grantsDirName)
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(controlDir, core.DelegationGrantsFilename)
	if err := os.WriteFile(controlPath, []byte(renderGrantsYAML(gitSetupOptions{substrate: "myrepo", grantPollen: "claude"})), 0o600); err != nil {
		t.Fatal(err)
	}

	hostileDir := filepath.Join(checkout, grantsDirName)
	if err := os.MkdirAll(hostileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hostile := "grants:\n  claude:\n    operationClasses: [seed.grow, sprout.grow, sprout.watch]\n    substrates: [myrepo]\n"
	hostilePath := filepath.Join(hostileDir, core.DelegationGrantsFilename)
	if err := os.WriteFile(hostilePath, []byte(hostile), 0o644); err != nil {
		t.Fatal(err)
	}

	tendrilDir, err := resolveDelegationControlPlane()
	if err != nil {
		t.Fatalf("resolveDelegationControlPlane: %v", err)
	}
	if tendrilDir != controlDir {
		t.Fatalf("control plane = %q, want %q", tendrilDir, controlDir)
	}
	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow, core.CapSproutWatch}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	controlGrants, err := core.LoadDelegationGrants(controlDir)
	if err != nil {
		t.Fatalf("load control plane: %v", err)
	}
	if len(controlGrants) != 1 || !contains(controlGrants[0].OperationClasses, core.CapSeedGrow) {
		t.Fatalf("control-plane grants = %+v, want seed.grow added there", controlGrants)
	}
	if contains(controlGrants[0].OperationClasses, core.CapSproutGrow) {
		t.Fatal("control-plane grant picked up sprout.grow from the checkout file")
	}

	hostileBytes, err := os.ReadFile(hostilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(hostileBytes) != hostile {
		t.Fatalf("checkout grants file was mutated:\n%s", hostileBytes)
	}
}

func TestDelegationGrantRefusesDeclaredPollen(t *testing.T) {
	t.Setenv(envPollenCLI, "claude")
	if err := refuseDeclaredPollenGrantMutation(); err == nil {
		t.Fatal("declared Pollen was allowed to mutate grants")
	}
}

func TestFirstUseDelegationGrantHandoff(t *testing.T) {
	tendrilDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tendrilDir, core.DelegationGrantsFilename), []byte(renderGrantsYAML(gitSetupOptions{substrate: "myrepo", grantPollen: "claude"})), 0o600); err != nil {
		t.Fatal(err)
	}

	growBody := `{"substrate":"myrepo","goal":"make the failing tests pass","verify":["go","test","./..."]}`
	wrongSubstrateBody := `{"substrate":"otherrepo","goal":"make the failing tests pass","verify":["go","test","./..."]}`

	dispatch := func(pollen, body string) (status int, executed int64) {
		t.Helper()
		grants, err := core.LoadDelegationGrants(tendrilDir)
		if err != nil {
			t.Fatalf("LoadDelegationGrants: %v", err)
		}
		var ran atomic.Int64
		coreSvc := core.NewService(nil).WithSeed(core.SeedOperations{
			Run: func(ctx context.Context, spec core.SeedSpec) (core.SeedGrowResult, error) {
				ran.Add(1)
				return core.SeedGrowResult{Status: core.SeedStatusSatisfied, Iterations: 1, Branch: "tendril/claude/seed"}, nil
			},
		})
		handler := receptors.NewSeedHandler(coreSvc).WithDelegation(&receptors.DelegationGate{
			Authorizer: core.NewDelegationAuthorizer(grants),
			Bus:        eventbus.New(),
		})
		mux := http.NewServeMux()
		handler.Register(mux, nil)
		req := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow", strings.NewReader(body))
		if pollen != "" {
			req.Header.Set(receptors.PollenHeader, pollen)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, ran.Load()
	}

	status, executed := dispatch("claude", growBody)
	if status != http.StatusForbidden {
		t.Fatalf("A git-only grant: status = %d, want 403", status)
	}
	if executed != 0 {
		t.Fatal("A git-only grant still grew a Seed")
	}

	authorizerFromFile := func() *core.DelegationAuthorizer {
		grants, err := core.LoadDelegationGrants(tendrilDir)
		if err != nil {
			t.Fatalf("LoadDelegationGrants: %v", err)
		}
		return core.NewDelegationAuthorizer(grants)
	}
	if decision := authorizerFromFile().Authorize(core.DelegationRequest{Pollen: "claude", OperationClass: core.CapSproutWatch, Substrate: "myrepo"}); decision.Authorized {
		t.Fatal("A git-only grant authorized sprout.watch")
	}

	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow, core.CapSproutWatch}); err != nil {
		t.Fatalf("B explicit grant: %v", err)
	}

	status, executed = dispatch("claude", growBody)
	if status != http.StatusOK {
		t.Fatalf("C same Pollinator: status = %d, want 200", status)
	}
	if executed != 1 {
		t.Fatalf("C same Pollinator executed %d grow(s), want 1", executed)
	}

	status, executed = dispatch("other", growBody)
	if status != http.StatusForbidden {
		t.Fatalf("D different Pollen: status = %d, want 403", status)
	}
	if executed != 0 {
		t.Fatal("D different Pollen still grew a Seed")
	}

	status, executed = dispatch("claude", wrongSubstrateBody)
	if status != http.StatusForbidden {
		t.Fatalf("E different Substrate: status = %d, want 403", status)
	}
	if executed != 0 {
		t.Fatal("E different Substrate still grew a Seed")
	}

	if decision := authorizerFromFile().Authorize(core.DelegationRequest{Pollen: "claude", OperationClass: core.CapSproutWatch, Substrate: "myrepo"}); !decision.Authorized {
		t.Fatalf("sprout.watch denied after explicit grant: %s", decision.Reason)
	}

	if err := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("F revoke: %v", err)
	}
	status, executed = dispatch("claude", growBody)
	if status != http.StatusForbidden {
		t.Fatalf("F after revoke: status = %d, want 403", status)
	}
	if executed != 0 {
		t.Fatal("F after revoke still grew a Seed")
	}
	if decision := authorizerFromFile().Authorize(core.DelegationRequest{Pollen: "claude", OperationClass: core.CapSproutWatch, Substrate: "myrepo"}); !decision.Authorized {
		t.Fatalf("revoking seed.grow removed sprout.watch: %s", decision.Reason)
	}
}

func TestDelegationGrantsListingDoesNotMutate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	controlDir := filepath.Join(home, grantsDirName)
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := renderGrantsYAML(gitSetupOptions{substrate: "myrepo", grantPollen: "claude"})
	path := filepath.Join(controlDir, core.DelegationGrantsFilename)
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printMatchingGrants(controlDir, "claude", "myrepo")
	_ = w.Close()
	os.Stdout = oldStdout
	_, _ = buf.ReadFrom(r)
	out := buf.String()
	if !strings.Contains(out, "pollen: claude") || !strings.Contains(out, "git.status") {
		t.Fatalf("listing = %q, want the git-only grant", out)
	}
	if strings.Contains(out, "seed.grow") {
		t.Fatalf("listing invented seed.grow:\n%s", out)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatalf("listing mutated the grants file")
	}
}

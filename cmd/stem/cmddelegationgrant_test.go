package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
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

func TestDelegationUsageNamesCreateAndRemove(t *testing.T) {
	stdout, _ := captureVerifyOutput(t, printDelegationUsage)
	for _, want := range []string{
		"<pending|approve|deny|create|grant|grants|revoke|remove>",
		"create",
		"remove",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("delegation usage = %q, want %q", stdout, want)
		}
	}
}

func TestParseDelegationCreateFlagsAcceptsRepeatedSubstratesAndOperations(t *testing.T) {
	flags, err := parseDelegationCreateFlags([]string{
		"--pollen", "claude",
		"--substrate", "myrepo",
		"--substrate", "otherrepo",
		"--operation", core.CapGitStatus,
		"--operation", core.CapSeedGrow,
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if flags.pollen != "claude" {
		t.Fatalf("pollen = %q, want claude", flags.pollen)
	}
	if !slices.Equal(flags.substrates, []string{"myrepo", "otherrepo"}) {
		t.Fatalf("substrates = %v", flags.substrates)
	}
	if !slices.Equal(flags.operations, []string{core.CapGitStatus, core.CapSeedGrow}) {
		t.Fatalf("operations = %v", flags.operations)
	}
}

func TestRequireDelegationCreateFlags(t *testing.T) {
	cases := []struct {
		name  string
		flags delegationCreateFlags
		want  string
	}{
		{name: "missing pollen", flags: delegationCreateFlags{substrates: []string{"myrepo"}, operations: []string{core.CapSeedGrow}}, want: "missing --pollen"},
		{name: "missing substrate", flags: delegationCreateFlags{pollen: "claude", operations: []string{core.CapSeedGrow}}, want: "missing --substrate"},
		{name: "missing operation", flags: delegationCreateFlags{pollen: "claude", substrates: []string{"myrepo"}}, want: "missing --operation"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := requireDelegationCreateFlags(tt.flags); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseDelegationCreateFlagsRejectsCallerSelectedGrantPaths(t *testing.T) {
	for _, name := range []string{"--dir", "--grants", "--grants-file", "--grants-path"} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDelegationCreateFlags([]string{name, "/tmp/checkout"}); err == nil || !strings.Contains(err.Error(), "does not accept a grants file path") {
				t.Fatalf("error = %v, want control-plane diagnostic", err)
			}
		})
	}
}

func TestParseDelegationCreateFlagsRequiresExactlyOnePollen(t *testing.T) {
	if _, err := parseDelegationCreateFlags([]string{"--pollen", "claude", "--pollen", "other", "--substrate", "myrepo", "--operation", core.CapSeedGrow}); err == nil || !strings.Contains(err.Error(), "exactly one --pollen") {
		t.Fatalf("duplicate Pollen error = %v", err)
	}
	flags, err := parseDelegationCreateFlags([]string{"--pollen", "   ", "--substrate", "myrepo", "--operation", core.CapSeedGrow})
	if err != nil {
		t.Fatalf("blank Pollen parse: %v", err)
	}
	if err := requireDelegationCreateFlags(flags); err == nil || !strings.Contains(err.Error(), "missing --pollen") {
		t.Fatalf("blank Pollen validation error = %v", err)
	}
}

func TestParseDelegationRemoveFlagsRejectsUnsupportedArguments(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "substrate", args: []string{"--pollen", "claude", "--substrate", "myrepo"}, want: "does not accept --substrate"},
		{name: "operation", args: []string{"--pollen", "claude", "--operation", core.CapSeedGrow}, want: "does not accept --operation"},
		{name: "dir", args: []string{"--dir", "/tmp/checkout"}, want: "does not accept a grants file path"},
		{name: "grants", args: []string{"--grants", "/tmp/grants.yaml"}, want: "does not accept a grants file path"},
		{name: "grants-file", args: []string{"--grants-file", "/tmp/grants.yaml"}, want: "does not accept a grants file path"},
		{name: "grants-path", args: []string{"--grants-path", "/tmp/grants.yaml"}, want: "does not accept a grants file path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseDelegationRemoveFlags(tt.args); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestRequireDelegationRemoveFlagsRequiresPollen(t *testing.T) {
	for _, pollen := range []string{"", "   "} {
		if err := requireDelegationRemoveFlags(delegationRemoveFlags{pollen: pollen}); err == nil || !strings.Contains(err.Error(), "missing --pollen") {
			t.Fatalf("Pollen %q error = %v, want missing Pollen", pollen, err)
		}
	}
}

func TestDelegationCreateRefusesDeclaredPollen(t *testing.T) {
	t.Setenv(envPollenCLI, "claude")
	if err := refuseDeclaredPollenGrantMutation(); err == nil {
		t.Fatal("declared Pollen was allowed to create a grant")
	}
}

func TestDelegationRemoveRefusesDeclaredPollen(t *testing.T) {
	t.Setenv(envPollenCLI, "claude")
	if err := refuseDeclaredPollenGrantMutation(); err == nil {
		t.Fatal("declared Pollen was allowed to remove a grant")
	}
}

func TestDelegationCreateUsesCoreLifecycleAndShowsCompleteGrant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	controlDir := filepath.Join(home, grantsDirName)
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := captureVerifyOutput(t, func() {
		runDelegationCreateCmd([]string{
			"--pollen", "claude",
			"--substrate", "myrepo",
			"--substrate", "otherrepo",
			"--operation", core.CapGitStatus,
			"--operation", core.CapSeedGrow,
			"--operation", core.CapSproutWatch,
		})
	})
	if strings.Contains(strings.ToLower(stdout+stderr), "restart") {
		t.Fatalf("create output implied a restart:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	for _, want := range []string{"pollen: claude", "substrates: [myrepo, otherrepo]", "git.status", "seed.grow", "sprout.watch"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("create output = %q, want %q", stdout, want)
		}
	}

	grants, err := core.LoadDelegationGrants(controlDir)
	if err != nil {
		t.Fatalf("LoadDelegationGrants: %v", err)
	}
	if len(grants) != 1 || !slices.Equal(grants[0].Substrates, []string{"myrepo", "otherrepo"}) || !slices.Equal(grants[0].OperationClasses, []string{core.CapGitStatus, core.CapSeedGrow, core.CapSproutWatch}) {
		t.Fatalf("created grants = %+v", grants)
	}
}

func TestDelegationCreateSurfacesCoreValidationAndDuplicateFailure(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		name       string
		operations []string
		want       string
	}{
		{name: "wildcard", operations: []string{"seed.*"}, want: "wildcard"},
		{name: "unknown", operations: []string{"made.up"}, want: "unknown operation class"},
		{name: "non-delegable", operations: []string{core.CapGenomeView}, want: "not delegable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := core.CreateDelegationGrant(dir, "claude", []string{"myrepo"}, tt.operations)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want Core validation containing %q", err, tt.want)
			}
		})
	}
	if err := core.CreateDelegationGrant(dir, "claude", []string{"myrepo"}, []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := core.CreateDelegationGrant(dir, "claude", []string{"myrepo"}, []string{core.CapSeedGrow}); err == nil || !strings.Contains(err.Error(), "already has a grant") {
		t.Fatalf("duplicate error = %v, want Core duplicate diagnostic", err)
	}
}

func TestDelegationCreateAndRemoveUseStemControlPlane(t *testing.T) {
	home := t.TempDir()
	checkout := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(checkout)
	controlDir := filepath.Join(home, grantsDirName)
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hostileDir := filepath.Join(checkout, grantsDirName)
	if err := os.MkdirAll(hostileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hostilePath := filepath.Join(hostileDir, core.DelegationGrantsFilename)
	hostile := renderGrantsYAML(gitSetupOptions{substrate: "myrepo", grantPollen: "claude"})
	if err := os.WriteFile(hostilePath, []byte(hostile), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _ = captureVerifyOutput(t, func() {
		runDelegationCreateCmd([]string{"--pollen", "claude", "--substrate", "myrepo", "--operation", core.CapSeedGrow})
	})
	controlGrants, err := core.LoadDelegationGrants(controlDir)
	if err != nil {
		t.Fatalf("load control grants after create: %v", err)
	}
	if len(controlGrants) != 1 || controlGrants[0].Pollen != "claude" {
		t.Fatalf("control grants after create = %+v", controlGrants)
	}
	unchanged, err := os.ReadFile(hostilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != hostile {
		t.Fatalf("create changed checkout-local grants: %s", unchanged)
	}

	_, _ = captureVerifyOutput(t, func() {
		runDelegationRemoveCmd([]string{"--pollen", "claude"})
	})
	controlGrants, err = core.LoadDelegationGrants(controlDir)
	if err != nil {
		t.Fatalf("load control grants after remove: %v", err)
	}
	if len(controlGrants) != 0 {
		t.Fatalf("control grants after remove = %+v, want empty", controlGrants)
	}
	unchanged, err = os.ReadFile(hostilePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != hostile {
		t.Fatalf("remove changed checkout-local grants: %s", unchanged)
	}
}

func TestDelegationRemoveReportsCoreBooleanDirectly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	controlDir := filepath.Join(home, grantsDirName)
	if err := os.MkdirAll(controlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := core.CreateDelegationGrant(controlDir, "claude", []string{"myrepo"}, []string{core.CapSeedGrow}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := captureVerifyOutput(t, func() {
		runDelegationRemoveCmd([]string{"--pollen", "claude"})
	})
	if !strings.Contains(strings.ToLower(stdout), "removed") || strings.Contains(strings.ToLower(stdout+stderr), "restart") {
		t.Fatalf("removed output = %q / %q", stdout, stderr)
	}
	stdout, stderr = captureVerifyOutput(t, func() {
		runDelegationRemoveCmd([]string{"--pollen", "claude"})
	})
	if !strings.Contains(stdout, "already absent") || strings.Contains(strings.ToLower(stdout+stderr), "restart") {
		t.Fatalf("already-absent output = %q / %q", stdout, stderr)
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

	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	growBody := `{"substrate":"myrepo","goal":"make the failing tests pass","verify":["go","test","./..."],"idempotencyKey":"delegation-grant-test-key"}`
	wrongSubstrateBody := `{"substrate":"otherrepo","goal":"make the failing tests pass","verify":["go","test","./..."],"idempotencyKey":"wrong-substrate-test-key"}`

	started := make(chan struct{})
	release := make(chan struct{})
	var ran atomic.Int64
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	newMux := func() *http.ServeMux {
		t.Helper()
		grants, err := core.LoadDelegationGrants(tendrilDir)
		if err != nil {
			t.Fatalf("LoadDelegationGrants: %v", err)
		}
		coreSvc := core.NewService(manager).
			WithSeed(core.SeedOperations{
				Run: func(ctx context.Context, spec core.SeedSpec, lifecycle *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
					ran.Add(1)
					select {
					case <-started:
					default:
						close(started)
					}
					select {
					case <-release:
					case <-ctx.Done():
						return core.SeedGrowResult{}, ctx.Err()
					}
					if lifecycle != nil {
						if _, err := lifecycle.AcquireSettlementFence(ctx); err != nil {
							return core.SeedGrowResult{}, err
						}
					}
					// Return the Stem-created Phytomer identity only. Do not
					// invent a Fruit branch or commit.
					return core.SeedGrowResult{
						Status:     core.SeedStatusSatisfied,
						Iterations: 1,
						PhytomerID: spec.PhytomerID,
					}, nil
				},
			}).
			WithSeedPersistence(seedPersistence(store)).
			WithContinuationPersistence(continuationPersistence(store)).
			WithPhytomerObservationSource(phytomerObservationSource(store))
		gate := &receptors.DelegationGate{
			Authorizer: core.NewDelegationAuthorizer(grants),
			Bus:        eventbus.New(),
		}
		mux := http.NewServeMux()
		receptors.NewSeedHandler(coreSvc).WithDelegation(gate).WithHistory(store).Register(mux, nil)
		sessions := receptors.NewSessionsHandler(coreSvc, manager, store, eventbus.New()).
			WithWatch(receptors.NewWatchAuthority(gate, store))
		sessions.Register(mux, gate.Middleware, nil)
		return mux
	}

	dispatch := func(mux *http.ServeMux, pollen, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/seeds/grow/async", strings.NewReader(body))
		if pollen != "" {
			req.Header.Set(receptors.PollenHeader, pollen)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	collect := func(mux *http.ServeMux, handle, pollen string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/seeds/runs/"+handle, nil)
		if pollen != "" {
			req.Header.Set(receptors.PollenHeader, pollen)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	watchDenied := func(mux *http.ServeMux, phytomerID, pollen string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/phytomers/"+phytomerID+"/watch", nil)
		if pollen != "" {
			req.Header.Set(receptors.PollenHeader, pollen)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	authorizerFromFile := func() *core.DelegationAuthorizer {
		t.Helper()
		grants, err := core.LoadDelegationGrants(tendrilDir)
		if err != nil {
			t.Fatalf("LoadDelegationGrants: %v", err)
		}
		return core.NewDelegationAuthorizer(grants)
	}

	gitOnly := newMux()
	rec := dispatch(gitOnly, "claude", growBody)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("A git-only grant: status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if ran.Load() != 0 {
		t.Fatal("A git-only grant still grew a Seed")
	}
	if decision := authorizerFromFile().Authorize(core.DelegationRequest{Pollen: "claude", OperationClass: core.CapSproutWatch, Substrate: "myrepo"}); decision.Authorized {
		t.Fatal("A git-only grant authorized sprout.watch")
	}

	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow, core.CapSproutWatch}); err != nil {
		t.Fatalf("B explicit grant: %v", err)
	}

	mux := newMux()
	rec = dispatch(mux, "claude", growBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("C same Pollinator: status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		Handle     string `json:"handle"`
		PhytomerID string `json:"phytomerId"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode dispatch: %v", err)
	}
	if accepted.Handle == "" || accepted.PhytomerID == "" || accepted.Status != "running" {
		t.Fatalf("dispatch payload = %+v, want handle, phytomerId, and status running", accepted)
	}
	if !strings.HasPrefix(accepted.PhytomerID, session.IDPrefix) {
		t.Fatalf("phytomerId %q was not Stem-created", accepted.PhytomerID)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("seed execution did not start")
	}

	obs := firstUseWatchObservation(t, mux, accepted.PhytomerID, "claude")
	if obs.Handle != accepted.Handle || obs.PhytomerID != accepted.PhytomerID || obs.Status != "running" {
		t.Fatalf("owner watch = %+v, dispatch = %+v", obs, accepted)
	}
	if obs.Pollen != "claude" || obs.Substrate != "myrepo" {
		t.Fatalf("ownership in current state = %+v", obs)
	}
	if obs.Branch != "" || obs.Commit != "" {
		t.Fatalf("watch invented Fruit identity: %+v", obs)
	}

	foreign := watchDenied(mux, accepted.PhytomerID, "other")
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("D different Pollen watch: status = %d, want 403: %s", foreign.Code, foreign.Body.String())
	}

	beforeWrong := ran.Load()
	otherPollen := dispatch(mux, "other", growBody)
	if otherPollen.Code != http.StatusForbidden {
		t.Fatalf("D different Pollen dispatch: status = %d, want 403: %s", otherPollen.Code, otherPollen.Body.String())
	}
	otherSubstrate := dispatch(mux, "claude", wrongSubstrateBody)
	if otherSubstrate.Code != http.StatusForbidden {
		t.Fatalf("E different Substrate: status = %d, want 403: %s", otherSubstrate.Code, otherSubstrate.Body.String())
	}
	if ran.Load() != beforeWrong {
		t.Fatal("a denied dispatch still grew a Seed")
	}

	close(release)
	settled := waitForFirstUseSeedRun(t, store, accepted.Handle)
	if settled.Status != core.SeedStatusSatisfied {
		t.Fatalf("settled status = %q, want satisfied", settled.Status)
	}
	if settled.PhytomerID != accepted.PhytomerID || settled.Handle != accepted.Handle {
		t.Fatalf("settled identities = %+v, dispatch = %+v", settled, accepted)
	}
	if settled.Branch != "" || settled.Commit != "" {
		t.Fatalf("settlement invented Fruit identity: %+v", settled)
	}

	fruit := collect(mux, accepted.Handle, "claude")
	if fruit.Code != http.StatusOK {
		t.Fatalf("owner collect: status = %d, want 200: %s", fruit.Code, fruit.Body.String())
	}
	var collected historydb.SeedRun
	if err := json.Unmarshal(fruit.Body.Bytes(), &collected); err != nil {
		t.Fatalf("decode collect: %v", err)
	}
	if collected.Handle != accepted.Handle || collected.PhytomerID != accepted.PhytomerID {
		t.Fatalf("collected identities = %+v, dispatch = %+v", collected, accepted)
	}

	foreignCollect := collect(mux, accepted.Handle, "other")
	if foreignCollect.Code != http.StatusForbidden {
		t.Fatalf("D different Pollen collect: status = %d, want 403: %s", foreignCollect.Code, foreignCollect.Body.String())
	}

	if err := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("F revoke seed.grow: %v", err)
	}
	afterRevokeGrow := newMux()
	revokedDispatch := dispatch(afterRevokeGrow, "claude", growBody)
	if revokedDispatch.Code != http.StatusForbidden {
		t.Fatalf("F after seed.grow revoke: status = %d, want 403: %s", revokedDispatch.Code, revokedDispatch.Body.String())
	}
	stillWatch := firstUseWatchObservation(t, afterRevokeGrow, accepted.PhytomerID, "claude")
	if stillWatch.Handle != accepted.Handle || stillWatch.PhytomerID != accepted.PhytomerID {
		t.Fatalf("revoking seed.grow removed sprout.watch: %+v", stillWatch)
	}

	if err := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSproutWatch}); err != nil {
		t.Fatalf("F revoke sprout.watch: %v", err)
	}
	afterRevokeWatch := newMux()
	revokedWatch := watchDenied(afterRevokeWatch, accepted.PhytomerID, "claude")
	if revokedWatch.Code != http.StatusForbidden {
		t.Fatalf("F after sprout.watch revoke: status = %d, want 403: %s", revokedWatch.Code, revokedWatch.Body.String())
	}
}

func firstUseWatchObservation(t *testing.T, mux http.Handler, phytomerID, pollen string) core.PhytomerObservation {
	t.Helper()
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/phytomers/"+phytomerID+"/watch", nil)
	if err != nil {
		t.Fatalf("watch request: %v", err)
	}
	if pollen != "" {
		req.Header.Set(receptors.PollenHeader, pollen)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("watch do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("owner watch = %d: %s", resp.StatusCode, body)
	}
	obs := readFirstUseObservation(t, resp.Body)
	cancel()
	return obs
}

func readFirstUseObservation(t *testing.T, r io.Reader) core.PhytomerObservation {
	t.Helper()
	scanner := bufio.NewScanner(r)
	var event, data string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data == "" {
				continue
			}
			if event != "observation" {
				t.Fatalf("event = %q, want observation (%s)", event, data)
			}
			var obs core.PhytomerObservation
			if err := json.Unmarshal([]byte(data), &obs); err != nil {
				t.Fatalf("decode observation: %v (%s)", err, data)
			}
			return obs
		}
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(value)
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("read watch stream: %v", err)
	}
	t.Fatal("watch stream closed before an observation")
	return core.PhytomerObservation{}
}

func waitForFirstUseSeedRun(t *testing.T, store *historydb.Store, handle string) historydb.SeedRun {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, found, err := store.GetSeedRun(context.Background(), handle)
		if err != nil {
			t.Fatalf("GetSeedRun: %v", err)
		}
		if found && run.Status != "running" {
			return run
		}
		time.Sleep(10 * time.Millisecond) // poll: wait until the async SeedRun leaves running
	}
	t.Fatalf("seed run %s did not settle in time", handle)
	return historydb.SeedRun{}
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

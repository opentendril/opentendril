package conductor

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
)

func restoreSeedGoProxy(t *testing.T) {
	t.Helper()
	original := seedGoModuleProxyBase
	t.Cleanup(func() { seedGoModuleProxyBase = original })
}

func TestGoModuleProxyUsesCanonicalEscaping(t *testing.T) {
	item := goModuleVersion{Path: "github.com/Azure/go-ntlmssp", Version: "v0.0.0"}
	escapedPath, err := module.EscapePath(item.Path)
	if err != nil {
		t.Fatalf("EscapePath: %v", err)
	}
	if escapedPath != "github.com/!azure/go-ntlmssp" {
		t.Fatalf("escaped path = %q, want github.com/!azure/go-ntlmssp", escapedPath)
	}
	fetch, err := seedGoModuleFetch("https://proxy.golang.org", item, goModuleZipSuffix)
	if err != nil {
		t.Fatalf("seedGoModuleFetch: %v", err)
	}
	wantURL := "https://proxy.golang.org/github.com/!azure/go-ntlmssp/@v/v0.0.0.zip"
	if fetch.URL != wantURL {
		t.Fatalf("URL = %q, want %q", fetch.URL, wantURL)
	}
	if fetch.Path != "go-proxy/github.com/!azure/go-ntlmssp/@v/v0.0.0.zip" {
		t.Fatalf("path = %q", fetch.Path)
	}
}

func TestDuplicateGoSumLinesDoNotDuplicateFetches(t *testing.T) {
	modBytes := []byte("module example.com/seed\n\ngo 1.25\n\nrequire example.com/leaf v1.0.0\n")
	sumBytes := []byte(strings.Join([]string{
		"example.com/leaf v1.0.0 h1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa=",
		"example.com/leaf v1.0.0 h1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa=",
		"example.com/leaf v1.0.0/go.mod h1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb=",
		"example.com/leaf v1.0.0/go.mod h1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb=",
	}, "\n") + "\n")
	modules, err := lockedGoModuleVersions(modBytes, sumBytes)
	if err != nil {
		t.Fatalf("lockedGoModuleVersions: %v", err)
	}
	if len(modules) != 1 || modules[0].Path != "example.com/leaf" || modules[0].Version != "v1.0.0" {
		t.Fatalf("modules = %+v, want one example.com/leaf@v1.0.0", modules)
	}
	fetches, err := seedGoModuleFetches("https://proxy.example", modules)
	if err != nil {
		t.Fatalf("seedGoModuleFetches: %v", err)
	}
	if len(fetches) != 3 {
		t.Fatalf("fetches = %d, want 3", len(fetches))
	}
}

func TestMalformedAndUnboundedGoMetadataFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("not a go.mod file\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	execution := &StomaExecution{}
	if err := configureSeedGoVerification(root, []string{"go", "test", "."}, execution); err == nil {
		t.Fatal("malformed go.mod was accepted")
	}

	modBytes := []byte("module example.com/seed\n\ngo 1.25\n")
	var sum strings.Builder
	for i := 0; i < seedGoModuleVersionLimit+1; i++ {
		fmt.Fprintf(&sum, "example.com/dep%d v1.0.0 h1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa=\n", i)
		fmt.Fprintf(&sum, "example.com/dep%d v1.0.0/go.mod h1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb=\n", i)
	}
	if _, err := lockedGoModuleVersions(modBytes, []byte(sum.String())); err == nil {
		t.Fatal("unbounded module metadata was accepted")
	}

	oversize := bytes.Repeat([]byte("x"), seedGoMetadataFileLimit+1)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/seed\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("rewrite go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.sum"), oversize, 0o644); err != nil {
		t.Fatalf("write go.sum: %v", err)
	}
	if _, err := planSeedGoModuleFetches(root); err == nil {
		t.Fatal("oversize go.sum was accepted")
	}

	if _, err := lockedGoModuleVersions([]byte("module example.com/seed\n\ngo 1.25\n\nrequire example.com/leaf v1.0.0\n"), nil); err == nil {
		t.Fatal("unlocked require was accepted")
	}
}

func TestVendoredGoCandidateRequiresNoExternalFetch(t *testing.T) {
	restoreSeedGoProxy(t)
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()
	seedGoModuleProxyBase = server.URL

	root := t.TempDir()
	writeSeedGoFiles(t, root, map[string]string{
		"go.mod":                          "module example.com/seed\n\ngo 1.25\n\nrequire example.com/leaf v1.0.0\n",
		"go.sum":                          "example.com/leaf v1.0.0 h1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa=\n",
		"vendor/modules.txt":              "# example.com/leaf v1.0.0\n## explicit\nexample.com/leaf\n",
		"vendor/example.com/leaf/leaf.go": "package leaf\n",
	})
	execution := &StomaExecution{Egress: []string{hostOf(t, server.URL)}}
	if err := configureSeedGoVerification(root, []string{"go", "test", "."}, execution); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !execution.SkipHostModuleCache || !execution.ReadOnlyWorkspace || !execution.GoVendorMode || execution.PopulateGoModuleCache || len(execution.Fetches) != 0 {
		t.Fatalf("vendored execution = %+v, want vendor mode with no fetches", execution)
	}
	if hits.Load() != 0 {
		t.Fatal("vendored candidate performed an external fetch")
	}
}

func TestLockedGoModulesProduceUsableReadonlyLocalProxy(t *testing.T) {
	restoreSeedGoProxy(t)
	leaf := testLeafModule(t)
	var hits atomic.Int64
	server := startGoModuleProxy(t, leaf.objects, &hits)
	defer server.Close()
	seedGoModuleProxyBase = server.URL

	root := t.TempDir()
	writeSeedGoFiles(t, root, map[string]string{
		"go.mod":       leaf.consumerMod,
		"go.sum":       leaf.goSum,
		"seed.go":      leaf.consumerSrc,
		"seed_test.go": leaf.consumerTest,
	})
	execution := &StomaExecution{}
	if err := configureSeedGoVerification(root, []string{"go", "test", "."}, execution); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !execution.PopulateGoModuleCache || len(execution.Fetches) != 3 {
		t.Fatalf("execution fetches = %d populate=%v, want 3 locked proxy objects", len(execution.Fetches), execution.PopulateGoModuleCache)
	}

	payloads, err := fetchEgressPayloads(context.Background(), NewEgressPolicy([]string{hostOf(t, server.URL)}), execution.Fetches)
	if err != nil {
		t.Fatalf("fetch locked modules: %v", err)
	}
	if hits.Load() != 3 {
		t.Fatalf("proxy hits = %d, want 3", hits.Load())
	}
	proxyDir := writeReadonlyProxy(t, payloads)
	cacheDir := t.TempDir()
	makeWritableForCleanup(t, proxyDir, cacheDir)
	beforeMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	beforeSum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatalf("read go.sum: %v", err)
	}

	download := exec.Command("go", "mod", "download")
	download.Dir = root
	download.Env = isolatedGoEnv(t, cacheDir, "file://"+proxyDir+"/", false)
	if out, err := download.CombinedOutput(); err != nil {
		t.Fatalf("go mod download against local proxy: %v\n%s", err, out)
	}
	testCmd := exec.Command("go", "test", ".")
	testCmd.Dir = root
	testCmd.Env = isolatedGoEnv(t, cacheDir, "off", true)
	if out, err := testCmd.CombinedOutput(); err != nil {
		t.Fatalf("GOPROXY=off go test after local proxy population: %v\n%s", err, out)
	}
	afterMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("re-read go.mod: %v", err)
	}
	afterSum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatalf("re-read go.sum: %v", err)
	}
	if !bytes.Equal(beforeMod, afterMod) || !bytes.Equal(beforeSum, afterSum) {
		t.Fatal("local proxy population changed go.mod or go.sum")
	}
}

func TestEmptyEgressDeniesGoDependencyAcquisitionBeforePredicate(t *testing.T) {
	restoreSeedGoProxy(t)
	leaf := testLeafModule(t)
	var hits atomic.Int64
	server := startGoModuleProxy(t, leaf.objects, &hits)
	defer server.Close()
	seedGoModuleProxyBase = server.URL

	origRun := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = origRun })
	runStomaCommandFn = func(context.Context, StomaExecution, []terrarium.FilePayload, time.Duration) (StomaResult, error) {
		t.Fatal("predicate ran before denied dependency acquisition")
		return StomaResult{}, nil
	}

	root := t.TempDir()
	writeSeedGoFiles(t, root, map[string]string{
		"go.mod": leaf.consumerMod,
		"go.sum": leaf.goSum,
	})
	execution := StomaExecution{
		Workspace: root,
		Command:   []string{"go", "test", "."},
	}
	if err := configureSeedGoVerification(root, execution.Command, &execution); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if _, err := RunStoma(context.Background(), execution); err == nil || !strings.Contains(err.Error(), "egress denied") {
		t.Fatalf("empty egress error = %v, want an egress denial", err)
	}
	if hits.Load() != 0 {
		t.Fatal("empty egress still contacted the module proxy")
	}
}

func TestGrantedProxyHostPermitsGoDependencyAcquisition(t *testing.T) {
	restoreSeedGoProxy(t)
	leaf := testLeafModule(t)
	var hits atomic.Int64
	server := startGoModuleProxy(t, leaf.objects, &hits)
	defer server.Close()
	seedGoModuleProxyBase = server.URL

	root := t.TempDir()
	writeSeedGoFiles(t, root, map[string]string{
		"go.mod": leaf.consumerMod,
		"go.sum": leaf.goSum,
	})
	execution := StomaExecution{
		Workspace: root,
		Command:   []string{"go", "test", "."},
		Egress:    []string{hostOf(t, server.URL)},
	}
	if err := configureSeedGoVerification(root, execution.Command, &execution); err != nil {
		t.Fatalf("configure: %v", err)
	}
	origRun := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = origRun })
	var gotPayloads []terrarium.FilePayload
	runStomaCommandFn = func(_ context.Context, _ StomaExecution, payloads []terrarium.FilePayload, _ time.Duration) (StomaResult, error) {
		gotPayloads = payloads
		return StomaResult{ExitCode: 0}, nil
	}
	if _, err := RunStoma(context.Background(), execution); err != nil {
		t.Fatalf("granted proxy fetch: %v", err)
	}
	if hits.Load() != 3 || len(gotPayloads) != 3 {
		t.Fatalf("hits=%d payloads=%d, want 3 locked objects", hits.Load(), len(gotPayloads))
	}
	for _, payload := range gotPayloads {
		if payload.Mode != 0o444 {
			t.Fatalf("payload mode = %o, want read-only 444", payload.Mode)
		}
		if !strings.HasPrefix(payload.Path, stomaEgressDirectory+"/go-proxy/") {
			t.Fatalf("payload path = %q, want it under the local GOPROXY tree", payload.Path)
		}
	}
}

func TestGoModuleCachePopulationHappensBeforePredicate(t *testing.T) {
	origEnsure := ensureSproutImageFn
	origProvider := terrariumNewProviderFn
	t.Cleanup(func() {
		ensureSproutImageFn = origEnsure
		terrariumNewProviderFn = origProvider
	})
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	recorder := &recordingStomaTerrarium{
		run: func(i int, spec terrarium.CommandSpec) (terrarium.CommandResult, error) {
			if i == 0 {
				return terrarium.CommandResult{ExitCode: 0}, nil
			}
			return terrarium.CommandResult{ExitCode: 1, Stderr: "FAIL: TestLeaf"}, nil
		},
	}
	var spec terrarium.TerrariumSpec
	terrariumNewProviderFn = func(context.Context, string, ...terrarium.ActivationObserver) (terrarium.TerrariumProvider, error) {
		return &stubProvider{createFn: func(created terrarium.TerrariumSpec) (terrarium.Terrarium, error) {
			spec = created
			return recorder, nil
		}}, nil
	}

	result, err := runStomaCommand(context.Background(), StomaExecution{
		Workspace:             t.TempDir(),
		Command:               []string{"go", "test", "."},
		SkipHostModuleCache:   true,
		ReadOnlyWorkspace:     true,
		PopulateGoModuleCache: true,
		Timeout:               time.Second,
	}, nil, time.Second)
	if err != nil {
		t.Fatalf("runStomaCommand: %v", err)
	}
	if result.ExitCode != 1 {
		t.Fatalf("predicate exit = %d, want 1", result.ExitCode)
	}
	if len(recorder.commands) != 2 {
		t.Fatalf("commands = %d, want populate then predicate", len(recorder.commands))
	}
	if got := strings.Join(recorder.commands[0].Command, " "); got != "go mod download" {
		t.Fatalf("first command = %q, want go mod download", got)
	}
	if got := strings.Join(recorder.commands[1].Command, " "); got != "go test ." {
		t.Fatalf("second command = %q, want the configured predicate", got)
	}
	if proxy := recorder.commands[0].Environment["GOPROXY"]; proxy != seedGoProxyFileURL() || strings.Contains(proxy, "direct") {
		t.Fatalf("populate GOPROXY = %q", recorder.commands[0].Environment["GOPROXY"])
	}
	if got := recorder.commands[1].Environment["GOPROXY"]; got != "off" {
		t.Fatalf("predicate GOPROXY = %q, want off", got)
	}
	if got := recorder.commands[1].Environment["GOTOOLCHAIN"]; got != "local" {
		t.Fatalf("predicate GOTOOLCHAIN = %q, want local", got)
	}
	if spec.NetworkMode != terrarium.NetworkModeNone {
		t.Fatalf("network mode = %q, want none", spec.NetworkMode)
	}
	for _, mount := range spec.Mounts {
		if mount.Target == "/go/pkg/mod" {
			t.Fatal("Seed Go verification mounted a host module cache")
		}
	}
}

func TestGoModuleCachePopulationFailureIsInfrastructure(t *testing.T) {
	origEnsure := ensureSproutImageFn
	origProvider := terrariumNewProviderFn
	t.Cleanup(func() {
		ensureSproutImageFn = origEnsure
		terrariumNewProviderFn = origProvider
	})
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	recorder := &recordingStomaTerrarium{
		run: func(int, terrarium.CommandSpec) (terrarium.CommandResult, error) {
			return terrarium.CommandResult{ExitCode: 1, Stderr: "missing zip"}, nil
		},
	}
	terrariumNewProviderFn = func(context.Context, string, ...terrarium.ActivationObserver) (terrarium.TerrariumProvider, error) {
		return &stubProvider{createFn: func(terrarium.TerrariumSpec) (terrarium.Terrarium, error) {
			return recorder, nil
		}}, nil
	}
	_, err := runStomaCommand(context.Background(), StomaExecution{
		Workspace:             t.TempDir(),
		Command:               []string{"go", "test", "."},
		SkipHostModuleCache:   true,
		ReadOnlyWorkspace:     true,
		PopulateGoModuleCache: true,
		Timeout:               time.Second,
	}, nil, time.Second)
	if err == nil {
		t.Fatal("cache population failure returned a predicate result")
	}
	if !strings.Contains(err.Error(), "populate Seed Go module cache") {
		t.Fatalf("error = %v, want a typed preparation failure", err)
	}
	if len(recorder.commands) != 1 {
		t.Fatalf("commands = %d, want only the failed preparation", len(recorder.commands))
	}
}

func TestSeedGoPreparationFailureWithersWithoutAnotherSprout(t *testing.T) {
	restoreSeeds(t)
	restoreSeedGoProxy(t)
	leaf := testLeafModule(t)
	var hits atomic.Int64
	server := startGoModuleProxy(t, leaf.objects, &hits)
	defer server.Close()
	seedGoModuleProxyBase = server.URL

	repo := newSeedRepo(t)
	commit := commitFiles(t, repo, map[string]string{
		"go.mod": leaf.consumerMod,
		"go.sum": leaf.goSum,
	})
	origRun := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = origRun })
	runStomaCommandFn = func(context.Context, StomaExecution, []terrarium.FilePayload, time.Duration) (StomaResult, error) {
		t.Fatal("predicate ran after a preparation failure")
		return StomaResult{}, nil
	}

	var builds int
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		builds++
		return SproutRunReport{Outcome: SproutOutcomeComplete, seedCandidateCommit: commit, RequestsMade: true}, nil
	}
	seedVerifyFn = runSeedVerify

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate:     repo,
		Goal:          "keep tests green",
		Verify:        []string{"go", "test", "."},
		MaxIterations: 2,
		SessionID:     "seed-go-prep-fail",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if builds != 1 {
		t.Fatalf("Sprout builds = %d, want 1", builds)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
	if res.Branch != "" || res.Commit != "" {
		t.Fatalf("Fruit identity = %q/%q, want none", res.Branch, res.Commit)
	}
	if len(res.VerificationDiagnostics) != 1 || res.VerificationDiagnostics[0].Outcome != core.SeedVerificationOutcomeInfrastructureFailed {
		t.Fatalf("diagnostics = %+v, want infrastructure-failed", res.VerificationDiagnostics)
	}
	if hits.Load() != 0 {
		t.Fatal("deny-all egress still fetched modules")
	}
}

func TestSeedGoPredicateFailureAfterSuccessfulPreparation(t *testing.T) {
	restoreSeeds(t)
	restoreSeedGoProxy(t)
	leaf := testLeafModule(t)
	server := startGoModuleProxy(t, leaf.objects, nil)
	defer server.Close()
	seedGoModuleProxyBase = server.URL

	repo := newSeedRepo(t)
	commit := commitFiles(t, repo, map[string]string{
		"go.mod": leaf.consumerMod,
		"go.sum": leaf.goSum,
	})
	origRun := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = origRun })
	runStomaCommandFn = func(_ context.Context, execution StomaExecution, payloads []terrarium.FilePayload, _ time.Duration) (StomaResult, error) {
		if !execution.PopulateGoModuleCache || !execution.SkipHostModuleCache || !execution.ReadOnlyWorkspace || len(payloads) != 3 {
			t.Fatalf("predicate ran without successful preparation: %+v payloads=%d", execution, len(payloads))
		}
		return StomaResult{ExitCode: 1, Stderr: "FAIL: TestLeaf"}, nil
	}

	var builds int
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		builds++
		return SproutRunReport{Outcome: SproutOutcomeComplete, seedCandidateCommit: commit, RequestsMade: true}, nil
	}
	seedVerifyFn = runSeedVerify

	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate:     repo,
		Goal:          "keep tests green",
		Verify:        []string{"go", "test", "."},
		MaxIterations: 2,
		Egress:        []string{hostOf(t, server.URL)},
		SessionID:     "seed-go-predicate-fail",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if builds != 2 {
		t.Fatalf("Sprout builds = %d, want 2 after a genuine predicate failure", builds)
	}
	if res.Status != SeedStatusExhausted {
		t.Fatalf("status = %q, want exhausted", res.Status)
	}
	if len(res.VerificationDiagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want one per iteration", res.VerificationDiagnostics)
	}
	for _, diagnostic := range res.VerificationDiagnostics {
		if diagnostic.Outcome != core.SeedVerificationOutcomePredicateFailed || diagnostic.ExitCode == nil || *diagnostic.ExitCode != 1 {
			t.Fatalf("diagnostic = %+v, want predicate-failed with exit 1", diagnostic)
		}
	}
}

func TestSeedGoVerificationLeavesModuleMetadataUnchanged(t *testing.T) {
	restoreSeedGoProxy(t)
	leaf := testLeafModule(t)
	server := startGoModuleProxy(t, leaf.objects, nil)
	defer server.Close()
	seedGoModuleProxyBase = server.URL

	repo := newSeedRepo(t)
	commit := commitFiles(t, repo, map[string]string{
		"go.mod": leaf.consumerMod,
		"go.sum": leaf.goSum,
	})
	origRun := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = origRun })
	runStomaCommandFn = func(context.Context, StomaExecution, []terrarium.FilePayload, time.Duration) (StomaResult, error) {
		return StomaResult{ExitCode: 0}, nil
	}
	report := runSeedVerify(context.Background(), repo, commit, []string{"go", "test", "."}, []string{hostOf(t, server.URL)})
	if report.Err != nil {
		t.Fatalf("runSeedVerify: %v", report.Err)
	}
	gotMod, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	gotSum, err := os.ReadFile(filepath.Join(repo, "go.sum"))
	if err != nil {
		t.Fatalf("read go.sum: %v", err)
	}
	if string(gotMod) != leaf.consumerMod || string(gotSum) != leaf.goSum {
		t.Fatal("candidate go.mod or go.sum changed")
	}
}

func TestSeedGoVerificationDoesNotConsultHostModuleCache(t *testing.T) {
	origConsult := consultHostGoModCache
	t.Cleanup(func() { consultHostGoModCache = origConsult })
	consultHostGoModCache = func() (string, bool) {
		t.Fatal("Seed Go verification consulted host GOMODCACHE")
		return "", false
	}

	root := t.TempDir()
	writeSeedGoFiles(t, root, map[string]string{
		"go.mod": "module example.com/seed\n\ngo 1.25\n",
	})
	execution := StomaExecution{Workspace: root, Command: []string{"go", "test", "."}}
	if err := configureSeedGoVerification(root, execution.Command, &execution); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !execution.SkipHostModuleCache || !execution.ReadOnlyWorkspace {
		t.Fatal("Seed Go verification did not skip the host module cache")
	}
	for _, mount := range stomaBindMounts(execution) {
		if mount.Target == "/go/pkg/mod" {
			t.Fatal("Seed Go verification mounted host GOMODCACHE")
		}
	}
}

func TestSeedGoFetchesIgnoreExtraEgressHosts(t *testing.T) {
	restoreSeedGoProxy(t)
	seedGoModuleProxyBase = "https://proxy.golang.org"
	root := t.TempDir()
	writeSeedGoFiles(t, root, map[string]string{
		"go.mod": "module example.com/seed\n\ngo 1.25\n\nrequire example.com/leaf v1.0.0\n",
		"go.sum": "example.com/leaf v1.0.0 h1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa=\nexample.com/leaf v1.0.0/go.mod h1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb=\n",
	})
	execution := StomaExecution{Egress: []string{"proxy.golang.org", "evil.example.com"}}
	if err := configureSeedGoVerification(root, []string{"go", "test", "."}, &execution); err != nil {
		t.Fatalf("configure: %v", err)
	}
	for _, fetch := range execution.Fetches {
		if !strings.HasPrefix(fetch.URL, "https://proxy.golang.org/") {
			t.Fatalf("fetch URL = %q, want the canonical proxy", fetch.URL)
		}
		if strings.Contains(fetch.URL, "evil.example.com") {
			t.Fatal("an extra egress host became a fetch URL")
		}
	}
}

func TestNonGoSeedVerificationBehaviorUnchanged(t *testing.T) {
	root := t.TempDir()
	writeSeedGoFiles(t, root, map[string]string{
		"go.mod": "module example.com/seed\n\ngo 1.25\n",
	})
	execution := &StomaExecution{}
	if err := configureSeedGoVerification(root, []string{"true"}, execution); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if execution.SkipHostModuleCache || execution.ReadOnlyWorkspace || execution.GoVendorMode || execution.PopulateGoModuleCache || len(execution.Fetches) != 0 {
		t.Fatalf("non-Go execution was rewritten: %+v", execution)
	}

	origConsult := consultHostGoModCache
	t.Cleanup(func() { consultHostGoModCache = origConsult })
	var consulted atomic.Bool
	consultHostGoModCache = func() (string, bool) {
		consulted.Store(true)
		return "/host/modcache", true
	}
	mounts := stomaBindMounts(StomaExecution{Workspace: root})
	if !consulted.Load() {
		t.Fatal("non-Go Stoma stopped consulting the host module cache")
	}
	foundCache := false
	for _, mount := range mounts {
		if mount.Target == "/app" && mount.ReadOnly {
			t.Fatal("non-Go Stoma mounted /app read-only")
		}
		if mount.Target == "/go/pkg/mod" && mount.Source == "/host/modcache" && mount.ReadOnly {
			foundCache = true
		}
	}
	if !foundCache {
		t.Fatalf("non-Go mounts = %+v, want the host module cache", mounts)
	}

	stubLocalStoma(t)
	repo := newSeedRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/seed\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "add", "go.mod"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "-m", "add go.mod"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	commit, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	report := runSeedVerify(ctx, repo, strings.TrimSpace(commit), []string{"true"}, nil)
	if report.Err != nil || !report.Passed {
		t.Fatalf("non-Go Seed verify = %+v", report)
	}
}

func TestNonGoSeedVerifierDoesNotApplyGoMetadataContract(t *testing.T) {
	stubLocalStoma(t)
	repo := newSeedRepo(t)
	originalMod := "module example.com/seed\n\ngo 1.25\n"
	commit := commitFiles(t, repo, map[string]string{
		"go.mod": originalMod,
		"go.sum": string(bytes.Repeat([]byte("x"), seedGoMetadataFileLimit+1)),
	})
	execution := &StomaExecution{}
	if err := configureSeedGoVerification(repo, []string{"sh", "-c", "true"}, execution); err != nil {
		t.Fatalf("non-Go configure: %v", err)
	}
	if execution.SkipHostModuleCache || execution.ReadOnlyWorkspace {
		t.Fatal("non-Go configure activated the Go Seed preparation path")
	}

	report := runSeedVerify(context.Background(), repo, commit, []string{"sh", "-c", "printf 'mutated\\n' > go.mod"}, nil)
	if report.Err != nil || !report.Passed {
		t.Fatalf("non-Go Seed verify with oversized go.sum and mutated go.mod = %+v", report)
	}
}

func TestSeedGoVerificationMountsCandidateReadOnly(t *testing.T) {
	restoreSeedGoProxy(t)
	leaf := testLeafModule(t)
	root := t.TempDir()
	writeSeedGoFiles(t, root, map[string]string{
		"go.mod": leaf.consumerMod,
		"go.sum": leaf.goSum,
	})
	execution := StomaExecution{Workspace: root, Command: []string{"go", "test", "."}}
	if err := configureSeedGoVerification(root, execution.Command, &execution); err != nil {
		t.Fatalf("configure: %v", err)
	}
	if !execution.ReadOnlyWorkspace || !execution.PopulateGoModuleCache {
		t.Fatalf("Go Seed path = %+v, want a read-only candidate plus cache population", execution)
	}
	app, ok := mountByTarget(stomaBindMounts(execution), "/app")
	if !ok || !app.ReadOnly {
		t.Fatalf("/app mount = %+v, want read-only", app)
	}

	origEnsure := ensureSproutImageFn
	origProvider := terrariumNewProviderFn
	t.Cleanup(func() {
		ensureSproutImageFn = origEnsure
		terrariumNewProviderFn = origProvider
	})
	ensureSproutImageFn = func(context.Context, string) error { return nil }
	recorder := &recordingStomaTerrarium{}
	var spec terrarium.TerrariumSpec
	terrariumNewProviderFn = func(context.Context, string, ...terrarium.ActivationObserver) (terrarium.TerrariumProvider, error) {
		return &stubProvider{createFn: func(created terrarium.TerrariumSpec) (terrarium.Terrarium, error) {
			spec = created
			return recorder, nil
		}}, nil
	}
	if _, err := runStomaCommand(context.Background(), execution, nil, time.Second); err != nil {
		t.Fatalf("runStomaCommand: %v", err)
	}
	if len(recorder.commands) != 2 {
		t.Fatalf("commands = %d, want populate then predicate on one Terrarium", len(recorder.commands))
	}
	app, ok = mountByTarget(spec.Mounts, "/app")
	if !ok || !app.ReadOnly {
		t.Fatalf("shared Terrarium /app mount = %+v, want read-only for populate and predicate", app)
	}
	if mount, found := mountByTarget(spec.Mounts, "/go/pkg/mod"); found {
		t.Fatalf("unexpected host module-cache mount: %+v", mount)
	}
}

func TestSeedGoWorkIsUnsupportedInfrastructure(t *testing.T) {
	restoreSeeds(t)
	restoreSeedGoProxy(t)
	leaf := testLeafModule(t)
	var hits atomic.Int64
	server := startGoModuleProxy(t, leaf.objects, &hits)
	defer server.Close()
	seedGoModuleProxyBase = server.URL

	root := t.TempDir()
	writeSeedGoFiles(t, root, map[string]string{
		"go.mod":  leaf.consumerMod,
		"go.sum":  leaf.goSum,
		"go.work": "go 1.25\n\nuse .\n",
	})
	execution := StomaExecution{Egress: []string{hostOf(t, server.URL)}}
	err := configureSeedGoVerification(root, []string{"go", "test", "."}, &execution)
	if err == nil || !strings.Contains(err.Error(), "go.work is unsupported") {
		t.Fatalf("configure error = %v, want unsupported go.work", err)
	}
	if execution.PopulateGoModuleCache || len(execution.Fetches) != 0 {
		t.Fatalf("go.work candidate planned fetches: %+v", execution)
	}
	if hits.Load() != 0 {
		t.Fatal("go.work detection fetched modules")
	}

	repo := newSeedRepo(t)
	commit := commitFiles(t, repo, map[string]string{
		"go.mod":  leaf.consumerMod,
		"go.sum":  leaf.goSum,
		"go.work": "go 1.25\n\nuse .\n",
	})
	origRun := runStomaCommandFn
	t.Cleanup(func() { runStomaCommandFn = origRun })
	runStomaCommandFn = func(context.Context, StomaExecution, []terrarium.FilePayload, time.Duration) (StomaResult, error) {
		t.Fatal("predicate ran for an unsupported go.work candidate")
		return StomaResult{}, nil
	}
	var builds int
	seedBuildFn = func(context.Context, *DockerOrchestrator, string) (SproutRunReport, error) {
		builds++
		return SproutRunReport{Outcome: SproutOutcomeComplete, seedCandidateCommit: commit, RequestsMade: true}, nil
	}
	seedVerifyFn = runSeedVerify
	res, err := RunSeed(context.Background(), SeedExecution{
		Substrate:     repo,
		Goal:          "keep tests green",
		Verify:        []string{"go", "test", "."},
		MaxIterations: 2,
		Egress:        []string{hostOf(t, server.URL)},
		SessionID:     "seed-go-work-unsupported",
	})
	if err != nil {
		t.Fatalf("RunSeed: %v", err)
	}
	if builds != 1 {
		t.Fatalf("Sprout builds = %d, want 1", builds)
	}
	if res.Status != SeedStatusWithered {
		t.Fatalf("status = %q, want withered", res.Status)
	}
	if res.Branch != "" || res.Commit != "" {
		t.Fatalf("Fruit identity = %q/%q, want none", res.Branch, res.Commit)
	}
	if len(res.VerificationDiagnostics) != 1 || res.VerificationDiagnostics[0].Outcome != core.SeedVerificationOutcomeInfrastructureFailed {
		t.Fatalf("diagnostics = %+v, want infrastructure-failed", res.VerificationDiagnostics)
	}
	if hits.Load() != 0 {
		t.Fatal("go.work Seed still fetched modules")
	}
}

func TestStomaCommandEnvironmentNeverEnablesDirectFallback(t *testing.T) {
	populate := stomaCommandEnvironment(StomaExecution{SkipHostModuleCache: true}, true)
	predicate := stomaCommandEnvironment(StomaExecution{SkipHostModuleCache: true, PopulateGoModuleCache: true}, false)
	vendor := stomaCommandEnvironment(StomaExecution{SkipHostModuleCache: true, GoVendorMode: true}, false)
	for label, env := range map[string]map[string]string{"populate": populate, "predicate": predicate, "vendor": vendor} {
		if strings.Contains(env["GOPROXY"], "direct") {
			t.Fatalf("%s GOPROXY = %q, must not enable direct VCS fallback", label, env["GOPROXY"])
		}
	}
	if predicate["GOPROXY"] != "off" {
		t.Fatalf("predicate GOPROXY = %q, want off", predicate["GOPROXY"])
	}
	if vendor["GOFLAGS"] != "-buildvcs=false -mod=vendor" {
		t.Fatalf("vendor GOFLAGS = %q", vendor["GOFLAGS"])
	}
}

type recordingStomaTerrarium struct {
	stubTerrarium
	commands []terrarium.CommandSpec
	run      func(int, terrarium.CommandSpec) (terrarium.CommandResult, error)
}

func (s *recordingStomaTerrarium) Run(_ context.Context, spec terrarium.CommandSpec) (terrarium.CommandResult, error) {
	s.commands = append(s.commands, spec)
	if s.run != nil {
		return s.run(len(s.commands)-1, spec)
	}
	return terrarium.CommandResult{}, nil
}

type testLeaf struct {
	objects      map[string][]byte
	goSum        string
	consumerMod  string
	consumerSrc  string
	consumerTest string
}

func testLeafModule(t *testing.T) testLeaf {
	t.Helper()
	modBytes := []byte("module example.com/leaf\n\ngo 1.25\n")
	src := "package leaf\n\nfunc Value() string { return \"ok\" }\n"
	zipBytes := zipModule(t, "example.com/leaf", "v1.0.0", map[string]string{
		"go.mod":  string(modBytes),
		"leaf.go": src,
	})
	info := []byte(`{"Version":"v1.0.0","Time":"2024-01-01T00:00:00Z"}`)
	escapedPath, err := module.EscapePath("example.com/leaf")
	if err != nil {
		t.Fatalf("EscapePath: %v", err)
	}
	objects := map[string][]byte{
		"/" + escapedPath + "/@v/v1.0.0.info": info,
		"/" + escapedPath + "/@v/v1.0.0.mod":  modBytes,
		"/" + escapedPath + "/@v/v1.0.0.zip":  zipBytes,
	}
	return testLeaf{
		objects:      objects,
		goSum:        goSumFor(t, "example.com/leaf", "v1.0.0", modBytes, zipBytes),
		consumerMod:  "module example.com/seed\n\ngo 1.25\n\nrequire example.com/leaf v1.0.0\n",
		consumerSrc:  "package seed\n\nimport \"example.com/leaf\"\n\nfunc Use() string { return leaf.Value() }\n",
		consumerTest: "package seed\n\nimport \"testing\"\n\nfunc TestUse(t *testing.T) {\n\tif Use() != \"ok\" {\n\t\tt.Fatalf(\"got %q\", Use())\n\t}\n}\n",
	}
}

func startGoModuleProxy(t *testing.T, objects map[string][]byte, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		body, ok := objects[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
}

func writeSeedGoFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func commitFiles(t *testing.T, repo string, files map[string]string) string {
	t.Helper()
	writeSeedGoFiles(t, repo, files)
	ctx := context.Background()
	if _, err := runGitCommand(ctx, repo, "add", "-A"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := runGitCommand(ctx, repo, "commit", "-m", "candidate"); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	commit, err := runGitCommand(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return strings.TrimSpace(commit)
}

func zipModule(t *testing.T, path, version string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	prefix := path + "@" + version + "/"
	for _, name := range names {
		file, err := writer.Create(prefix + name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := file.Write([]byte(files[name])); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func goSumFor(t *testing.T, path, version string, modBytes, zipBytes []byte) string {
	t.Helper()
	modHash, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(modBytes)), nil
	})
	if err != nil {
		t.Fatalf("hash go.mod: %v", err)
	}
	zipPath := filepath.Join(t.TempDir(), "module.zip")
	if err := os.WriteFile(zipPath, zipBytes, 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}
	zipHash, err := dirhash.HashZip(zipPath, dirhash.Hash1)
	if err != nil {
		t.Fatalf("hash zip: %v", err)
	}
	return fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", path, version, zipHash, path, version, modHash)
}

func writeReadonlyProxy(t *testing.T, payloads []terrarium.FilePayload) string {
	t.Helper()
	root := t.TempDir()
	prefix := stomaEgressDirectory + "/" + seedGoProxyRelativeRoot + "/"
	for _, payload := range payloads {
		rel := strings.TrimPrefix(payload.Path, prefix)
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir proxy %s: %v", path, err)
		}
		if err := os.WriteFile(path, payload.Content, 0o444); err != nil {
			t.Fatalf("write proxy %s: %v", path, err)
		}
	}
	return root
}

func mountByTarget(mounts []terrarium.MountSpec, target string) (terrarium.MountSpec, bool) {
	for _, mount := range mounts {
		if mount.Target == target {
			return mount, true
		}
	}
	return terrarium.MountSpec{}, false
}

func makeWritableForCleanup(t *testing.T, roots ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, root := range roots {
			_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil || info == nil {
					return nil
				}
				_ = os.Chmod(path, 0o755)
				return nil
			})
		}
	})
}

func isolatedGoEnv(t *testing.T, modCache, proxy string, proxyOff bool) []string {
	t.Helper()
	goproxy := proxy
	if proxyOff {
		goproxy = "off"
	}
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GOPROXY=" + goproxy,
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOMODCACHE=" + modCache,
		"GOCACHE=" + t.TempDir(),
		"GOPATH=" + t.TempDir(),
		"GOFLAGS=-mod=readonly -buildvcs=false",
		"GO111MODULE=on",
	}
}

func TestSeedGoPreparationErrorIsNotInferredFromStderr(t *testing.T) {
	err := seedGoPrepErrorf("locked module/version count %d exceeds the %d bound", 9, 8)
	if !strings.Contains(err.Error(), "prepare Seed Go verification") {
		t.Fatalf("error = %v, want a typed preparation error", err)
	}
	if errors.Is(err, errUnusableReply) {
		t.Fatal("preparation error was classified as a Sprout repair failure")
	}
}

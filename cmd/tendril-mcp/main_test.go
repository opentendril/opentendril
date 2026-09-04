package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opentendril/opentendril/internal/buildinfo"
	"github.com/opentendril/opentendril/internal/pollinatorconfig"
)

const (
	initFrame   = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	listFrame   = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	mintedToken = "minted-access-token-not-the-root"
)

type stemTraffic struct {
	mu       sync.Mutex
	healthN  int
	mintN    int
	v1N      int
	bodies   [][]byte
	v1Auths  []string
	mintAuth []string
}

type fakeStemOpts struct {
	owner      *int
	root       string
	healthBody []byte
}

func startFakeStem(t *testing.T, opts fakeStemOpts) (string, *stemTraffic) {
	t.Helper()
	traffic := &stemTraffic{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		traffic.mu.Lock()
		traffic.healthN++
		traffic.mu.Unlock()
		if opts.healthBody != nil {
			_, _ = w.Write(opts.healthBody)
			return
		}
		_ = json.NewEncoder(w).Encode(struct {
			Owner *int `json:"owner,omitempty"`
		}{Owner: opts.owner})
	})
	mux.HandleFunc("POST /v1/pollinator/token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		traffic.mu.Lock()
		traffic.mintN++
		traffic.mintAuth = append(traffic.mintAuth, auth)
		traffic.mu.Unlock()
		if auth != "Bearer "+opts.root {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":     mintedToken,
			"expiresAt": time.Now().Add(2 * time.Minute),
		})
	})
	mux.HandleFunc("POST /v1", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		traffic.mu.Lock()
		traffic.v1N++
		traffic.bodies = append(traffic.bodies, append([]byte(nil), body...))
		traffic.v1Auths = append(traffic.v1Auths, r.Header.Get("Authorization"))
		traffic.mu.Unlock()
		writeMCPResult(w, body)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL, traffic
}

func writeMCPResult(w http.ResponseWriter, body []byte) {
	var frame struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(body, &frame)
	if len(frame.ID) == 0 {
		frame.ID = json.RawMessage("null")
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(frame.ID),
		"result":  map[string]any{"ok": true},
	})
}

func configureConnection(t *testing.T, name, endpoint, credential, secret string) {
	t.Helper()
	path, err := pollinatorconfig.ResolveCredentialReference(credential)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pollinatorconfig.Save(pollinatorconfig.Config{
		Version: 1,
		Connections: map[string]pollinatorconfig.Connection{
			name: {Endpoint: endpoint, Credential: credential},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func configureConnections(t *testing.T, cfg pollinatorconfig.Config, credentials map[string]string) {
	t.Helper()
	for name, secret := range credentials {
		path, err := pollinatorconfig.ResolveCredentialReference(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pollinatorconfig.Save(cfg); err != nil {
		t.Fatal(err)
	}
}

func runArgs(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runCommand(context.Background(), args, strings.NewReader(initFrame+"\n"), &out, &errOut)
	return out.String(), errOut.String(), err
}

func pointLegacyEnvironmentAt(t *testing.T, endpoint string) {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERROIR_HOST", u.Hostname())
	t.Setenv("PORT", u.Port())
	t.Setenv("TENDRIL_POLLEN", "legacy")
	t.Setenv("TENDRIL_POLLINATOR_CREDENTIAL", filepath.Join(t.TempDir(), "legacy"))
	t.Setenv("TENDRIL_MCP_CREDENTIAL", filepath.Join(t.TempDir(), "legacy-two"))
}

func TestVersionRunsBeforeConfigurationOrNetwork(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing-config"))
	old := buildinfo.Version
	buildinfo.Version = "0.3.13+test"
	t.Cleanup(func() { buildinfo.Version = old })

	var out, errOut bytes.Buffer
	if err := runCommand(context.Background(), []string{"--version"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != "tendril-mcp 0.3.13+test\n" || errOut.Len() != 0 {
		t.Fatalf("version output = %q, stderr = %q", out.String(), errOut.String())
	}
}

func TestConnectionCommandsManageProfilesWithoutReadingSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	secret := "never-print-this-root"
	var out bytes.Buffer
	if err := runConnectionCommand([]string{"set", "local", "--endpoint", "http://127.0.0.1:8080", "--credential", "codex"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := runConnectionCommand([]string{"set", "backup", "--endpoint", "http://127.0.0.1:8081", "--credential", "other"}, &out); err != nil {
		t.Fatal(err)
	}
	configBytes, err := os.ReadFile(pollinatorconfig.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configBytes), secret) {
		t.Fatal("connection metadata contained credential contents")
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("connection command output leaked a credential secret")
	}

	path, err := pollinatorconfig.ResolveCredentialReference("codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runConnectionCommand([]string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "local") || strings.Contains(out.String(), "default") {
		t.Fatalf("list = %q, want names without an implicit default", out.String())
	}
	if err := runConnectionCommand([]string{"use", "local"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runConnectionCommand([]string{"show", "local"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: local", "endpoint: http://127.0.0.1:8080", "credential reference: codex", path} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("show = %q, missing %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("show output leaked credential contents")
	}
	if err := runConnectionCommand([]string{"remove", "local"}, &out); err == nil || !strings.Contains(err.Error(), "choose another default") {
		t.Fatalf("remove default error = %v", err)
	}
	if err := runConnectionCommand([]string{"remove", "backup"}, &out); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionSetCreatesCanonicalRestrictiveConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out bytes.Buffer
	if err := runConnectionCommand([]string{"set", "local", "--endpoint", "http://127.0.0.1:8080/", "--credential", "codex"}, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := pollinatorconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Default != "" || cfg.Connections["local"].Endpoint != "http://127.0.0.1:8080" {
		t.Fatalf("saved config = %+v, want no implicit default and normalized endpoint", cfg)
	}
	info, err := os.Stat(pollinatorconfig.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestExplicitAndDefaultSelectionIgnoreLegacyEnvironment(t *testing.T) {
	owner := os.Getuid() + 1
	endpointA, trafficA := startFakeStem(t, fakeStemOpts{owner: &owner, root: "root-a"})
	endpointB, trafficB := startFakeStem(t, fakeStemOpts{owner: &owner, root: "root-b"})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TERROIR_HOST", "127.0.0.1")
	t.Setenv("PORT", "1")
	t.Setenv("TENDRIL_POLLEN", "legacy")
	t.Setenv("TENDRIL_POLLINATOR_CREDENTIAL", filepath.Join(t.TempDir(), "legacy"))
	t.Setenv("TENDRIL_MCP_CREDENTIAL", filepath.Join(t.TempDir(), "legacy-two"))
	configureConnections(t, pollinatorconfig.Config{
		Version: 1,
		Default: "first",
		Connections: map[string]pollinatorconfig.Connection{
			"first":  {Endpoint: endpointA, Credential: "root-a"},
			"second": {Endpoint: endpointB, Credential: "root-b"},
		},
	}, map[string]string{"root-a": "root-a", "root-b": "root-b"})

	if _, _, err := runArgs(t, "--connection", "second"); err != nil {
		t.Fatal(err)
	}
	trafficA.mu.Lock()
	aHealth, aMint := trafficA.healthN, trafficA.mintN
	trafficA.mu.Unlock()
	trafficB.mu.Lock()
	bHealth, bMint := trafficB.healthN, trafficB.mintN
	trafficB.mu.Unlock()
	if aHealth != 0 || aMint != 0 || bHealth != 1 || bMint != 1 {
		t.Fatalf("explicit selection traffic A=(%d,%d) B=(%d,%d)", aHealth, aMint, bHealth, bMint)
	}

	if _, _, err := runArgs(t); err != nil {
		t.Fatal(err)
	}
	trafficA.mu.Lock()
	aHealth = trafficA.healthN
	aMint = trafficA.mintN
	trafficA.mu.Unlock()
	if aHealth != 1 || aMint != 1 {
		t.Fatalf("default selection traffic A=(%d,%d), want one additional preflight", aHealth, aMint)
	}
}

func TestRestrictedBridgeRefusesUnqualifiedTransportBeforeCredentialPresentation(t *testing.T) {
	owner := os.Getuid() + 1
	legacyEndpoint, traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: "legacy-root"})
	pointLegacyEnvironmentAt(t, legacyEndpoint)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	for _, endpoint := range []string{
		"http://192.0.2.10:8080",
		"https://127.0.0.1:8080",
	} {
		t.Run(endpoint, func(t *testing.T) {
			configureConnections(t, pollinatorconfig.Config{
				Version: 1,
				Connections: map[string]pollinatorconfig.Connection{
					"selected": {Endpoint: endpoint, Credential: "selected-root"},
				},
			}, map[string]string{"selected-root": "selected-durable-root"})

			_, _, err := runArgs(t, "--connection", "selected")
			if err == nil || !strings.Contains(err.Error(), "transport is not supported") {
				t.Fatalf("unqualified transport error = %v, want transport posture refusal", err)
			}
			if strings.Contains(err.Error(), "selected-durable-root") {
				t.Fatal("transport refusal exposed the durable credential")
			}
			traffic.mu.Lock()
			healthN, mintN := traffic.healthN, traffic.mintN
			traffic.mu.Unlock()
			if healthN != 0 || mintN != 0 {
				t.Fatalf("legacy endpoint received traffic health=%d mint=%d", healthN, mintN)
			}
		})
	}
}

func TestNoSelectionNeverFallsBackToLocalhost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configureConnections(t, pollinatorconfig.Config{Version: 1, Connections: map[string]pollinatorconfig.Connection{
		"local": {Endpoint: "http://127.0.0.1:8080", Credential: "codex"},
	}}, map[string]string{"codex": "root"})
	_, _, err := runArgs(t)
	if err == nil || !strings.Contains(err.Error(), "no connection selected") {
		t.Fatalf("run without selection = %v, want actionable selection error", err)
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "empty"))
	_, _, err = runArgs(t, "--connection", "local")
	if err == nil || !strings.Contains(err.Error(), "no Pollinator connection config") {
		t.Fatalf("missing config = %v, want fail-closed configuration error", err)
	}
}

func TestPreflightRefusesSameUIDAndInvalidCredentials(t *testing.T) {
	owner := os.Getuid()
	endpoint, traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: "root"})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configureConnection(t, "local", endpoint, "codex", "root")
	_, _, err := runArgs(t, "-c", "local")
	if err == nil || !strings.Contains(err.Error(), "separately owned governed Stem") {
		t.Fatalf("same-UID preflight = %v, want refusal", err)
	}
	traffic.mu.Lock()
	if traffic.mintN != 0 {
		t.Fatalf("same-UID preflight minted %d tokens", traffic.mintN)
	}
	traffic.mu.Unlock()

	owner = os.Getuid() + 1
	endpoint, traffic = startFakeStem(t, fakeStemOpts{owner: &owner, root: "accepted"})
	configureConnection(t, "local", endpoint, "codex", "rejected")
	_, _, err = runArgs(t, "-c", "local")
	if err == nil || !strings.Contains(err.Error(), "the Stem refused you") {
		t.Fatalf("invalid credential preflight = %v, want refusal", err)
	}
	traffic.mu.Lock()
	if traffic.v1N != 0 || traffic.mintN != 1 {
		t.Fatalf("invalid credential traffic = mint %d, v1 %d", traffic.mintN, traffic.v1N)
	}
	traffic.mu.Unlock()
}

func TestForwardingPreservesFramesAndDoesNotLeakCredential(t *testing.T) {
	owner := os.Getuid() + 1
	endpoint, traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: "secret-root"})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configureConnection(t, "local", endpoint, "codex", "secret-root")
	var out, errOut bytes.Buffer
	err := runCommand(context.Background(), []string{"--connection", "local"}, strings.NewReader(initFrame+"\n"+listFrame+"\n"), &out, &errOut)
	if err != nil {
		t.Fatalf("bridge: %v; stderr=%s", err, errOut.String())
	}
	if strings.Contains(out.String(), "secret-root") || strings.Contains(errOut.String(), "secret-root") {
		t.Fatal("credential appeared in bridge output")
	}
	traffic.mu.Lock()
	defer traffic.mu.Unlock()
	if traffic.v1N != 2 || string(traffic.bodies[0]) != initFrame || string(traffic.bodies[1]) != listFrame {
		t.Fatalf("forwarded traffic = %d %#v", traffic.v1N, traffic.bodies)
	}
	for _, auth := range traffic.v1Auths {
		if auth != "Bearer "+mintedToken {
			t.Fatalf("forwarded auth = %q, want minted token", auth)
		}
	}
}

func TestDiagnoseReportsNonSecretPreflightState(t *testing.T) {
	owner := os.Getuid() + 1
	endpoint, traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: "diagnose-root"})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configureConnection(t, "local", endpoint, "codex", "diagnose-root")
	stdout, _, err := runArgs(t, "diagnose", "-c", "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"tendril-mcp version:",
		"config file:",
		"selected connection: local",
		"selection source: explicit",
		"endpoint: " + endpoint,
		"credential reference: codex",
		"credential path:",
		"credential permission/readability: accepted",
		"Stem reachable: yes",
		"reported Stem owner: uid",
		"same-principal refusal: no",
		"authentication: accepted",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("diagnose output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "diagnose-root") || strings.Contains(stdout, mintedToken) {
		t.Fatal("diagnose leaked credential or access token")
	}
	traffic.mu.Lock()
	healthN, mintN := traffic.healthN, traffic.mintN
	traffic.mu.Unlock()
	if healthN != 1 || mintN != 1 {
		t.Fatalf("loopback diagnose traffic health=%d mint=%d, want one governed preflight", healthN, mintN)
	}
}

func TestDiagnoseReportsUnsupportedTransportWithoutCredentialOrNetworkAccess(t *testing.T) {
	owner := os.Getuid() + 1
	legacyEndpoint, traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: "legacy-root"})
	pointLegacyEnvironmentAt(t, legacyEndpoint)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := pollinatorconfig.Save(pollinatorconfig.Config{
		Version: 1,
		Connections: map[string]pollinatorconfig.Connection{
			"remote": {Endpoint: "https://127.0.0.1:8080", Credential: "missing-root"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runArgs(t, "diagnose", "--connection", "remote")
	if err == nil {
		t.Fatal("unsupported transport diagnose succeeded")
	}
	for _, want := range []string{
		"selected connection: remote",
		"endpoint: https://127.0.0.1:8080",
		"transport posture: unsupported",
		"authentication: refused (transport posture is not supported",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("diagnose output missing %q:\n%s", want, stdout)
		}
	}
	for _, forbidden := range []string{
		"credential permission/readability:",
		"credential path:",
		"Stem reachable:",
	} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("diagnose output contains %q despite transport refusal:\n%s", forbidden, stdout)
		}
	}
	traffic.mu.Lock()
	healthN, mintN := traffic.healthN, traffic.mintN
	traffic.mu.Unlock()
	if healthN != 0 || mintN != 0 {
		t.Fatalf("unsupported diagnose traffic health=%d mint=%d", healthN, mintN)
	}
}

func TestDiagnoseReportsUnreachableAndInvalidCredential(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	configureConnection(t, "local", "http://127.0.0.1:1", "codex", "root")
	stdout, _, err := runArgs(t, "diagnose", "-c", "local")
	if err == nil || !strings.Contains(stdout, "Stem reachable: no") || !strings.Contains(stdout, "authentication: refused") {
		t.Fatalf("unreachable diagnose = err %v output:\n%s", err, stdout)
	}

	owner := os.Getuid() + 1
	endpoint, _ := startFakeStem(t, fakeStemOpts{owner: &owner, root: "accepted"})
	configureConnection(t, "local", endpoint, "codex", "")
	stdout, _, err = runArgs(t, "diagnose", "-c", "local")
	if err == nil || !strings.Contains(stdout, "credential permission/readability: refused") || !strings.Contains(stdout, "authentication: refused") {
		t.Fatalf("invalid credential diagnose = err %v output:\n%s", err, stdout)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opentendril/opentendril/internal/mcpclient"
)

const (
	initFrame   = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"tendril-mcp-test","version":"0"}}}`
	listFrame   = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	callFrame   = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"example-tool","arguments":{}}}`
	stemDenial  = `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"delegation denied: not granted"}],"isError":true}}`
	mintedToken = "minted-access-token-not-the-root"
)

type stemTraffic struct {
	mu        sync.Mutex
	healthN   int
	mintN     int
	v1N       int
	v1Bodies  [][]byte
	v1Auths   []string
	mintAuths []string
}

func (s *stemTraffic) snapshot() (healthN, mintN, v1N int, bodies [][]byte, v1Auths, mintAuths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bodies = append([][]byte(nil), s.v1Bodies...)
	v1Auths = append([]string(nil), s.v1Auths...)
	mintAuths = append([]string(nil), s.mintAuths...)
	return s.healthN, s.mintN, s.v1N, bodies, v1Auths, mintAuths
}

type fakeStemOpts struct {
	owner      *int
	root       string
	token      string
	healthBody []byte
	v1Handler  func(w http.ResponseWriter, r *http.Request, body []byte)
}

func startFakeStem(t *testing.T, opts fakeStemOpts) *stemTraffic {
	t.Helper()
	traffic := &stemTraffic{}
	token := opts.token
	if token == "" {
		token = mintedToken
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		traffic.mu.Lock()
		traffic.healthN++
		traffic.mu.Unlock()
		if opts.healthBody != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(opts.healthBody)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Owner *int `json:"owner,omitempty"`
		}{Owner: opts.owner})
	})
	mux.HandleFunc("POST /v1/pollinator/token", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		traffic.mu.Lock()
		traffic.mintN++
		traffic.mintAuths = append(traffic.mintAuths, auth)
		traffic.mu.Unlock()
		if auth != "Bearer "+opts.root {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":     token,
			"pollen":    "test",
			"expiresAt": time.Now().Add(2 * time.Minute),
		})
	})
	mux.HandleFunc("POST /v1", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		auth := r.Header.Get("Authorization")
		traffic.mu.Lock()
		traffic.v1N++
		traffic.v1Bodies = append(traffic.v1Bodies, append([]byte(nil), body...))
		traffic.v1Auths = append(traffic.v1Auths, auth)
		traffic.mu.Unlock()
		if opts.v1Handler != nil {
			opts.v1Handler(w, r, body)
			return
		}
		writeMCPResult(w, body)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	pointAt(t, server.URL)
	return traffic
}

func writeMCPResult(w http.ResponseWriter, body []byte) {
	var frame struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	_ = json.Unmarshal(body, &frame)
	id := json.RawMessage("null")
	if len(frame.ID) > 0 {
		id = frame.ID
	}

	var result any
	switch frame.Method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "governed-stem", "version": "0"},
		}
	case "tools/list":
		result = map[string]any{
			"tools": []map[string]any{
				{"name": "example-tool", "description": "example", "inputSchema": map[string]any{"type": "object"}},
			},
		}
	default:
		result = map[string]any{"ok": true}
	}
	out, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func pointAt(t *testing.T, rawURL string) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse server URL %q: %v", rawURL, err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port %q: %v", u.Host, err)
	}
	t.Setenv("TERROIR_HOST", host)
	t.Setenv("PORT", port)
}

func writeCred(t *testing.T, secret string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte(secret), mode); err != nil {
		t.Fatalf("write credential: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod credential: %v", err)
	}
	return path
}

func setDurableCredential(t *testing.T, secret string) string {
	t.Helper()
	path := writeCred(t, secret, 0o600)
	t.Setenv(mcpclient.EnvPollinatorCredential, path)
	t.Setenv(mcpclient.EnvMCPCredential, "")
	t.Setenv("TENDRIL_POLLEN", "")
	return path
}

func unsetCredentials(t *testing.T) {
	t.Helper()
	t.Setenv(mcpclient.EnvPollinatorCredential, "")
	t.Setenv(mcpclient.EnvMCPCredential, "")
	t.Setenv("TENDRIL_POLLEN", "")
}

func otherOwner() int {
	return os.Getuid() + 1
}

func runStdio(t *testing.T, ctx context.Context, stdin string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = run(ctx, bytes.NewBufferString(stdin), &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestValidCredentialSeparatelyOwnedStemForwardsMCP(t *testing.T) {
	root := "tendril_refresh_valid_root"
	setDurableCredential(t, root)
	owner := otherOwner()
	traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: root})

	stdout, stderr, err := runStdio(t, context.Background(), initFrame+"\n"+listFrame+"\n")
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}

	lines := nonEmptyLines(stdout)
	if len(lines) != 2 {
		t.Fatalf("stdout lines = %d, want 2\nstdout: %s", len(lines), stdout)
	}

	var initResp struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatalf("initialize response: %v (%s)", err, lines[0])
	}
	if len(initResp.Error) > 0 && string(initResp.Error) != "null" {
		t.Fatalf("initialize returned error: %s", lines[0])
	}
	if initResp.Result.ProtocolVersion == "" || initResp.Result.ServerInfo.Name == "" {
		t.Fatalf("initialize result not protocol-shaped: %s", lines[0])
	}

	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("tools/list response: %v (%s)", err, lines[1])
	}
	if len(listResp.Result.Tools) == 0 || listResp.Result.Tools[0].Name == "" {
		t.Fatalf("tools/list missing tool name: %s", lines[1])
	}

	_, _, v1N, bodies, v1Auths, _ := traffic.snapshot()
	if v1N != 2 {
		t.Fatalf("/v1 calls = %d, want 2", v1N)
	}
	if string(bodies[0]) != initFrame {
		t.Fatalf("/v1 body 0 = %s, want initialize frame", bodies[0])
	}
	if string(bodies[1]) != listFrame {
		t.Fatalf("/v1 body 1 = %s, want tools/list frame", bodies[1])
	}
	for i, auth := range v1Auths {
		if auth != "Bearer "+mintedToken {
			t.Fatalf("/v1 Authorization[%d] = %q, want minted token", i, auth)
		}
		if strings.Contains(auth, root) {
			t.Fatal("durable root used as /v1 Authorization")
		}
	}
}

func TestUnreachableStemFailsClosed(t *testing.T) {
	setDurableCredential(t, "tendril_refresh_unreachable")
	t.Setenv("TERROIR_HOST", "127.0.0.1")
	t.Setenv("PORT", "65534")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stdout, _, err := runStdio(t, ctx, initFrame+"\n")
	if err == nil {
		t.Fatal("expected fail-closed when no Stem is answering")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if !strings.Contains(err.Error(), "no Stem is answering") {
		t.Fatalf("error must name that no Stem is answering, got %v", err)
	}
	if !strings.Contains(err.Error(), "65534") {
		t.Fatalf("error must name the probed address, got %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal("run hung until the test deadline")
	}
}

func TestHealthWithoutOwnerFailsClosed(t *testing.T) {
	root := "tendril_refresh_no_owner"
	setDurableCredential(t, root)
	traffic := startFakeStem(t, fakeStemOpts{owner: nil, root: root, healthBody: []byte(`{}`)})

	stdout, _, err := runStdio(t, context.Background(), initFrame+"\n")
	if err == nil {
		t.Fatal("expected fail-closed when ownership is not established")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if !strings.Contains(err.Error(), "ownership was not established") {
		t.Fatalf("error must name missing ownership, got %v", err)
	}
	_, mintN, v1N, _, _, _ := traffic.snapshot()
	if mintN != 0 || v1N != 0 {
		t.Fatalf("forwarding started: mint=%d v1=%d", mintN, v1N)
	}
}

func TestSameOwnerFailsClosed(t *testing.T) {
	root := "tendril_refresh_same_owner"
	setDurableCredential(t, root)
	owner := os.Getuid()
	traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: root})

	stdout, _, err := runStdio(t, context.Background(), initFrame+"\n")
	if err == nil {
		t.Fatal("expected fail-closed when the Stem reports this process's uid")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if !strings.Contains(err.Error(), "separately owned governed Stem") {
		t.Fatalf("error must refuse same-owner Stem, got %v", err)
	}
	_, mintN, v1N, _, _, _ := traffic.snapshot()
	if mintN != 0 || v1N != 0 {
		t.Fatalf("forwarding started: mint=%d v1=%d", mintN, v1N)
	}
}

func TestMissingCredentialFailsClosed(t *testing.T) {
	unsetCredentials(t)
	owner := otherOwner()
	traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: "unused"})

	stdout, _, err := runStdio(t, context.Background(), initFrame+"\n")
	if err == nil {
		t.Fatal("expected fail-closed when no credential is configured")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	msg := err.Error()
	for _, want := range []string{
		mcpclient.EnvPollinatorCredential,
		mcpclient.EnvMCPCredential,
		"TENDRIL_POLLEN",
		"~/.config/tendril/pollinators/<pollen>",
		"tendril pollinator issue",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("actionable error missing %q, got %v", want, err)
		}
	}
	_, mintN, v1N, _, _, _ := traffic.snapshot()
	if mintN != 0 || v1N != 0 {
		t.Fatalf("forwarding started: mint=%d v1=%d", mintN, v1N)
	}
}

func TestUnsafeCredentialFailsClosed(t *testing.T) {
	secret := "tendril_refresh_MUST_NOT_LEAK_unsafe"
	path := writeCred(t, secret, 0o644)
	t.Setenv(mcpclient.EnvPollinatorCredential, path)
	t.Setenv(mcpclient.EnvMCPCredential, "")
	t.Setenv("TENDRIL_POLLEN", "")

	owner := otherOwner()
	traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: secret})

	stdout, stderr, err := runStdio(t, context.Background(), initFrame+"\n")
	if err == nil {
		t.Fatal("expected fail-closed for a too-permissive credential file")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	msg := err.Error()
	if !strings.Contains(msg, "too permissive") || !strings.Contains(msg, "0600") {
		t.Fatalf("error must mention too permissive / 0600, got %v", err)
	}
	if strings.Contains(msg, secret) || strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
		t.Fatal("secret leaked into error, stdout, or stderr")
	}
	_, mintN, v1N, _, _, _ := traffic.snapshot()
	if mintN != 0 || v1N != 0 {
		t.Fatalf("forwarding started: mint=%d v1=%d", mintN, v1N)
	}
}

func TestAuthorizationRefusalPassedUnaltered(t *testing.T) {
	root := "tendril_refresh_denied"
	setDurableCredential(t, root)
	owner := otherOwner()
	traffic := startFakeStem(t, fakeStemOpts{
		owner: &owner,
		root:  root,
		v1Handler: func(w http.ResponseWriter, r *http.Request, body []byte) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(stemDenial))
		},
	})

	stdout, stderr, err := runStdio(t, context.Background(), callFrame+"\n")
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}
	if stdout != stemDenial+"\n" {
		t.Fatalf("client rewrote the Stem denial\n got: %q\nwant: %q", stdout, stemDenial+"\n")
	}
	_, _, v1N, _, _, _ := traffic.snapshot()
	if v1N != 1 {
		t.Fatalf("/v1 calls = %d, want 1", v1N)
	}
}

func TestInProcessEnvDoesNotAlterBehavior(t *testing.T) {
	root := "tendril_refresh_in_process_check"
	setDurableCredential(t, root)

	t.Setenv("TERROIR_HOST", "127.0.0.1")
	t.Setenv("PORT", "65534")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Setenv("TENDRIL_MCP_IN_PROCESS", "")
	outUnset, _, errUnset := runStdio(t, ctx, "")
	t.Setenv("TENDRIL_MCP_IN_PROCESS", "1")
	outSet, _, errSet := runStdio(t, ctx, "")

	if errUnset == nil || errSet == nil {
		t.Fatalf("both no-Stem runs must fail closed; unset=%v set=%v", errUnset, errSet)
	}
	if errUnset.Error() != errSet.Error() {
		t.Fatalf("TENDRIL_MCP_IN_PROCESS=1 changed the no-Stem error\nunset: %v\nset:   %v", errUnset, errSet)
	}
	if outUnset != "" || outSet != "" {
		t.Fatalf("no-Stem runs must not write stdout; unset=%q set=%q", outUnset, outSet)
	}

	owner := otherOwner()
	_ = startFakeStem(t, fakeStemOpts{owner: &owner, root: root})

	t.Setenv("TENDRIL_MCP_IN_PROCESS", "")
	outUnset, stderrUnset, errUnset := runStdio(t, context.Background(), initFrame+"\n")
	t.Setenv("TENDRIL_MCP_IN_PROCESS", "1")
	outSet, stderrSet, errSet := runStdio(t, context.Background(), initFrame+"\n")

	if errUnset != nil || errSet != nil {
		t.Fatalf("both other-owner runs must start forwarding; unset=%v set=%v\nstderr unset=%s\nstderr set=%s", errUnset, errSet, stderrUnset, stderrSet)
	}
	if strings.TrimSpace(outUnset) == "" || strings.TrimSpace(outSet) == "" {
		t.Fatalf("both other-owner runs must pass a frame; unset=%q set=%q", outUnset, outSet)
	}
}

func TestDurableRootNeverAppears(t *testing.T) {
	secret := fmt.Sprintf("tendril_refresh_MUST_NOT_LEAK_%d", time.Now().UnixNano())
	setDurableCredential(t, secret)
	owner := otherOwner()
	traffic := startFakeStem(t, fakeStemOpts{owner: &owner, root: secret})

	stdout, stderr, err := runStdio(t, context.Background(), initFrame+"\n")
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("expected an initialize response")
	}

	if strings.Contains(stdout, secret) {
		t.Fatal("durable root leaked onto stdout")
	}
	if strings.Contains(stderr, secret) {
		t.Fatal("durable root leaked onto stderr")
	}
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Fatal("durable root leaked into the returned error")
	}
	for _, arg := range os.Args {
		if strings.Contains(arg, secret) {
			t.Fatal("durable root leaked into os.Args")
		}
	}
	_, _, _, bodies, v1Auths, mintAuths := traffic.snapshot()
	for _, body := range bodies {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatal("durable root leaked into a forwarded MCP frame")
		}
	}
	for _, auth := range v1Auths {
		if strings.Contains(auth, secret) {
			t.Fatal("durable root used as /v1 Authorization")
		}
	}
	if len(mintAuths) == 0 || mintAuths[0] != "Bearer "+secret {
		t.Fatalf("mint must present the durable root as Authorization, got %v", mintAuths)
	}
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

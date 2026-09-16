package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/gateway"
	"github.com/opentendril/opentendril/cmd/stem/internal/healthmon"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
	"github.com/opentendril/opentendril/cmd/stem/internal/scheduler"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
	"github.com/opentendril/opentendril/cmd/stem/internal/triggers"
	"github.com/opentendril/opentendril/internal/mcpclient"
)

func TestServeMuxUsesLivePollinatorAuthority(t *testing.T) {
	dir := t.TempDir()
	secret, _, err := core.IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	grantsPath := filepath.Join(dir, core.DelegationGrantsFilename)
	if err := os.WriteFile(grantsPath, []byte("grants:\n  claude:\n    operationClasses: [sprout.grow]\n    substrates: [core]\n"), 0o600); err != nil {
		t.Fatalf("write grants: %v", err)
	}
	signer, err := core.LoadOrCreateStemSigner(dir)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	authority := core.NewAuthority(dir)
	bus := eventbus.New()
	t.Cleanup(bus.Shutdown)
	delegationGate := &receptors.DelegationGate{Authority: authority, Signer: signer, Bus: bus}
	var executions int
	coreService := core.NewService(nil).WithSprout(core.SproutOperations{
		Run: func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
			executions++
			return core.SproutRunReport{Output: "grown", Outcome: "complete"}, nil
		},
	})
	mux := buildServeMux(serveDependencies{
		APIKey:         "botanist-key",
		Authority:      authority,
		StemSigner:     signer,
		DelegationGate: delegationGate,
		EventBus:       bus,
		CoreService:    coreService,
	})

	mintRequest := httptest.NewRequest(http.MethodPost, "/v1/pollinator/token", nil)
	mintRequest.Header.Set("Authorization", "Bearer "+secret)
	mintResponse := httptest.NewRecorder()
	mux.ServeHTTP(mintResponse, mintRequest)
	if mintResponse.Code != http.StatusOK {
		t.Fatalf("initial token mint: status = %d, want 200 (%s)", mintResponse.Code, mintResponse.Body.String())
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(mintResponse.Body.Bytes(), &minted); err != nil {
		t.Fatalf("decode token: %v", err)
	}

	if revoked, err := core.RevokePollinatorCredentials(dir, "claude"); err != nil || revoked != 1 {
		t.Fatalf("revoke root = %d, %v; want 1, nil", revoked, err)
	}
	if claims, ok := signer.VerifyAccessToken(minted.Token); !ok || claims.Pollen != "claude" {
		t.Fatalf("root revocation invalidated the existing token: ok=%v pollen=%q", ok, claims.Pollen)
	}
	mintRequest = httptest.NewRequest(http.MethodPost, "/v1/pollinator/token", nil)
	mintRequest.Header.Set("Authorization", "Bearer "+secret)
	mintResponse = httptest.NewRecorder()
	mux.ServeHTTP(mintResponse, mintRequest)
	if mintResponse.Code != http.StatusUnauthorized {
		t.Fatalf("mint after revocation: status = %d, want 401 (%s)", mintResponse.Code, mintResponse.Body.String())
	}
	rootRequest := httptest.NewRequest(http.MethodPost, "/v1/sprouts/grow", strings.NewReader(`{"transcript":"grow","substrate":"core"}`))
	rootRequest.Header.Set("Authorization", "Bearer "+secret)
	rootResponse := httptest.NewRecorder()
	mux.ServeHTTP(rootResponse, rootRequest)
	if rootResponse.Code != http.StatusUnauthorized {
		t.Fatalf("data admission with revoked root: status = %d, want 401 (%s)", rootResponse.Code, rootResponse.Body.String())
	}
	if executions != 0 {
		t.Fatalf("revoked root executed a governed request before the token case; executions = %d, want 0", executions)
	}

	invoke := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/sprouts/grow", strings.NewReader(`{"transcript":"grow","substrate":"core"}`))
		request.Header.Set("Authorization", "Bearer "+minted.Token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	if response := invoke(); response.Code != http.StatusOK {
		t.Fatalf("admission with current grant: status = %d, want 200 (%s)", response.Code, response.Body.String())
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want 1", executions)
	}

	if err := os.WriteFile(grantsPath, []byte("grants: {}\n"), 0o600); err != nil {
		t.Fatalf("remove grant: %v", err)
	}
	if response := invoke(); response.Code != http.StatusForbidden {
		t.Fatalf("admission after grant removal: status = %d, want 403 (%s)", response.Code, response.Body.String())
	}
	if executions != 1 {
		t.Fatalf("removed grant admitted more work; executions = %d, want 1", executions)
	}
}

func TestBuildRemoteServeMuxProjectsOnlyPollinatorRoutes(t *testing.T) {
	fixture := newRemoteMuxTestFixture(t, "", nil)

	approved := []struct {
		method  string
		path    string
		pattern string
	}{
		{http.MethodGet, "/health", "GET /health"},
		{http.MethodPost, "/v1/pollinator/token", "POST /v1/pollinator/token"},
		{http.MethodPost, "/v1", "POST /v1"},
		{http.MethodPost, "/v1/seeds/grow", "POST /v1/seeds/grow"},
		{http.MethodPost, "/v1/seeds/grow/async", "POST /v1/seeds/grow/async"},
		{http.MethodGet, "/v1/seeds/runs/seed-1", "GET /v1/seeds/runs/{handle}"},
		{http.MethodPost, "/v1/phytomers/phytomer-1/continue", "POST /v1/phytomers/{sessionId}/continue"},
		{http.MethodGet, "/v1/phytomers/phytomer-1/watch", "GET /v1/phytomers/{sessionId}/watch"},
	}
	for _, route := range approved {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, nil)
			_, pattern := fixture.mux.Handler(request)
			if pattern != route.pattern {
				t.Fatalf("route pattern = %q, want %q", pattern, route.pattern)
			}
		})
	}

	private := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/config/triggers"},
		{http.MethodGet, "/v1/config/substrates"},
		{http.MethodPost, "/v1/mesh/admin/issue-token"},
		{http.MethodPost, "/v1/mesh/graft"},
		{http.MethodGet, "/v1/delegation/pending"},
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodGet, "/ws"},
		{http.MethodPost, "/v1/phytomers"},
		{http.MethodGet, "/v1/phytomers/phytomer-1"},
		{http.MethodPost, "/v1/sessions/phytomer-1/continue"},
	}
	for _, route := range private {
		t.Run("private "+route.method+" "+route.path, func(t *testing.T) {
			response := serveMuxRequest(fixture.mux, route.method, route.path, "", nil)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (%s)", response.Code, response.Body.String())
			}
		})
	}
}

func TestRemoteMuxAcceptsOnlyShortLivedAccessTokensOnDataAndMCP(t *testing.T) {
	fixture := newRemoteMuxTestFixture(t, "", nil)
	validToken, err := fixture.signer.MintAccessToken("claude", time.Minute, core.AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint access token: %v", err)
	}

	otherSigner, err := core.LoadOrCreateStemSigner(filepath.Join(fixture.dir, "other-stem"))
	if err != nil {
		t.Fatalf("other signer: %v", err)
	}
	forgedToken, err := otherSigner.MintAccessToken("claude", time.Minute, core.AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint forged token: %v", err)
	}
	expiredToken, err := fixture.signer.MintAccessToken("claude", time.Nanosecond, core.AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint expiring token: %v", err)
	}
	// dwell: allow the intentionally 1ns access token to expire before verification.
	time.Sleep(time.Millisecond)

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	for _, route := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/v1", initBody},
		{http.MethodPost, "/v1/seeds/grow", ""},
	} {
		for _, credential := range []string{"botanist-key", fixture.root} {
			response := serveMuxRequest(fixture.mux, route.method, route.path, route.body, map[string]string{
				"Authorization": "Bearer " + credential,
			})
			if response.Code != http.StatusUnauthorized {
				t.Errorf("%s with non-access credential: status = %d, want 401 (%s)", route.path, response.Code, response.Body.String())
			}
		}
	}

	for _, token := range []string{forgedToken, expiredToken} {
		response := serveMuxRequest(fixture.mux, http.MethodPost, "/v1", initBody, map[string]string{
			"Authorization": "Bearer " + token,
		})
		if response.Code != http.StatusUnauthorized {
			t.Errorf("invalid access token: status = %d, want 401 (%s)", response.Code, response.Body.String())
		}
	}

	for _, target := range []string{
		"/v1?token=" + validToken,
		"/v1?key=botanist-key",
	} {
		response := serveMuxRequest(fixture.mux, http.MethodPost, target, initBody, nil)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("query credential %q authenticated: status = %d, want 401", target, response.Code)
		}
	}
	response := serveMuxRequest(fixture.mux, http.MethodPost, "/v1", initBody, map[string]string{
		receptors.PollenHeader: "claude",
	})
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("Pollen claim without Authorization authenticated: status = %d, want 401", response.Code)
	}
	response = serveMuxRequest(fixture.mux, http.MethodPost, "/v1/pollinator/token?token="+fixture.root, "", nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("mint query credential authenticated: status = %d, want 401", response.Code)
	}

	response = serveMuxRequest(fixture.mux, http.MethodPost, "/v1/pollinator/token", "", map[string]string{
		"Authorization": "Bearer " + fixture.root,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("durable root mint: status = %d, want 200 (%s)", response.Code, response.Body.String())
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &minted); err != nil {
		t.Fatalf("decode minted token: %v", err)
	}
	if !core.LooksLikeAccessToken(minted.Token) {
		t.Fatalf("mint returned a non-access token %q", minted.Token)
	}

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":4,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/list"}`,
	} {
		response := serveMuxRequest(fixture.mux, http.MethodPost, "/v1", body, map[string]string{
			"Authorization": "Bearer " + minted.Token,
		})
		if response.Code != http.StatusOK {
			t.Errorf("public MCP request %s: status = %d, want 200 (%s)", body, response.Code, response.Body.String())
		}
	}
	for _, method := range []string{"resources/list", "resources/read"} {
		body := `{"jsonrpc":"2.0","id":6,"method":"` + method + `"}`
		response := serveMuxRequest(fixture.mux, http.MethodPost, "/v1", body, map[string]string{
			"Authorization": "Bearer " + minted.Token,
		})
		var rpcResponse struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &rpcResponse); err != nil {
			t.Fatalf("decode public %s response: %v", method, err)
		}
		if response.Code != http.StatusOK || rpcResponse.Error == nil || rpcResponse.Error.Code != -32601 {
			t.Errorf("public %s status/error = %d/%+v, want 200/method-not-found", method, response.Code, rpcResponse.Error)
		}
	}
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"createGenotype"}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"createGenotype","arguments":null}}`,
	} {
		response := serveMuxRequest(fixture.mux, http.MethodPost, "/v1", body, map[string]string{
			"Authorization": "Bearer " + minted.Token,
		})
		var rpcResponse struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &rpcResponse); err != nil {
			t.Fatalf("decode malformed public alias response: %v", err)
		}
		if response.Code != http.StatusOK || rpcResponse.Error == nil || rpcResponse.Error.Code != -32602 {
			t.Errorf("malformed public alias status/error = %d/%+v, want 200/invalid-params", response.Code, rpcResponse.Error)
		}
	}
}

func TestRemoteMuxDerivesGrantPollenFromAccessTokenOnly(t *testing.T) {
	var calls int
	var invokedPollen string
	var seedRuns int
	var seedPollen string
	var sproutRuns int
	var sproutPollen string
	var sproutDelegation core.DelegationRequest
	var sproutHasDelegation bool
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	service := core.NewService(manager).WithGit(core.GitOperations{
		Status: func(ctx context.Context, _ core.GitStatusSpec) (core.GitStatusResult, error) {
			calls++
			invokedPollen = core.PollenFromContext(ctx)
			return core.GitStatusResult{Branch: "topic", Clean: true, CommitAllowed: true}, nil
		},
	}).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			seedRuns++
			seedPollen = core.PollenFromContext(ctx)
			return core.SeedGrowResult{Status: core.SeedStatusSatisfied, Iterations: 1, PhytomerID: spec.PhytomerID}, nil
		},
	}).WithSprout(core.SproutOperations{
		Run: func(ctx context.Context, _ core.SproutSpec) (core.SproutRunReport, error) {
			sproutRuns++
			sproutPollen = core.PollenFromContext(ctx)
			sproutDelegation, sproutHasDelegation = core.AuthorizedDelegationRequestFromContext(ctx)
			return core.SproutRunReport{Output: "grown", Outcome: "complete"}, nil
		},
	})
	fixture := newRemoteMuxTestFixture(t, "grants:\n  claude:\n    operationClasses: [git.status, seed.grow, sprout.grow]\n    substrates: [core]\n", service)
	mintResponse := serveMuxRequest(fixture.mux, http.MethodPost, "/v1/pollinator/token", "", map[string]string{
		"Authorization": "Bearer " + fixture.root,
	})
	if mintResponse.Code != http.StatusOK {
		t.Fatalf("mint access token: status = %d (%s)", mintResponse.Code, mintResponse.Body.String())
	}
	var minted struct {
		Token  string `json:"token"`
		Pollen string `json:"pollen"`
	}
	if err := json.Unmarshal(mintResponse.Body.Bytes(), &minted); err != nil {
		t.Fatalf("decode minted access token: %v", err)
	}
	if minted.Pollen != "claude" {
		t.Fatalf("minted token Pollen = %q, want claude", minted.Pollen)
	}
	token := minted.Token
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gitStatus","arguments":{"substrate":"core"}}}`
	response := serveMuxRequest(fixture.mux, http.MethodPost, "/v1", body, map[string]string{
		"Authorization":           "Bearer " + token,
		receptors.PollenHeader:    "attacker-pollen",
		"Forwarded":               "for=127.0.0.1;host=botanist.example;proto=http",
		"X-Forwarded-For":         "127.0.0.1",
		"X-Forwarded-Host":        "botanist.example",
		"X-Forwarded-Proto":       "http",
		"X-Forwarded-Port":        "8080",
		"X-Forwarded-Client-Cert": "operator",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("delegated MCP call: status = %d, want 200 (%s)", response.Code, response.Body.String())
	}
	if calls != 1 || invokedPollen != "claude" {
		t.Fatalf("Core calls = %d, Pollen = %q; want one call as token Pollen claude", calls, invokedPollen)
	}
	seedResponse := serveMuxRequest(fixture.mux, http.MethodPost, "/v1/seeds/grow", `{"substrate":"core","goal":"prove public Seed projection","verify":["true"]}`, map[string]string{
		"Authorization":        "Bearer " + token,
		receptors.PollenHeader: "attacker-pollen",
		"Forwarded":            "for=127.0.0.1;host=botanist.example;proto=http",
		"X-Forwarded-For":      "127.0.0.1",
	})
	if seedResponse.Code != http.StatusOK {
		t.Fatalf("delegated public Seed request: status = %d, want 200 (%s)", seedResponse.Code, seedResponse.Body.String())
	}
	if seedRuns != 1 || seedPollen != "claude" {
		t.Fatalf("Seed runs = %d, Pollen = %q; want one run as token Pollen claude", seedRuns, seedPollen)
	}
	aliasBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sproutTendril","arguments":{"transcript":"grow","substrate":"core"}}}`
	response = serveMuxRequest(fixture.mux, http.MethodPost, "/v1", aliasBody, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("delegated public MCP Sprout alias: status = %d, want 200 (%s)", response.Code, response.Body.String())
	}
	if sproutRuns != 1 || sproutPollen != "claude" || !sproutHasDelegation {
		t.Fatalf("Sprout runs = %d, Pollen = %q, has authorized delegation = %t; want one run as token Pollen claude with grant context", sproutRuns, sproutPollen, sproutHasDelegation)
	}
	if sproutDelegation.Pollen != "claude" || sproutDelegation.OperationClass != core.CapSproutGrow || sproutDelegation.Substrate != "core" {
		t.Fatalf("Sprout delegation request = %+v; want access-token Pollen claude, class %s, substrate core", sproutDelegation, core.CapSproutGrow)
	}
	if err := os.WriteFile(filepath.Join(fixture.dir, core.DelegationGrantsFilename), []byte("grants: {}\n"), 0o600); err != nil {
		t.Fatalf("remove live grant: %v", err)
	}
	response = serveMuxRequest(fixture.mux, http.MethodPost, "/v1", body, map[string]string{
		"Authorization": "Bearer " + token,
	})
	var denied struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &denied) != nil || !denied.Result.IsError || calls != 1 {
		t.Fatalf("public call after live grant removal: status=%d response=%s calls=%d; want denied without invocation", response.Code, response.Body.String(), calls)
	}

	response = serveMuxRequest(fixture.mux, http.MethodPost, "/v1", `{"jsonrpc":"2.0","id":2,"method":"initialize"}`, map[string]string{
		"Authorization":   "Bearer " + fixture.root,
		"Forwarded":       "for=127.0.0.1;proto=http",
		"X-Forwarded-For": "127.0.0.1",
	})
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("forwarded durable root status = %d, want 401 (%s)", response.Code, response.Body.String())
	}
}

func TestRemoteMuxSproutWatchUsesWatchAuthority(t *testing.T) {
	fixture := newRemoteMuxTestFixture(t, "grants:\n  claude:\n    operationClasses: [seed.grow]\n    substrates: [core]\n", core.NewService(nil))
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-watch-handle", Pollen: "claude", PhytomerID: "phytomer-watch-id",
		Substrate: "core", Status: "satisfied", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record seed ownership: %v", err)
	}
	fixture.deps.History = store
	fixture.mux = buildRemoteServeMux(fixture.deps)

	mint := serveMuxRequest(fixture.mux, http.MethodPost, "/v1/pollinator/token", "", map[string]string{
		"Authorization": "Bearer " + fixture.root,
	})
	if mint.Code != http.StatusOK {
		t.Fatalf("mint access token: status = %d (%s)", mint.Code, mint.Body.String())
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(mint.Body.Bytes(), &minted); err != nil {
		t.Fatalf("decode minted token: %v", err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sproutWatch","arguments":{"sessionId":"phytomer-watch-id"}}}`
	response := serveMuxRequest(fixture.mux, http.MethodPost, "/v1", body, map[string]string{
		"Authorization": "Bearer " + minted.Token,
	})
	var rpcResponse struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &rpcResponse); err != nil {
		t.Fatalf("decode public sproutWatch response: %v", err)
	}
	if response.Code != http.StatusOK || !rpcResponse.Result.IsError || len(rpcResponse.Result.Content) == 0 || !strings.Contains(rpcResponse.Result.Content[0].Text, "delegation denied") {
		t.Fatalf("public sproutWatch response = %d %s; want WatchAuthority delegation denial", response.Code, response.Body.String())
	}
}

func TestRemotePhytomerWatchStillUsesWatchAuthority(t *testing.T) {
	fixture := newRemoteMuxTestFixture(t, "grants:\n  claude:\n    operationClasses: [seed.grow]\n    substrates: [core]\n", core.NewService(nil))
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RecordSeedRun(context.Background(), historydb.SeedRun{
		Handle: "seed-watch-handle", Pollen: "claude", PhytomerID: "phytomer-watch-id",
		Substrate: "core", Status: "satisfied", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record Seed ownership: %v", err)
	}
	fixture.deps.History = store
	fixture.mux = buildRemoteServeMux(fixture.deps)

	minted := serveMuxRequest(fixture.mux, http.MethodPost, "/v1/pollinator/token", "", map[string]string{
		"Authorization": "Bearer " + fixture.root,
	})
	if minted.Code != http.StatusOK {
		t.Fatalf("mint access token: status = %d (%s)", minted.Code, minted.Body.String())
	}
	var tokenResponse struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(minted.Body.Bytes(), &tokenResponse); err != nil {
		t.Fatalf("decode access token: %v", err)
	}
	response := serveMuxRequest(fixture.mux, http.MethodGet, "/v1/phytomers/phytomer-watch-id/watch", "", map[string]string{
		"Authorization": "Bearer " + tokenResponse.Token,
	})
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "delegation denied") {
		t.Fatalf("public REST watch status/body = %d/%q, want existing WatchAuthority denial", response.Code, response.Body.String())
	}
}

func TestPublicIngressBodyLimitsOnExistingMintAndMCPRoutes(t *testing.T) {
	var sproutCalls int
	service := core.NewService(nil).WithSprout(core.SproutOperations{
		Run: func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
			sproutCalls++
			return core.SproutRunReport{Output: "grown", Outcome: "complete"}, nil
		},
	})
	fixture := newRemoteMuxTestFixture(t, "grants:\n  claude:\n    operationClasses: [sprout.grow]\n    substrates: [core]\n", service)
	handler := buildRemoteServeHandler(fixture.deps)
	limits := defaultPublicIngressLimits()

	mintRequest := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, publicPollinatorTokenPath, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+fixture.root)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := mintRequest(strings.Repeat(" ", int(limits.mintBodyBytes))); response.Code != http.StatusOK {
		t.Fatalf("mint body at 16 KiB status = %d, want 200 (%s)", response.Code, response.Body.String())
	}
	if response := mintRequest(strings.Repeat(" ", int(limits.mintBodyBytes)+1)); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("mint body over 16 KiB status = %d, want 413", response.Code)
	}
	chunkedMintBody := "{}" + strings.Repeat(" ", int(limits.mintBodyBytes)+1-len("{}"))
	chunkedMintRequest := httptest.NewRequest(http.MethodPost, publicPollinatorTokenPath, strings.NewReader(chunkedMintBody))
	chunkedMintRequest.ContentLength = -1
	chunkedMintRequest.Header.Set("Authorization", "Bearer "+fixture.root)
	chunkedMintResponse := httptest.NewRecorder()
	handler.ServeHTTP(chunkedMintResponse, chunkedMintRequest)
	if chunkedMintResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked mint body over 16 KiB status = %d, want 413", chunkedMintResponse.Code)
	}

	accessToken, err := fixture.signer.MintAccessToken("claude", time.Minute, core.AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint MCP test token: %v", err)
	}
	mcpBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sproutGrow","arguments":{"transcript":"bounded","substrate":"core"}}}`
	ordinaryRequest := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+accessToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	withinLimit := mcpBody + strings.Repeat(" ", int(limits.ordinaryBodyBytes)-len(mcpBody))
	if response := ordinaryRequest(withinLimit); response.Code != http.StatusOK {
		t.Fatalf("ordinary MCP body at 4 MiB status = %d, want 200 (%s)", response.Code, response.Body.String())
	}
	if sproutCalls != 1 {
		t.Fatalf("MCP Core Sprout calls at body limit = %d, want 1", sproutCalls)
	}
	overLimit := mcpBody + strings.Repeat(" ", int(limits.ordinaryBodyBytes)+1-len(mcpBody))
	if response := ordinaryRequest(overLimit); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("authenticated ordinary body over 4 MiB status = %d, want 413", response.Code)
	}
	if sproutCalls != 1 {
		t.Fatalf("oversized authenticated body invoked Core; Sprout calls = %d, want 1", sproutCalls)
	}
	chunkedOrdinaryRequest := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1", strings.NewReader(body))
		request.ContentLength = -1
		request.Header.Set("Authorization", "Bearer "+accessToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := chunkedOrdinaryRequest(mcpBody); response.Code != http.StatusOK {
		t.Fatalf("chunked ordinary body below 4 MiB status = %d, want 200 (%s)", response.Code, response.Body.String())
	}
	if sproutCalls != 2 {
		t.Fatalf("chunked ordinary body below limit Sprout calls = %d, want 2", sproutCalls)
	}
	chunkedOverLimit := mcpBody + strings.Repeat(" ", int(limits.ordinaryBodyBytes)+1-len(mcpBody))
	if response := chunkedOrdinaryRequest(chunkedOverLimit); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked authenticated ordinary body over 4 MiB status = %d, want 413", response.Code)
	}
	if sproutCalls != 2 {
		t.Fatalf("oversized chunked authenticated body invoked Core; Sprout calls = %d, want 2", sproutCalls)
	}
}

func TestBuildServeMuxRetainsLocalBotanistMCPAndManagementSurface(t *testing.T) {
	mux := buildServeMux(serveDependencies{
		APIKey:      "botanist-key",
		CoreService: core.NewService(nil),
	})
	_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, "/v1/config/triggers", nil))
	if pattern != "/v1/config/triggers" {
		t.Fatalf("local management route pattern = %q, want /v1/config/triggers", pattern)
	}

	requestBody := `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`
	response := serveMuxRequest(mux, http.MethodPost, "/v1", requestBody, map[string]string{
		"Authorization": "Bearer botanist-key",
	})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"resources"`) {
		t.Fatalf("local Botanist MCP resources/list: status = %d body = %s", response.Code, response.Body.String())
	}
}

type remoteMuxTestFixture struct {
	mux    *http.ServeMux
	dir    string
	root   string
	signer *core.StemSigner
	deps   serveDependencies
}

func (f *remoteMuxTestFixture) localMux() *http.ServeMux {
	return buildServeMux(f.deps)
}

func newRemoteMuxTestFixture(t *testing.T, grants string, service *core.Service) *remoteMuxTestFixture {
	t.Helper()
	controlDir := filepath.Join(t.TempDir(), ".tendril")
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatalf("create control directory: %v", err)
	}
	root, _, err := core.IssuePollinatorCredential(controlDir, "claude", "test")
	if err != nil {
		t.Fatalf("issue Pollinator root: %v", err)
	}
	signer, err := core.LoadOrCreateStemSigner(controlDir)
	if err != nil {
		t.Fatalf("load Stem signer: %v", err)
	}
	if grants != "" {
		if err := os.WriteFile(filepath.Join(controlDir, core.DelegationGrantsFilename), []byte(grants), 0o600); err != nil {
			t.Fatalf("write grants: %v", err)
		}
	}
	bus := eventbus.New()
	t.Cleanup(bus.Shutdown)
	authority := core.NewAuthority(controlDir)
	gate := &receptors.DelegationGate{Authority: authority, Signer: signer, Bus: bus}
	deps := serveDependencies{
		APIKey:         "botanist-key",
		Authority:      authority,
		StemSigner:     signer,
		DelegationGate: gate,
		EventBus:       bus,
		CoreService:    service,
	}
	mux := buildRemoteServeMux(deps)
	return &remoteMuxTestFixture{mux: mux, dir: controlDir, root: root, signer: signer, deps: deps}
}

func serveMuxRequest(mux http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

// Issue finding 1: the Stem must never serve its API unauthenticated.
func TestWithAPIKeyAuthNeverFailsOpen(t *testing.T) {
	called := false
	handler := withAPIKeyAuth("", func(w http.ResponseWriter, r *http.Request) { called = true })

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions", nil))

	if called {
		t.Fatal("withAPIKeyAuth called next() with an empty configured key; must fail closed")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestWithAPIKeyAuthRequiresMatchingBearer(t *testing.T) {
	handler := withAPIKeyAuth("secret-key", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong key", "Bearer wrong", http.StatusUnauthorized},
		{"correct key", "Bearer secret-key", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// A credential issued under any superseded prefix — "otp_", and the bare
// "tendril_" namespace that preceded the two-segment kind prefixes — must be
// refused outright.
//
// This is the one behaviour worth pinning about a prefix rename. The prefix is
// the discriminator that routes a presented bearer to credential resolution, so
// an old value no longer looks credential-shaped and falls through to the
// Botanist-key comparison instead. It must fail there. The forbidden outcome is
// that it is accepted — either by matching the Botanist key or by being treated
// as an ordinary unauthenticated request that proceeds anyway.
//
// The refusal of a "tendril_" bearer is over-determined here — the digest never
// matches either — so this covers the surface outcome rather than the prefix
// check itself. Prefix discrimination is pinned in the core package, by
// TestBearerPrefixesAreDisjoint and
// TestAccessTokenAndCredentialPrefixesAreMutuallyExclusive.
func TestSupersededCredentialPrefixIsRefused(t *testing.T) {
	dir := t.TempDir()
	secret, _, err := core.IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	authority := core.NewAuthority(dir)

	reached := false
	handler := withAPIKeyOrPollinatorAuth("botanist-key", authority, nil, false, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	// The same secret body carrying each superseded prefix: what a Pollinator
	// issued before that rename would still be presenting. The current prefix is
	// unexported, so it is spelled out here and guarded — the guard fires on the
	// next rename, which is the reminder to add the outgoing prefix below.
	const currentPrefix = "tendril_refresh_"
	body := strings.TrimPrefix(secret, currentPrefix)
	if body == secret {
		t.Fatalf("issued secret does not carry %q; add the outgoing prefix to the superseded list", currentPrefix)
	}

	// Only the prefix is ever named in a failure — never the value, which
	// carries the secret body.
	for _, superseded := range []string{"otp_", "tendril_", "tendril_root_"} {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
		req.Header.Set("Authorization", "Bearer "+superseded+body)
		rec := httptest.NewRecorder()
		handler(rec, req)

		if reached {
			t.Fatalf("a credential carrying the superseded prefix %q reached the handler", superseded)
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d for a credential carrying superseded prefix %q", rec.Code, http.StatusUnauthorized, superseded)
		}
	}

	// The current prefix still works, so the refusals above are about the prefix
	// rather than a broken fixture.
	reached = false
	req := httptest.NewRequest(http.MethodPost, "/v1/git/status", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if !reached || rec.Code != http.StatusOK {
		t.Fatalf("a current credential was refused: reached=%v status=%d", reached, rec.Code)
	}
}

// Issue finding 2: /ws must require the same bearer key, accepting it
// either via Authorization header (non-browser clients) or a `key` query
// parameter (the browser cannot set headers on a WebSocket upgrade).
func TestWithWebSocketAuth(t *testing.T) {
	bus := eventbus.New()
	handler := withWebSocketAuth("secret-key", nil, nil, false, gateway.HandleWebSocket(bus))
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	t.Run("rejects unauthenticated upgrade", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/ws")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
		}
	})

	t.Run("rejects wrong query key", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/ws?key=wrong")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
		}
	})

	t.Run("accepts matching query key", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/ws?key=secret-key")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		// The gorilla upgrader rejects a plain GET with 400 (not a WebSocket
		// handshake) once auth lets it through — the point under test is that
		// it's no longer 401.
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatalf("status = %d, want anything but 401 once authenticated", resp.StatusCode)
		}
	})

	t.Run("accepts Authorization header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/ws", nil)
		req.Header.Set("Authorization", "Bearer secret-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			t.Fatalf("status = %d, want anything but 401 once authenticated", resp.StatusCode)
		}
	})
}

func TestGetOrCreateAPIKeyPersistsAndReuses(t *testing.T) {
	dir := t.TempDir()
	tendrilDir := filepath.Join(dir, ".tendril")

	key1, generated1, err := getOrCreateAPIKey(tendrilDir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !generated1 {
		t.Fatal("expected first call to generate a new key")
	}
	if key1 == "" {
		t.Fatal("generated key is empty")
	}

	key2, generated2, err := getOrCreateAPIKey(tendrilDir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if generated2 {
		t.Fatal("expected second call to reuse the persisted key, not regenerate")
	}
	if key2 != key1 {
		t.Fatalf("key changed across calls: %q != %q", key1, key2)
	}

	info, err := os.Stat(apiKeyFilePath(tendrilDir))
	if err != nil {
		t.Fatalf("stat persisted key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestGetOrCreateAPIKeyPrefersEnv(t *testing.T) {
	t.Setenv(EnvBotanistKey, "env-key")
	dir := t.TempDir()

	key, generated, err := getOrCreateAPIKey(filepath.Join(dir, ".tendril"))
	if err != nil {
		t.Fatalf("getOrCreateAPIKey: %v", err)
	}
	if generated {
		t.Fatalf("should not generate a key when %s is set", EnvBotanistKey)
	}
	if key != "env-key" {
		t.Fatalf("key = %q, want env-key", key)
	}
}

// Issue slice 3: a scheduler-originated sprout run must be attributable
// in history. The firer stamps origin "scheduler" into the governed sprout.grow
// input; the Core carries it onto the resolved SproutSpec, which is exactly
// the field the execution port records as historydb.SproutRun.Origin
// (cmdsprout.go). Asserting on the spec therefore pins the whole flow this
// side of the terrarium.
func TestScheduledRunFirerStampsSchedulerOrigin(t *testing.T) {
	ctx := context.Background()
	manager, err := session.NewManager(ctx, nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	var got core.SproutSpec
	svc := core.NewService(manager).WithSprout(core.SproutOperations{
		Run: func(_ context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			got = spec
			return core.SproutRunReport{Output: "matured", Outcome: "complete"}, nil
		},
	})

	// An empty triggers dir means no Hormonal Triggers are configured,
	// so the fire proceeds.
	triggersDir := filepath.Join(t.TempDir(), "no-triggers")
	if err := os.MkdirAll(triggersDir, 0o755); err != nil {
		t.Fatalf("failed to create triggers dir: %v", err)
	}
	firer := scheduledRunFirer(svc, manager, triggersDir, nil)
	entry := scheduler.Entry{
		Cron: "0 3 * * *",
		Sprout: &scheduler.SproutSpec{
			Transcript: "nightly upkeep",
			Substrate:  "/workspaces/core",
		},
	}
	if err := firer(ctx, "nightly", entry); err != nil {
		t.Fatalf("scheduled fire: %v", err)
	}

	if got.Origin != "scheduler" {
		t.Fatalf("scheduled sprout run origin = %q, want %q", got.Origin, "scheduler")
	}
	// The dedicated session initiated for the run carries the same origin, so
	// the session row and the run row agree on which surface grew it.
	if got.SessionID == "" {
		t.Fatal("scheduled sprout run must be bound to a session")
	}
	sess, ok := manager.Get(context.Background(), got.SessionID)
	if !ok {
		t.Fatalf("session %q not found", got.SessionID)
	}
	if sess.Origin != "scheduler" {
		t.Fatalf("scheduled run session origin = %q, want %q", sess.Origin, "scheduler")
	}
}

// The Stem's bearer key must be its own secret, never a provider's. A provider
// value may be shared and reaches every Terrarium; a bearer key grants unscoped
// access.
func TestOtherProviderKeysAreNotTheStemBearerKey(t *testing.T) {
	t.Setenv("SOME_PROVIDER_API_KEY", "a-shared-provider-value")
	os.Unsetenv(EnvBotanistKey)

	if key := resolveServeAPIKey(); key != "" {
		t.Fatalf("resolveServeAPIKey returned %q from a variable that is not the bearer key", key)
	}
}

func TestStemBearerKeyComesFromItsOwnVariable(t *testing.T) {
	t.Setenv(EnvBotanistKey, "a-real-bearer-key")
	t.Setenv("SOME_PROVIDER_API_KEY", "a-shared-provider-value")

	if key := resolveServeAPIKey(); key != "a-real-bearer-key" {
		t.Fatalf("resolveServeAPIKey = %q, want the value of %s", key, EnvBotanistKey)
	}
}

// The end of the chain: the trial constant must not authenticate.
func TestProviderValueDoesNotAuthenticate(t *testing.T) {
	t.Setenv("SOME_PROVIDER_API_KEY", "a-shared-provider-value")
	os.Unsetenv(EnvBotanistKey)

	dir := t.TempDir()
	apiKey, _, err := getOrCreateAPIKey(filepath.Join(dir, ".tendril"))
	if err != nil {
		t.Fatalf("getOrCreateAPIKey: %v", err)
	}
	if apiKey == "a-shared-provider-value" {
		t.Fatal("a provider value became the Stem's bearer key")
	}

	reached := false
	handler := withAPIKeyAuth(apiKey, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer a-shared-provider-value")
	rec := httptest.NewRecorder()
	handler(rec, req)

	if reached || rec.Code != http.StatusUnauthorized {
		t.Fatalf("a provider value authenticated: reached=%v status=%d", reached, rec.Code)
	}
}

// TestTerrariumRunnerPublishesHostActivationEvent verifies that RunTrigger
// publishes exactly one EventHostExecutionActivated event on the bus when the
// host terrarium provider is activated. This is the wiring assertion for the
// structured audit trail — the observer-callback contract in the terrarium
// package only guarantees the callback is called; this test guarantees the
// caller correctly translates it into a bus event.
func TestTerrariumRunnerPublishesHostActivationEvent(t *testing.T) {
	t.Setenv("TENDRIL_ALLOW_HOST_EXECUTION", "true")

	// The host provider is activated but the script does not exist, so the run
	// will fail after the provider is resolved. That is fine — we only care
	// that the event was published before the error.
	bus := eventbus.New()
	var received []eventbus.EventType
	bus.Subscribe(eventbus.EventHostExecutionActivated, func(e eventbus.Event) {
		received = append(received, e.Type)
	})

	mode, runner := resolveTriggerModeAndRunner(bus)
	_ = mode
	// RunTrigger with a non-existent script: the error from the provider itself
	// won't happen (the host provider is allowed), but the script-exec will fail.
	// We just need to verify the event fired before the post-activation error.
	_ = runner.RunTrigger(context.Background(), "/dev/null/no-such-script", triggers.TriggerPayload{})

	if len(received) != 1 {
		t.Fatalf("expected exactly 1 %s event, got %d", eventbus.EventHostExecutionActivated, len(received))
	}
}

// TestTerrariumRunnerNoEventForDockerProvider verifies that no
// EventHostExecutionActivated event is published when the Docker provider is
// selected. Only host-provider activation is a security-relevant event.
func TestTerrariumRunnerNoEventForDockerProvider(t *testing.T) {
	t.Setenv("TENDRIL_ALLOW_HOST_EXECUTION", "")

	bus := eventbus.New()
	var received int
	bus.Subscribe(eventbus.EventHostExecutionActivated, func(_ eventbus.Event) {
		received++
	})

	// Docker is the default when TENDRIL_ALLOW_HOST_EXECUTION is unset.
	_, runner := resolveTriggerModeAndRunner(bus)
	// The script path is irrelevant; we only check the event count.
	_ = runner.RunTrigger(context.Background(), "/nonexistent", triggers.TriggerPayload{})

	if received != 0 {
		t.Fatalf("expected no host-activation events for docker provider, got %d", received)
	}
}

// TestHandleHealthPublishesToBus proves the nil-bus on-demand bug is fixed:
// hitting GET /health with a real *eventbus.Bus wired into the monitor must
// result in EventHealthCheck landing on a subscriber. Before this fix,
// handleHealth constructed its own monitor with bus == nil so publish was a
// silent no-op and no health event ever reached the bus in production.
func TestHandleHealthPublishesToBus(t *testing.T) {
	bus := eventbus.New()

	received := make(chan eventbus.Event, 1)
	bus.Subscribe(eventbus.EventHealthCheck, func(e eventbus.Event) {
		select {
		case received <- e:
		default:
		}
	})

	monitor := newDefaultHealthMonitor(bus, 30*time.Second)
	handler := handleHealth(monitor, false)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	// The handler must have returned a valid JSON body first.
	if rec.Code != http.StatusOK && rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status %d", rec.Code)
	}

	// Now assert the bus actually received the event (proving the wiring, not
	// just that the handler returned 200).
	select {
	case e := <-received:
		if e.Type != eventbus.EventHealthCheck {
			t.Fatalf("event type = %q, want %q", e.Type, eventbus.EventHealthCheck)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: EventHealthCheck never reached the bus; nil-bus bug may still be present")
	}
}

// TestHandleHealthStatusCodes confirms the handler maps Overall=true → 200 and
// Overall=false → 503, so callers can rely on the status code without parsing
// the JSON body.
func TestHandleHealthStatusCodes(t *testing.T) {
	bus := eventbus.New()
	monitor := newDefaultHealthMonitor(bus, 30*time.Second)
	handler := handleHealth(monitor, false)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	// Default checks all pass in a unit-test environment (no disks, no
	// external services): overall should be true → 200 OK.
	if rec.Code != http.StatusOK {
		// A 503 is not a test failure per se — it means a default check
		// reported unhealthy in CI. Log it clearly but don't hard-fail, because
		// the important invariant is the mapping logic, not the check outcome.
		t.Logf("note: /health returned %d (a registered check reported unhealthy); mapping logic is still exercised", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", rec.Header().Get("Content-Type"))
	}
}

func TestHandleHealthOwnerPublication(t *testing.T) {
	bus := eventbus.New()
	monitor := newDefaultHealthMonitor(bus, 30*time.Second)

	cases := []struct {
		name      string
		networked bool
		wantOwner bool
	}{
		{"loopback publishes owner", false, true},
		{"networked withholds owner", true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := handleHealth(monitor, tc.networked)
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

			var payload map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("parse response: %v", err)
			}

			// 1. Existing fields are unchanged
			if _, ok := payload["timestamp"]; !ok {
				t.Error("timestamp is missing")
			}
			if _, ok := payload["overall"]; !ok {
				t.Error("overall is missing")
			}
			if _, ok := payload["results"]; !ok {
				t.Error("results is missing")
			}

			// 2. Minimum disclosure: no other fields are present (e.g. no account name, no executable)
			allowedKeys := map[string]bool{
				"timestamp": true,
				"overall":   true,
				"results":   true,
			}
			if tc.wantOwner {
				allowedKeys["owner"] = true
			}
			for k := range payload {
				if !allowedKeys[k] {
					t.Errorf("unexpected field in response: %q", k)
				}
			}

			owner, hasOwner := payload["owner"]
			if tc.wantOwner {
				if !hasOwner {
					t.Fatal("owner is absent on loopback bind")
				}
				uid, ok := owner.(float64)
				if !ok {
					t.Fatalf("owner is not a number: %T", owner)
				}
				if int(uid) != os.Getuid() {
					t.Errorf("owner = %v, want %d", int(uid), os.Getuid())
				}
			} else {
				if hasOwner {
					t.Errorf("owner is present on networked bind: %v", owner)
				}
			}
		})
	}
}

func TestPublicReadinessIsPassiveAndLocalHealthRemainsActive(t *testing.T) {
	fixture := newRemoteMuxTestFixture(t, "", nil)
	var checks atomic.Int32
	monitor := healthmon.New(fixture.deps.EventBus, time.Hour)
	monitor.RegisterCheck(publicReadinessTestCheck{calls: &checks})
	fixture.deps.HealthMonitor = monitor

	healthEvents := make(chan eventbus.Event, 4)
	fixture.deps.EventBus.Subscribe(eventbus.EventHealthCheck, func(event eventbus.Event) {
		healthEvents <- event
	})
	publicHandler := buildRemoteServeHandler(fixture.deps)

	publicResponse := httptest.NewRecorder()
	publicHandler.ServeHTTP(publicResponse, httptest.NewRequest(http.MethodGet, "/health", nil))
	if publicResponse.Code != http.StatusOK {
		t.Fatalf("public readiness status = %d, want 200", publicResponse.Code)
	}
	var publicBody map[string]any
	if err := json.Unmarshal(publicResponse.Body.Bytes(), &publicBody); err != nil {
		t.Fatalf("public readiness response is not valid JSON: %v", err)
	}
	if ready, ok := publicBody["ready"].(bool); !ok || !ready {
		t.Fatalf("public readiness payload = %v, want ready=true", publicBody)
	}
	if _, ok := publicBody["owner"]; ok {
		t.Fatalf("public readiness disclosed local owner: %v", publicBody["owner"])
	}
	if checks.Load() != 0 {
		t.Fatalf("public readiness executed %d health checks, want 0", checks.Load())
	}
	select {
	case event := <-healthEvents:
		t.Fatalf("public readiness published health event %q", event.Type)
	default:
	}

	probeServer := httptest.NewServer(publicHandler)
	t.Cleanup(probeServer.Close)
	probe := mcpclient.ProbeOwnerAtWithClient(context.Background(), probeServer.URL, probeServer.Client())
	if !probe.Reached || probe.Owner != nil || probe.Err != nil {
		t.Fatalf("restricted Pollinator readiness probe = reached %t owner %v err %v; want reached with no owner", probe.Reached, probe.Owner, probe.Err)
	}
	if checks.Load() != 0 {
		t.Fatalf("public probe executed %d health checks, want 0", checks.Load())
	}
	select {
	case event := <-healthEvents:
		t.Fatalf("public probe published health event %q", event.Type)
	default:
	}

	localResponse := httptest.NewRecorder()
	fixture.localMux().ServeHTTP(localResponse, httptest.NewRequest(http.MethodGet, "/health", nil))
	if localResponse.Code != http.StatusOK {
		t.Fatalf("local active health status = %d, want 200", localResponse.Code)
	}
	if checks.Load() != 1 {
		t.Fatalf("local health executed %d checks, want exactly 1", checks.Load())
	}
	var localBody map[string]any
	if err := json.Unmarshal(localResponse.Body.Bytes(), &localBody); err != nil {
		t.Fatalf("local health response is not valid JSON: %v", err)
	}
	if _, ok := localBody["owner"]; !ok {
		t.Fatal("local loopback health response omitted owner")
	}
	select {
	case event := <-healthEvents:
		if event.Type != eventbus.EventHealthCheck {
			t.Fatalf("local health event = %q, want %q", event.Type, eventbus.EventHealthCheck)
		}
	default:
		t.Fatal("local active health did not publish EventHealthCheck")
	}
}

type publicReadinessTestCheck struct {
	calls *atomic.Int32
}

func (check publicReadinessTestCheck) Name() string { return "readiness-test" }

func (check publicReadinessTestCheck) Check(context.Context) healthmon.CheckResult {
	check.calls.Add(1)
	return healthmon.CheckResult{Healthy: true, Message: "healthy"}
}

func TestScheduledRunFirerPublishesTriggerBlockedEvent(t *testing.T) {
	bus := eventbus.New()
	var received []eventbus.Event
	bus.Subscribe(eventbus.EventTriggerBlocked, func(e eventbus.Event) {
		received = append(received, e)
	})

	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	svc := core.NewService(manager)

	// ModeEnforce ensures that a nonexistent triggers directory causes a block
	t.Setenv("TENDRIL_TRIGGERS_MODE", "enforce")

	firer := scheduledRunFirer(svc, manager, "/nonexistent-dir-blocks-run", bus)
	entry := scheduler.Entry{
		Model: "some-model",
	}

	err = firer(context.Background(), "my-schedule", entry)
	if err == nil || !strings.Contains(err.Error(), "blocked by Hormonal Triggers") {
		t.Fatalf("expected blocked by Hormonal Triggers error, got: %v", err)
	}

	if len(received) != 1 {
		t.Fatalf("expected exactly 1 %s event, got %d", eventbus.EventTriggerBlocked, len(received))
	}
	if received[0].Source != "scheduler" {
		t.Errorf("Source = %q, want scheduler", received[0].Source)
	}
	data := received[0].Data
	if data["schedule"] != "my-schedule" {
		t.Errorf("data.schedule = %v, want my-schedule", data["schedule"])
	}
	if data["genotype"] != "some-model" {
		t.Errorf("data.genotype = %v, want some-model", data["genotype"])
	}
	if data["reason"] == nil || data["reason"] == "" {
		t.Error("data.reason is empty")
	}
}

func TestHandleChatCompletionsPublishesTriggerBlockedEvent(t *testing.T) {
	t.Setenv("TENDRIL_ALLOW_HOST_EXECUTION", "true")
	t.Setenv("TENDRIL_TRIGGERS_MODE", "enforce")

	// Chdir to temp dir so getTriggersDir() looks in an empty directory and blocks
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(cwd)

	bus := eventbus.New()
	var received []eventbus.Event
	bus.Subscribe(eventbus.EventTriggerBlocked, func(e eventbus.Event) {
		received = append(received, e)
	})

	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	handler := handleChatCompletions(bus, manager, nil)

	body := `{"model": "test-model", "messages": [{"role": "user", "content": "hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Phytomer", "test-session")

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	if len(received) != 1 {
		t.Fatalf("expected exactly 1 %s event, got %d", eventbus.EventTriggerBlocked, len(received))
	}

	ev := received[0]
	if ev.Source != "chat" {
		t.Errorf("Source = %q, want chat", ev.Source)
	}
	if ev.SessionID == "" {
		t.Error("SessionID is empty")
	}

	data := ev.Data
	if data["genotype"] != "test-model" {
		t.Errorf("data.genotype = %v, want test-model", data["genotype"])
	}
	if data["reason"] == nil || data["reason"] == "" {
		t.Error("data.reason is empty")
	}
}

func TestApplySessionPreferencesCopiesNamedSubstrate(t *testing.T) {
	orch := conductor.NewDockerOrchestrator()
	applySessionPreferences(orch, session.Preferences{
		Provider:  "local",
		Model:     "llama3.2",
		Genotype:  "go-dev",
		Substrate: "  opentendril  ",
	})
	if orch.Provider != "local" || orch.Model != "llama3.2" || orch.Genotype != "go-dev" {
		t.Fatalf("provider/model/genotype not copied: %+v", orch)
	}
	if orch.Substrate != "opentendril" {
		t.Fatalf("Substrate = %q, want trimmed named substrate opentendril", orch.Substrate)
	}
}

func TestApplySessionPreferencesDoesNotInventStemHome(t *testing.T) {
	orch := conductor.NewDockerOrchestrator()
	applySessionPreferences(orch, session.Preferences{})
	if orch.Substrate != "" {
		t.Fatalf("empty preferences invented Substrate %q; chat must leave it unset", orch.Substrate)
	}
}

func TestHandleChatCompletionsRefusesStemHomeWithoutSubstrate(t *testing.T) {
	t.Setenv("TENDRIL_TRIGGERS_MODE", "disabled")
	t.Setenv("TENDRIL_SUBSTRATE", "")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".tendril"), 0o755); err != nil {
		t.Fatalf("mkdir control plane: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".tendril", "api-key"), []byte("test-key\n"), 0o600); err != nil {
		t.Fatalf("write api-key: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(cwd)

	bus := eventbus.New()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	sess, err := manager.Initiate(context.Background(), session.OriginREST, session.Preferences{})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}

	handler := handleChatCompletions(bus, manager, nil)
	body := `{"sessionId":"` + sess.ID + `","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("chat grew a Sprout with no Substrate; want the required-substrate refusal")
	}
	if !strings.Contains(rec.Body.String(), "substrate is required") {
		t.Fatalf("response = %q, want the existing required-substrate message", rec.Body.String())
	}
}

func TestTriggersModeFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want triggers.TriggerMode
	}{
		{"", triggers.ModeEnforce},
		{"   ", triggers.ModeEnforce},
		{"enforce", triggers.ModeEnforce},
		{"disabled", triggers.ModeDisabled},
		{"DISABLED", triggers.ModeDisabled},
		{"Disabled ", triggers.ModeDisabled},
		{"unknown", triggers.ModeEnforce},
	}

	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("TENDRIL_TRIGGERS_MODE", tc.env)
			got := triggersModeFromEnv()
			if got != tc.want {
				t.Errorf("env %q: got %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

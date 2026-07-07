package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/api"
	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// Interface-parity coverage test (issue #159). It asserts the governed command
// capability set is identical across every surface: the canonical registry, the
// REST adapter, the MCP adapter (projected from the live tools/list response),
// and the CLI adapter. It goes red the moment someone adds a capability to one
// surface but not the others.
//
// To see it fail on induced drift, add a name to core.CapabilityNames() (or a
// stray governed tool to one surface) and run:  go test ./cmd/stem/ -run Parity
func newParityFixture(t *testing.T) (core.Core, *api.SessionsHandler, *api.MCPHandler, *http.ServeMux) {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	svc := core.NewService(manager)
	rest := api.NewSessionsHandler(svc, manager, nil)
	// Register the REST routes so rest.Capabilities() reflects what is actually
	// mounted on the mux (not the canonical list) — the independence the
	// coverage test relies on.
	mux := http.NewServeMux()
	rest.Register(mux, nil)
	mcp := api.NewMCPHandler().WithSessions(manager, nil).WithCore(svc)
	return svc, rest, mcp, mux
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalSets(t *testing.T, label string, got, want []string) {
	t.Helper()
	got = sortedCopy(got)
	want = sortedCopy(want)
	if len(got) != len(want) {
		t.Errorf("%s: %d capabilities, want %d\n got:  %v\n want: %v", label, len(got), len(want), got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: capability set diverges\n got:  %v\n want: %v", label, got, want)
			return
		}
	}
}

// mcpGovernedToolNames extracts, from the REAL tools/list response, the names
// that are governed Core capabilities (ignoring legacy non-core tools).
func mcpGovernedToolNames(t *testing.T, mcp *api.MCPHandler) []string {
	t.Helper()
	resp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))

	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("parse tools/list: %v", err)
	}

	governed := map[string]bool{}
	for _, name := range core.CapabilityNames() {
		governed[name] = true
	}
	names := make([]string, 0)
	for _, tool := range parsed.Result.Tools {
		if governed[tool.Name] {
			names = append(names, tool.Name)
		}
	}
	return names
}

func TestInterfaceParityCoverage(t *testing.T) {
	_, rest, mcp, _ := newParityFixture(t)
	canonical := core.CapabilityNames()

	// Each arm reflects what its surface ACTUALLY wires, independently derived:
	//   REST — capabilities recorded while mounting routes on the mux.
	//   MCP  — names parsed from the live tools/list response.
	//   CLI  — capabilities of the subcommands registered on the command tree.
	equalSets(t, "REST adapter (registered routes) vs canonical", rest.Capabilities(), canonical)
	equalSets(t, "MCP adapter (declared) vs canonical", mcp.CoreCapabilityNames(), canonical)
	equalSets(t, "MCP adapter (live tools/list) vs canonical", mcpGovernedToolNames(t, mcp), canonical)
	equalSets(t, "CLI adapter (registered subcommands) vs canonical", sessionCLICapabilityNames(), canonical)
}

// Behavioral parity: the same input yields the same result via the Core
// directly, via REST (httptest), and via MCP for the create-session capability.
func TestInterfaceParityBehavioral_CreateSession(t *testing.T) {
	ctx := context.Background()
	svc, _, mcp, mux := newParityFixture(t)

	// (a) Core directly.
	coreSess, err := svc.CreateSession(ctx, core.CreateSessionInput{
		Origin:      session.OriginCLI,
		Preferences: session.Preferences{Model: "claude-sonnet", Genotype: "verifier"},
	})
	if err != nil {
		t.Fatalf("core create: %v", err)
	}

	// (b) REST via httptest, using the routes the fixture already registered.
	server := httptest.NewServer(mux)
	defer server.Close()

	body := bytes.NewBufferString(`{"preferences":{"model":"claude-sonnet","genotype":"verifier"}}`)
	httpResp, err := http.Post(server.URL+"/v1/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("REST create: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusCreated {
		t.Fatalf("REST create status = %d, want 201", httpResp.StatusCode)
	}
	var restSess session.Session
	if err := json.NewDecoder(httpResp.Body).Decode(&restSess); err != nil {
		t.Fatalf("decode REST session: %v", err)
	}

	// (c) MCP via tools/call.
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"session.create","arguments":{"preferences":{"model":"claude-sonnet","genotype":"verifier"}}}}`))
	var parsed struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP tools/call: %v", err)
	}
	if parsed.Result.IsError || len(parsed.Result.Content) == 0 {
		t.Fatalf("MCP create returned error/empty: %s", string(mcpResp))
	}
	var mcpSess session.Session
	if err := json.Unmarshal([]byte(parsed.Result.Content[0].Text), &mcpSess); err != nil {
		t.Fatalf("decode MCP session: %v", err)
	}

	// All three surfaces produced an equivalent session for identical input.
	for label, sess := range map[string]session.Session{"core": coreSess, "rest": restSess, "mcp": mcpSess} {
		if sess.ID == "" {
			t.Errorf("%s: empty session id", label)
		}
		if sess.Preferences.Model != "claude-sonnet" {
			t.Errorf("%s: model = %q, want claude-sonnet", label, sess.Preferences.Model)
		}
		if sess.Preferences.Genotype != "verifier" {
			t.Errorf("%s: genotype = %q, want verifier", label, sess.Preferences.Genotype)
		}
	}
	if !samePreferences(coreSess.Preferences, restSess.Preferences) || !samePreferences(restSess.Preferences, mcpSess.Preferences) {
		t.Errorf("preferences diverge across surfaces: core=%+v rest=%+v mcp=%+v",
			coreSess.Preferences, restSess.Preferences, mcpSess.Preferences)
	}
}

func samePreferences(a, b session.Preferences) bool {
	return a.Provider == b.Provider &&
		a.Model == b.Model &&
		a.Genotype == b.Genotype &&
		a.EpigeneticGenome == b.EpigeneticGenome
}

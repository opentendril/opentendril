package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/receptors"
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
func newParityFixture(t *testing.T) (core.Core, *receptors.SessionsHandler, *receptors.GenomeHandler, *receptors.PlasmidHandler, *receptors.GraftHandler, *receptors.SequenceHandler, *receptors.MCPHandler, *http.ServeMux) {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	root := t.TempDir()
	svc := core.NewService(manager).WithGenome(core.GenomeOps{
		Root:   root,
		Reduce: func(context.Context, string) error { return nil },
		Evolve: func(context.Context, string) error { return nil },
	}).WithPlasmid(core.PlasmidOps{
		Root: root,
		Inject: func(context.Context, string, string) (core.PlasmidInjection, error) {
			return core.PlasmidInjection{}, nil
		},
	}).WithMesh(core.MeshOps{
		ResolveWorkspace: func(_ context.Context, substrate string) (string, error) { return substrate, nil },
		DelegatePush:     func(context.Context, string, string, string) (string, error) { return "deadbeef", nil },
	})
	rest := receptors.NewSessionsHandler(svc, manager, nil)
	genomeRest := receptors.NewGenomeHandler(svc)
	plasmidRest := receptors.NewPlasmidHandler(svc)
	graftRest := receptors.NewGraftHandler(svc)
	sequenceRest := receptors.NewSequenceHandler(svc)
	// Register the REST routes so the handlers' Capabilities() reflect what is
	// actually mounted on the mux (not the canonical list) — the independence
	// the coverage test relies on.
	mux := http.NewServeMux()
	rest.Register(mux, nil)
	genomeRest.Register(mux, nil)
	plasmidRest.Register(mux, nil)
	graftRest.Register(mux, nil)
	sequenceRest.Register(mux, nil)

	mcp := receptors.NewMCPHandler().WithSessions(manager, nil).WithCore(svc)
	return svc, rest, genomeRest, plasmidRest, graftRest, sequenceRest, mcp, mux
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
func mcpGovernedToolNames(t *testing.T, mcp *receptors.MCPHandler) []string {
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
	_, rest, genomeRest, plasmidRest, graftRest, sequenceRest, mcp, _ := newParityFixture(t)
	canonical := core.CapabilityNames()

	// Each arm reflects what its surface ACTUALLY wires, independently derived:
	//   REST — capabilities recorded while mounting routes on the mux
	//          (sessions + genome + plasmid + graft + sequence handlers).
	//   MCP  — names parsed from the live tools/list response.
	//   CLI  — capabilities of the subcommands registered on the command trees
	//          (`tendril session` + `tendril genome` + `tendril plasmid` + `tendril mesh` + `tendril sequence`).
	restCaps := append(rest.Capabilities(), genomeRest.Capabilities()...)
	restCaps = append(restCaps, plasmidRest.Capabilities()...)
	restCaps = append(restCaps, graftRest.Capabilities()...)
	restCaps = append(restCaps, sequenceRest.Capabilities()...)
	cliCaps := append(sessionCLICapabilityNames(), genomeCLICapabilityNames()...)
	cliCaps = append(cliCaps, plasmidCLICapabilityNames()...)
	cliCaps = append(cliCaps, meshCLICapabilityNames()...)
	cliCaps = append(cliCaps, sequenceCLICapabilityNames()...)
	equalSets(t, "REST adapter (registered routes) vs canonical", restCaps, canonical)
	equalSets(t, "MCP adapter (declared) vs canonical", mcp.CoreCapabilityNames(), canonical)
	equalSets(t, "MCP adapter (live tools/list) vs canonical", mcpGovernedToolNames(t, mcp), canonical)
	equalSets(t, "CLI adapter (registered subcommands) vs canonical", cliCaps, canonical)
}

// Behavioral parity: the same input yields the same result via the Core
// directly, via REST (httptest), and via MCP for the create-session capability.
func TestInterfaceParityBehavioral_CreateSession(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, _, _, mcp, mux := newParityFixture(t)

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

// ---------------------------------------------------------------------------
// Behavioral parity, part two (issue #159): TestInterfaceParityBehavioral_*
// above proves REST and MCP produce equivalent *outputs* against a real
// Core. This proves the adapters carry zero business logic of their own —
// CLI included — by asserting on the *shape of the call itself*. A mock Core
// records exactly which typed method it received and with what argument
// struct; REST, MCP, and CLI must each decode an equivalent request into the
// identical typed input and invoke the identical method, not merely produce
// similar-looking JSON.
// ---------------------------------------------------------------------------

// mockCoreCall records one invocation of a mockCore method: which method,
// and the exact typed input struct it received.
type mockCoreCall struct {
	method string
	input  any
}

// mockCore is a stub core.Core. Every typed method only records its call and
// returns a canned result — no orchestration, no session manager, no disk.
// Capabilities()/Invoke() route through those same typed methods via the
// identical decode-then-dispatch pattern the real Service uses
// (cmd/stem/internal/core/registry.go), so an MCP or CLI call and a direct
// REST call are provably the same code path, not two independent
// implementations that happen to agree.
type mockCore struct {
	mu    sync.Mutex
	calls []mockCoreCall

	createSessionResult session.Session
	getSessionResult    session.Session
}

func (m *mockCore) record(method string, input any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, mockCoreCall{method: method, input: input})
}

// reset clears recorded calls between surfaces so each surface's assertion
// only sees the call it caused.
func (m *mockCore) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = nil
}

func (m *mockCore) inputsFor(method string) []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []any
	for _, c := range m.calls {
		if c.method == method {
			out = append(out, c.input)
		}
	}
	return out
}

func (m *mockCore) CreateSession(_ context.Context, in core.CreateSessionInput) (session.Session, error) {
	m.record("CreateSession", in)
	return m.createSessionResult, nil
}

func (m *mockCore) ListSessions(_ context.Context) ([]session.Session, error) {
	m.record("ListSessions", struct{}{})
	return nil, nil
}

func (m *mockCore) GetSession(_ context.Context, in core.GetSessionInput) (session.Session, error) {
	m.record("GetSession", in)
	return m.getSessionResult, nil
}

func (m *mockCore) UpdateSessionPreferences(_ context.Context, in core.UpdateSessionInput) (session.Session, error) {
	m.record("UpdateSessionPreferences", in)
	return session.Session{}, nil
}

func (m *mockCore) DeleteSession(_ context.Context, in core.DeleteSessionInput) error {
	m.record("DeleteSession", in)
	return nil
}

func (m *mockCore) SessionHistory(_ context.Context, in core.SessionHistoryInput) ([]session.Message, error) {
	m.record("SessionHistory", in)
	return nil, nil
}

func (m *mockCore) GenomeView(_ context.Context) ([]core.GenomeSeed, error) {
	m.record("GenomeView", struct{}{})
	return nil, nil
}

func (m *mockCore) GenomeReduce(_ context.Context) (string, error) {
	m.record("GenomeReduce", struct{}{})
	return ".tendril/genome/epigenetics.md", nil
}

func (m *mockCore) GenomeEvolve(_ context.Context) (string, error) {
	m.record("GenomeEvolve", struct{}{})
	return ".tendril/genome/epigenetics.md", nil
}

func (m *mockCore) PlasmidList(_ context.Context) ([]string, error) {
	m.record("PlasmidList", struct{}{})
	return nil, nil
}

func (m *mockCore) PlasmidInject(_ context.Context, in core.PlasmidInjectInput) (core.PlasmidInjection, error) {
	m.record("PlasmidInject", in)
	return core.PlasmidInjection{
		Source: ".tendril/genotypes/plasmids/go-rules.md",
		Dest:   ".tendril/genome/go-rules.md",
	}, nil
}

func (m *mockCore) MeshGraft(_ context.Context, in core.MeshGraftInput) (core.MeshDelegation, error) {
	m.record("MeshGraft", in)
	return core.MeshDelegation{Workspace: "/workspaces/core", Commit: "deadbeef"}, nil
}

func (m *mockCore) MeshPromote(_ context.Context, in core.MeshPromoteInput) (core.MeshPromotion, error) {
	m.record("MeshPromote", in)
	return core.MeshPromotion{Workspace: "/workspaces/core", Commit: "deadbeef", PRNumber: in.PRNumber}, nil
}

func (m *mockCore) SequenceList(_ context.Context) ([]string, error) {
	m.record("SequenceList", struct{}{})
	return nil, nil
}

func (m *mockCore) SequenceRun(_ context.Context, in core.SequenceRunInput) (core.SequenceRunResult, error) {
	m.record("SequenceRun", in)
	return core.SequenceRunResult{
		Name:  "deploy",
		Steps: []core.SequenceStepOutcome{{ID: "meristem", Status: "matured"}},
	}, nil
}

// Capabilities mirrors the real registry's declarative shape closely enough
// for the MCP adapter's isCoreCapability/tool-listing checks — but every
// Invoke closure below dispatches to this mock's own typed methods above,
// exactly like core.Service.Capabilities() dispatches to Service's typed
// methods (registry.go).
func (m *mockCore) Capabilities() []core.Capability {
	return []core.Capability{
		{
			Name:        core.CapCreateSession,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.CreateSessionInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.CreateSession(ctx, in)
			},
		},
		{
			Name:        core.CapListSessions,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return m.ListSessions(ctx)
			},
		},
		{
			Name:        core.CapGetSession,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.GetSessionInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.GetSession(ctx, in)
			},
		},
		{
			Name:        core.CapUpdateSession,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.UpdateSessionInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.UpdateSessionPreferences(ctx, in)
			},
		},
		{
			Name:        core.CapDeleteSession,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.DeleteSessionInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				if err := m.DeleteSession(ctx, in); err != nil {
					return nil, err
				}
				return map[string]any{"sessionId": in.SessionID, "deleted": true}, nil
			},
		},
		{
			Name:        core.CapSessionHistory,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.SessionHistoryInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.SessionHistory(ctx, in)
			},
		},
		{
			Name:        core.CapGenomeView,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return m.GenomeView(ctx)
			},
		},
		{
			Name:        core.CapGenomeReduce,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				path, err := m.GenomeReduce(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{"path": path, "reduced": true}, nil
			},
		},
		{
			Name:        core.CapGenomeEvolve,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				path, err := m.GenomeEvolve(ctx)
				if err != nil {
					return nil, err
				}
				return map[string]any{"path": path, "evolved": true}, nil
			},
		},
		{
			Name:        core.CapPlasmidList,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return m.PlasmidList(ctx)
			},
		},
		{
			Name:        core.CapPlasmidInject,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.PlasmidInjectInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.PlasmidInject(ctx, in)
			},
		},
		{
			Name:        core.CapMeshGraft,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.MeshGraftInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.MeshGraft(ctx, in)
			},
		},
		{
			Name:        core.CapMeshPromote,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.MeshPromoteInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.MeshPromote(ctx, in)
			},
		},
		{
			Name:        core.CapSequenceList,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return m.SequenceList(ctx)
			},
		},
		{
			Name:        core.CapSequenceRun,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.SequenceRunInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.SequenceRun(ctx, in)
			},
		},
	}
}

// Invoke dispatches by capability name via the same declarative-registry
// pattern the real Service uses — this is the path the MCP and CLI adapters
// call through.
func (m *mockCore) Invoke(ctx context.Context, name string, input map[string]any) (any, error) {
	for _, capability := range m.Capabilities() {
		if capability.Name == name {
			return capability.Invoke(ctx, input)
		}
	}
	return nil, fmt.Errorf("mock core: unknown capability %q", name)
}

var _ core.Core = (*mockCore)(nil)

// decodeMockInput mirrors core.decodeInput's JSON round-trip (unexported in
// package core), so the mock decodes MCP/CLI argument maps into typed
// structs exactly like the real Service does.
func decodeMockInput(input map[string]any, target any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

// newMockParityFixture wires the REST and MCP adapters over a mockCore
// instead of a real Service, so assertions are about how each adapter
// translates a request into a Core call — not about session-manager
// behavior (already covered above with a real Core).
func newMockParityFixture(t *testing.T) (*mockCore, *http.ServeMux, *receptors.MCPHandler) {
	t.Helper()
	mock := &mockCore{}

	// A real, empty, in-memory manager only backs the REST handler's
	// ungoverned routes (events/sprout-runs/async-sequence) — never touched
	// by this test — and MCP's non-core tools. No disk I/O (nil store).
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	rest := receptors.NewSessionsHandler(mock, manager, nil)
	genomeRest := receptors.NewGenomeHandler(mock)
	plasmidRest := receptors.NewPlasmidHandler(mock)
	graftRest := receptors.NewGraftHandler(mock)
	sequenceRest := receptors.NewSequenceHandler(mock)
	mux := http.NewServeMux()
	rest.Register(mux, nil)
	genomeRest.Register(mux, nil)
	plasmidRest.Register(mux, nil)
	graftRest.Register(mux, nil)
	sequenceRest.Register(mux, nil)

	mcp := receptors.NewMCPHandler().WithSessions(manager, nil).WithCore(mock)

	return mock, mux, mcp
}

// TestBehavioralParity proves the CLI, REST, and MCP adapters carry zero
// business logic: for each capability under test, an equivalent payload
// fired through all three surfaces must decode into the identical typed
// Core input and invoke the identical Core method exactly once — asserted
// against a mock Core that records exactly what it received, and that each
// adapter maps the mock's canned result back to its own surface without
// error.
func TestBehavioralParity(t *testing.T) {
	const sessionID = "parity-session-1"
	const parityOrigin = "parity-origin"

	cases := []struct {
		name   string // capability name; also the MCP tool name
		method string // mockCore method every surface must invoke exactly once
		want   any    // the exact typed input every surface must produce

		restRequest func(t *testing.T, serverURL string) *http.Response
		mcpParams   map[string]any

		cliSubcommand string
		cliArgs       []string // args after the subcommand token, as runSessionCmd passes them to parseSessionArgs
	}{
		{
			name:   core.CapCreateSession,
			method: "CreateSession",
			want: core.CreateSessionInput{
				Origin:      parityOrigin,
				Preferences: session.Preferences{Model: "claude-sonnet", Genotype: "verifier"},
			},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				body := bytes.NewBufferString(`{"origin":"parity-origin","preferences":{"model":"claude-sonnet","genotype":"verifier"}}`)
				resp, err := http.Post(serverURL+"/v1/sessions", "application/json", body)
				if err != nil {
					t.Fatalf("REST session.create: %v", err)
				}
				return resp
			},
			// origin is passed explicitly (rather than left to each surface's
			// own default-origin stamping) so the comparison isolates adapter
			// translation fidelity from that surface-specific metadata.
			mcpParams: map[string]any{
				"origin":      parityOrigin,
				"preferences": map[string]any{"model": "claude-sonnet", "genotype": "verifier"},
			},
			cliSubcommand: "create",
			cliArgs:       []string{"--origin", parityOrigin, "--model", "claude-sonnet", "--genotype", "verifier"},
		},
		{
			name:   core.CapGetSession,
			method: "GetSession",
			want:   core.GetSessionInput{SessionID: sessionID},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				resp, err := http.Get(serverURL + "/v1/sessions/" + sessionID)
				if err != nil {
					t.Fatalf("REST session.get: %v", err)
				}
				return resp
			},
			mcpParams:     map[string]any{"sessionId": sessionID},
			cliSubcommand: "get",
			cliArgs:       []string{sessionID},
		},
		{
			name:   core.CapDeleteSession,
			method: "DeleteSession",
			want:   core.DeleteSessionInput{SessionID: sessionID},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				req, err := http.NewRequest(http.MethodDelete, serverURL+"/v1/sessions/"+sessionID, nil)
				if err != nil {
					t.Fatalf("build REST session.delete request: %v", err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("REST session.delete: %v", err)
				}
				return resp
			},
			mcpParams:     map[string]any{"sessionId": sessionID},
			cliSubcommand: "delete",
			cliArgs:       []string{sessionID},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, mux, mcp := newMockParityFixture(t)
			server := httptest.NewServer(mux)
			defer server.Close()
			ctx := context.Background()

			// --- REST ------------------------------------------------------
			mock.reset()
			restResp := tc.restRequest(t, server.URL)
			defer restResp.Body.Close()
			if restResp.StatusCode >= 300 {
				respBody, _ := io.ReadAll(restResp.Body)
				t.Fatalf("REST %s status = %d, body = %s", tc.name, restResp.StatusCode, respBody)
			}
			restCalls := mock.inputsFor(tc.method)
			if len(restCalls) != 1 {
				t.Fatalf("REST %s: Core.%s called %d times, want 1", tc.name, tc.method, len(restCalls))
			}
			if !reflect.DeepEqual(restCalls[0], tc.want) {
				t.Errorf("REST %s: Core.%s received %#v, want %#v", tc.name, tc.method, restCalls[0], tc.want)
			}

			// --- MCP ---------------------------------------------------------
			mock.reset()
			argsJSON, err := json.Marshal(tc.mcpParams)
			if err != nil {
				t.Fatalf("marshal MCP params: %v", err)
			}
			mcpReq := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tc.name, argsJSON)
			mcpResp := mcp.ProcessMCPMessage([]byte(mcpReq))
			var parsed struct {
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(mcpResp, &parsed); err != nil {
				t.Fatalf("parse MCP %s response: %v", tc.name, err)
			}
			if parsed.Error != nil {
				t.Fatalf("MCP %s returned a protocol error: %s", tc.name, parsed.Error.Message)
			}
			if parsed.Result.IsError {
				t.Fatalf("MCP %s returned a tool error: %s", tc.name, mcpResp)
			}
			mcpCalls := mock.inputsFor(tc.method)
			if len(mcpCalls) != 1 {
				t.Fatalf("MCP %s: Core.%s called %d times, want 1", tc.name, tc.method, len(mcpCalls))
			}
			if !reflect.DeepEqual(mcpCalls[0], tc.want) {
				t.Errorf("MCP %s: Core.%s received %#v, want %#v", tc.name, tc.method, mcpCalls[0], tc.want)
			}

			// --- CLI ---------------------------------------------------------
			// runSessionCmd itself constructs its own Core (buildSessionCore,
			// backed by a real history DB) and calls os.Exit/prints to stdout
			// on completion, so it cannot be driven directly in-process. This
			// instead exercises the exact same three steps it performs —
			// lookupSessionCommand, parseSessionArgs, then Core.Invoke — using
			// the real production functions, substituting only the mock for
			// the terminal Core.Invoke call.
			mock.reset()
			command, ok := lookupSessionCommand(tc.cliSubcommand)
			if !ok {
				t.Fatalf("CLI %s: no subcommand registered for %q", tc.name, tc.cliSubcommand)
			}
			if command.capability != tc.name {
				t.Fatalf("CLI subcommand %q maps to capability %q, want %q", tc.cliSubcommand, command.capability, tc.name)
			}
			input, err := parseSessionArgs(command.capability, tc.cliArgs)
			if err != nil {
				t.Fatalf("CLI %s: parseSessionArgs: %v", tc.name, err)
			}
			if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
				t.Fatalf("CLI %s: Core.Invoke: %v", tc.name, err)
			}
			cliCalls := mock.inputsFor(tc.method)
			if len(cliCalls) != 1 {
				t.Fatalf("CLI %s: Core.%s called %d times, want 1", tc.name, tc.method, len(cliCalls))
			}
			if !reflect.DeepEqual(cliCalls[0], tc.want) {
				t.Errorf("CLI %s: Core.%s received %#v, want %#v", tc.name, tc.method, cliCalls[0], tc.want)
			}

			// --- Cross-surface: all three produced the identical struct -----
			if !reflect.DeepEqual(restCalls[0], mcpCalls[0]) || !reflect.DeepEqual(mcpCalls[0], cliCalls[0]) {
				t.Errorf("%s: surfaces diverged on the Core input\n rest: %#v\n mcp:  %#v\n cli:  %#v",
					tc.name, restCalls[0], mcpCalls[0], cliCalls[0])
			}
		})
	}
}

// TestBehavioralParity_GenomeReduce extends the zero-business-logic proof to
// the genome family (issue #181 slice 1): REST, MCP, and the CLI dispatch path
// must each invoke Core.GenomeReduce exactly once for an equivalent request.
func TestBehavioralParity_GenomeReduce(t *testing.T) {
	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx := context.Background()

	// --- REST ---------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/genome/reduce", "application/json", nil)
	if err != nil {
		t.Fatalf("REST genome.reduce: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST genome.reduce status = %d, body = %s", resp.StatusCode, body)
	}
	if calls := mock.inputsFor("GenomeReduce"); len(calls) != 1 {
		t.Fatalf("REST genome.reduce: Core.GenomeReduce called %d times, want 1", len(calls))
	}

	// --- MCP (governed name) --------------------------------------------------
	mock.reset()
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"genome.reduce","arguments":{}}}`))
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP genome.reduce response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP genome.reduce failed: %s", mcpResp)
	}
	if calls := mock.inputsFor("GenomeReduce"); len(calls) != 1 {
		t.Fatalf("MCP genome.reduce: Core.GenomeReduce called %d times, want 1", len(calls))
	}

	// --- MCP (deprecated alias reduceGenome routes through the same Core) ----
	mock.reset()
	aliasResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"reduceGenome","arguments":{}}}`))
	if err := json.Unmarshal(aliasResp, &parsed); err != nil {
		t.Fatalf("parse MCP reduceGenome response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP reduceGenome alias failed: %s", aliasResp)
	}
	if calls := mock.inputsFor("GenomeReduce"); len(calls) != 1 {
		t.Fatalf("MCP reduceGenome alias: Core.GenomeReduce called %d times, want 1", len(calls))
	}

	// --- CLI ------------------------------------------------------------------
	mock.reset()
	command, ok := lookupGenomeCommand("reduce")
	if !ok {
		t.Fatal("CLI: no genome subcommand registered for \"reduce\"")
	}
	if command.capability != core.CapGenomeReduce {
		t.Fatalf("CLI subcommand \"reduce\" maps to %q, want %q", command.capability, core.CapGenomeReduce)
	}
	input, err := parseGenomeArgs(command.capability, nil)
	if err != nil {
		t.Fatalf("CLI parseGenomeArgs: %v", err)
	}
	if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
		t.Fatalf("CLI genome.reduce: Core.Invoke: %v", err)
	}
	if calls := mock.inputsFor("GenomeReduce"); len(calls) != 1 {
		t.Fatalf("CLI genome.reduce: Core.GenomeReduce called %d times, want 1", len(calls))
	}
}

// TestBehavioralParity_PlasmidInject extends the zero-business-logic proof to
// the plasmid family (issue #181 slice 2): REST, MCP (governed name and the
// deprecated injectPlasmid alias), and the CLI dispatch path must each decode
// an equivalent request into the identical typed input and invoke
// Core.PlasmidInject exactly once.
func TestBehavioralParity_PlasmidInject(t *testing.T) {
	const plasmidName = "go-rules"
	want := core.PlasmidInjectInput{Name: plasmidName}

	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx := context.Background()

	assertOneInjectCall := func(t *testing.T, surface string) {
		t.Helper()
		calls := mock.inputsFor("PlasmidInject")
		if len(calls) != 1 {
			t.Fatalf("%s plasmid.inject: Core.PlasmidInject called %d times, want 1", surface, len(calls))
		}
		if !reflect.DeepEqual(calls[0], want) {
			t.Errorf("%s plasmid.inject: Core.PlasmidInject received %#v, want %#v", surface, calls[0], want)
		}
	}

	// --- REST -----------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/plasmids/inject", "application/json",
		bytes.NewBufferString(`{"name":"go-rules"}`))
	if err != nil {
		t.Fatalf("REST plasmid.inject: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST plasmid.inject status = %d, body = %s", resp.StatusCode, body)
	}
	assertOneInjectCall(t, "REST")

	// --- MCP (governed name) ----------------------------------------------------
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	mock.reset()
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"plasmid.inject","arguments":{"name":"go-rules"}}}`))
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP plasmid.inject response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP plasmid.inject failed: %s", mcpResp)
	}
	assertOneInjectCall(t, "MCP")

	// --- MCP (deprecated alias injectPlasmid routes through the same Core) -----
	mock.reset()
	aliasResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"injectPlasmid","arguments":{"name":"go-rules"}}}`))
	if err := json.Unmarshal(aliasResp, &parsed); err != nil {
		t.Fatalf("parse MCP injectPlasmid response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP injectPlasmid alias failed: %s", aliasResp)
	}
	assertOneInjectCall(t, "MCP alias")

	// --- CLI --------------------------------------------------------------------
	mock.reset()
	command, ok := lookupPlasmidCommand("inject")
	if !ok {
		t.Fatal("CLI: no plasmid subcommand registered for \"inject\"")
	}
	if command.capability != core.CapPlasmidInject {
		t.Fatalf("CLI subcommand \"inject\" maps to %q, want %q", command.capability, core.CapPlasmidInject)
	}
	input, err := parsePlasmidArgs(command.capability, []string{plasmidName})
	if err != nil {
		t.Fatalf("CLI parsePlasmidArgs: %v", err)
	}
	if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
		t.Fatalf("CLI plasmid.inject: Core.Invoke: %v", err)
	}
	assertOneInjectCall(t, "CLI")
}

// TestBehavioralParity_MeshPromote extends the zero-business-logic proof to
// the substrate-grafting family (issue #181 slice 3): REST, MCP (governed
// name with camelCase keys AND the deprecated promotePR alias with its legacy
// kebab-case keys), and the CLI dispatch path must each decode an equivalent
// request into the identical typed input and invoke Core.MeshPromote exactly
// once.
func TestBehavioralParity_MeshPromote(t *testing.T) {
	want := core.MeshPromoteInput{Substrate: "core", Branch: "feat/x", PRNumber: "42"}

	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx := context.Background()

	assertOnePromoteCall := func(t *testing.T, surface string) {
		t.Helper()
		calls := mock.inputsFor("MeshPromote")
		if len(calls) != 1 {
			t.Fatalf("%s mesh.promote: Core.MeshPromote called %d times, want 1", surface, len(calls))
		}
		if !reflect.DeepEqual(calls[0], want) {
			t.Errorf("%s mesh.promote: Core.MeshPromote received %#v, want %#v", surface, calls[0], want)
		}
	}

	// --- REST -----------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/mesh/promotions", "application/json",
		bytes.NewBufferString(`{"substrate":"core","branch":"feat/x","prNumber":"42"}`))
	if err != nil {
		t.Fatalf("REST mesh.promote: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST mesh.promote status = %d, body = %s", resp.StatusCode, body)
	}
	assertOnePromoteCall(t, "REST")

	// --- MCP (governed name, camelCase contract keys) ----------------------------
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	mock.reset()
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mesh.promote","arguments":{"substrate":"core","branch":"feat/x","prNumber":"42"}}}`))
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP mesh.promote response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP mesh.promote failed: %s", mcpResp)
	}
	assertOnePromoteCall(t, "MCP")

	// --- MCP (deprecated promotePR alias, legacy kebab-case keys) --------------
	mock.reset()
	aliasResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"promotePR","arguments":{"substrate":"core","branch":"feat/x","pr-number":"42"}}}`))
	if err := json.Unmarshal(aliasResp, &parsed); err != nil {
		t.Fatalf("parse MCP promotePR response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP promotePR alias failed: %s", aliasResp)
	}
	assertOnePromoteCall(t, "MCP alias")

	// --- CLI --------------------------------------------------------------------
	mock.reset()
	command, ok := lookupMeshCommand("promote")
	if !ok {
		t.Fatal("CLI: no mesh subcommand registered for \"promote\"")
	}
	if command.capability != core.CapMeshPromote {
		t.Fatalf("CLI subcommand \"promote\" maps to %q, want %q", command.capability, core.CapMeshPromote)
	}
	input, err := parseMeshArgs(command.capability, []string{"core", "--branch", "feat/x", "--pr-number", "42"})
	if err != nil {
		t.Fatalf("CLI parseMeshArgs: %v", err)
	}
	if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
		t.Fatalf("CLI mesh.promote: Core.Invoke: %v", err)
	}
	assertOnePromoteCall(t, "CLI")

	// --- Graft, quickly: same four paths must reach MeshGraft ------------------
	mock.reset()
	graftResp, err := http.Post(server.URL+"/v1/mesh/grafts", "application/json",
		bytes.NewBufferString(`{"substrate":"core"}`))
	if err != nil {
		t.Fatalf("REST mesh.graft: %v", err)
	}
	defer graftResp.Body.Close()
	if graftResp.StatusCode >= 300 {
		body, _ := io.ReadAll(graftResp.Body)
		t.Fatalf("REST mesh.graft status = %d, body = %s", graftResp.StatusCode, body)
	}
	if calls := mock.inputsFor("MeshGraft"); len(calls) != 1 {
		t.Fatalf("REST mesh.graft: Core.MeshGraft called %d times, want 1", len(calls))
	}

	mock.reset()
	graftAlias := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"graftSubstrate","arguments":{"substrate":"core"}}}`))
	if err := json.Unmarshal(graftAlias, &parsed); err != nil {
		t.Fatalf("parse MCP graftSubstrate response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP graftSubstrate alias failed: %s", graftAlias)
	}
	if calls := mock.inputsFor("MeshGraft"); len(calls) != 1 {
		t.Fatalf("MCP graftSubstrate alias: Core.MeshGraft called %d times, want 1", len(calls))
	}
}

// TestBehavioralParity_SequenceRun extends the zero-business-logic proof to
// the sequence family (issue #181 slice 4): REST, MCP (governed name and the
// deprecated runSequence alias, including its legacy `path` argument
// fallback), and the CLI dispatch path must each decode an equivalent request
// into the identical typed input and invoke Core.SequenceRun exactly once.
func TestBehavioralParity_SequenceRun(t *testing.T) {
	want := core.SequenceRunInput{PathOrName: "deploy", Provider: "local"}

	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx := context.Background()

	assertOneRunCall := func(t *testing.T, surface string, wantInput core.SequenceRunInput) {
		t.Helper()
		calls := mock.inputsFor("SequenceRun")
		if len(calls) != 1 {
			t.Fatalf("%s sequence.run: Core.SequenceRun called %d times, want 1", surface, len(calls))
		}
		if !reflect.DeepEqual(calls[0], wantInput) {
			t.Errorf("%s sequence.run: Core.SequenceRun received %#v, want %#v", surface, calls[0], wantInput)
		}
	}

	// --- REST -----------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/sequences/run", "application/json",
		bytes.NewBufferString(`{"pathOrName":"deploy","provider":"local"}`))
	if err != nil {
		t.Fatalf("REST sequence.run: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST sequence.run status = %d, body = %s", resp.StatusCode, body)
	}
	assertOneRunCall(t, "REST", want)

	// --- MCP (governed name) ------------------------------------------------------
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	mock.reset()
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sequence.run","arguments":{"pathOrName":"deploy","provider":"local"}}}`))
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP sequence.run response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP sequence.run failed: %s", mcpResp)
	}
	assertOneRunCall(t, "MCP", want)

	// --- MCP (deprecated runSequence alias, legacy `path` fallback key) --------
	mock.reset()
	aliasResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"runSequence","arguments":{"path":"deploy"}}}`))
	if err := json.Unmarshal(aliasResp, &parsed); err != nil {
		t.Fatalf("parse MCP runSequence response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP runSequence alias failed: %s", aliasResp)
	}
	// The alias never carried provider overrides — it maps to a bare run.
	assertOneRunCall(t, "MCP alias", core.SequenceRunInput{PathOrName: "deploy"})

	// --- CLI --------------------------------------------------------------------
	mock.reset()
	command, ok := lookupSequenceCommand("run")
	if !ok {
		t.Fatal("CLI: no sequence subcommand registered for \"run\"")
	}
	if command.capability != core.CapSequenceRun {
		t.Fatalf("CLI subcommand \"run\" maps to %q, want %q", command.capability, core.CapSequenceRun)
	}
	input, detach, err := parseSequenceArgs(command.capability, []string{"deploy", "--provider", "local"})
	if err != nil {
		t.Fatalf("CLI parseSequenceArgs: %v", err)
	}
	if detach {
		t.Fatal("CLI parseSequenceArgs: detach must default to false")
	}
	if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
		t.Fatalf("CLI sequence.run: Core.Invoke: %v", err)
	}
	assertOneRunCall(t, "CLI", want)

	// --- List, quickly: REST and CLI dispatch reach SequenceList ---------------
	mock.reset()
	listResp, err := http.Get(server.URL + "/v1/sequences")
	if err != nil {
		t.Fatalf("REST sequence.list: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode >= 300 {
		t.Fatalf("REST sequence.list status = %d", listResp.StatusCode)
	}
	if calls := mock.inputsFor("SequenceList"); len(calls) != 1 {
		t.Fatalf("REST sequence.list: Core.SequenceList called %d times, want 1", len(calls))
	}
}

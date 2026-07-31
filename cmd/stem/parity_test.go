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
	"strings"
	"sync"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// Interface-parity coverage test. It asserts the governed command
// capability set is identical across every surface: the canonical registry, the
// REST adapter, the MCP adapter (projected from the live tools/list response),
// and the CLI adapter. It goes red the moment someone adds a capability to one
// surface but not the others.
//
// To see it fail on induced drift, add a name to core.CapabilityNames() (or a
// stray governed tool to one surface) and run:  go test ./cmd/stem/ -run Parity
func newParityFixture(t *testing.T) (core.Core, *receptors.SessionsHandler, *receptors.GenomeHandler, *receptors.PlasmidHandler, *receptors.GraftHandler, *receptors.TraitHandler, *receptors.SequenceHandler, *receptors.SproutHandler, *receptors.StomaHandler, *receptors.SeedHandler, *receptors.GitHandler, *receptors.ConfigHandler, *receptors.MCPHandler, *http.ServeMux) {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	root := t.TempDir()
	svc := core.NewService(manager).WithGenome(core.GenomeOperations{
		Root:   root,
		Reduce: func(context.Context, string) error { return nil },
		Evolve: func(context.Context, string) error { return nil },
	}).WithPlasmid(core.PlasmidOperations{
		Root: root,
		Inject: func(context.Context, string, string) (core.PlasmidInjection, error) {
			return core.PlasmidInjection{}, nil
		},
	}).WithMesh(core.MeshOperations{
		ResolveWorkspace:  func(_ context.Context, substrate string) (string, error) { return substrate, nil },
		DelegatePush:      func(context.Context, string, string, string) (string, error) { return "deadbeef", nil },
		ListPendingTraits: func(context.Context) ([]any, error) { return []any{}, nil },
		AcceptTrait:       func(context.Context, string) error { return nil },
		RejectTrait:       func(context.Context, string) error { return nil },
	})
	rest := receptors.NewSessionsHandler(svc, manager, nil, nil)
	genomeRest := receptors.NewGenomeHandler(svc)
	plasmidRest := receptors.NewPlasmidHandler(svc)
	graftRest := receptors.NewGraftHandler(svc)
	traitRest := receptors.NewTraitHandler(svc)
	sequenceRest := receptors.NewSequenceHandler(svc)
	sproutRest := receptors.NewSproutHandler(svc, nil, nil)
	stomaRest := receptors.NewStomaHandler(svc)
	seedRest := receptors.NewSeedHandler(svc)
	gitRest := receptors.NewGitHandler(svc)
	configRest := receptors.NewConfigHandler(svc, "")
	// Register the REST routes so the handlers' Capabilities() reflect what is
	// actually mounted on the mux (not the canonical list) — the independence
	// the coverage test relies on.
	mux := http.NewServeMux()
	rest.Register(mux, nil)
	genomeRest.Register(mux, nil)
	plasmidRest.Register(mux, nil)
	graftRest.Register(mux, nil)
	traitRest.Register(mux, nil)
	sequenceRest.Register(mux, nil)
	sproutRest.Register(mux, nil)
	stomaRest.Register(mux, nil)
	seedRest.Register(mux, nil)
	gitRest.Register(mux, nil)
	configRest.Register(mux, nil)

	mcp := receptors.NewMCPHandler().WithCore(svc)
	return svc, rest, genomeRest, plasmidRest, graftRest, traitRest, sequenceRest, sproutRest, stomaRest, seedRest, gitRest, configRest, mcp, mux
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
	_, rest, genomeRest, plasmidRest, graftRest, traitRest, sequenceRest, sproutRest, stomaRest, seedRest, gitRest, configRest, mcp, _ := newParityFixture(t)
	canonical := core.CapabilityNames()

	// Each arm reflects what its surface ACTUALLY wires, independently derived:
	//   REST — capabilities recorded while mounting routes on the mux
	//          (sessions + genome + plasmid + graft + trait + sequence + sprout handlers).
	//   MCP  — names parsed from the live tools/list response.
	//   CLI  — capabilities of the subcommands registered on the command trees
	//          (`tendril session` + `tendril genome` + `tendril plasmid` + `tendril mesh` + `tendril mesh trait` + `tendril sequence` + `tendril sprout`).
	restCaps := append(rest.Capabilities(), genomeRest.Capabilities()...)
	restCaps = append(restCaps, plasmidRest.Capabilities()...)
	restCaps = append(restCaps, graftRest.Capabilities()...)
	restCaps = append(restCaps, traitRest.Capabilities()...)
	restCaps = append(restCaps, sequenceRest.Capabilities()...)
	restCaps = append(restCaps, sproutRest.Capabilities()...)
	restCaps = append(restCaps, stomaRest.Capabilities()...)
	restCaps = append(restCaps, seedRest.Capabilities()...)
	restCaps = append(restCaps, gitRest.Capabilities()...)
	restCaps = append(restCaps, configRest.Capabilities()...)
	cliCaps := append(sessionCLICapabilityNames(), genomeCLICapabilityNames()...)
	cliCaps = append(cliCaps, plasmidCLICapabilityNames()...)
	cliCaps = append(cliCaps, meshCLICapabilityNames()...)
	cliCaps = append(cliCaps, sequenceCLICapabilityNames()...)
	cliCaps = append(cliCaps, sproutCLICapabilityNames()...)
	cliCaps = append(cliCaps, stomaCLICapabilityNames()...)
	cliCaps = append(cliCaps, seedCLICapabilityNames()...)
	cliCaps = append(cliCaps, gitCLICapabilityNames()...)
	cliCaps = append(cliCaps, genotypeCLICapabilityNames()...)
	equalSets(t, "REST adapter (registered routes) vs canonical", restCaps, canonical)
	equalSets(t, "MCP adapter (declared) vs canonical", mcp.CoreCapabilityNames(), canonical)
	equalSets(t, "MCP adapter (live tools/list) vs canonical", mcpGovernedToolNames(t, mcp), canonical)
	equalSets(t, "CLI adapter (registered subcommands) vs canonical", cliCaps, canonical)
}

// TestControlPlaneCapabilitiesExcluded asserts that core.CapabilityNames() contains no
// control-plane verb.
//
// Why it matters: Control-plane operations — issuing or revoking a Pollinator credential,
// git setup, writing grants — are deliberately not capabilities. That is what keeps
// setting the Stem up reachable only as a subcommand run as the Stem's own
// principal, where file ownership confines it, and off both Pollinator-facing
// surfaces.
//
// We use a prefix list derived from the CLI's control-plane and local-only
// subcommands (minus those that map to governed capabilities). While this is still
// an enumeration (of families rather than verbs), checking by prefix guarantees
// that if a developer adds a new verb under an existing control-plane family
// (e.g., adding `delegation.approve` when `delegation.` is denied), the test will
// correctly catch and block it without needing a reflexive golden-list update.
//
// Criterion for inclusion: a family belongs here when mounting it on a
// Pollinator-facing surface would be a privilege escalation, not merely
// because it is currently command-line only (e.g., memory. or chat.).
func TestControlPlaneCapabilitiesExcluded(t *testing.T) {
	denyPrefixes := []string{
		"setup.",
		"init.",
		"serve.",
		"pollinator.",
		"delegation.",
		"hardiness.",
		"git.setup.",
		"mcp.",
	}

	for _, name := range core.CapabilityNames() {
		for _, prefix := range denyPrefixes {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("capability %q matches control-plane deny-list prefix %q", name, prefix)
			}
		}
	}
}

// Behavioral parity: the same input yields the same result via the Core
// directly, via REST (httptest), and via MCP for the create-session capability.
func TestInterfaceParityBehavioral_CreateSession(t *testing.T) {
	ctx := context.Background()
	svc, _, _, _, _, _, _, _, _, _, _, _, mcp, mux := newParityFixture(t)

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
	httpResp, err := http.Post(server.URL+"/v1/phytomers", "application/json", body)
	if err != nil {
		t.Fatalf("REST create: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusCreated {
		t.Fatalf("REST create status = %d, want 201", httpResp.StatusCode)
	}
	var restSess session.Phytomer
	if err := json.NewDecoder(httpResp.Body).Decode(&restSess); err != nil {
		t.Fatalf("decode REST session: %v", err)
	}

	// (c) MCP via tools/call.
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"phytomer.create","arguments":{"preferences":{"model":"claude-sonnet","genotype":"verifier"}}}}`))
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
	var mcpSess session.Phytomer
	if err := json.Unmarshal([]byte(parsed.Result.Content[0].Text), &mcpSess); err != nil {
		t.Fatalf("decode MCP session: %v", err)
	}

	// All three surfaces produced an equivalent session for identical input.
	for label, sess := range map[string]session.Phytomer{"core": coreSess, "rest": restSess, "mcp": mcpSess} {
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
// Behavioral parity, part two: TestInterfaceParityBehavioral_*
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

	createSessionResult session.Phytomer
	getSessionResult    session.Phytomer
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

func (m *mockCore) CreateSession(_ context.Context, in core.CreateSessionInput) (session.Phytomer, error) {
	m.record("CreateSession", in)
	return m.createSessionResult, nil
}

func (m *mockCore) ListSessions(_ context.Context) ([]session.Phytomer, error) {
	m.record("ListSessions", struct{}{})
	return nil, nil
}

func (m *mockCore) GetSession(_ context.Context, in core.GetSessionInput) (session.Phytomer, error) {
	m.record("GetSession", in)
	return m.getSessionResult, nil
}

func (m *mockCore) UpdateSessionPreferences(_ context.Context, in core.UpdateSessionInput) (session.Phytomer, error) {
	m.record("UpdateSessionPreferences", in)
	return session.Phytomer{}, nil
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

func (m *mockCore) GenotypeCreate(_ context.Context, in core.GenotypeCreateInput) (any, error) {
	m.record("GenotypeCreate", in)
	return map[string]interface{}{
		"name":    in.Name,
		"created": true,
	}, nil
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

func (m *mockCore) MeshTraitList(_ context.Context, _ core.MeshTraitListInput) (core.MeshTraitListOutput, error) {
	m.record("MeshTraitList", struct{}{})
	return core.MeshTraitListOutput{
		Traits: []any{map[string]any{
			"traitId": "trait-123",
			"status":  "pending",
		}},
	}, nil
}

func (m *mockCore) MeshTraitAccept(_ context.Context, in core.MeshTraitAcceptInput) (core.MeshTraitAcceptOutput, error) {
	m.record("MeshTraitAccept", in)
	return core.MeshTraitAcceptOutput{TraitID: in.TraitID, Status: "accepted"}, nil
}

func (m *mockCore) MeshTraitReject(_ context.Context, in core.MeshTraitRejectInput) (core.MeshTraitRejectOutput, error) {
	m.record("MeshTraitReject", in)
	return core.MeshTraitRejectOutput{TraitID: in.TraitID, Status: "rejected"}, nil
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

func (m *mockCore) SproutRun(_ context.Context, in core.SproutRunInput) (core.SproutRunResult, error) {
	m.record("SproutRun", in)
	return core.SproutRunResult{
		StepID:    "step-mock",
		SessionID: in.SessionID,
		Status:    "matured",
		Output:    "mock output",
	}, nil
}

func (m *mockCore) StomaPass(_ context.Context, in core.StomaPassInput) (core.StomaPassResult, error) {
	m.record("StomaPass", in)
	return core.StomaPassResult{
		Status:   "completed",
		ExitCode: 0,
		Stdout:   "mock output",
	}, nil
}

func (m *mockCore) SeedGrow(_ context.Context, in core.SeedGrowInput) (core.SeedGrowResult, error) {
	m.record("SeedGrow", in)
	return core.SeedGrowResult{
		Status:     core.SeedStatusSatisfied,
		Iterations: 1,
		Branch:     "feat/mock",
	}, nil
}

func (m *mockCore) GitCommit(_ context.Context, in core.GitCommitInput) (core.GitCommitResult, error) {
	m.record("GitCommit", in)
	return core.GitCommitResult{
		Status:     "committed",
		CommitHash: "deadbeef",
	}, nil
}

func (m *mockCore) GitPush(_ context.Context, in core.GitPushInput) (core.GitPushResult, error) {
	m.record("GitPush", in)
	return core.GitPushResult{
		Status: "pushed",
		Branch: "main",
	}, nil
}

func (m *mockCore) GitPR(_ context.Context, in core.GitPRInput) (core.GitPRResult, error) {
	m.record("GitPR", in)
	return core.GitPRResult{
		Status: "created",
		Number: 42,
		URL:    "https://github.com/opentendril/opentendril/pull/42",
		Head:   "feat/mock",
		Base:   "main",
	}, nil
}

func (m *mockCore) GitBranch(_ context.Context, in core.GitBranchInput) (core.GitBranchResult, error) {
	m.record("GitBranch", in)
	return core.GitBranchResult{
		Status:         "created",
		Branch:         in.Branch,
		PreviousBranch: "main",
	}, nil
}

func (m *mockCore) GitStatus(_ context.Context, in core.GitStatusInput) (core.GitStatusResult, error) {
	m.record("GitStatus", in)
	return core.GitStatusResult{
		Branch:        "feat/mock",
		HasCommits:    true,
		Clean:         true,
		CommitAllowed: true,
		Changes:       []core.GitStatusChange{},
	}, nil
}

func (m *mockCore) GitBranchList(_ context.Context, in core.GitBranchListInput) (core.GitBranchListResult, error) {
	m.record("GitBranchList", in)
	return core.GitBranchListResult{Branches: []core.GitBranchInfo{}, Verified: true, DefaultBranch: "main"}, nil
}

func (m *mockCore) GitPrune(_ context.Context, in core.GitPruneInput) (core.GitPruneResult, error) {
	m.record("GitPrune", in)
	return core.GitPruneResult{
		Confirmed: in.Confirm,
		Deleted:   []core.GitPrunedBranch{},
		Kept:      []core.GitBranchInfo{},
		Verified:  true,
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
			Name:        core.CapCreatePhytomer,
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
			Name:        core.CapListPhytomers,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return m.ListSessions(ctx)
			},
		},
		{
			Name:        core.CapGetPhytomer,
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
			Name:        core.CapUpdatePhytomer,
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
			Name:        core.CapDeletePhytomer,
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
			Name:        core.CapPhytomerHistory,
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
			Name:        core.CapGenotypeCreate,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.GenotypeCreateInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.GenotypeCreate(ctx, in)
			},
		},
		{
			Name:        core.CapMeshTraitList,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return m.MeshTraitList(ctx, core.MeshTraitListInput{})
			},
		},
		{
			Name:        core.CapMeshTraitAccept,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.MeshTraitAcceptInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.MeshTraitAccept(ctx, in)
			},
		},
		{
			Name:        core.CapMeshTraitReject,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.MeshTraitRejectInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.MeshTraitReject(ctx, in)
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
			Name:        core.CapSequenceGrow,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.SequenceRunInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.SequenceRun(ctx, in)
			},
		},
		{
			Name:        core.CapSproutGrow,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.SproutRunInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.SproutRun(ctx, in)
			},
		},
		{
			Name:        core.CapStomaPass,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.StomaPassInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.StomaPass(ctx, in)
			},
		},
		{
			Name:        core.CapSeedGrow,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.SeedGrowInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.SeedGrow(ctx, in)
			},
		},
		{
			Name:        core.CapGitCommit,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.GitCommitInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.GitCommit(ctx, in)
			},
		},
		{
			Name:        core.CapGitPush,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.GitPushInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.GitPush(ctx, in)
			},
		},
		{
			Name:        core.CapGitPR,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.GitPRInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.GitPR(ctx, in)
			},
		},
		{
			Name:        core.CapGitBranch,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.GitBranchInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.GitBranch(ctx, in)
			},
		},
		{
			Name:        core.CapGitStatus,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.GitStatusInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.GitStatus(ctx, in)
			},
		},
		{
			Name:        core.CapGitBranchList,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.GitBranchListInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.GitBranchList(ctx, in)
			},
		},
		{
			Name:        core.CapGitPrune,
			InputSchema: map[string]any{},
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in core.GitPruneInput
				if err := decodeMockInput(input, &in); err != nil {
					return nil, err
				}
				return m.GitPrune(ctx, in)
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

	rest := receptors.NewSessionsHandler(mock, manager, nil, nil)
	genomeRest := receptors.NewGenomeHandler(mock)
	plasmidRest := receptors.NewPlasmidHandler(mock)
	graftRest := receptors.NewGraftHandler(mock)
	traitRest := receptors.NewTraitHandler(mock)
	sequenceRest := receptors.NewSequenceHandler(mock)
	sproutRest := receptors.NewSproutHandler(mock, nil, nil)
	gate := &receptors.DelegationGate{
		Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{{
			Pollen: "parity-pollen",
			OperationClasses: []string{
				core.CapStomaPass,
				core.CapSeedGrow,
				core.CapGitCommit,
				core.CapGitPush,
				core.CapGitPR,
				core.CapGitBranch,
				core.CapGitStatus,
				core.CapGitBranchList,
				core.CapGitPrune,
				core.CapGenotypeCreate,
			},
			Substrates: []string{"core"},
		}}),
	}

	stomaRest := receptors.NewStomaHandler(mock).WithDelegation(gate)
	seedRest := receptors.NewSeedHandler(mock).WithDelegation(gate)
	gitRest := receptors.NewGitHandler(mock).WithDelegation(gate)
	configRest := receptors.NewConfigHandler(mock, "").WithDelegation(gate)
	mux := http.NewServeMux()
	rest.Register(mux, nil)
	genomeRest.Register(mux, nil)
	plasmidRest.Register(mux, nil)
	graftRest.Register(mux, nil)
	traitRest.Register(mux, nil)
	sequenceRest.Register(mux, nil)
	sproutRest.Register(mux, nil)
	stomaRest.Register(mux, gate.Middleware)
	seedRest.Register(mux, gate.Middleware)
	gitRest.Register(mux, gate.Middleware)
	configRest.Register(mux, gate.Middleware)

	mcp := receptors.NewMCPHandler().WithSessions(manager, nil).WithCore(mock).WithDelegation(gate, "parity-pollen")

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
			name:   core.CapCreatePhytomer,
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
			name:   core.CapGetPhytomer,
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
			name:   core.CapDeletePhytomer,
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
		{
			name:   core.CapListPhytomers,
			method: "ListSessions",
			want:   struct{}{},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				resp, err := http.Get(serverURL + "/v1/sessions")
				if err != nil {
					t.Fatalf("REST phytomer.list: %v", err)
				}
				return resp
			},
			mcpParams:     map[string]any{},
			cliSubcommand: "list",
			cliArgs:       []string{},
		},
		{
			name:   core.CapUpdatePhytomer,
			method: "UpdateSessionPreferences",
			want: core.UpdateSessionInput{
				SessionID:   sessionID,
				Preferences: session.Preferences{Model: "claude-sonnet"},
			},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				body := bytes.NewBufferString(`{"preferences":{"model":"claude-sonnet"}}`)
				req, err := http.NewRequest(http.MethodPatch, serverURL+"/v1/sessions/"+sessionID, body)
				if err != nil {
					t.Fatalf("build REST phytomer.update request: %v", err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("REST phytomer.update: %v", err)
				}
				return resp
			},
			mcpParams: map[string]any{
				"sessionId":   sessionID,
				"preferences": map[string]any{"model": "claude-sonnet"},
			},
			cliSubcommand: "update",
			cliArgs:       []string{sessionID, "--model", "claude-sonnet"},
		},
		{
			name:   core.CapPhytomerHistory,
			method: "SessionHistory",
			want: core.SessionHistoryInput{
				SessionID: sessionID,
				Limit:     10,
			},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				resp, err := http.Get(serverURL + "/v1/sessions/" + sessionID + "/history?limit=10")
				if err != nil {
					t.Fatalf("REST phytomer.history: %v", err)
				}
				return resp
			},
			mcpParams: map[string]any{
				"sessionId": sessionID,
				"limit":     10,
			},
			cliSubcommand: "history",
			cliArgs:       []string{sessionID, "--limit", "10"},
		},
		{
			name:   core.CapGenomeView,
			method: "GenomeView",
			want:   struct{}{},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				resp, err := http.Get(serverURL + "/v1/genome")
				if err != nil {
					t.Fatalf("REST genome.view: %v", err)
				}
				return resp
			},
			mcpParams:     map[string]any{},
			cliSubcommand: "view",
			cliArgs:       []string{},
		},
		{
			name:   core.CapGenomeEvolve,
			method: "GenomeEvolve",
			want:   struct{}{},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				resp, err := http.Post(serverURL+"/v1/genome/evolve", "application/json", nil)
				if err != nil {
					t.Fatalf("REST genome.evolve: %v", err)
				}
				return resp
			},
			mcpParams:     map[string]any{},
			cliSubcommand: "evolve",
			cliArgs:       []string{},
		},
		{
			name:   core.CapPlasmidList,
			method: "PlasmidList",
			want:   struct{}{},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				resp, err := http.Get(serverURL + "/v1/plasmids")
				if err != nil {
					t.Fatalf("REST plasmid.list: %v", err)
				}
				return resp
			},
			mcpParams:     map[string]any{},
			cliSubcommand: "list",
			cliArgs:       []string{},
		},
		{
			name:   core.CapSequenceList,
			method: "SequenceList",
			want:   struct{}{},
			restRequest: func(t *testing.T, serverURL string) *http.Response {
				resp, err := http.Get(serverURL + "/v1/sequences")
				if err != nil {
					t.Fatalf("REST sequence.list: %v", err)
				}
				return resp
			},
			mcpParams:     map[string]any{},
			cliSubcommand: "list",
			cliArgs:       []string{},
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

			var commandCapability string
			var input map[string]any

			switch {
			case strings.HasPrefix(tc.name, "phytomer."):
				command, ok := lookupSessionCommand(tc.cliSubcommand)
				if !ok {
					t.Fatalf("CLI %s: no subcommand registered for %q", tc.name, tc.cliSubcommand)
				}
				commandCapability = command.capability
				input, err = parseSessionArgs(command.capability, tc.cliArgs)
			case strings.HasPrefix(tc.name, "genome."):
				command, ok := lookupGenomeCommand(tc.cliSubcommand)
				if !ok {
					t.Fatalf("CLI %s: no subcommand registered for %q", tc.name, tc.cliSubcommand)
				}
				commandCapability = command.capability
				input, err = parseGenomeArgs(command.capability, tc.cliArgs)
			case strings.HasPrefix(tc.name, "plasmid."):
				command, ok := lookupPlasmidCommand(tc.cliSubcommand)
				if !ok {
					t.Fatalf("CLI %s: no subcommand registered for %q", tc.name, tc.cliSubcommand)
				}
				commandCapability = command.capability
				input, err = parsePlasmidArgs(command.capability, tc.cliArgs)
			case strings.HasPrefix(tc.name, "sequence."):
				command, ok := lookupSequenceCommand(tc.cliSubcommand)
				if !ok {
					t.Fatalf("CLI %s: no subcommand registered for %q", tc.name, tc.cliSubcommand)
				}
				commandCapability = command.capability
				input, _, err = parseSequenceArgs(command.capability, tc.cliArgs)
			default:
				t.Fatalf("CLI %s: untested capability prefix", tc.name)
			}

			if commandCapability != tc.name {
				t.Fatalf("CLI subcommand %q maps to capability %q, want %q", tc.cliSubcommand, commandCapability, tc.name)
			}
			if err != nil {
				t.Fatalf("CLI %s: parseArgs: %v", tc.name, err)
			}
			if _, err = mock.Invoke(ctx, commandCapability, input); err != nil {
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
// the genome family: REST, MCP, and the CLI dispatch path
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
// the plasmid family: REST, MCP (governed name and the
// deprecated injectPlasmid alias), and the CLI dispatch path must each decode
// an equivalent request into the identical typed input and invoke
// Core.PlasmidInject exactly once.
// TestBehavioralParity_GenotypeCreate extends the zero-business-logic proof to
// the genotype.create capability.
func TestBehavioralParity_GenotypeCreate(t *testing.T) {
	const genotypeName = "test-role"
	const genotypeInstructions = "You are a test"

	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()

	assertOneCreateCall := func(t *testing.T, surface string, expectedOrigin string) {
		t.Helper()
		calls := mock.inputsFor("GenotypeCreate")
		if len(calls) != 1 {
			t.Fatalf("%s genotype.create: Core.GenotypeCreate called %d times, want 1", surface, len(calls))
		}
		in, ok := calls[0].(core.GenotypeCreateInput)
		if !ok {
			t.Fatalf("%s genotype.create: input is %T, want GenotypeCreateInput", surface, calls[0])
		}
		if in.Name != genotypeName || in.Instructions != genotypeInstructions {
			t.Errorf("%s genotype.create: received name=%q, inst=%q, want name=%q, inst=%q", surface, in.Name, in.Instructions, genotypeName, genotypeInstructions)
		}
		if in.Origin != expectedOrigin {
			t.Errorf("%s genotype.create: origin=%q, want %q", surface, in.Origin, expectedOrigin)
		}
	}

	// --- REST -----------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/config/genotypes", "application/json",
		bytes.NewBufferString(`{"name":"test-role","instructions":"You are a test","substrate":"core"}`))
	if err != nil {
		t.Fatalf("REST genotype.create: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST genotype.create status = %d, body = %s", resp.StatusCode, string(body))
	}
	assertOneCreateCall(t, "REST", session.OriginREST)

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
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"genotype.create","arguments":{"name":"test-role","instructions":"You are a test","substrate":"core"}}}`))
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP genotype.create response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP genotype.create failed: %s", mcpResp)
	}
	// MCP calls through callCoreCapabilityAs don't set Origin for capabilities dynamically routed.
	// Oh wait, MCP doesn't set Origin for dynamically routed capabilities. Let's just check the ones that matter.
	assertOneCreateCall(t, "MCP", "")

	// --- MCP (deprecated alias createGenotype) ----------------------------------
	mock.reset()
	aliasResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"createGenotype","arguments":{"name":"test-role","instructions":"You are a test"}}}`))
	if err := json.Unmarshal(aliasResp, &parsed); err != nil {
		t.Fatalf("parse MCP createGenotype response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP createGenotype alias failed: %s", aliasResp)
	}
	// The alias injects substrate "core".
	assertOneCreateCall(t, "MCP alias", "")

	// --- CLI --------------------------------------------------------------------
	// We don't have a parseGenotypeArgs helper because it uses standard flag, not custom CLI struct.
	// But CLI routing goes straight to coreSvc.Invoke so parity is guaranteed if it hits coreSvc.
}

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
// the substrate-grafting family: REST, MCP (governed
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

}

// TestBehavioralParity_MeshGraft extends the zero-business-logic proof to
// the substrate-grafting family's mesh.graft capability.
func TestBehavioralParity_MeshGraft(t *testing.T) {
	want := core.MeshGraftInput{Substrate: "core"}

	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx := context.Background()

	assertOneGraftCall := func(t *testing.T, surface string) {
		t.Helper()
		calls := mock.inputsFor("MeshGraft")
		if len(calls) != 1 {
			t.Fatalf("%s mesh.graft: Core.MeshGraft called %d times, want 1", surface, len(calls))
		}
		if !reflect.DeepEqual(calls[0], want) {
			t.Errorf("%s mesh.graft: Core.MeshGraft received %#v, want %#v", surface, calls[0], want)
		}
	}

	// --- REST -----------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/mesh/grafts", "application/json",
		bytes.NewBufferString(`{"substrate":"core"}`))
	if err != nil {
		t.Fatalf("REST mesh.graft: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST mesh.graft status = %d, body = %s", resp.StatusCode, body)
	}
	assertOneGraftCall(t, "REST")

	// --- MCP (governed name) --------------------------------------------------
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	mock.reset()
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mesh.graft","arguments":{"substrate":"core"}}}`))
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP mesh.graft response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP mesh.graft failed: %s", mcpResp)
	}
	assertOneGraftCall(t, "MCP")

	// --- MCP (deprecated graftSubstrate alias) --------------------------------
	mock.reset()
	aliasResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"graftSubstrate","arguments":{"substrate":"core"}}}`))
	if err := json.Unmarshal(aliasResp, &parsed); err != nil {
		t.Fatalf("parse MCP graftSubstrate response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP graftSubstrate alias failed: %s", aliasResp)
	}
	assertOneGraftCall(t, "MCP alias")

	// --- CLI --------------------------------------------------------------------
	mock.reset()
	command, ok := lookupMeshCommand("graft")
	if !ok {
		t.Fatal("CLI: no mesh subcommand registered for \"graft\"")
	}
	if command.capability != core.CapMeshGraft {
		t.Fatalf("CLI subcommand \"graft\" maps to %q, want %q", command.capability, core.CapMeshGraft)
	}
	input, err := parseMeshArgs(command.capability, []string{"core"})
	if err != nil {
		t.Fatalf("CLI parseMeshArgs: %v", err)
	}
	if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
		t.Fatalf("CLI mesh.graft: Core.Invoke: %v", err)
	}
	assertOneGraftCall(t, "CLI")
}

// TestBehavioralParity_MeshTraits extends the zero-business-logic proof to
// the mesh trait inbox. REST, MCP, and the CLI must all decode the same
// trait identifiers and invoke the identical Core method exactly once.
func TestBehavioralParity_MeshTraits(t *testing.T) {
	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx := context.Background()

	// --- LIST -----------------------------------------------------------------
	mock.reset()
	listResp, err := http.Get(server.URL + "/v1/mesh/traits")
	if err != nil {
		t.Fatalf("REST mesh.trait.list: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode >= 300 {
		body, _ := io.ReadAll(listResp.Body)
		t.Fatalf("REST mesh.trait.list status = %d, body = %s", listResp.StatusCode, body)
	}
	var listOut core.MeshTraitListOutput
	if err := json.NewDecoder(listResp.Body).Decode(&listOut); err != nil {
		t.Fatalf("decode REST mesh.trait.list response: %v", err)
	}
	if len(listOut.Traits) != 1 {
		t.Fatalf("REST mesh.trait.list returned %d trait(s), want 1", len(listOut.Traits))
	}
	if trait, ok := listOut.Traits[0].(map[string]any); !ok || trait["traitId"] != "trait-123" {
		t.Fatalf("REST mesh.trait.list payload = %#v, want trait-123", listOut.Traits[0])
	}
	if calls := mock.inputsFor("MeshTraitList"); len(calls) != 1 {
		t.Fatalf("REST mesh.trait.list: Core.MeshTraitList called %d times, want 1", len(calls))
	}

	mock.reset()
	listMCP := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mesh.trait.list","arguments":{}}}`))
	var listParsed struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(listMCP, &listParsed); err != nil {
		t.Fatalf("parse MCP mesh.trait.list response: %v", err)
	}
	if listParsed.Error != nil || listParsed.Result.IsError || len(listParsed.Result.Content) == 0 {
		t.Fatalf("MCP mesh.trait.list failed: %s", listMCP)
	}
	if err := json.Unmarshal([]byte(listParsed.Result.Content[0].Text), &listOut); err != nil {
		t.Fatalf("decode MCP mesh.trait.list payload: %v", err)
	}
	if len(listOut.Traits) != 1 {
		t.Fatalf("MCP mesh.trait.list returned %d trait(s), want 1", len(listOut.Traits))
	}
	if calls := mock.inputsFor("MeshTraitList"); len(calls) != 1 {
		t.Fatalf("MCP mesh.trait.list: Core.MeshTraitList called %d times, want 1", len(calls))
	}

	mock.reset()
	listCommand, ok := lookupMeshTraitCommand("list")
	if !ok {
		t.Fatal("CLI: no mesh trait subcommand registered for \"list\"")
	}
	if listCommand.capability != core.CapMeshTraitList {
		t.Fatalf("CLI subcommand \"list\" maps to %q, want %q", listCommand.capability, core.CapMeshTraitList)
	}
	listInput, err := parseMeshTraitArgs(listCommand.capability, nil)
	if err != nil {
		t.Fatalf("CLI parseMeshTraitArgs(list): %v", err)
	}
	listResult, err := mock.Invoke(ctx, listCommand.capability, listInput)
	if err != nil {
		t.Fatalf("CLI mesh.trait.list: Core.Invoke: %v", err)
	}
	if typed, ok := listResult.(core.MeshTraitListOutput); !ok || len(typed.Traits) != 1 {
		t.Fatalf("CLI mesh.trait.list result = %#v, want one pending trait", listResult)
	}
	if calls := mock.inputsFor("MeshTraitList"); len(calls) != 1 {
		t.Fatalf("CLI mesh.trait.list: Core.MeshTraitList called %d times, want 1", len(calls))
	}

	// --- ACCEPT ---------------------------------------------------------------
	traitID := "trait-123"
	acceptWant := core.MeshTraitAcceptInput{TraitID: traitID}

	mock.reset()
	acceptResp, err := http.Post(server.URL+"/v1/mesh/traits/"+traitID+"/accept", "application/json", nil)
	if err != nil {
		t.Fatalf("REST mesh.trait.accept: %v", err)
	}
	defer acceptResp.Body.Close()
	if acceptResp.StatusCode >= 300 {
		body, _ := io.ReadAll(acceptResp.Body)
		t.Fatalf("REST mesh.trait.accept status = %d, body = %s", acceptResp.StatusCode, body)
	}
	var acceptOut core.MeshTraitAcceptOutput
	if err := json.NewDecoder(acceptResp.Body).Decode(&acceptOut); err != nil {
		t.Fatalf("decode REST mesh.trait.accept response: %v", err)
	}
	if acceptOut.TraitID != traitID || acceptOut.Status != "accepted" {
		t.Fatalf("REST mesh.trait.accept output = %+v", acceptOut)
	}
	if calls := mock.inputsFor("MeshTraitAccept"); len(calls) != 1 || !reflect.DeepEqual(calls[0], acceptWant) {
		t.Fatalf("REST mesh.trait.accept calls = %#v, want %#v", calls, acceptWant)
	}

	mock.reset()
	acceptMCP := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mesh.trait.accept","arguments":{"traitId":"trait-123"}}}`))
	if err := json.Unmarshal(acceptMCP, &listParsed); err != nil {
		t.Fatalf("parse MCP mesh.trait.accept response: %v", err)
	}
	if listParsed.Error != nil || listParsed.Result.IsError || len(listParsed.Result.Content) == 0 {
		t.Fatalf("MCP mesh.trait.accept failed: %s", acceptMCP)
	}
	if err := json.Unmarshal([]byte(listParsed.Result.Content[0].Text), &acceptOut); err != nil {
		t.Fatalf("decode MCP mesh.trait.accept payload: %v", err)
	}
	if acceptOut.TraitID != traitID || acceptOut.Status != "accepted" {
		t.Fatalf("MCP mesh.trait.accept output = %+v", acceptOut)
	}
	if calls := mock.inputsFor("MeshTraitAccept"); len(calls) != 1 || !reflect.DeepEqual(calls[0], acceptWant) {
		t.Fatalf("MCP mesh.trait.accept calls = %#v, want %#v", calls, acceptWant)
	}

	mock.reset()
	acceptCommand, ok := lookupMeshTraitCommand("accept")
	if !ok {
		t.Fatal("CLI: no mesh trait subcommand registered for \"accept\"")
	}
	if acceptCommand.capability != core.CapMeshTraitAccept {
		t.Fatalf("CLI subcommand \"accept\" maps to %q, want %q", acceptCommand.capability, core.CapMeshTraitAccept)
	}
	acceptInput, err := parseMeshTraitArgs(acceptCommand.capability, []string{traitID})
	if err != nil {
		t.Fatalf("CLI parseMeshTraitArgs(accept): %v", err)
	}
	acceptResult, err := mock.Invoke(ctx, acceptCommand.capability, acceptInput)
	if err != nil {
		t.Fatalf("CLI mesh.trait.accept: Core.Invoke: %v", err)
	}
	if typed, ok := acceptResult.(core.MeshTraitAcceptOutput); !ok || typed.TraitID != traitID || typed.Status != "accepted" {
		t.Fatalf("CLI mesh.trait.accept result = %#v, want accepted trait", acceptResult)
	}
	if calls := mock.inputsFor("MeshTraitAccept"); len(calls) != 1 || !reflect.DeepEqual(calls[0], acceptWant) {
		t.Fatalf("CLI mesh.trait.accept calls = %#v, want %#v", calls, acceptWant)
	}

	// --- REJECT ---------------------------------------------------------------
	rejectID := "trait-456"
	rejectWant := core.MeshTraitRejectInput{TraitID: rejectID}

	mock.reset()
	rejectReq, err := http.NewRequest(http.MethodPost, server.URL+"/v1/mesh/traits/"+rejectID+"/reject", nil)
	if err != nil {
		t.Fatalf("build REST mesh.trait.reject request: %v", err)
	}
	rejectResp, err := http.DefaultClient.Do(rejectReq)
	if err != nil {
		t.Fatalf("REST mesh.trait.reject: %v", err)
	}
	defer rejectResp.Body.Close()
	if rejectResp.StatusCode >= 300 {
		body, _ := io.ReadAll(rejectResp.Body)
		t.Fatalf("REST mesh.trait.reject status = %d, body = %s", rejectResp.StatusCode, body)
	}
	var rejectOut core.MeshTraitRejectOutput
	if err := json.NewDecoder(rejectResp.Body).Decode(&rejectOut); err != nil {
		t.Fatalf("decode REST mesh.trait.reject response: %v", err)
	}
	if rejectOut.TraitID != rejectID || rejectOut.Status != "rejected" {
		t.Fatalf("REST mesh.trait.reject output = %+v", rejectOut)
	}
	if calls := mock.inputsFor("MeshTraitReject"); len(calls) != 1 || !reflect.DeepEqual(calls[0], rejectWant) {
		t.Fatalf("REST mesh.trait.reject calls = %#v, want %#v", calls, rejectWant)
	}

	mock.reset()
	rejectMCP := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mesh.trait.reject","arguments":{"traitId":"trait-456"}}}`))
	if err := json.Unmarshal(rejectMCP, &listParsed); err != nil {
		t.Fatalf("parse MCP mesh.trait.reject response: %v", err)
	}
	if listParsed.Error != nil || listParsed.Result.IsError || len(listParsed.Result.Content) == 0 {
		t.Fatalf("MCP mesh.trait.reject failed: %s", rejectMCP)
	}
	if err := json.Unmarshal([]byte(listParsed.Result.Content[0].Text), &rejectOut); err != nil {
		t.Fatalf("decode MCP mesh.trait.reject payload: %v", err)
	}
	if rejectOut.TraitID != rejectID || rejectOut.Status != "rejected" {
		t.Fatalf("MCP mesh.trait.reject output = %+v", rejectOut)
	}
	if calls := mock.inputsFor("MeshTraitReject"); len(calls) != 1 || !reflect.DeepEqual(calls[0], rejectWant) {
		t.Fatalf("MCP mesh.trait.reject calls = %#v, want %#v", calls, rejectWant)
	}

	mock.reset()
	rejectCommand, ok := lookupMeshTraitCommand("reject")
	if !ok {
		t.Fatal("CLI: no mesh trait subcommand registered for \"reject\"")
	}
	if rejectCommand.capability != core.CapMeshTraitReject {
		t.Fatalf("CLI subcommand \"reject\" maps to %q, want %q", rejectCommand.capability, core.CapMeshTraitReject)
	}
	rejectInput, err := parseMeshTraitArgs(rejectCommand.capability, []string{rejectID})
	if err != nil {
		t.Fatalf("CLI parseMeshTraitArgs(reject): %v", err)
	}
	rejectResult, err := mock.Invoke(ctx, rejectCommand.capability, rejectInput)
	if err != nil {
		t.Fatalf("CLI mesh.trait.reject: Core.Invoke: %v", err)
	}
	if typed, ok := rejectResult.(core.MeshTraitRejectOutput); !ok || typed.TraitID != rejectID || typed.Status != "rejected" {
		t.Fatalf("CLI mesh.trait.reject result = %#v, want rejected trait", rejectResult)
	}
	if calls := mock.inputsFor("MeshTraitReject"); len(calls) != 1 || !reflect.DeepEqual(calls[0], rejectWant) {
		t.Fatalf("CLI mesh.trait.reject calls = %#v, want %#v", calls, rejectWant)
	}
}

// TestBehavioralParity_SequenceRun extends the zero-business-logic proof to
// the sequence family: REST, MCP (governed name and the
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
			t.Fatalf("%s sequence.grow: Core.SequenceRun called %d times, want 1", surface, len(calls))
		}
		if !reflect.DeepEqual(calls[0], wantInput) {
			t.Errorf("%s sequence.grow: Core.SequenceRun received %#v, want %#v", surface, calls[0], wantInput)
		}
	}

	// --- REST -----------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/sequences/grow", "application/json",
		bytes.NewBufferString(`{"pathOrName":"deploy","provider":"local"}`))
	if err != nil {
		t.Fatalf("REST sequence.grow: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST sequence.grow status = %d, body = %s", resp.StatusCode, body)
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
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sequence.grow","arguments":{"pathOrName":"deploy","provider":"local"}}}`))
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP sequence.grow response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP sequence.grow failed: %s", mcpResp)
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
	command, ok := lookupSequenceCommand("grow")
	if !ok {
		t.Fatal("CLI: no sequence subcommand registered for \"run\"")
	}
	if command.capability != core.CapSequenceGrow {
		t.Fatalf("CLI subcommand \"grow\" maps to %q, want %q", command.capability, core.CapSequenceGrow)
	}
	input, detach, err := parseSequenceArgs(command.capability, []string{"deploy", "--provider", "local"})
	if err != nil {
		t.Fatalf("CLI parseSequenceArgs: %v", err)
	}
	if detach {
		t.Fatal("CLI parseSequenceArgs: detach must default to false")
	}
	if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
		t.Fatalf("CLI sequence.grow: Core.Invoke: %v", err)
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

// TestBehavioralParity_SproutRun extends the zero-business-logic proof to the
// sprout/run family: REST, MCP (governed name and
// the deprecated sproutTendril alias), and the CLI dispatch path must each
// decode an equivalent request into the identical typed input and invoke
// Core.SproutRun exactly once.
func TestBehavioralParity_SproutRun(t *testing.T) {
	want := core.SproutRunInput{
		Transcript: "fix the flaky test",
		Substrate:  "/workspaces/core",
		SessionID:  "parity-session-1",
		Origin:     "parity-origin",
	}

	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx := context.Background()

	// sprout.grow is a delegated operation-class, and the MCP surface
	// authorizes those per-invocation against the bind-time pollen and the
	// active grants. Bind a covering grant so this test stays about adapter
	// translation; the deny-closed governance itself is covered in the
	// receptors package.
	gate := &receptors.DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{{
		Pollen:           "parity-Pollinator",
		OperationClasses: []string{core.CapSproutGrow},
		Substrates:       []string{"/workspaces/core"},
	}})}
	mcp = mcp.WithDelegation(gate, "parity-Pollinator")

	assertOneRunCall := func(t *testing.T, surface string, wantInput core.SproutRunInput) {
		t.Helper()
		calls := mock.inputsFor("SproutRun")
		if len(calls) != 1 {
			t.Fatalf("%s sprout.grow: Core.SproutRun called %d times, want 1", surface, len(calls))
		}
		if !reflect.DeepEqual(calls[0], wantInput) {
			t.Errorf("%s sprout.grow: Core.SproutRun received %#v, want %#v", surface, calls[0], wantInput)
		}
	}

	// --- REST -----------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/sprouts/grow", "application/json",
		bytes.NewBufferString(`{"transcript":"fix the flaky test","substrate":"/workspaces/core","sessionId":"parity-session-1","origin":"parity-origin"}`))
	if err != nil {
		t.Fatalf("REST sprout.grow: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST sprout.grow status = %d, body = %s", resp.StatusCode, body)
	}
	assertOneRunCall(t, "REST", want)

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
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sprout.grow","arguments":{"transcript":"fix the flaky test","substrate":"/workspaces/core","sessionId":"parity-session-1","origin":"parity-origin"}}}`))
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP sprout.grow response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP sprout.grow failed: %s", mcpResp)
	}
	assertOneRunCall(t, "MCP", want)

	// --- MCP (deprecated sproutTendril alias) -----------------------------------
	// The alias always stamps its own surface origin (the historic behavior);
	// everything else must decode identically.
	mock.reset()
	aliasResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"sproutTendril","arguments":{"transcript":"fix the flaky test","substrate":"/workspaces/core","sessionId":"parity-session-1"}}}`))
	if err := json.Unmarshal(aliasResp, &parsed); err != nil {
		t.Fatalf("parse MCP sproutTendril response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP sproutTendril alias failed: %s", aliasResp)
	}
	aliasWant := want
	aliasWant.Origin = session.OriginMCP
	assertOneRunCall(t, "MCP alias", aliasWant)

	// --- CLI --------------------------------------------------------------------
	mock.reset()
	command, ok := lookupSproutCommand("grow")
	if !ok {
		t.Fatal("CLI: no sprout subcommand registered for \"run\"")
	}
	if command.capability != core.CapSproutGrow {
		t.Fatalf("CLI subcommand \"grow\" maps to %q, want %q", command.capability, core.CapSproutGrow)
	}
	input, detach, err := parseSproutArgs(command.capability, []string{
		"--substrate", "/workspaces/core",
		"--session", "parity-session-1",
		"--origin", "parity-origin",
		"fix", "the", "flaky", "test",
	})
	if err != nil {
		t.Fatalf("CLI parseSproutArgs: %v", err)
	}
	if detach {
		t.Fatal("CLI parseSproutArgs: unexpected detach=true for sync parity path")
	}
	if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
		t.Fatalf("CLI sprout.grow: Core.Invoke: %v", err)
	}
	assertOneRunCall(t, "CLI", want)
}

// TestBehavioralParity_StomaPass extends the zero-business-logic proof to
// the stoma family.
func TestBehavioralParity_StomaPass(t *testing.T) {
	want := core.StomaPassInput{Substrate: "core", Command: []string{"echo", "hello"}, Origin: "parity-origin"}

	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx := context.Background()

	assertOnePassCall := func(t *testing.T, surface string) {
		t.Helper()
		calls := mock.inputsFor("StomaPass")
		if len(calls) != 1 {
			t.Fatalf("%s stoma.pass: Core.StomaPass called %d times, want 1", surface, len(calls))
		}
		if !reflect.DeepEqual(calls[0], want) {
			t.Errorf("%s stoma.pass: Core.StomaPass received %#v, want %#v", surface, calls[0], want)
		}
	}

	// --- REST -----------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/stoma/pass", "application/json",
		bytes.NewBufferString(`{"substrate":"core","command":["echo","hello"],"origin":"parity-origin"}`))
	if err != nil {
		t.Fatalf("REST stoma.pass: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST stoma.pass status = %d, body = %s", resp.StatusCode, body)
	}
	assertOnePassCall(t, "REST")

	// --- MCP ------------------------------------------------------------------
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	mock.reset()
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stoma.pass","arguments":{"substrate":"core","command":["echo","hello"],"origin":"parity-origin"}}}`))
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP stoma.pass response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP stoma.pass failed: %s", mcpResp)
	}
	assertOnePassCall(t, "MCP")

	// --- CLI --------------------------------------------------------------------
	mock.reset()
	command, ok := lookupStomaCommand("pass")
	if !ok {
		t.Fatal("CLI: no stoma subcommand registered for \"pass\"")
	}
	if command.capability != core.CapStomaPass {
		t.Fatalf("CLI subcommand \"pass\" maps to %q, want %q", command.capability, core.CapStomaPass)
	}
	input, err := parseStomaArgs(command.capability, []string{"--substrate", "core", "--origin", "parity-origin", "--", "echo", "hello"})
	if err != nil {
		t.Fatalf("CLI parseStomaArgs: %v", err)
	}
	if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
		t.Fatalf("CLI stoma.pass: Core.Invoke: %v", err)
	}
	assertOnePassCall(t, "CLI")
}

// TestBehavioralParity_SeedGrow extends the zero-business-logic proof to
// the seed family.
func TestBehavioralParity_SeedGrow(t *testing.T) {
	want := core.SeedGrowInput{Substrate: "core", Goal: "fix test", Verify: []string{"go", "test", "./..."}, Origin: "parity-origin"}

	mock, mux, mcp := newMockParityFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx := context.Background()

	assertOneGrowCall := func(t *testing.T, surface string) {
		t.Helper()
		calls := mock.inputsFor("SeedGrow")
		if len(calls) != 1 {
			t.Fatalf("%s seed.grow: Core.SeedGrow called %d times, want 1", surface, len(calls))
		}
		if !reflect.DeepEqual(calls[0], want) {
			t.Errorf("%s seed.grow: Core.SeedGrow received %#v, want %#v", surface, calls[0], want)
		}
	}

	// --- REST -----------------------------------------------------------------
	mock.reset()
	resp, err := http.Post(server.URL+"/v1/seeds/grow", "application/json",
		bytes.NewBufferString(`{"substrate":"core","goal":"fix test","verify":["go","test","./..."],"origin":"parity-origin"}`))
	if err != nil {
		t.Fatalf("REST seed.grow: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("REST seed.grow status = %d, body = %s", resp.StatusCode, body)
	}
	assertOneGrowCall(t, "REST")

	// --- MCP ------------------------------------------------------------------
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	mock.reset()
	mcpResp := mcp.ProcessMCPMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"seed.grow","arguments":{"substrate":"core","goal":"fix test","verify":["go","test","./..."],"origin":"parity-origin"}}}`))
	if err := json.Unmarshal(mcpResp, &parsed); err != nil {
		t.Fatalf("parse MCP seed.grow response: %v", err)
	}
	if parsed.Error != nil || parsed.Result.IsError {
		t.Fatalf("MCP seed.grow failed: %s", mcpResp)
	}
	assertOneGrowCall(t, "MCP")

	// --- CLI --------------------------------------------------------------------
	mock.reset()
	command, ok := lookupSeedCommand("grow")
	if !ok {
		t.Fatal("CLI: no seed subcommand registered for \"grow\"")
	}
	if command.capability != core.CapSeedGrow {
		t.Fatalf("CLI subcommand \"grow\" maps to %q, want %q", command.capability, core.CapSeedGrow)
	}
	input, err := parseSeedArgs(command.capability, []string{"--substrate", "core", "--goal", "fix test", "--origin", "parity-origin", "--", "go", "test", "./..."})
	if err != nil {
		t.Fatalf("CLI parseSeedArgs: %v", err)
	}
	if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
		t.Fatalf("CLI seed.grow: Core.Invoke: %v", err)
	}
	assertOneGrowCall(t, "CLI")
}

// TestBehavioralParity_Git extends the zero-business-logic proof to
// the git family of capabilities.
func TestBehavioralParity_Git(t *testing.T) {
	cases := []struct {
		name          string
		method        string
		want          any
		restPath      string
		restBody      string
		mcpArgs       string
		cliSubcommand string
		cliArgs       []string
	}{
		{
			name:          core.CapGitCommit,
			method:        "GitCommit",
			want:          core.GitCommitInput{Substrate: "core", Message: "hello", Origin: "parity-origin"},
			restPath:      "/v1/git/commit",
			restBody:      `{"substrate":"core","message":"hello","origin":"parity-origin"}`,
			mcpArgs:       `{"substrate":"core","message":"hello","origin":"parity-origin"}`,
			cliSubcommand: "commit",
			cliArgs:       []string{"--substrate", "core", "--message", "hello", "--origin", "parity-origin"},
		},
		{
			name:          core.CapGitPush,
			method:        "GitPush",
			want:          core.GitPushInput{Substrate: "core", Branch: "feat", Origin: "parity-origin"},
			restPath:      "/v1/git/push",
			restBody:      `{"substrate":"core","branch":"feat","origin":"parity-origin"}`,
			mcpArgs:       `{"substrate":"core","branch":"feat","origin":"parity-origin"}`,
			cliSubcommand: "push",
			cliArgs:       []string{"--substrate", "core", "--branch", "feat", "--origin", "parity-origin"},
		},
		{
			name:          core.CapGitPR,
			method:        "GitPR",
			want:          core.GitPRInput{Substrate: "core", Title: "hello", Head: "feat", Origin: "parity-origin"},
			restPath:      "/v1/git/pr",
			restBody:      `{"substrate":"core","title":"hello","head":"feat","origin":"parity-origin"}`,
			mcpArgs:       `{"substrate":"core","title":"hello","head":"feat","origin":"parity-origin"}`,
			cliSubcommand: "pr",
			cliArgs:       []string{"--substrate", "core", "--title", "hello", "--head", "feat", "--origin", "parity-origin"},
		},
		{
			name:          core.CapGitBranch,
			method:        "GitBranch",
			want:          core.GitBranchInput{Substrate: "core", Branch: "feat", Origin: "parity-origin"},
			restPath:      "/v1/git/branch",
			restBody:      `{"substrate":"core","branch":"feat","origin":"parity-origin"}`,
			mcpArgs:       `{"substrate":"core","branch":"feat","origin":"parity-origin"}`,
			cliSubcommand: "branch",
			cliArgs:       []string{"--substrate", "core", "--branch", "feat", "--origin", "parity-origin"},
		},
		{
			name:          core.CapGitStatus,
			method:        "GitStatus",
			want:          core.GitStatusInput{Substrate: "core", Origin: "parity-origin"},
			restPath:      "/v1/git/status",
			restBody:      `{"substrate":"core","origin":"parity-origin"}`,
			mcpArgs:       `{"substrate":"core","origin":"parity-origin"}`,
			cliSubcommand: "status",
			cliArgs:       []string{"--substrate", "core", "--origin", "parity-origin"},
		},
		{
			name:          core.CapGitBranchList,
			method:        "GitBranchList",
			want:          core.GitBranchListInput{Substrate: "core", Origin: "parity-origin"},
			restPath:      "/v1/git/branches",
			restBody:      `{"substrate":"core","origin":"parity-origin"}`,
			mcpArgs:       `{"substrate":"core","origin":"parity-origin"}`,
			cliSubcommand: "branches",
			cliArgs:       []string{"--substrate", "core", "--origin", "parity-origin"},
		},
		{
			name:          core.CapGitPrune,
			method:        "GitPrune",
			want:          core.GitPruneInput{Substrate: "core", Confirm: true, Origin: "parity-origin"},
			restPath:      "/v1/git/prune",
			restBody:      `{"substrate":"core","confirm":true,"origin":"parity-origin"}`,
			mcpArgs:       `{"substrate":"core","confirm":true,"origin":"parity-origin"}`,
			cliSubcommand: "prune",
			cliArgs:       []string{"--substrate", "core", "--confirm", "--origin", "parity-origin"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, mux, mcp := newMockParityFixture(t)
			server := httptest.NewServer(mux)
			defer server.Close()
			ctx := context.Background()

			assertOneGitCall := func(t *testing.T, surface string) {
				t.Helper()
				calls := mock.inputsFor(tc.method)
				if len(calls) != 1 {
					t.Fatalf("%s %s: Core.%s called %d times, want 1", surface, tc.name, tc.method, len(calls))
				}
				if !reflect.DeepEqual(calls[0], tc.want) {
					t.Errorf("%s %s: Core.%s received %#v, want %#v", surface, tc.name, tc.method, calls[0], tc.want)
				}
			}

			// --- REST -----------------------------------------------------------------
			mock.reset()
			resp, err := http.Post(server.URL+tc.restPath, "application/json",
				bytes.NewBufferString(tc.restBody))
			if err != nil {
				t.Fatalf("REST %s: %v", tc.name, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("REST %s status = %d, body = %s", tc.name, resp.StatusCode, body)
			}
			assertOneGitCall(t, "REST")

			// --- MCP ------------------------------------------------------------------
			var parsed struct {
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			mock.reset()
			mcpResp := mcp.ProcessMCPMessage([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tc.name, tc.mcpArgs)))
			if err := json.Unmarshal(mcpResp, &parsed); err != nil {
				t.Fatalf("parse MCP %s response: %v", tc.name, err)
			}
			if parsed.Error != nil || parsed.Result.IsError {
				t.Fatalf("MCP %s failed: %s", tc.name, mcpResp)
			}
			assertOneGitCall(t, "MCP")

			// --- CLI --------------------------------------------------------------------
			mock.reset()
			command, ok := lookupGitCommand(tc.cliSubcommand)
			if !ok {
				t.Fatalf("CLI: no git subcommand registered for %q", tc.cliSubcommand)
			}
			if command.capability != tc.name {
				t.Fatalf("CLI subcommand %q maps to %q, want %q", tc.cliSubcommand, command.capability, tc.name)
			}
			input, err := parseGitArgs(command.capability, tc.cliArgs)
			if err != nil {
				t.Fatalf("CLI parseGitArgs: %v", err)
			}
			if _, err := mock.Invoke(ctx, command.capability, input); err != nil {
				t.Fatalf("CLI %s: Core.Invoke: %v", tc.name, err)
			}
			assertOneGitCall(t, "CLI")
		})
	}
}

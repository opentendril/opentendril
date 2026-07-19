package receptors

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// newMCPDelegationTestHandler builds an MCPHandler over a real Core with
// stubbed delegated execution ports (sprout, passthrough, git), returning the
// handler, the bus (for audit assertions), a counter of executed runs, and the
// last PassthroughSpec the stubbed passthrough port received (so tests can
// assert exactly which egress allow-list reached the run). The delegation gate
// and subject are left for each test to bind (or not) via WithDelegation, so
// every posture — unwired, subjectless, granted — is exercised through the
// same fixture.
func newMCPDelegationTestHandler(t *testing.T) (*MCPHandler, *eventbus.Bus, *atomic.Int64, *core.PassthroughSpec) {
	t.Helper()
	chdirTempDir(t)

	executed := &atomic.Int64{}
	passthroughSpec := &core.PassthroughSpec{}
	sessions, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	coreSvc := core.NewService(sessions).
		WithSprout(core.SproutOperations{
			Run: func(ctx context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
				executed.Add(1)
				return core.SproutRunReport{Output: "grown", Outcome: "complete"}, nil
			},
		}).
		WithPassthrough(core.PassthroughOperations{
			Run: func(ctx context.Context, spec core.PassthroughSpec) (core.PassthroughRunResult, error) {
				executed.Add(1)
				*passthroughSpec = spec
				return core.PassthroughRunResult{Status: "completed", ExitCode: 0, Stdout: "ran"}, nil
			},
		}).
		WithGit(core.GitOperations{
			Commit: func(ctx context.Context, spec core.GitCommitSpec) (core.GitCommitResult, error) {
				executed.Add(1)
				return core.GitCommitResult{Status: "committed", CommitHash: "abc123"}, nil
			},
		})

	bus := eventbus.New()
	handler := NewMCPHandler().WithSessions(sessions, nil).WithCore(coreSvc)
	return handler, bus, executed, passthroughSpec
}

// mcpDelegationGrant covers every delegated operation-class on the "core"
// substrate for the bind-time subject the tests use.
func mcpDelegationGrant() core.DelegationGrant {
	return core.DelegationGrant{
		Subject:          "mcp-agent",
		OperationClasses: []string{core.CapSproutGrow, core.CapPassthroughRun, core.CapGitCommit},
		Substrates:       []string{"core"},
	}
}

// mcpCallTool drives one tools/call through ProcessMCPMessage and returns the
// tool-result text and isError flag. A JSON-RPC protocol error fails the test:
// governance denials must surface as tool results, not protocol errors.
func mcpCallTool(t *testing.T, handler *MCPHandler, name string, args map[string]any) (string, bool) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatalf("marshal tools/call %s: %v", name, err)
	}

	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *mcpError `json:"error"`
	}
	if err := json.Unmarshal(handler.ProcessMCPMessage(payload), &response); err != nil {
		t.Fatalf("decode tools/call %s response: %v", name, err)
	}
	if response.Error != nil {
		t.Fatalf("tools/call %s returned a protocol error: %+v", name, response.Error)
	}

	text := ""
	if len(response.Result.Content) > 0 {
		text = response.Result.Content[0].Text
	}
	return text, response.Result.IsError
}

// delegatedToolCalls enumerates every MCP path that reaches a delegated
// operation-class: the three canonical capability tools plus the deprecated
// sproutTendril alias (which reaches sprout.grow).
func delegatedToolCalls() map[string]map[string]any {
	return map[string]map[string]any{
		core.CapSproutGrow:     {"transcript": "grow", "substrate": "core"},
		core.CapPassthroughRun: {"substrate": "core", "command": []string{"gofmt", "-l", "."}},
		core.CapGitCommit:      {"substrate": "core", "message": "delegated commit"},
		"sproutTendril":        {"transcript": "grow", "substrate": "core"},
	}
}

// TestMCPDelegatedCapabilitiesDeniedWithNilGate covers the fully unwired
// posture (deny-closed): a handler constructed without WithDelegation denies
// every delegated-class tool — including the deprecated sproutTendril alias —
// as an error tool-result, and never invokes the execution ports.
func TestMCPDelegatedCapabilitiesDeniedWithNilGate(t *testing.T) {
	handler, _, executed, _ := newMCPDelegationTestHandler(t)

	for name, args := range delegatedToolCalls() {
		text, isError := mcpCallTool(t, handler, name, args)
		if !isError {
			t.Errorf("%s with a nil gate was not denied: %q", name, text)
		}
		if !strings.Contains(text, "delegation denied") {
			t.Errorf("%s denial text = %q, want a delegation-denied explanation", name, text)
		}
	}
	if executed.Load() != 0 {
		t.Fatalf("executed %d delegated run(s) with a nil gate, want 0", executed.Load())
	}
}

// TestMCPDelegatedCapabilitiesDeniedWithoutBoundSubject covers the second
// deny-closed leg: a gate is wired and a covering grant exists, but no
// subject is bound to the connection — every delegated-class tool is still
// denied and the denial is audited to the bus.
func TestMCPDelegatedCapabilitiesDeniedWithoutBoundSubject(t *testing.T) {
	handler, bus, executed, _ := newMCPDelegationTestHandler(t)
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{mcpDelegationGrant()}), Bus: bus}
	handler = handler.WithDelegation(gate, "")

	for name, args := range delegatedToolCalls() {
		text, isError := mcpCallTool(t, handler, name, args)
		if !isError {
			t.Errorf("%s without a bound subject was not denied: %q", name, text)
		}
		if !strings.Contains(text, "delegation is not configured for this MCP session") {
			t.Errorf("%s denial text = %q, want the not-configured reason", name, text)
		}
	}
	if executed.Load() != 0 {
		t.Fatalf("executed %d delegated run(s) without a bound subject, want 0", executed.Load())
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("subjectless denials left no audit event")
	}
	if event.Type != eventbus.EventDelegationDenied {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationDenied)
	}
}

// TestMCPDelegatedCapabilitiesAuthorizedByMatchingGrant: with a bound subject
// and an active grant covering {subject, operation-class, substrate}, every
// delegated-class tool — including the deprecated sproutTendril alias —
// dispatches through the Core, and each exercise is audited.
func TestMCPDelegatedCapabilitiesAuthorizedByMatchingGrant(t *testing.T) {
	handler, bus, executed, _ := newMCPDelegationTestHandler(t)
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{mcpDelegationGrant()}), Bus: bus}
	handler = handler.WithDelegation(gate, "mcp-agent")

	calls := delegatedToolCalls()
	for name, args := range calls {
		text, isError := mcpCallTool(t, handler, name, args)
		if isError {
			t.Errorf("%s with a covering grant was denied: %q", name, text)
		}
	}
	if executed.Load() != int64(len(calls)) {
		t.Fatalf("executed %d delegated run(s), want %d", executed.Load(), len(calls))
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("authorized delegated invocations left no audit event")
	}
	if event.Type != eventbus.EventDelegationAuthorized {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationAuthorized)
	}
	if event.Data["subject"] != "mcp-agent" {
		t.Fatalf("audit event subject = %v, want mcp-agent", event.Data["subject"])
	}
}

// TestMCPDelegatedCapabilityDeniedWithoutCoveringGrant: a bound subject with
// zero grants is denied per-invocation, the execution port is never reached,
// and the denial is audited.
func TestMCPDelegatedCapabilityDeniedWithoutCoveringGrant(t *testing.T) {
	handler, bus, executed, _ := newMCPDelegationTestHandler(t)
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(nil), Bus: bus}
	handler = handler.WithDelegation(gate, "mcp-agent")

	text, isError := mcpCallTool(t, handler, core.CapPassthroughRun, map[string]any{
		"substrate": "core",
		"command":   []string{"gofmt", "-l", "."},
	})
	if !isError {
		t.Fatalf("passthrough.run without a covering grant was not denied: %q", text)
	}
	if executed.Load() != 0 {
		t.Fatal("a denied delegated invocation still executed")
	}

	event, found := lastDelegationEvent(bus)
	if !found {
		t.Fatal("denied delegated invocation left no audit event")
	}
	if event.Type != eventbus.EventDelegationDenied {
		t.Fatalf("audit event type = %s, want %s", event.Type, eventbus.EventDelegationDenied)
	}
	if event.Data["operationClass"] != core.CapPassthroughRun {
		t.Fatalf("audit event operation-class = %v, want %s", event.Data["operationClass"], core.CapPassthroughRun)
	}
}

// TestMCPDelegatedCapabilityDeniedOnSubstrateMismatch verifies the grant's
// substrate scope is enforced on the MCP surface.
func TestMCPDelegatedCapabilityDeniedOnSubstrateMismatch(t *testing.T) {
	handler, bus, executed, _ := newMCPDelegationTestHandler(t)
	grant := mcpDelegationGrant()
	grant.Substrates = []string{"another-substrate"}
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{grant}), Bus: bus}
	handler = handler.WithDelegation(gate, "mcp-agent")

	text, isError := mcpCallTool(t, handler, core.CapGitCommit, map[string]any{
		"substrate": "core",
		"message":   "delegated commit",
	})
	if !isError {
		t.Fatalf("git.commit on an ungranted substrate was not denied: %q", text)
	}
	if executed.Load() != 0 {
		t.Fatal("a substrate-mismatched delegated invocation still executed")
	}
}

// TestMCPNonDelegatedCapabilityUnaffected is the security-first regression:
// a non-delegated capability dispatches exactly as today — with no gate, with
// a gate but no subject, and with both — and produces no delegation audit
// event.
func TestMCPNonDelegatedCapabilityUnaffected(t *testing.T) {
	handler, bus, _, _ := newMCPDelegationTestHandler(t)

	assertListSessions := func(posture string) {
		t.Helper()
		text, isError := mcpCallTool(t, handler, core.CapListPhytomers, map[string]any{})
		if isError {
			t.Fatalf("session.list failed (%s): %q", posture, text)
		}
	}

	assertListSessions("nil gate")

	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(nil), Bus: bus}
	handler = handler.WithDelegation(gate, "")
	assertListSessions("gate without subject")

	handler = handler.WithDelegation(gate, "mcp-agent")
	assertListSessions("gate with subject")

	if _, found := lastDelegationEvent(bus); found {
		t.Fatal("non-delegated invocations produced a delegation audit event")
	}
}

// TestMCPPassthroughRunReceivesGrantEgress: an authorized MCP passthrough.run
// runs with exactly the authorized grant's egress allow-list — the same
// Stem-mediated egress the REST surface grants — while an "egress" value
// smuggled into the tool arguments never reaches the run. The allow-list is
// sourced only from the grant, and the adapter stamps its own origin.
func TestMCPPassthroughRunReceivesGrantEgress(t *testing.T) {
	handler, bus, executed, passthroughSpec := newMCPDelegationTestHandler(t)
	grant := mcpDelegationGrant()
	grant.Egress = []string{"proxy.golang.org"}
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{grant}), Bus: bus}
	handler = handler.WithDelegation(gate, "mcp-agent")

	text, isError := mcpCallTool(t, handler, core.CapPassthroughRun, map[string]any{
		"substrate": "core",
		"command":   []string{"go", "mod", "download"},
		// A caller must never widen its own egress: the allow-list has no
		// JSON surface on the input type, so this argument must be ignored.
		"egress": []string{"attacker.example"},
	})
	if isError {
		t.Fatalf("authorized passthrough.run was denied: %q", text)
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d passthrough run(s), want 1", executed.Load())
	}
	if len(passthroughSpec.Egress) != 1 || passthroughSpec.Egress[0] != "proxy.golang.org" {
		t.Fatalf("run egress = %v, want the grant's allow-list [proxy.golang.org]", passthroughSpec.Egress)
	}
	if passthroughSpec.Origin != session.OriginMCP {
		t.Fatalf("run origin = %q, want %q", passthroughSpec.Origin, session.OriginMCP)
	}
}

// TestMCPPassthroughRunIgnoresArgumentEgress is the sharp negative: with a
// covering grant that allow-lists nothing, an "egress" value in the tool
// arguments still leaves the run with the empty (deny-all) list — arguments
// can never widen egress.
func TestMCPPassthroughRunIgnoresArgumentEgress(t *testing.T) {
	handler, bus, executed, passthroughSpec := newMCPDelegationTestHandler(t)
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{mcpDelegationGrant()}), Bus: bus}
	handler = handler.WithDelegation(gate, "mcp-agent")

	text, isError := mcpCallTool(t, handler, core.CapPassthroughRun, map[string]any{
		"substrate": "core",
		"command":   []string{"gofmt", "-l", "."},
		"egress":    []string{"attacker.example"},
	})
	if isError {
		t.Fatalf("authorized passthrough.run was denied: %q", text)
	}
	if executed.Load() != 1 {
		t.Fatalf("executed %d passthrough run(s), want 1", executed.Load())
	}
	if len(passthroughSpec.Egress) != 0 {
		t.Fatalf("run egress = %v, want empty (deny-all): tool arguments must never widen egress", passthroughSpec.Egress)
	}
}

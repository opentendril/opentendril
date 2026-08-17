package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/gateway"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
)

const (
	wsAPIKey     = "botanist-key"
	wsOwnPhyto   = "tendril-own"
	wsOtherPhyto = "tendril-other"
)

type streamFixture struct {
	server     *httptest.Server
	bus        *eventbus.Bus
	store      *historydb.Store
	credential string
	token      string
}

// newStreamFixture stands up the live stream exactly as the daemon mounts it:
// the shared bearer resolver in front, the observation authority behind it, and
// a history store recording that "claude" dispatched the run in wsOwnPhyto
// while "codex" dispatched the one in wsOtherPhyto.
func newStreamFixture(t *testing.T) (server *httptest.Server, bus *eventbus.Bus, credential string) {
	t.Helper()
	fx := newStreamFixtureWithRuns(t, []historydb.SproutRun{
		{RunID: "own", SessionID: wsOwnPhyto, Pollen: "claude", Substrate: "myrepo", Status: "matured"},
		{RunID: "other", SessionID: wsOtherPhyto, Pollen: "codex", Substrate: "myrepo", Status: "matured"},
	}, false)
	return fx.server, fx.bus, fx.credential
}

func newDispatchStreamFixture(t *testing.T) streamFixture {
	t.Helper()
	return newStreamFixtureWithRuns(t, []historydb.SproutRun{
		{RunID: "own-dispatch", SessionID: wsOwnPhyto, Pollen: "claude", Substrate: "myrepo", Status: "running"},
		{RunID: "other-dispatch", SessionID: wsOtherPhyto, Pollen: "codex", Substrate: "myrepo", Status: "running"},
	}, true)
}

func newStreamFixtureWithRuns(t *testing.T, runs []historydb.SproutRun, withToken bool) streamFixture {
	t.Helper()

	dir := t.TempDir()
	secret, _, err := core.IssuePollinatorCredential(dir, "claude", "")
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	credentials, err := core.LoadPollinatorCredentials(dir)
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}

	store, err := historydb.Open(context.Background(), filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, run := range runs {
		run.StartedAt = time.Now().UTC()
		if err := store.RecordSproutRun(context.Background(), run); err != nil {
			t.Fatalf("record %s: %v", run.RunID, err)
		}
	}

	bus := eventbus.New()
	t.Cleanup(bus.Shutdown)

	var signer *core.StemSigner
	token := ""
	if withToken {
		signer, err = core.LoadOrCreateStemSigner(dir)
		if err != nil {
			t.Fatalf("stem signer: %v", err)
		}
		token, err = signer.MintAccessToken("claude", 5*time.Minute, core.AccessTokenScope{})
		if err != nil {
			t.Fatalf("mint access token: %v", err)
		}
	}

	gate := &receptors.DelegationGate{
		Pollinators: credentials,
		Signer:      signer,
		Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{
			{Pollen: "claude", OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
		}),
		Bus: bus,
	}
	watch := receptors.NewWatchAuthority(gate, store)

	handler := withWebSocketAuth(wsAPIKey, credentials, signer, false,
		watch.StreamMiddleware(gateway.HandleWebSocket(bus)))
	server := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(server.Close)
	return streamFixture{
		server:     server,
		bus:        bus,
		store:      store,
		credential: secret,
		token:      token,
	}
}

// dialStream attempts a WebSocket upgrade and reports the handshake status.
func dialStream(t *testing.T, server *httptest.Server, path string, header http.Header) (*websocket.Conn, int) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + path
	conn, resp, err := websocket.DefaultDialer.Dial(url, header)
	status := 0
	if resp != nil {
		status = resp.StatusCode
		defer resp.Body.Close()
	}
	if err != nil {
		return nil, status
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, status
}

func bearerHeader(secret string) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + secret}}
}

// TestSproutRunRecordTakesItsSubjectFromTheContext pins where attribution can
// come from. The synchronous run path records through this one constructor, and
// a run whose subject came from the request body would be a run any caller
// could file under somebody else's name.
func TestSproutRunRecordTakesItsSubjectFromTheContext(t *testing.T) {
	spec := core.SproutSpec{StepID: "step-1", SessionID: "sess-1", Transcript: "grow"}

	delegated := openSproutRunRecord(core.WithPollen(context.Background(), "claude"), spec, "myrepo")
	if delegated.Pollen != "claude" {
		t.Fatalf("Pollen = %q, want claude", delegated.Pollen)
	}
	if delegated.Substrate != "myrepo" {
		t.Fatalf("Substrate = %q, want myrepo", delegated.Substrate)
	}

	operator := openSproutRunRecord(context.Background(), spec, "myrepo")
	if operator.Pollen != "" {
		t.Fatalf("a run with no authenticated subject was attributed to %q", operator.Pollen)
	}
}

// TestDelegatedStreamAcceptsItsOwnPhytomer is the access half: an issued
// credential opens the live stream for the phytomer its own run is in, which
// the Botanist key alone could do before.
func TestDelegatedStreamAcceptsItsOwnPhytomer(t *testing.T) {
	server, _, secret := newStreamFixture(t)

	conn, status := dialStream(t, server, "/ws?sessionId="+wsOwnPhyto, bearerHeader(secret))
	if conn == nil {
		t.Fatalf("a delegated caller was refused its own phytomer: status=%d", status)
	}
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", status)
	}
}

// TestDelegatedStreamRefusesAnotherPhytomer is the half that matters: the same
// credential, the same grant, a phytomer somebody else dispatched into.
func TestDelegatedStreamRefusesAnotherPhytomer(t *testing.T) {
	server, _, secret := newStreamFixture(t)

	conn, status := dialStream(t, server, "/ws?sessionId="+wsOtherPhyto, bearerHeader(secret))
	if conn != nil {
		t.Fatal("a delegated caller opened a stream on another subject's phytomer")
	}
	if status != http.StatusForbidden {
		t.Fatalf("handshake status = %d, want 403", status)
	}
}

// TestDelegatedStreamMustNameItsPhytomer refuses the unscoped firehose. A grant
// admits a caller to its own work, never to the whole organism's telemetry.
func TestDelegatedStreamMustNameItsPhytomer(t *testing.T) {
	server, _, secret := newStreamFixture(t)

	conn, status := dialStream(t, server, "/ws", bearerHeader(secret))
	if conn != nil {
		t.Fatal("a delegated caller opened the unscoped stream")
	}
	if status != http.StatusForbidden {
		t.Fatalf("handshake status = %d, want 403", status)
	}
}

// TestCredentialInQueryIsNotMistakenForTheOperator is the escalation this
// change had to avoid. The browser handshake carries its bearer in the query
// string, and everything downstream reads identity from the Authorization
// header — so a credential left in the query would authenticate the connection
// and then be invisible to the gate, admitting a Pollinator as the operator.
func TestCredentialInQueryIsNotMistakenForTheOperator(t *testing.T) {
	server, _, secret := newStreamFixture(t)

	// Unscoped: an operator may do this, a Pollinator may not. If the query
	// bearer were dropped after authenticating, this would upgrade.
	conn, status := dialStream(t, server, "/ws?key="+secret, nil)
	if conn != nil {
		t.Fatal("a credential presented in the query string was treated as the operator")
	}
	if status != http.StatusForbidden {
		t.Fatalf("handshake status = %d, want 403", status)
	}

	// Scoped to its own phytomer, the same query bearer is admitted — so the
	// refusal above is about identity rather than the query path being broken.
	scoped, scopedStatus := dialStream(t, server, "/ws?key="+secret+"&sessionId="+wsOwnPhyto, nil)
	if scoped == nil {
		t.Fatalf("a credential in the query string was refused its own phytomer: status=%d", scopedStatus)
	}
}

// TestOperatorStreamIsUnchanged pins the promise that scoping subjects did not
// scope the Botanist: its key still opens the unfiltered stream, by header and
// by query parameter, with no phytomer named.
func TestOperatorStreamIsUnchanged(t *testing.T) {
	server, _, _ := newStreamFixture(t)

	byHeader, headerStatus := dialStream(t, server, "/ws", bearerHeader(wsAPIKey))
	if byHeader == nil {
		t.Fatalf("the Botanist key was refused by header: status=%d", headerStatus)
	}
	byQuery, queryStatus := dialStream(t, server, "/ws?key="+wsAPIKey, nil)
	if byQuery == nil {
		t.Fatalf("the Botanist key was refused by query parameter: status=%d", queryStatus)
	}
}

// TestUnauthenticatedStreamIsRefused keeps the original guarantee: consulting a
// second resolver must not have opened a way in with no bearer at all.
func TestUnauthenticatedStreamIsRefused(t *testing.T) {
	server, _, _ := newStreamFixture(t)

	for _, path := range []string{"/ws", "/ws?sessionId=" + wsOwnPhyto, "/ws?key=wrong"} {
		conn, status := dialStream(t, server, path, nil)
		if conn != nil {
			t.Fatalf("%s upgraded with no valid bearer", path)
		}
		if status != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", path, status)
		}
	}
}

// TestDelegatedStreamCarriesOnlyItsOwnPhytomer is the widening check on the
// stream itself. Admission decides which connections open; this decides what an
// opened connection is allowed to hear, and an event belonging to another
// phytomer — or to no phytomer at all — must never reach it.
func TestDelegatedStreamCarriesOnlyItsOwnPhytomer(t *testing.T) {
	server, bus, secret := newStreamFixture(t)

	conn, status := dialStream(t, server, "/ws?sessionId="+wsOwnPhyto, bearerHeader(secret))
	if conn == nil {
		t.Fatalf("delegated dial refused: status=%d", status)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read connected frame: %v", err)
	}

	// Published in this order, so an unfiltered stream would deliver the
	// foreign and unattributed events FIRST and the assertion below would read
	// one of them rather than timing out.
	for _, event := range []eventbus.Event{
		{Type: eventbus.EventSproutEmerged, SessionID: wsOtherPhyto, Source: "test", Data: map[string]any{"mark": "foreign"}},
		{Type: eventbus.EventSproutEmerged, Source: "test", Data: map[string]any{"mark": "unattributed"}},
		{Type: eventbus.EventSproutEmerged, SessionID: wsOwnPhyto, Source: "test", Data: map[string]any{"mark": "mine"}},
	} {
		event.Timestamp = time.Now().UTC()
		bus.Publish(event)
	}

	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read event frame: %v", err)
	}
	var frame struct {
		SessionID string         `json:"sessionId"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("decode frame: %v", err)
	}
	if frame.SessionID != wsOwnPhyto {
		t.Fatalf("first event delivered was for phytomer %q, want %q: %s", frame.SessionID, wsOwnPhyto, payload)
	}
	if frame.Data["mark"] != "mine" {
		t.Fatalf("first event delivered was %v, want the one belonging to this subject: %s", frame.Data["mark"], payload)
	}
}

// TestDelegatedStreamAcceptsDispatchRowBeforeTerminal is the reachability
// half of dispatch-time ownership: a non-terminal row is enough. If ownership
// were written only when the run matured, this handshake would stay 403 for
// the entire live window.
func TestDelegatedStreamAcceptsDispatchRowBeforeTerminal(t *testing.T) {
	fx := newDispatchStreamFixture(t)

	conn, status := dialStream(t, fx.server, "/ws?sessionId="+wsOwnPhyto, bearerHeader(fx.credential))
	if conn == nil {
		t.Fatalf("a dispatch row was not enough to admit the dispatcher: status=%d", status)
	}
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", status)
	}
}

func TestDelegatedStreamRefusesAnotherSubjectsDispatchPhytomer(t *testing.T) {
	fx := newDispatchStreamFixture(t)

	conn, status := dialStream(t, fx.server, "/ws?sessionId="+wsOtherPhyto, bearerHeader(fx.credential))
	if conn != nil {
		t.Fatal("a delegated caller opened a stream on another subject's in-flight phytomer")
	}
	if status != http.StatusForbidden {
		t.Fatalf("handshake status = %d, want 403", status)
	}
}

func TestDelegatedStreamMustNameItsPhytomerWithOnlyDispatchRows(t *testing.T) {
	fx := newDispatchStreamFixture(t)

	conn, status := dialStream(t, fx.server, "/ws", bearerHeader(fx.credential))
	if conn != nil {
		t.Fatal("a delegated caller opened the unscoped stream against dispatch-only history")
	}
	if status != http.StatusForbidden {
		t.Fatalf("handshake status = %d, want 403", status)
	}
}

func TestOperatorStreamIsUnchangedWithOnlyDispatchRows(t *testing.T) {
	fx := newDispatchStreamFixture(t)

	byHeader, headerStatus := dialStream(t, fx.server, "/ws", bearerHeader(wsAPIKey))
	if byHeader == nil {
		t.Fatalf("the Botanist key was refused by header: status=%d", headerStatus)
	}
	byQuery, queryStatus := dialStream(t, fx.server, "/ws?key="+wsAPIKey, nil)
	if byQuery == nil {
		t.Fatalf("the Botanist key was refused by query parameter: status=%d", queryStatus)
	}
}

func TestCredentialInQueryIsNotMistakenForTheOperatorOnDispatchRow(t *testing.T) {
	fx := newDispatchStreamFixture(t)

	conn, status := dialStream(t, fx.server, "/ws?key="+fx.credential, nil)
	if conn != nil {
		t.Fatal("a credential presented in the query string was treated as the operator")
	}
	if status != http.StatusForbidden {
		t.Fatalf("handshake status = %d, want 403", status)
	}

	scoped, scopedStatus := dialStream(t, fx.server, "/ws?key="+fx.credential+"&sessionId="+wsOwnPhyto, nil)
	if scoped == nil {
		t.Fatalf("a credential in the query string was refused its own dispatch phytomer: status=%d", scopedStatus)
	}
}

func TestAccessTokenOpensDispatchRowOnLoopback(t *testing.T) {
	fx := newDispatchStreamFixture(t)
	if fx.token == "" {
		t.Fatal("fixture did not mint an access token")
	}

	conn, status := dialStream(t, fx.server, "/ws?sessionId="+wsOwnPhyto, bearerHeader(fx.token))
	if conn == nil {
		t.Fatalf("an access token was refused a dispatch row: status=%d", status)
	}
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", status)
	}

	unscoped, unscopedStatus := dialStream(t, fx.server, "/ws", bearerHeader(fx.token))
	if unscoped != nil {
		t.Fatal("an access token opened the unscoped stream")
	}
	if unscopedStatus != http.StatusForbidden {
		t.Fatalf("unscoped access-token status = %d, want 403", unscopedStatus)
	}
}

// TestGrowDispatchOwnershipAdmitsWatchBeforeTerrarium fails if the opening
// ownership row is written only when the run matures. The terrarium seam
// blocks after dispatch; a Pollen holding sprout.watch must already be
// admitted to that sessionId.
func TestGrowDispatchOwnershipAdmitsWatchBeforeTerrarium(t *testing.T) {
	fx := newDispatchStreamFixture(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	orig := runSproutTerrarium
	t.Cleanup(func() { runSproutTerrarium = orig })
	runSproutTerrarium = func(ctx context.Context, orch *conductor.DockerOrchestrator, transcript string) (conductor.SproutRunReport, error) {
		close(entered)
		<-release
		return conductor.SproutRunReport{Output: "later", Outcome: conductor.SproutOutcomeComplete}, nil
	}

	ops := sproutOperations(fx.store, fx.bus)
	spec := core.SproutSpec{
		StepID:     "step-live",
		SessionID:  "tendril-live",
		Transcript: "grow",
		Substrate:  "myrepo",
		Origin:     "test",
	}
	ctx := core.WithPollen(context.Background(), "claude")

	done := make(chan error, 1)
	go func() {
		_, err := ops.Run(ctx, spec)
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("terrarium seam was not reached")
	}

	runs, err := fx.store.LoadSproutRuns(context.Background(), "tendril-live", 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "running" || runs[0].Pollen != "claude" {
		t.Fatalf("dispatch row missing before Terrarium: %+v err=%v", runs, err)
	}

	conn, status := dialStream(t, fx.server, "/ws?sessionId=tendril-live", bearerHeader(fx.credential))
	if conn == nil {
		t.Fatalf("Pollen was refused its in-flight phytomer: status=%d", status)
	}
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", status)
	}

	unscoped, unscopedStatus := dialStream(t, fx.server, "/ws", bearerHeader(fx.credential))
	if unscoped != nil || unscopedStatus != http.StatusForbidden {
		t.Fatalf("unscoped Pollen during the run: conn=%v status=%d", unscoped != nil, unscopedStatus)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("grow: %v", err)
	}
}

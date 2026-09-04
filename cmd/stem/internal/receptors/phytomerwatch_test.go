package receptors

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

type sseEvent struct {
	event string
	data  string
}

type phytomerWatchStream struct {
	status int
	events <-chan sseEvent
	cancel context.CancelFunc
	close  func()
}

func newPhytomerWatchFixture(t *testing.T, grants []core.DelegationGrant) (*http.ServeMux, *historydb.Store, *eventbus.Bus) {
	t.Helper()

	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	bus := eventbus.New()
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: eventbus.New()}
	coreSvc := core.NewService(nil).WithPhytomerObservationSource(testPhytomerObservationSource(store))
	handler := NewSessionsHandler(coreSvc, nil, store, bus).
		WithWatch(NewWatchAuthority(gate, store))
	handler.watchPoll = 15 * time.Millisecond

	mux := http.NewServeMux()
	handler.Register(mux, gate.Middleware, nil)
	return mux, store, bus
}

func watchOwnerGrants() []core.DelegationGrant {
	return []core.DelegationGrant{
		{Pollen: watchOwner, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
		{Pollen: watchOther, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"myrepo"}},
	}
}

func recordWatchSeed(t *testing.T, store *historydb.Store, run historydb.SeedRun) {
	t.Helper()
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	if err := store.RecordSeedRun(context.Background(), run); err != nil {
		t.Fatalf("record seed: %v", err)
	}
}

func openPhytomerWatch(t *testing.T, mux http.Handler, phytomerID, pollen string) *phytomerWatchStream {
	t.Helper()
	server := httptest.NewServer(mux)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/phytomers/"+phytomerID+"/watch", nil)
	if err != nil {
		cancel()
		server.Close()
		t.Fatalf("watch request: %v", err)
	}
	if pollen != "" {
		req.Header.Set(PollenHeader, pollen)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		cancel()
		server.Close()
		t.Fatalf("watch do: %v", err)
	}

	events := make(chan sseEvent, 16)
	go func() {
		defer close(events)
		readSSEEvents(resp.Body, events)
	}()

	closed := false
	closeFn := func() {
		if closed {
			return
		}
		closed = true
		cancel()
		_ = resp.Body.Close()
		server.Close()
	}
	stream := &phytomerWatchStream{
		status: resp.StatusCode,
		events: events,
		cancel: func() {
			cancel()
			_ = resp.Body.Close()
		},
		close: closeFn,
	}
	t.Cleanup(stream.close)
	if resp.StatusCode == http.StatusOK {
		contentType := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(contentType, "text/event-stream") {
			t.Fatalf("Content-Type = %q, want text/event-stream", contentType)
		}
	}
	return stream
}

func readSSEEvents(r io.Reader, out chan<- sseEvent) {
	scanner := bufio.NewScanner(r)
	var event, data string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data != "" {
				out <- sseEvent{event: event, data: data}
			}
			event, data = "", ""
			continue
		}
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(value)
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = strings.TrimSpace(value)
		}
	}
	if data != "" {
		out <- sseEvent{event: event, data: data}
	}
}

func (s *phytomerWatchStream) nextObservation(t *testing.T, wait time.Duration) core.PhytomerObservation {
	t.Helper()
	ev := s.nextEvent(t, wait)
	if ev.event != "observation" {
		t.Fatalf("event = %q, want observation (%s)", ev.event, ev.data)
	}
	var obs core.PhytomerObservation
	if err := json.Unmarshal([]byte(ev.data), &obs); err != nil {
		t.Fatalf("decode observation: %v (%s)", err, ev.data)
	}
	return obs
}

func (s *phytomerWatchStream) nextEvent(t *testing.T, wait time.Duration) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			t.Fatal("watch stream closed before the next event")
		}
		return ev
	case <-time.After(wait):
		t.Fatal("timed out waiting for a watch event")
		return sseEvent{}
	}
}

func (s *phytomerWatchStream) expectClosed(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case ev, ok := <-s.events:
		if ok {
			t.Fatalf("watch stream emitted an extra event after terminal state: %+v", ev)
		}
	case <-time.After(wait):
		t.Fatal("watch stream did not close after terminal state")
	}
}

func observationFromRecorder(t *testing.T, rec *httptest.ResponseRecorder) core.PhytomerObservation {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("watch status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	events := make(chan sseEvent, 8)
	readSSEEvents(strings.NewReader(rec.Body.String()), events)
	close(events)
	ev, ok := <-events
	if !ok || ev.event != "observation" {
		t.Fatalf("recorder body was not an observation: %s", rec.Body.String())
	}
	var obs core.PhytomerObservation
	if err := json.Unmarshal([]byte(ev.data), &obs); err != nil {
		t.Fatalf("decode observation: %v (%s)", err, ev.data)
	}
	return obs
}

func TestPhytomerWatchCurrentStateBeforeSprout(t *testing.T) {
	mux, store, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-owned", Pollen: watchOwner, PhytomerID: "tendril-seed-owned",
		Substrate: "myrepo", Status: "running",
	})

	stream := openPhytomerWatch(t, mux, "tendril-seed-owned", watchOwner)
	if stream.status != http.StatusOK {
		t.Fatalf("owner watch before sprout = %d", stream.status)
	}
	obs := stream.nextObservation(t, time.Second)
	if obs.Handle != "seed-owned" || obs.PhytomerID != "tendril-seed-owned" || obs.Pollen != watchOwner {
		t.Fatalf("current state identities = %+v", obs)
	}
	if obs.Status != "running" || len(obs.Sprouts) != 0 || obs.Commit != "" {
		t.Fatalf("current state before sprout = %+v", obs)
	}
}

func TestPhytomerWatchDeniesWrongPollen(t *testing.T) {
	mux, store, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-owned", Pollen: watchOwner, PhytomerID: "tendril-seed-owned",
		Substrate: "myrepo", Status: "running",
	})
	denied := watchRequest(t, mux, "/v1/phytomers/tendril-seed-owned/watch", watchOther)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("wrong pollen watch = %d, want 403: %s", denied.Code, denied.Body.String())
	}
	if strings.Contains(denied.Body.String(), "seed-owned") || strings.Contains(denied.Body.String(), "event: observation") {
		t.Fatalf("denied watch leaked state: %s", denied.Body.String())
	}
}

func TestPhytomerWatchUnknownPhytomerFailsClosed(t *testing.T) {
	mux, _, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	denied := watchRequest(t, mux, "/v1/phytomers/tendril-unknown/watch", watchOwner)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("unknown phytomer watch = %d, want 403: %s", denied.Code, denied.Body.String())
	}
}

func TestPhytomerWatchTerminalClosesWithFruit(t *testing.T) {
	for i := 0; i < 20; i++ {
		mux, store, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
		recordWatchSeed(t, store, historydb.SeedRun{
			Handle: "seed-done", Pollen: watchOwner, PhytomerID: "tendril-done",
			Substrate: "myrepo", Status: core.SeedStatusSatisfied, Iterations: 2,
			Branch: "tendril/seed-fruit", Commit: "abc123def456",
		})
		rec := watchRequest(t, mux, "/v1/phytomers/tendril-done/watch", watchOwner)
		obs := observationFromRecorder(t, rec)
		if obs.Status != core.SeedStatusSatisfied || obs.Branch != "tendril/seed-fruit" || obs.Commit != "abc123def456" {
			t.Fatalf("iteration %d terminal fruit = %+v", i, obs)
		}
		if obs.Handle != "seed-done" || obs.PhytomerID != "tendril-done" {
			t.Fatalf("iteration %d identities = %+v", i, obs)
		}
	}
}

func TestPhytomerWatchExhaustedCloses(t *testing.T) {
	mux, store, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-exhausted", Pollen: watchOwner, PhytomerID: "tendril-exhausted",
		Substrate: "myrepo", Status: core.SeedStatusExhausted, Iterations: 3,
	})
	rec := watchRequest(t, mux, "/v1/phytomers/tendril-exhausted/watch", watchOwner)
	obs := observationFromRecorder(t, rec)
	if obs.Status != core.SeedStatusExhausted {
		t.Fatalf("exhausted status = %q", obs.Status)
	}
	if obs.Commit != "" {
		t.Fatalf("exhausted fabricated a commit: %q", obs.Commit)
	}
}

func TestPhytomerWatchRaceDoesNotLeakContradictorySprout(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-race", Pollen: watchOwner, PhytomerID: "tendril-race",
		Substrate: "myrepo", Status: "running",
	})

	src := testPhytomerObservationSource(store)
	loadSprouts := src.SproutsByPhytomer
	src.SproutsByPhytomer = func(ctx context.Context, phytomerID string) ([]core.SproutObservationEvidence, error) {
		seedWatchRun(t, store, historydb.SproutRun{
			RunID: "run-intruder", SessionID: phytomerID, StepID: "run-intruder",
			Pollen: watchOther, Substrate: "myrepo", Status: "running",
			Provider: "intruder-provider", Model: "intruder-model",
		})
		return loadSprouts(ctx, phytomerID)
	}

	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(watchOwnerGrants()), Bus: eventbus.New()}
	handler := NewSessionsHandler(core.NewService(nil).WithPhytomerObservationSource(src), nil, store, eventbus.New()).
		WithWatch(NewWatchAuthority(gate, store))
	mux := http.NewServeMux()
	handler.Register(mux, gate.Middleware, nil)

	rec := watchRequest(t, mux, "/v1/phytomers/tendril-race/watch", watchOwner)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("race watch = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, banned := range []string{
		"run-intruder", "intruder-provider", "intruder-model",
		"event: observation", watchOther,
	} {
		if strings.Contains(body, banned) {
			t.Fatalf("contradictory sprout leaked after authorized watch: %s", body)
		}
	}
}

func TestPhytomerWatchMidStreamContradictionDoesNotLeak(t *testing.T) {
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open history store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-mid", Pollen: watchOwner, PhytomerID: "tendril-mid",
		Substrate: "myrepo", Status: "running",
	})

	src := testPhytomerObservationSource(store)
	loadSprouts := src.SproutsByPhytomer
	calls := 0
	src.SproutsByPhytomer = func(ctx context.Context, phytomerID string) ([]core.SproutObservationEvidence, error) {
		calls++
		if calls == 2 {
			if err := store.RecordSproutRun(ctx, historydb.SproutRun{
				RunID: "run-intruder", SessionID: phytomerID, StepID: "run-intruder",
				Pollen: watchOther, Substrate: "myrepo", Status: "running",
				Provider: "intruder-provider", Model: "intruder-model",
				StartedAt: time.Now().UTC(),
			}); err != nil {
				return nil, err
			}
		}
		return loadSprouts(ctx, phytomerID)
	}

	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(watchOwnerGrants()), Bus: eventbus.New()}
	bus := eventbus.New()
	handler := NewSessionsHandler(core.NewService(nil).WithPhytomerObservationSource(src), nil, store, bus).
		WithWatch(NewWatchAuthority(gate, store))
	handler.watchPoll = 15 * time.Millisecond
	mux := http.NewServeMux()
	handler.Register(mux, gate.Middleware, nil)

	stream := openPhytomerWatch(t, mux, "tendril-mid", watchOwner)
	if stream.status != http.StatusOK {
		t.Fatalf("initial watch = %d", stream.status)
	}
	first := stream.nextObservation(t, time.Second)
	if first.Handle != "seed-mid" || first.Status != "running" {
		t.Fatalf("initial observation = %+v", first)
	}
	bus.Publish(eventbus.Event{Type: eventbus.EventSproutEmerged, SessionID: "tendril-mid", Source: "test"})

	for {
		select {
		case ev, ok := <-stream.events:
			if !ok {
				return
			}
			for _, banned := range []string{"run-intruder", "intruder-provider", "intruder-model"} {
				if strings.Contains(ev.data, banned) {
					t.Fatalf("mid-stream contradictory sprout leaked: %+v", ev)
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatal("watch stream did not close after contradictory sprout evidence appeared")
		}
	}
}

func TestPhytomerWatchDoesNotExposeRawSeedError(t *testing.T) {
	mux, store, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	hostile := "internal path /home/operator/private\nAuthorization: Bearer secret-token\nPRIVATE_PROMPT_CONTENT"
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-hostile", Pollen: watchOwner, PhytomerID: "tendril-hostile",
		Substrate: "myrepo", Status: core.SeedStatusWithered,
		Goal: "PRIVATE_PROMPT_CONTENT", Diff: "internal path /home/operator/private",
		Logs: "Authorization: Bearer secret-token", Error: hostile,
	})
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-hostile", SessionID: "tendril-hostile", StepID: "run-hostile",
		Pollen: watchOwner, Substrate: "myrepo", Status: "withered",
		Outcome: "failed", FailureCategory: string(core.FailureCategoryExecutionFailed),
		Transcript: "private reasoning SECRET_TOKEN=sk-secret",
		Output:     "chain-of-thought hidden",
		Error:      "Authorization: Bearer secret-token",
	})
	rec := watchRequest(t, mux, "/v1/phytomers/tendril-hostile/watch", watchOwner)
	obs := observationFromRecorder(t, rec)
	if obs.Status != core.SeedStatusWithered || obs.Handle != "seed-hostile" {
		t.Fatalf("hostile seed observation = %+v", obs)
	}
	body := rec.Body.String()
	for _, banned := range []string{
		"internal path /home/operator/private",
		"Authorization: Bearer secret-token",
		"PRIVATE_PROMPT_CONTENT",
		"private reasoning",
		"SECRET_TOKEN",
		"sk-secret",
		"chain-of-thought",
		"Bearer ",
	} {
		if strings.Contains(body, banned) {
			t.Fatalf("unsafe material %q in watch body: %s", banned, body)
		}
	}
}

func TestPhytomerWatchWitheredExposesStructuredFailure(t *testing.T) {
	mux, store, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-withered", Pollen: watchOwner, PhytomerID: "tendril-withered",
		Substrate: "myrepo", Status: core.SeedStatusWithered, Error: "growth failed",
	})
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-withered", SessionID: "tendril-withered", StepID: "run-withered",
		Pollen: watchOwner, Substrate: "myrepo", Status: "withered",
		Provider: "anthropic", Model: "claude-sonnet",
		Outcome: "failed", FailureCategory: string(core.FailureCategoryProviderAuthRejected),
		ProviderDiagnostic:       &historydb.ProviderDiagnostic{StatusCode: 401, Message: "User not found", Provider: "anthropic"},
		ProviderRequestAttempted: true,
		Transcript:               "private reasoning SECRET_TOKEN=sk-secret",
		Output:                   "chain-of-thought hidden",
	})
	rec := watchRequest(t, mux, "/v1/phytomers/tendril-withered/watch", watchOwner)
	obs := observationFromRecorder(t, rec)
	if obs.Status != core.SeedStatusWithered {
		t.Fatalf("withered status = %q", obs.Status)
	}
	if len(obs.Sprouts) != 1 {
		t.Fatalf("sprouts = %+v", obs.Sprouts)
	}
	if obs.Sprouts[0].FailureCategory != string(core.FailureCategoryProviderAuthRejected) {
		t.Fatalf("failureCategory = %q", obs.Sprouts[0].FailureCategory)
	}
	if obs.Sprouts[0].ProviderDiagnostic == nil || obs.Sprouts[0].ProviderDiagnostic.StatusCode != 401 {
		t.Fatalf("providerDiagnostic = %+v", obs.Sprouts[0].ProviderDiagnostic)
	}
	body := rec.Body.String()
	for _, banned := range []string{"private reasoning", "SECRET_TOKEN", "sk-secret", "chain-of-thought", "Bearer "} {
		if strings.Contains(body, banned) {
			t.Fatalf("unsafe material %q in watch body: %s", banned, body)
		}
	}
}

func TestPhytomerWatchLateConnectKeepsPriorState(t *testing.T) {
	mux, store, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-late", Pollen: watchOwner, PhytomerID: "tendril-late",
		Substrate: "myrepo", Status: "running", Iterations: 0,
	})
	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-1", SessionID: "tendril-late", StepID: "run-1",
		Pollen: watchOwner, Substrate: "myrepo", Status: "running",
		Provider: "anthropic", Model: "claude-sonnet",
		ProviderRequestAttempted: true, ToolInvocations: 4,
	})

	stream := openPhytomerWatch(t, mux, "tendril-late", watchOwner)
	obs := stream.nextObservation(t, time.Second)
	if obs.Status != "running" || len(obs.Sprouts) != 1 || obs.Sprouts[0].ToolInvocations != 4 {
		t.Fatalf("late connect lost prior state: %+v", obs)
	}
	if !obs.Sprouts[0].ProviderRequestAttempted || obs.Sprouts[0].Provider != "anthropic" {
		t.Fatalf("late connect sprout = %+v", obs.Sprouts[0])
	}
}

func TestPhytomerWatchFollowsProgressUntilTerminal(t *testing.T) {
	mux, store, bus := newPhytomerWatchFixture(t, watchOwnerGrants())
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-live", Pollen: watchOwner, PhytomerID: "tendril-live",
		Substrate: "myrepo", Status: "running",
	})

	stream := openPhytomerWatch(t, mux, "tendril-live", watchOwner)
	first := stream.nextObservation(t, time.Second)
	if first.Status != "running" || len(first.Sprouts) != 0 {
		t.Fatalf("initial observation = %+v", first)
	}

	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-1", SessionID: "tendril-live", StepID: "run-1",
		Pollen: watchOwner, Substrate: "myrepo", Status: "running",
		Provider: "anthropic", Model: "claude-sonnet",
		ProviderRequestAttempted: true, ToolInvocations: 1,
	})
	bus.Publish(eventbus.Event{Type: eventbus.EventSproutEmerged, SessionID: "tendril-live", Source: "test"})

	second := stream.nextObservation(t, 2*time.Second)
	if len(second.Sprouts) != 1 || second.Sprouts[0].RunID != "run-1" || second.Sprouts[0].ToolInvocations != 1 {
		t.Fatalf("progress observation = %+v", second)
	}

	seedWatchRun(t, store, historydb.SproutRun{
		RunID: "run-2", SessionID: "tendril-live", StepID: "run-2",
		Pollen: watchOwner, Substrate: "myrepo", Status: "running",
		Provider: "anthropic", Model: "claude-sonnet",
		ProviderRequestAttempted: true, ToolInvocations: 2,
	})
	bus.Publish(eventbus.Event{Type: eventbus.EventSproutEmerged, SessionID: "tendril-live", Source: "test"})

	third := stream.nextObservation(t, 2*time.Second)
	if len(third.Sprouts) != 2 {
		t.Fatalf("iteration observation sprouts = %d, want 2: %+v", len(third.Sprouts), third)
	}

	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-live", Pollen: watchOwner, PhytomerID: "tendril-live",
		Substrate: "myrepo", Status: core.SeedStatusSatisfied, Iterations: 2,
		Branch: "tendril/seed-fruit", Commit: "def789aaa",
	})

	terminal := stream.nextObservation(t, 2*time.Second)
	if terminal.Status != core.SeedStatusSatisfied || terminal.Branch != "tendril/seed-fruit" || terminal.Commit != "def789aaa" {
		t.Fatalf("terminal observation = %+v", terminal)
	}
	if len(terminal.Sprouts) != 2 {
		t.Fatalf("terminal sprouts = %d, want 2", len(terminal.Sprouts))
	}
	stream.expectClosed(t, time.Second)
}

func TestPhytomerWatchClientCancelStopsServerWork(t *testing.T) {
	for i := 0; i < 10; i++ {
		mux, store, bus := newPhytomerWatchFixture(t, watchOwnerGrants())
		recordWatchSeed(t, store, historydb.SeedRun{
			Handle: "seed-cancel", Pollen: watchOwner, PhytomerID: "tendril-cancel",
			Substrate: "myrepo", Status: "running",
		})
		stream := openPhytomerWatch(t, mux, "tendril-cancel", watchOwner)
		_ = stream.nextObservation(t, time.Second)
		if bus.HandlerCount(eventbus.EventSproutEmerged) != 1 {
			t.Fatalf("iteration %d handler count = %d, want 1", i, bus.HandlerCount(eventbus.EventSproutEmerged))
		}
		stream.cancel()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if bus.HandlerCount(eventbus.EventSproutEmerged) == 0 {
				break
			}
			time.Sleep(5 * time.Millisecond) // poll: wait until the watch unsubscribes after client cancel
		}
		if bus.HandlerCount(eventbus.EventSproutEmerged) != 0 {
			t.Fatalf("iteration %d subscription survived client cancel", i)
		}
	}
}

func TestPhytomerWatchOperatorSeesOwnedSeed(t *testing.T) {
	mux, store, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-op", Pollen: watchOwner, PhytomerID: "tendril-op",
		Substrate: "myrepo", Status: core.SeedStatusSatisfied,
		Branch: "tendril/seed-fruit", Commit: "opcommit1",
	})
	rec := watchRequest(t, mux, "/v1/phytomers/tendril-op/watch", "")
	obs := observationFromRecorder(t, rec)
	if obs.Commit != "opcommit1" || obs.Status != core.SeedStatusSatisfied {
		t.Fatalf("operator watch = %+v", obs)
	}
}

func TestPhytomerWatchEmitsFruitPublicationFailureAndCloses(t *testing.T) {
	mux, store, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	recordWatchSeed(t, store, historydb.SeedRun{
		Handle: "seed-publication-failure", Pollen: watchOwner, PhytomerID: "tendril-publication-failure",
		Substrate: "myrepo", Status: core.SeedStatusFruitPublicationFailed, Iterations: 2,
		PublicationDiagnostic: &historydb.SeedPublicationDiagnostic{
			FailureCategory: "fruit-publication", ExecutionStatus: core.SeedStatusSatisfied,
			Phase: "commit-mutation", Outcome: "reconciliation-unavailable", RetrySafe: false,
			Message: "read-only GitHub reconciliation could not establish the target state", RequestID: "req-safe-123",
		},
	})

	rec := watchRequest(t, mux, "/v1/phytomers/tendril-publication-failure/watch", watchOwner)
	obs := observationFromRecorder(t, rec)
	if obs.Status != core.SeedStatusFruitPublicationFailed || obs.Branch != "" || obs.Commit != "" {
		t.Fatalf("publication failure watch = %+v", obs)
	}
	if obs.PublicationDiagnostic == nil || obs.PublicationDiagnostic.Outcome != "reconciliation-unavailable" {
		t.Fatalf("publication diagnostic = %+v", obs.PublicationDiagnostic)
	}
	if strings.Contains(rec.Body.String(), "Authorization") || strings.Contains(rec.Body.String(), "PRIVATE_PROMPT_CONTENT") {
		t.Fatalf("unsafe publication material leaked: %s", rec.Body.String())
	}
}

func TestPhytomerWatchDoesNotAddSeedWatchCapability(t *testing.T) {
	for _, name := range core.CapabilityNames() {
		if name == "seed.watch" {
			t.Fatal("governed registry includes seed.watch")
		}
	}
	if core.IsDelegatedCapability("seed.watch") {
		t.Fatal("seed.watch was added to the delegated set")
	}
	mux, _, _ := newPhytomerWatchFixture(t, watchOwnerGrants())
	rec := watchRequest(t, mux, "/v1/seeds/watch", watchOwner)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/seeds/watch = %d, want 404 or 405: %s", rec.Code, rec.Body.String())
	}
}

func TestPhytomerWatchDispatchToFruitIntegration(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	store, err := historydb.Open(context.Background(), filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	started := make(chan struct{})
	release := make(chan struct{})
	coreSvc := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(ctx context.Context, spec core.SeedSpec, _ *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return core.SeedGrowResult{}, ctx.Err()
			}
			return core.SeedGrowResult{
				Status:     core.SeedStatusSatisfied,
				Iterations: 2,
				PhytomerID: spec.PhytomerID,
				Branch:     "tendril/seed-fruit",
				Commit:     "cafebabedeadbeef",
			}, nil
		},
	}).WithSeedPersistence(testSeedPersistence(store)).
		WithContinuationPersistence(testContinuationPersistence(store)).
		WithPhytomerObservationSource(testPhytomerObservationSource(store))

	grants := []core.DelegationGrant{
		{Pollen: watchOwner, OperationClasses: []string{core.CapSeedGrow, core.CapSproutWatch}, Substrates: []string{"core"}},
		{Pollen: watchOther, OperationClasses: []string{core.CapSproutWatch}, Substrates: []string{"core"}},
	}
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: eventbus.New()}
	bus := eventbus.New()
	sessions := NewSessionsHandler(coreSvc, manager, store, bus).WithWatch(NewWatchAuthority(gate, store))
	sessions.watchPoll = 15 * time.Millisecond
	seeds := NewSeedHandler(coreSvc).WithDelegation(gate).WithHistory(store)

	mux := http.NewServeMux()
	seeds.Register(mux, nil)
	sessions.Register(mux, gate.Middleware, nil)

	dispatch := dispatchSeedAsync(t, mux, watchOwner)
	if dispatch.Code != http.StatusAccepted {
		t.Fatalf("dispatch = %d: %s", dispatch.Code, dispatch.Body.String())
	}
	var accepted struct {
		Handle     string `json:"handle"`
		PhytomerID string `json:"phytomerId"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(dispatch.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode dispatch: %v", err)
	}
	if accepted.Handle == "" || accepted.PhytomerID == "" {
		t.Fatalf("dispatch payload = %+v", accepted)
	}

	foreign := watchRequest(t, mux, "/v1/phytomers/"+accepted.PhytomerID+"/watch", watchOther)
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("second pollen watch = %d, want 403: %s", foreign.Code, foreign.Body.String())
	}

	stream := openPhytomerWatch(t, mux, accepted.PhytomerID, watchOwner)
	if stream.status != http.StatusOK {
		t.Fatalf("owner watch = %d", stream.status)
	}
	initial := stream.nextObservation(t, time.Second)
	if initial.Handle != accepted.Handle || initial.PhytomerID != accepted.PhytomerID || initial.Status != "running" {
		t.Fatalf("initial watch = %+v, dispatch = %+v", initial, accepted)
	}
	if initial.Pollen != watchOwner || initial.Substrate != "core" {
		t.Fatalf("ownership in current state = %+v", initial)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("seed execution did not start")
	}

	if err := store.RecordSproutRun(context.Background(), historydb.SproutRun{
		RunID: "run-build-1", SessionID: accepted.PhytomerID, StepID: "run-build-1",
		Pollen: watchOwner, Substrate: "core", Status: "running",
		Provider: "anthropic", Model: "claude-sonnet",
		ProviderRequestAttempted: true, ToolInvocations: 2,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record sprout: %v", err)
	}
	bus.Publish(eventbus.Event{Type: eventbus.EventSproutEmerged, SessionID: accepted.PhytomerID, Source: "test"})

	progress := stream.nextObservation(t, 2*time.Second)
	if len(progress.Sprouts) != 1 || progress.Sprouts[0].ToolInvocations != 2 || !progress.Sprouts[0].ProviderRequestAttempted {
		t.Fatalf("progress watch = %+v", progress)
	}

	close(release)
	terminal := stream.nextObservation(t, 2*time.Second)
	if terminal.Status != core.SeedStatusSatisfied || terminal.Branch != "tendril/seed-fruit" || terminal.Commit != "cafebabedeadbeef" {
		t.Fatalf("terminal watch = %+v", terminal)
	}
	if terminal.Handle != accepted.Handle || terminal.PhytomerID != accepted.PhytomerID {
		t.Fatalf("terminal identities drifted: %+v", terminal)
	}
	stream.expectClosed(t, time.Second)

	stillDenied := watchRequest(t, mux, "/v1/phytomers/"+accepted.PhytomerID+"/watch", watchOther)
	if stillDenied.Code != http.StatusForbidden {
		t.Fatalf("second pollen after terminal = %d, want 403: %s", stillDenied.Code, stillDenied.Body.String())
	}

	events := watchRequest(t, mux, "/v1/phytomers/"+accepted.PhytomerID+"/events", watchOwner)
	if events.Code != http.StatusOK {
		t.Fatalf("existing events view = %d: %s", events.Code, events.Body.String())
	}
	runs := watchRequest(t, mux, "/v1/phytomers/"+accepted.PhytomerID+"/sprout-runs", watchOwner)
	if runs.Code != http.StatusOK {
		t.Fatalf("existing sprout-runs view = %d: %s", runs.Code, runs.Body.String())
	}
}

func TestPhytomerWatchNoHistoryIsNotImplemented(t *testing.T) {
	grants := watchOwnerGrants()
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer(grants), Bus: eventbus.New()}
	handler := NewSessionsHandler(core.NewService(nil), nil, nil, nil).
		WithWatch(NewWatchAuthority(gate, nil))
	mux := http.NewServeMux()
	handler.Register(mux, gate.Middleware, nil)
	rec := watchRequest(t, mux, "/v1/phytomers/tendril-x/watch", watchOwner)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("watch without history = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

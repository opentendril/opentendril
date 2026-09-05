package receptors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// SessionsHandler is the REST adapter for the session-lifecycle capabilities.
// The six governed capabilities (see core.CapabilityNames) route through the
// transport-free core.Core; this handler only translates HTTP↔core and holds
// no business logic for them. The manager/history/bus references remain for the
// ungoverned read routes (events, sprout-runs, watch) and the async sequence
// trigger, which are follow-up capabilities not yet part of the parity registry.
type SessionsHandler struct {
	core    core.Core
	manager *session.Manager
	history *historydb.Store
	bus     *eventbus.Bus
	// watch decides who may observe a phytomer's events, run records, and
	// headless current-state watch. A nil authority denies every delegated
	// observer and leaves the operator's view unchanged.
	watch *WatchAuthority
	// delegation gates phytomer.continue per-invocation. Ordinary phytomer
	// command routes stay on the blanket delegated-denial lane; continuation
	// is the one delegated Phytomer command and must reach a grant decision.
	delegation *DelegationGate
	// watchPoll is the bounded interval used to re-read durable current state
	// while a phytomer watch is open. Zero selects the default. EventBus
	// wakeups can prompt a re-read sooner; this interval is the fallback so
	// Seed settlement is not missed when no event is published.
	watchPoll time.Duration
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewSessionsHandler creates the sessions REST surface. core owns the governed
// session capabilities; manager/history/bus back the ungoverned routes.
// history may be nil when SQLite logging is disabled; bus may be nil in tests.
func NewSessionsHandler(coreSvc core.Core, manager *session.Manager, history *historydb.Store, bus *eventbus.Bus) *SessionsHandler {
	return &SessionsHandler{core: coreSvc, manager: manager, history: history, bus: bus}
}

// WithWatch wires the observation authority onto the handler and returns it
// for chaining.
func (h *SessionsHandler) WithWatch(watch *WatchAuthority) *SessionsHandler {
	h.watch = watch
	return h
}

// WithDelegation wires the grant authorizer used by phytomer.continue.
func (h *SessionsHandler) WithDelegation(gate *DelegationGate) *SessionsHandler {
	h.delegation = gate
	return h
}

// governedRoute binds one REST route to the Core capability it projects.
type governedRoute struct {
	pattern    string
	capability string
	handler    http.HandlerFunc
}

// governedRoutes is the single table of session-capability routes this adapter
// wires. Register mounts exactly these and records their capabilities, so the
// advertised set in Capabilities() reflects what is *actually registered* —
// deleting an entry here both un-mounts the route and makes the parity coverage
// test's REST arm diverge from the canonical registry.
func (h *SessionsHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"POST /v1/phytomers", core.CapCreatePhytomer, h.create},
		{"GET /v1/phytomers", core.CapListPhytomers, h.list},
		{"GET /v1/phytomers/{sessionId}", core.CapGetPhytomer, h.get},
		{"PATCH /v1/phytomers/{sessionId}", core.CapUpdatePhytomer, h.updatePreferences},
		{"DELETE /v1/phytomers/{sessionId}", core.CapDeletePhytomer, h.remove},
		{"GET /v1/phytomers/{sessionId}/history", core.CapPhytomerHistory, h.messages},
	}
}

// delegatedGovernedRoutes are governed Phytomer commands that must reach a
// per-invocation grant decision rather than the blanket delegated-denial lane.
func (h *SessionsHandler) delegatedGovernedRoutes() []governedRoute {
	return []governedRoute{
		{"POST /v1/phytomers/{sessionId}/continue", core.CapContinuePhytomer, h.continuePhytomer},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). The parity coverage test compares
// this to core.CapabilityNames(); an unregistered governed route makes it
// diverge. Register must run before this returns a meaningful set.
func (h *SessionsHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// writeCoreErr maps a transport-neutral core error onto an HTTP status.
func writeCoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrNotFound):
		http.Error(w, "session not found", http.StatusNotFound)
	case errors.Is(err, core.ErrContinuationInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, core.ErrContinuationTargetNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, core.ErrContinuationPollenMismatch):
		http.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, core.ErrContinuationNotEligible),
		errors.Is(err, core.ErrContinuationIdempotencyConflict),
		errors.Is(err, core.ErrContinuationTargetChanged):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, core.ErrContinuationHistoryUnavailable),
		errors.Is(err, core.ErrContinuationNotWired),
		errors.Is(err, core.ErrSeedHistoryUnavailable):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, conductor.ErrWorkspaceAbsent):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// Register mounts the session routes onto the mux. Two lanes, because the
// routes answer to two different authorities and a single wrapper could only
// serve one of them.
//
// auth carries the command routes: it authenticates, and it refuses a
// delegated caller outright, because those routes project capabilities that
// evaluate no per-invocation grant here.
//
// observeAuth carries the observation views. They authenticate the same way and
// then consult the observation authority per request, with the phytomer in
// hand, so a delegated caller reaches a decision about the phytomer it named
// rather than a blanket refusal.
func (h *SessionsHandler) Register(mux *http.ServeMux, auth, observeAuth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}
	if observeAuth == nil {
		observeAuth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	// Governed phytomer capabilities: mount each canonical /v1/phytomers route
	// and record the capability it projects, so Capabilities() reflects the
	// routes truly registered. Each route is also mounted under the legacy
	// /v1/sessions alias (same handler, same {sessionId} param) so existing
	// clients keep working through the botanisation; the alias is not recorded
	// in the parity set — the canonical /v1/phytomers surface is the contract.
	h.registered = h.registered[:0]
	seen := make(map[string]bool)
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		mux.HandleFunc(sessionAlias(route.pattern), auth(route.handler))
		if !seen[route.capability] {
			seen[route.capability] = true
			h.registered = append(h.registered, route.capability)
		}
	}

	// phytomer.continue is delegated: it uses the bare authentication lane
	// (observeAuth in production) so a Pollinator reaches a grant decision.
	// Ordinary phytomer command routes above stay on the refusing lane.
	for _, route := range h.delegatedGovernedRoutes() {
		mux.HandleFunc(route.pattern, observeAuth(route.handler))
		mux.HandleFunc(sessionAlias(route.pattern), observeAuth(route.handler))
		if !seen[route.capability] {
			seen[route.capability] = true
			h.registered = append(h.registered, route.capability)
		}
	}

	// Observation views — not part of the parity registry, and gated per
	// request inside the handler against the phytomer named in the path.
	// Canonical + legacy alias, as above.
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /v1/phytomers/{sessionId}/events":      h.events,
		"GET /v1/phytomers/{sessionId}/sprout-runs": h.sproutRuns,
		"GET /v1/phytomers/{sessionId}/watch":       h.phytomerWatch,
	} {
		mux.HandleFunc(pattern, observeAuth(handler))
		mux.HandleFunc(sessionAlias(pattern), observeAuth(handler))
	}

	// Ungoverned follow-up capability. It executes work rather than reporting
	// on it, and it evaluates no grant, so it stays on the refusing lane.
	for _, pattern := range []string{
		"POST /v1/phytomers/{sessionId}/sequences/grow",
	} {
		mux.HandleFunc(pattern, auth(h.runSequenceAsync))
		mux.HandleFunc(sessionAlias(pattern), auth(h.runSequenceAsync))
	}
}

// sessionAlias maps a canonical "/v1/phytomers…" route pattern to its legacy
// "/v1/sessions…" alias, preserving method and path parameters.
func sessionAlias(pattern string) string {
	return strings.Replace(pattern, "/v1/phytomers", "/v1/sessions", 1)
}

type createSessionRequest struct {
	Origin      string              `json:"origin,omitempty"`
	Preferences session.Preferences `json:"preferences"`
}

func (h *SessionsHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Origin == "" {
		req.Origin = session.OriginREST
	}

	sess, err := h.core.CreateSession(r.Context(), core.CreateSessionInput{
		Origin:      req.Origin,
		Preferences: req.Preferences,
	})
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (h *SessionsHandler) list(w http.ResponseWriter, r *http.Request) {
	sessions, err := h.core.ListSessions(r.Context())
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": sessions,
	})
}

func (h *SessionsHandler) get(w http.ResponseWriter, r *http.Request) {
	sess, err := h.core.GetSession(r.Context(), core.GetSessionInput{
		SessionID: r.PathValue("sessionId"),
	})
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

type updatePreferencesRequest struct {
	Preferences session.Preferences `json:"preferences"`
}

func (h *SessionsHandler) updatePreferences(w http.ResponseWriter, r *http.Request) {
	var req updatePreferencesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	sess, err := h.core.UpdateSessionPreferences(r.Context(), core.UpdateSessionInput{
		SessionID:   r.PathValue("sessionId"),
		Preferences: req.Preferences,
	})
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (h *SessionsHandler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.core.DeleteSession(r.Context(), core.DeleteSessionInput{
		SessionID: r.PathValue("sessionId"),
	}); err != nil {
		writeCoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type continuePhytomerRequest struct {
	Intent         string `json:"intent"`
	IdempotencyKey string `json:"idempotencyKey"`
	SessionID      string `json:"sessionId"`
}

func (h *SessionsHandler) continuePhytomer(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.PathValue("sessionId"))
	var req continuePhytomerRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if sessionID == "" || strings.TrimSpace(req.Intent) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		http.Error(w, "sessionId, intent, and idempotencyKey are required", http.StatusBadRequest)
		return
	}

	pollen, credentialOK := h.delegation.PollenFor(r)
	if !credentialOK {
		http.Error(w, "delegation denied: unknown or revoked Pollinator credential", http.StatusForbidden)
		return
	}
	ctx := core.WithPollen(r.Context(), pollen)
	in := core.ContinuationInput{
		PhytomerID:     sessionID,
		Intent:         req.Intent,
		IdempotencyKey: req.IdempotencyKey,
	}
	if pollen != "" {
		request, err := h.core.ResolveDelegationRequest(ctx, core.CapContinuePhytomer, map[string]any{
			"sessionId":      sessionID,
			"intent":         in.Intent,
			"idempotencyKey": in.IdempotencyKey,
		})
		if err != nil {
			writeCoreErr(w, err)
			return
		}
		decision := h.delegation.Authorize(request)
		if !decision.Authorized {
			http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
			return
		}
		ctx = core.WithAuthorizedDelegationRequest(ctx, request)
	}

	result, err := h.core.ContinuePhytomer(ctx, in)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *SessionsHandler) messages(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionId")
	messages, err := h.core.SessionHistory(r.Context(), core.SessionHistoryInput{
		SessionID: sessionID,
		Limit:     queryLimit(r, 50),
	})
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessionId": sessionID,
		"messages":  messages,
	})
}

func (h *SessionsHandler) events(w http.ResponseWriter, r *http.Request) {
	if h.history == nil {
		http.Error(w, "persistent history is disabled (TENDRIL_DB_LOGGING=false)", http.StatusNotImplemented)
		return
	}

	sessionID := r.PathValue("sessionId")

	// A phytomer's events are session-wide and name no owner individually, so
	// a delegated observer is admitted to all of them or to none.
	pollen, ok := h.watch.Observer(w, r)
	if !ok {
		return
	}
	if pollen != "" && !h.watch.AuthorizePhytomer(w, r, pollen, sessionID) {
		return
	}

	records, err := h.history.LoadEvents(r.Context(), sessionID, queryLimit(r, 100))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessionId": sessionID,
		"events":    records,
	})
}

func (h *SessionsHandler) sproutRuns(w http.ResponseWriter, r *http.Request) {
	if h.history == nil {
		http.Error(w, "persistent history is disabled (TENDRIL_DB_LOGGING=false)", http.StatusNotImplemented)
		return
	}

	sessionID := r.PathValue("sessionId")

	pollen, ok := h.watch.Observer(w, r)
	if !ok {
		return
	}

	runs, err := h.history.LoadSproutRuns(r.Context(), sessionID, queryLimit(r, 50))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A run record names the subject that dispatched it, so a delegated
	// observer is narrowed to its own rather than refused the phytomer. The
	// limit above is applied before the narrowing, so a busy phytomer answers
	// with the observer's share of one page rather than a page of its own.
	if pollen != "" {
		runs, ok = h.watch.AuthorizeRuns(w, r, pollen, sessionID, runs)
		if !ok {
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessionId":  sessionID,
		"sproutRuns": runs,
	})
}

type runSequenceRequest struct {
	PathOrName string `json:"pathOrName"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	BaseURL    string `json:"baseURL,omitempty"`
}

func (h *SessionsHandler) runSequenceAsync(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionId")

	var req runSequenceRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	if req.PathOrName == "" {
		http.Error(w, "pathOrName is required", http.StatusBadRequest)
		return
	}

	runID := "seqrun-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	bgCtx := context.WithoutCancel(r.Context())

	if sessionID == "new" {
		sess, err := h.manager.Initiate(bgCtx, session.OriginREST, session.Preferences{
			Provider: req.Provider,
			Model:    req.Model,
		})
		if err == nil {
			sessionID = sess.ID
		}
	} else {
		if _, ok := h.manager.Get(r.Context(), sessionID); !ok {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
	}

	// Write "running" immediately so observers can see the detached job start.
	if h.history != nil {
		_ = h.history.RecordSproutRun(bgCtx, historydb.SproutRun{
			RunID:      runID,
			SessionID:  sessionID,
			StepID:     runID,
			Origin:     "rest",
			Transcript: req.PathOrName,
			Status:     "running",
			StartedAt:  time.Now().UTC(),
		})
	}

	go func() {
		result, err := conductor.RunSequence(bgCtx, req.PathOrName, conductor.SequenceRunOptions{
			Provider: req.Provider,
			Model:    req.Model,
			BaseURL:  req.BaseURL,
		})
		if err != nil {
			h.bus.Publish(eventbus.Event{
				Type:      eventbus.EventSequenceFailure,
				SessionID: sessionID,
				Source:    "receptors",
				Timestamp: time.Now().UTC(),
				Data:      map[string]any{"runId": runID, "error": err.Error()},
			})
			if h.history != nil {
				_ = h.history.RecordSproutRun(bgCtx, historydb.SproutRun{
					RunID: runID, SessionID: sessionID, StepID: runID,
					Status: "withered", Error: err.Error(), FinishedAt: time.Now().UTC(),
				})
			}
			return
		}
		steps := 0
		if result != nil {
			steps = len(result.Steps)
		}
		h.bus.Publish(eventbus.Event{
			Type:      eventbus.EventSequenceComplete,
			SessionID: sessionID,
			Source:    "receptors",
			Timestamp: time.Now().UTC(),
			Data:      map[string]any{"runId": runID, "steps": steps},
		})
		if h.history != nil {
			_ = h.history.RecordSproutRun(bgCtx, historydb.SproutRun{
				RunID: runID, SessionID: sessionID, StepID: runID,
				Status: "matured", FinishedAt: time.Now().UTC(),
			})
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"runId":     runID,
		"sessionId": sessionID,
		"status":    "running",
	})
}

func queryLimit(r *http.Request, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return fallback
	}
	return limit
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

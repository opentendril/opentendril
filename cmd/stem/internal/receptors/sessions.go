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
// ungoverned read routes (events, sprout-runs) and the async sequence trigger,
// which are follow-up capabilities not yet part of the parity registry.
type SessionsHandler struct {
	core    core.Core
	manager *session.Manager
	history *historydb.Store
	bus     *eventbus.Bus
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
	if errors.Is(err, core.ErrNotFound) {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// Register mounts the session routes onto the mux, wrapping each handler
// with the provided auth middleware.
func (h *SessionsHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
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

	// Ungoverned routes (views / follow-up capabilities) — not part of the
	// parity registry. Canonical + legacy alias, as above.
	for _, pattern := range []string{
		"GET /v1/phytomers/{sessionId}/events",
		"GET /v1/phytomers/{sessionId}/sprout-runs",
		"POST /v1/phytomers/{sessionId}/sequences/grow",
	} {
		var handler http.HandlerFunc
		switch {
		case strings.HasSuffix(pattern, "/events"):
			handler = h.events
		case strings.HasSuffix(pattern, "/sprout-runs"):
			handler = h.sproutRuns
		default:
			handler = h.runSequenceAsync
		}
		mux.HandleFunc(pattern, auth(handler))
		mux.HandleFunc(sessionAlias(pattern), auth(handler))
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
		http.Error(w, "persistent history is disabled (OPENTENDRIL_DB_LOGGING=false)", http.StatusNotImplemented)
		return
	}

	sessionID := r.PathValue("sessionId")
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
		http.Error(w, "persistent history is disabled (OPENTENDRIL_DB_LOGGING=false)", http.StatusNotImplemented)
		return
	}

	sessionID := r.PathValue("sessionId")
	runs, err := h.history.LoadSproutRuns(r.Context(), sessionID, queryLimit(r, 50))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
		if _, ok := h.manager.Get(sessionID); !ok {
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

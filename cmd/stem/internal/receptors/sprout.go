package receptors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/eventbus"
	"github.com/opentendril/core/cmd/stem/internal/historydb"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// SproutHandler is the REST adapter for the governed sprout/run capability
// family. Exactly like GenomeHandler, it translates HTTP to and
// from the transport-free core.Core and holds no business logic.
//
// POST /v1/sprouts/run executes synchronously and answers when the Tendril
// matures or withers — the same semantics the MCP sproutTendril tool has
// always had. POST /v1/sessions/{sessionId}/sprout/run is the ungoverned
// detached (202 Accepted) path: it returns immediately and surfaces progress
// via the EventBus, /ws, and the session sprout-runs history view.
type SproutHandler struct {
	core    core.Core
	history *historydb.Store
	bus     *eventbus.Bus
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewSproutHandler creates the sprout REST surface over the shared Core.
// history may be nil when SQLite logging is disabled; bus may be nil in tests.
func NewSproutHandler(coreSvc core.Core, history *historydb.Store, bus *eventbus.Bus) *SproutHandler {
	return &SproutHandler{core: coreSvc, history: history, bus: bus}
}

// governedRoutes is the single table of sprout-capability routes this adapter
// wires (same contract as SessionsHandler.governedRoutes).
func (h *SproutHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"POST /v1/sprouts/run", core.CapSproutRun, h.run},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). Read by the parity coverage test.
func (h *SproutHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// Register mounts the sprout routes onto the mux, wrapping each handler with
// the provided auth middleware. The detached session-scoped route is
// ungoverned (like SessionsHandler's async sequence trigger) and is excluded
// from Capabilities() / the parity registry.
func (h *SproutHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}

	// Ungoverned detached path — not part of the parity registry.
	mux.HandleFunc("POST /v1/sessions/{sessionId}/sprout/run", auth(h.runSproutAsync))
}

func (h *SproutHandler) run(w http.ResponseWriter, r *http.Request) {
	var req core.SproutRunInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.Transcript) == "" || strings.TrimSpace(req.Substrate) == "" {
		http.Error(w, "transcript and substrate are required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}

	result, err := h.core.SproutRun(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// runSproutAsync detaches a sprout run onto a background goroutine and returns
// 202 Accepted immediately. Progress is observed via EventBus/WebSocket and
// GET /v1/sessions/{sessionId}/sprout-runs.
func (h *SproutHandler) runSproutAsync(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionId")
	if sessionID == "new" {
		sessionID = ""
	}

	var req core.SproutRunInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.Transcript) == "" || strings.TrimSpace(req.Substrate) == "" {
		http.Error(w, "transcript and substrate are required", http.StatusBadRequest)
		return
	}

	req.Origin = session.OriginREST
	if sessionID != "" {
		req.SessionID = sessionID
	} else {
		// Mint a session before accepting so the 202 payload carries a
		// trackable sessionId for observers (mirrors runSequenceAsync).
		sess, err := h.core.CreateSession(r.Context(), core.CreateSessionInput{
			Origin: session.OriginREST,
		})
		if err != nil {
			writeCoreErr(w, err)
			return
		}
		sessionID = sess.ID
		req.SessionID = sessionID
	}

	stepID := fmt.Sprintf("step-%d", time.Now().UTC().UnixNano())
	req.StepID = stepID

	// Write "running" status immediately so observers can see the job start.
	if h.history != nil {
		_ = h.history.RecordSproutRun(r.Context(), historydb.SproutRun{
			RunID:      stepID,
			SessionID:  sessionID,
			StepID:     stepID,
			Origin:     "rest",
			Transcript: req.Transcript,
			Status:     "running",
			StartedAt:  time.Now().UTC(),
		})
	}

	bgCtx := context.WithoutCancel(r.Context())
	go func() {
		result, err := h.core.SproutRun(bgCtx, req)
		sid := result.SessionID
		if sid == "" {
			sid = sessionID
		}
		if err != nil {
			h.bus.Publish(eventbus.Event{
				Type:      eventbus.EventSproutWithered,
				SessionID: sid,
				Source:    "receptors",
				Timestamp: time.Now().UTC(),
				Data:      map[string]any{"stepId": stepID, "error": err.Error()},
			})
			if h.history != nil {
				_ = h.history.RecordSproutRun(bgCtx, historydb.SproutRun{
					RunID: stepID, SessionID: sid, StepID: stepID,
					Status: "withered", Error: err.Error(), FinishedAt: time.Now().UTC(),
				})
			}
			return
		}
		h.bus.Publish(eventbus.Event{
			Type:      eventbus.EventSproutMatured,
			SessionID: sid,
			Source:    "receptors",
			Timestamp: time.Now().UTC(),
			Data:      map[string]any{"stepId": stepID},
		})
		if h.history != nil {
			_ = h.history.RecordSproutRun(bgCtx, historydb.SproutRun{
				RunID: stepID, SessionID: sid, StepID: stepID,
				Status: "matured", Output: result.Output, FinishedAt: time.Now().UTC(),
			})
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"stepId":    stepID,
		"sessionId": sessionID,
		"status":    "detached",
	})
}

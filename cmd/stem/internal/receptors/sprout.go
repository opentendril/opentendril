package receptors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// SproutHandler is the REST adapter for the governed sprout/run capability
// family. Exactly like GenomeHandler, it translates HTTP to and
// from the transport-free core.Core and holds no business logic.
//
// POST /v1/sprouts/grow executes synchronously and answers when the Tendril
// matures or withers — the same semantics the MCP sproutTendril tool has
// always had. POST /v1/sessions/{sessionId}/sprout/grow is the ungoverned
// detached (202 Accepted) path: it returns immediately and surfaces progress
// via the EventBus, /ws, and the session sprout-runs history view.
type SproutHandler struct {
	core    core.Core
	history *historydb.Store
	bus     *eventbus.Bus
	// delegation gates *delegated* invocations (PollenHeader) of
	// both sprout routes against the active grants. A nil gate denies every
	// delegated invocation; requests without the header are untouched.
	delegation *DelegationGate
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

// WithDelegation wires the delegation gate onto the handler and returns it
// for chaining.
func (h *SproutHandler) WithDelegation(gate *DelegationGate) *SproutHandler {
	h.delegation = gate
	return h
}

// authorizeDelegated gates a delegated sprout invocation. It returns the
// authorized Pollen and true when handling may proceed: either the request is
// not delegated (no marker header — today's path, untouched, and the Pollen is
// blank) or an active grant covers {pollen, operation-class, substrate}. On
// denial it writes 403 and the gate records the audit event.
//
// The Pollen is returned rather than discarded because a run that does not
// record who dispatched it can never be scoped back to them, and a run nobody
// can be scoped to is a run its own dispatcher cannot watch.
func (h *SproutHandler) authorizeDelegated(w http.ResponseWriter, r *http.Request, substrate string) (pollen string, ok bool) {
	pollen, credentialOK := h.delegation.PollenFor(r)
	if !credentialOK {
		http.Error(w, "delegation denied: unknown or revoked Pollinator credential", http.StatusForbidden)
		return "", false
	}
	if pollen == "" {
		return "", true
	}
	decision := h.delegation.Authorize(core.DelegationRequest{
		Pollen:         pollen,
		OperationClass: core.CapSproutGrow,
		Substrate:      strings.TrimSpace(substrate),
		Impact:         core.DelegationImpactHigh,
	})
	if !decision.Authorized {
		http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
		return "", false
	}
	return pollen, true
}

// governedRoutes is the single table of sprout-capability routes this adapter
// wires (same contract as SessionsHandler.governedRoutes).
func (h *SproutHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"POST /v1/sprouts/grow", core.CapSproutGrow, h.run},
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

	// Detached path — not part of the parity registry, but delegated
	// invocations of it pass through the delegation authorizer inside
	// runSproutAsync like every other governed surface. Canonical
	// /v1/phytomers path plus the legacy /v1/sessions alias.
	mux.HandleFunc("POST /v1/phytomers/{sessionId}/sprout/grow", auth(h.runSproutAsync))
	mux.HandleFunc("POST /v1/sessions/{sessionId}/sprout/grow", auth(h.runSproutAsync))
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
	pollen, ok := h.authorizeDelegated(w, r, req.Substrate)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}

	// Stamped after authorization and only from the resolved credential, so
	// the execution port records the run against a subject the caller could
	// not have named.
	r = r.WithContext(core.WithPollen(r.Context(), pollen))

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
	// The detached path is excluded from the parity registry but NOT from
	// delegation governance: a delegated invocation must hold an active grant
	// before any session is minted or any goroutine detaches.
	pollen, ok := h.authorizeDelegated(w, r, req.Substrate)
	if !ok {
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
	// Ownership is settled here, before the goroutine detaches: a delegated
	// caller that polls the instant it receives its 202 must already find a
	// record attributed to it, or the run it just started would read as
	// somebody else's.
	substrate := strings.TrimSpace(req.Substrate)
	if h.history != nil {
		_ = h.history.RecordSproutRun(r.Context(), historydb.SproutRun{
			RunID:      stepID,
			SessionID:  sessionID,
			StepID:     stepID,
			Origin:     "rest",
			Pollen:     pollen,
			Substrate:  substrate,
			Transcript: req.Transcript,
			Status:     "running",
			StartedAt:  time.Now().UTC(),
		})
	}

	bgCtx := context.WithoutCancel(r.Context())
	go func() {
		// Lifecycle events and the terminal history write are owned by the
		// conductor observer installed on the daemon's sprout execution port.
		// This goroutine only keeps the accepted run alive after 202. An inner
		// SproutOutcomeDetached is not an ending and must not be rewritten as
		// matured merely because Core returned a nil error.
		_, _ = h.core.SproutRun(bgCtx, req)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"stepId":    stepID,
		"sessionId": sessionID,
		"status":    "detached",
	})
}

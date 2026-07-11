package receptors

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

// SproutHandler is the REST adapter for the governed sprout/run capability
// family (issue #181). Exactly like GenomeHandler, it translates HTTP to and
// from the transport-free core.Core and holds no business logic.
//
// POST /v1/sprouts/run executes synchronously and answers when the Tendril
// matures or withers — the same semantics the MCP sproutTendril tool has
// always had. The /ws event stream and the session sprout-runs history
// endpoint remain the views for watching a run.
type SproutHandler struct {
	core core.Core
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewSproutHandler creates the sprout REST surface over the shared Core.
func NewSproutHandler(coreSvc core.Core) *SproutHandler {
	return &SproutHandler{core: coreSvc}
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
// the provided auth middleware.
func (h *SproutHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}
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

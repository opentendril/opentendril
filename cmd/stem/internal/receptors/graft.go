package receptors

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// GraftHandler is the REST adapter for the governed substrate-grafting
// capability family. Exactly like GenomeHandler, it
// translates HTTP to and from the transport-free core.Core and holds no
// business logic. Note the distinction from the mesh *server*: /v1/mesh/graft
// (WebSocket) is the receiving half of the mesh; these routes are the
// client-side delegation commands.
type GraftHandler struct {
	core core.Core
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewGraftHandler creates the substrate-grafting REST surface over the shared
// Core.
func NewGraftHandler(coreSvc core.Core) *GraftHandler {
	return &GraftHandler{core: coreSvc}
}

// governedRoutes is the single table of graft-capability routes this adapter
// wires (same contract as SessionsHandler.governedRoutes).
func (h *GraftHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"POST /v1/mesh/grafts", core.CapMeshGraft, h.graft},
		{"POST /v1/mesh/promotions", core.CapMeshPromote, h.promote},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). Read by the parity coverage test.
func (h *GraftHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// Register mounts the graft routes onto the mux, wrapping each handler with
// the provided auth middleware.
func (h *GraftHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}
}

func (h *GraftHandler) graft(w http.ResponseWriter, r *http.Request) {
	var req core.MeshGraftInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.Substrate) == "" {
		http.Error(w, "substrate is required", http.StatusBadRequest)
		return
	}

	result, err := h.core.MeshGraft(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *GraftHandler) promote(w http.ResponseWriter, r *http.Request) {
	var req core.MeshPromoteInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.Substrate) == "" {
		http.Error(w, "substrate is required", http.StatusBadRequest)
		return
	}

	result, err := h.core.MeshPromote(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

package receptors

import (
	"net/http"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/core"
)

// TraitHandler is the REST adapter for the governed mesh trait capability
// family. It translates HTTP to and from the transport-free core.Core and
// holds no business logic.
type TraitHandler struct {
	core core.Core
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewTraitHandler creates the trait REST surface over the shared Core.
func NewTraitHandler(coreSvc core.Core) *TraitHandler {
	return &TraitHandler{core: coreSvc}
}

// governedRoutes is the single table of mesh-trait routes this adapter wires.
func (h *TraitHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"GET /v1/mesh/traits", core.CapMeshTraitList, h.list},
		{"POST /v1/mesh/traits/{id}/accept", core.CapMeshTraitAccept, h.accept},
		{"POST /v1/mesh/traits/{id}/reject", core.CapMeshTraitReject, h.reject},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). Read by the parity coverage test.
func (h *TraitHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// Register mounts the trait routes onto the mux, wrapping each handler with
// the provided auth middleware.
func (h *TraitHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}
}

func (h *TraitHandler) list(w http.ResponseWriter, r *http.Request) {
	result, err := h.core.MeshTraitList(r.Context(), core.MeshTraitListInput{})
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *TraitHandler) accept(w http.ResponseWriter, r *http.Request) {
	traitID := strings.TrimSpace(r.PathValue("id"))
	if traitID == "" {
		http.Error(w, "trait id is required", http.StatusBadRequest)
		return
	}

	result, err := h.core.MeshTraitAccept(r.Context(), core.MeshTraitAcceptInput{TraitID: traitID})
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *TraitHandler) reject(w http.ResponseWriter, r *http.Request) {
	traitID := strings.TrimSpace(r.PathValue("id"))
	if traitID == "" {
		http.Error(w, "trait id is required", http.StatusBadRequest)
		return
	}

	result, err := h.core.MeshTraitReject(r.Context(), core.MeshTraitRejectInput{TraitID: traitID})
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

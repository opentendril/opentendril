package receptors

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/core"
)

// PlasmidHandler is the REST adapter for the governed plasmid capability
// family (issue #181, slice 2). Exactly like GenomeHandler, it translates
// HTTP to and from the transport-free core.Core and holds no business logic.
type PlasmidHandler struct {
	core core.Core
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewPlasmidHandler creates the plasmid REST surface over the shared Core.
func NewPlasmidHandler(coreSvc core.Core) *PlasmidHandler {
	return &PlasmidHandler{core: coreSvc}
}

// governedRoutes is the single table of plasmid-capability routes this
// adapter wires (same contract as SessionsHandler.governedRoutes).
func (h *PlasmidHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"GET /v1/plasmids", core.CapPlasmidList, h.list},
		{"POST /v1/plasmids/inject", core.CapPlasmidInject, h.inject},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). Read by the parity coverage test.
func (h *PlasmidHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// Register mounts the plasmid routes onto the mux, wrapping each handler with
// the provided auth middleware.
func (h *PlasmidHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}
}

func (h *PlasmidHandler) list(w http.ResponseWriter, r *http.Request) {
	plasmids, err := h.core.PlasmidList(r.Context())
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	if plasmids == nil {
		plasmids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"plasmids": plasmids, "count": len(plasmids)})
}

type injectPlasmidRequest struct {
	Name string `json:"name"`
}

func (h *PlasmidHandler) inject(w http.ResponseWriter, r *http.Request) {
	var req injectPlasmidRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	result, err := h.core.PlasmidInject(r.Context(), core.PlasmidInjectInput{Name: req.Name})
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

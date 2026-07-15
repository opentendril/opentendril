package receptors

import (
	"net/http"
	"sort"

	"github.com/opentendril/core/cmd/stem/internal/core"
)

// GenomeHandler is the REST adapter for the governed genome capability family
// . Exactly like SessionsHandler, it translates HTTP to
// and from the transport-free core.Core and holds no business logic.
type GenomeHandler struct {
	core core.Core
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewGenomeHandler creates the genome REST surface over the shared Core.
func NewGenomeHandler(coreSvc core.Core) *GenomeHandler {
	return &GenomeHandler{core: coreSvc}
}

// governedRoutes is the single table of genome-capability routes this adapter
// wires (same contract as SessionsHandler.governedRoutes).
func (h *GenomeHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"GET /v1/genome", core.CapGenomeView, h.view},
		{"POST /v1/genome/reduce", core.CapGenomeReduce, h.reduce},
		{"POST /v1/genome/evolve", core.CapGenomeEvolve, h.evolve},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). Read by the parity coverage test.
func (h *GenomeHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// Register mounts the genome routes onto the mux, wrapping each handler with
// the provided auth middleware.
func (h *GenomeHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}
}

func (h *GenomeHandler) view(w http.ResponseWriter, r *http.Request) {
	seeds, err := h.core.GenomeView(r.Context())
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	if seeds == nil {
		seeds = []core.GenomeSeed{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"seeds": seeds, "count": len(seeds)})
}

func (h *GenomeHandler) reduce(w http.ResponseWriter, r *http.Request) {
	path, err := h.core.GenomeReduce(r.Context())
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "reduced": true})
}

func (h *GenomeHandler) evolve(w http.ResponseWriter, r *http.Request) {
	path, err := h.core.GenomeEvolve(r.Context())
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "evolved": true})
}

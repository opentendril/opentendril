package receptors

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/core"
)

// SequenceHandler is the REST adapter for the governed sequence capability
// family. Exactly like GenomeHandler, it translates
// HTTP to and from the transport-free core.Core and holds no business logic.
//
// POST /v1/sequences/run executes synchronously and answers when the sequence
// finishes — the same semantics the MCP runSequence tool has always had. The
// session-scoped async trigger (POST /v1/sessions/{sessionId}/sequences/run,
// 202 + runId) remains available on SessionsHandler for fire-and-forget runs.
type SequenceHandler struct {
	core core.Core
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewSequenceHandler creates the sequence REST surface over the shared Core.
func NewSequenceHandler(coreSvc core.Core) *SequenceHandler {
	return &SequenceHandler{core: coreSvc}
}

// governedRoutes is the single table of sequence-capability routes this
// adapter wires (same contract as SessionsHandler.governedRoutes).
func (h *SequenceHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"GET /v1/sequences", core.CapSequenceList, h.list},
		{"POST /v1/sequences/run", core.CapSequenceRun, h.run},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). Read by the parity coverage test.
func (h *SequenceHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// Register mounts the sequence routes onto the mux, wrapping each handler
// with the provided auth middleware.
func (h *SequenceHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}
}

func (h *SequenceHandler) list(w http.ResponseWriter, r *http.Request) {
	sequences, err := h.core.SequenceList(r.Context())
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	if sequences == nil {
		sequences = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sequences": sequences, "count": len(sequences)})
}

func (h *SequenceHandler) run(w http.ResponseWriter, r *http.Request) {
	var req core.SequenceRunInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.PathOrName) == "" {
		http.Error(w, "pathOrName is required", http.StatusBadRequest)
		return
	}

	result, err := h.core.SequenceRun(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

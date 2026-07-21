package receptors

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// PassthroughHandler is the REST adapter for the governed passthrough/run
// capability family. Exactly like SproutHandler, it translates HTTP to and
// from the transport-free core.Core and holds no business logic.
//
// POST /v1/passthrough/run executes one bounded command synchronously inside
// a network-sealed Terrarium. Delegated invocations (marked with
// PollenHeader) are gated per-invocation by the delegation
// authorizer; a request without the marker follows the plain
// bearer-authenticated path with deny-all egress (no grant, no allow-list).
type PassthroughHandler struct {
	core core.Core
	// delegation gates *delegated* invocations against the active grants and
	// supplies the matching grant's egress allow-list. A nil gate denies every
	// delegated invocation; requests without the header are untouched.
	delegation *DelegationGate
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewPassthroughHandler creates the passthrough REST surface over the shared
// Core.
func NewPassthroughHandler(coreSvc core.Core) *PassthroughHandler {
	return &PassthroughHandler{core: coreSvc}
}

// WithDelegation wires the delegation gate onto the handler and returns it
// for chaining.
func (h *PassthroughHandler) WithDelegation(gate *DelegationGate) *PassthroughHandler {
	h.delegation = gate
	return h
}

// governedRoutes is the single table of passthrough-capability routes this
// adapter wires (same contract as SproutHandler.governedRoutes).
func (h *PassthroughHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"POST /v1/passthrough/run", core.CapPassthroughRun, h.run},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). Read by the parity coverage test.
func (h *PassthroughHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// Register mounts the passthrough routes onto the mux, wrapping each handler
// with the provided auth middleware.
func (h *PassthroughHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}
}

func (h *PassthroughHandler) run(w http.ResponseWriter, r *http.Request) {
	var req core.PassthroughRunInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.Substrate) == "" || len(req.Command) == 0 {
		http.Error(w, "substrate and command are required", http.StatusBadRequest)
		return
	}

	// Egress is grant material: it has no JSON surface on the input type, so
	// the decode above can never have populated it. It is set below — and only
	// below — from an authorized delegation grant. A non-delegated invocation
	// keeps the empty list: deny-all egress with zero configuration.
	pollen, credentialOK := h.delegation.PollenFor(r)
	if !credentialOK {
		http.Error(w, "delegation denied: unknown or revoked Pollinator credential", http.StatusForbidden)
		return
	}
	if pollen != "" {
		decision := h.delegation.Authorize(core.DelegationRequest{
			Pollen:         pollen,
			OperationClass: core.CapPassthroughRun,
			Substrate:      strings.TrimSpace(req.Substrate),
		})
		if !decision.Authorized {
			http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
			return
		}
		req.Egress = decision.Grant.Egress
	}
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}

	result, err := h.core.PassthroughRun(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

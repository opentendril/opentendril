package receptors

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// SeedHandler is the REST adapter for the governed seed/grow capability family —
// grow a Seed (a bounded intent) to Fruit. Exactly like StomaHandler, it
// translates HTTP to and from the transport-free core.Core and holds no
// business logic.
//
// POST /v1/seeds/grow grows a Seed. Detached=false (default) is synchronous;
// detached=true returns the active handle after Core durable opening.
// POST /v1/seeds/grow/async is compatibility presentation of the same Core
// detached contract. GET /v1/seeds/runs/{handle} collects the reviewable Fruit
// by that handle. All three are gated per-invocation by the delegation
// authorizer; collection is additionally scoped to the subject that dispatched
// the run.
type SeedHandler struct {
	core core.Core
	// delegation gates *delegated* invocations against the active grants and
	// supplies the matching grant's egress allow-list. A nil gate denies every
	// delegated invocation; requests without the header are untouched.
	delegation *DelegationGate
	// history persists dispatched runs so an async growth can be collected by
	// handle later. Nil disables the async/collect routes' persistence.
	history *historydb.Store
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewSeedHandler creates the seed REST surface over the shared Core.
func NewSeedHandler(coreSvc core.Core) *SeedHandler {
	return &SeedHandler{core: coreSvc}
}

// WithDelegation wires the delegation gate onto the handler and returns it
// for chaining.
func (h *SeedHandler) WithDelegation(gate *DelegationGate) *SeedHandler {
	h.delegation = gate
	return h
}

// WithHistory wires the run store used by the async/collect routes and returns
// the handler for chaining.
func (h *SeedHandler) WithHistory(store *historydb.Store) *SeedHandler {
	h.history = store
	return h
}

// governedRoutes is the single table of seed-capability routes this adapter
// wires (same contract as StomaHandler.governedRoutes).
func (h *SeedHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"POST /v1/seeds/grow", core.CapSeedGrow, h.grow},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). Read by the parity coverage test.
func (h *SeedHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// Register mounts the seed routes onto the mux, wrapping each handler with the
// provided auth middleware. The async dispatch and collect routes are outside
// the parity registry (like the detached sprout route), but every one of them
// consults the delegation authorizer per-invocation.
func (h *SeedHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}

	mux.HandleFunc("POST /v1/seeds/grow/async", auth(h.growAsync))
	mux.HandleFunc("GET /v1/seeds/runs/{handle}", auth(h.collect))
}

// authorizeSeed resolves and authorizes the Pollen for a delegated seed
// operation on the given substrate. It returns the authorized pollen ("" for a
// non-delegated request — today's local path), the grant's egress allow-list,
// and whether handling may proceed. On denial it writes the HTTP error and
// returns ok=false.
func (h *SeedHandler) authorizeSeed(w http.ResponseWriter, r *http.Request, substrate string) (pollen string, egress []string, ok bool) {
	pollen, credentialOK := h.delegation.PollenFor(r)
	if !credentialOK {
		http.Error(w, "delegation denied: unknown or revoked Pollinator credential", http.StatusForbidden)
		return "", nil, false
	}
	if pollen == "" {
		return "", nil, true
	}
	decision := h.delegation.Authorize(core.DelegationRequest{
		Pollen:         pollen,
		OperationClass: core.CapSeedGrow,
		Substrate:      strings.TrimSpace(substrate),
		Impact:         core.DelegationImpactHigh,
	})
	if !decision.Authorized {
		http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
		return "", nil, false
	}
	return pollen, decision.Grant.Egress, true
}

// decodeSeedRequest decodes and validates a seed.grow request body.
func decodeSeedRequest(w http.ResponseWriter, r *http.Request) (core.SeedGrowInput, bool) {
	var req core.SeedGrowInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return req, false
		}
	}
	if strings.TrimSpace(req.Substrate) == "" || strings.TrimSpace(req.Goal) == "" || len(req.Verify) == 0 {
		http.Error(w, "substrate, goal and verify are required", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

func (h *SeedHandler) grow(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeSeedRequest(w, r)
	if !ok {
		return
	}
	// Egress is grant material: it has no JSON surface on the input type, so the
	// decode above can never have populated it. It is set below — and only below
	// — from an authorized delegation grant.
	pollen, egress, ok := h.authorizeSeed(w, r, req.Substrate)
	if !ok {
		return
	}
	req.Egress = egress
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}
	r = r.WithContext(core.WithPollen(r.Context(), pollen))

	result, err := h.core.SeedGrow(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	status := http.StatusOK
	if req.Detached {
		status = http.StatusAccepted
	}
	writeJSON(w, status, result)
}

// growAsync is the compatibility presentation of detached seed.grow. It forces
// Detached=true and calls the same Core SeedGrow contract; it does not mint a
// handle or launch growth itself.
func (h *SeedHandler) growAsync(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeSeedRequest(w, r)
	if !ok {
		return
	}
	pollen, egress, ok := h.authorizeSeed(w, r, req.Substrate)
	if !ok {
		return
	}
	req.Egress = egress
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}
	req.Detached = true
	r = r.WithContext(core.WithPollen(r.Context(), pollen))

	result, err := h.core.SeedGrow(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

// collect returns the reviewable Fruit for a dispatched growth by handle. It is
// grant-gated on the run's substrate and scoped to the dispatching subject: a
// handle can only be collected by the Pollen that owns it.
func (h *SeedHandler) collect(w http.ResponseWriter, r *http.Request) {
	handle := strings.TrimSpace(r.PathValue("handle"))
	if handle == "" {
		http.Error(w, "handle is required", http.StatusBadRequest)
		return
	}
	if h.history == nil {
		http.Error(w, "seed run history is not available", http.StatusServiceUnavailable)
		return
	}

	run, found, err := h.history.GetSeedRun(r.Context(), handle)
	if err != nil {
		http.Error(w, "failed to read seed run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no seed run for handle "+handle, http.StatusNotFound)
		return
	}

	pollen, _, ok := h.authorizeSeed(w, r, run.Substrate)
	if !ok {
		return
	}
	if pollen != run.Pollen {
		http.Error(w, "delegation denied: this handle belongs to another subject", http.StatusForbidden)
		return
	}

	writeJSON(w, http.StatusOK, run)
}

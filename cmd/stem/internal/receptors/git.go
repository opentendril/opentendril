package receptors

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// GitHandler is the REST adapter for the governed git capability family.
// Exactly like PassthroughHandler, it translates HTTP to and from the
// transport-free core.Core and holds no business logic.
//
// POST /v1/git/commit commits the current state of a substrate's workspace
// under the substrate's configured commit identity; /v1/git/push publishes the
// branch; /v1/git/pr opens the pull request. Delegated invocations (marked with
// DelegationSubjectHeader) are gated per-invocation by the delegation
// authorizer, each against its own operation-class; a request without the
// marker follows the plain bearer-authenticated path.
type GitHandler struct {
	core core.Core
	// delegation gates *delegated* invocations against the active grants. A
	// nil gate denies every delegated invocation; requests without the header
	// are untouched.
	delegation *DelegationGate
	// registered accumulates the governed capability names actually mounted by
	// Register, so Capabilities() reflects the wired routes (not the canonical
	// list) — the independence the parity coverage test relies on.
	registered []string
}

// NewGitHandler creates the git REST surface over the shared Core.
func NewGitHandler(coreSvc core.Core) *GitHandler {
	return &GitHandler{core: coreSvc}
}

// WithDelegation wires the delegation gate onto the handler and returns it
// for chaining.
func (h *GitHandler) WithDelegation(gate *DelegationGate) *GitHandler {
	h.delegation = gate
	return h
}

// governedRoutes is the single table of git-capability routes this adapter
// wires (same contract as PassthroughHandler.governedRoutes).
func (h *GitHandler) governedRoutes() []governedRoute {
	return []governedRoute{
		{"POST /v1/git/commit", core.CapGitCommit, h.commit},
		{"POST /v1/git/push", core.CapGitPush, h.push},
		{"POST /v1/git/pr", core.CapGitPR, h.pullRequest},
		{"POST /v1/git/branch", core.CapGitBranch, h.branch},
		{"POST /v1/git/status", core.CapGitStatus, h.status},
	}
}

// Capabilities reports the governed capability names this REST adapter has
// actually mounted (populated by Register). Read by the parity coverage test.
func (h *GitHandler) Capabilities() []string {
	out := append([]string(nil), h.registered...)
	sort.Strings(out)
	return out
}

// Register mounts the git routes onto the mux, wrapping each handler with the
// provided auth middleware.
func (h *GitHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	h.registered = h.registered[:0]
	for _, route := range h.governedRoutes() {
		mux.HandleFunc(route.pattern, auth(route.handler))
		h.registered = append(h.registered, route.capability)
	}
}

func (h *GitHandler) commit(w http.ResponseWriter, r *http.Request) {
	var req core.GitCommitInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.Substrate) == "" || strings.TrimSpace(req.Message) == "" {
		http.Error(w, "substrate and message are required", http.StatusBadRequest)
		return
	}

	// A delegated invocation is authorized per-invocation against the active
	// grants; a non-delegated request follows the plain bearer-authenticated
	// path untouched.
	if subject := DelegatedSubject(r); subject != "" {
		decision := h.delegation.Authorize(core.DelegationRequest{
			Subject:        subject,
			OperationClass: core.CapGitCommit,
			Substrate:      strings.TrimSpace(req.Substrate),
		})
		if !decision.Authorized {
			http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
			return
		}
	}
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}

	result, err := h.core.GitCommit(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *GitHandler) push(w http.ResponseWriter, r *http.Request) {
	var req core.GitPushInput
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

	// A delegated invocation is authorized per-invocation against the active
	// grants; a non-delegated request follows the plain bearer-authenticated
	// path untouched.
	if subject := DelegatedSubject(r); subject != "" {
		decision := h.delegation.Authorize(core.DelegationRequest{
			Subject:        subject,
			OperationClass: core.CapGitPush,
			Substrate:      strings.TrimSpace(req.Substrate),
		})
		if !decision.Authorized {
			http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
			return
		}
	}
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}

	result, err := h.core.GitPush(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *GitHandler) pullRequest(w http.ResponseWriter, r *http.Request) {
	var req core.GitPRInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.Substrate) == "" || strings.TrimSpace(req.Title) == "" {
		http.Error(w, "substrate and title are required", http.StatusBadRequest)
		return
	}

	// A delegated invocation is authorized per-invocation against the active
	// grants; a non-delegated request follows the plain bearer-authenticated
	// path untouched. git.pr is its own operation-class, so a grant covering
	// commit and push does not confer it.
	if subject := DelegatedSubject(r); subject != "" {
		decision := h.delegation.Authorize(core.DelegationRequest{
			Subject:        subject,
			OperationClass: core.CapGitPR,
			Substrate:      strings.TrimSpace(req.Substrate),
		})
		if !decision.Authorized {
			http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
			return
		}
	}
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}

	result, err := h.core.GitPR(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *GitHandler) branch(w http.ResponseWriter, r *http.Request) {
	var req core.GitBranchInput
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if strings.TrimSpace(req.Substrate) == "" || strings.TrimSpace(req.Branch) == "" {
		http.Error(w, "substrate and branch are required", http.StatusBadRequest)
		return
	}

	// A delegated invocation is authorized per-invocation against the active
	// grants; git.branch is its own operation-class, so a grant covering the
	// commit/push/pull-request loop does not confer it.
	if subject := DelegatedSubject(r); subject != "" {
		decision := h.delegation.Authorize(core.DelegationRequest{
			Subject:        subject,
			OperationClass: core.CapGitBranch,
			Substrate:      strings.TrimSpace(req.Substrate),
		})
		if !decision.Authorized {
			http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
			return
		}
	}
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}

	result, err := h.core.GitBranch(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *GitHandler) status(w http.ResponseWriter, r *http.Request) {
	var req core.GitStatusInput
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

	// Read-only does not mean ungated: a status response names branches and
	// changed file paths, which is repository content. The deny-closed default
	// applies to disclosure as much as to mutation, so git.status is its own
	// operation-class and is conferred by nothing else.
	if subject := DelegatedSubject(r); subject != "" {
		decision := h.delegation.Authorize(core.DelegationRequest{
			Subject:        subject,
			OperationClass: core.CapGitStatus,
			Substrate:      strings.TrimSpace(req.Substrate),
		})
		if !decision.Authorized {
			http.Error(w, "delegation denied: "+decision.Reason, http.StatusForbidden)
			return
		}
	}
	if strings.TrimSpace(req.Origin) == "" {
		req.Origin = session.OriginREST
	}

	result, err := h.core.GitStatus(r.Context(), req)
	if err != nil {
		writeCoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

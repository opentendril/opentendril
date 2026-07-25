package receptors

import (
	"net/http"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// DelegationPendingHandler is the REST adapter for listing and resolving pending delegation confirmations.
type DelegationPendingHandler struct {
	store *core.PendingConfirmationStore
}

// NewDelegationPendingHandler creates the pending-confirmation REST surface.
func NewDelegationPendingHandler(store *core.PendingConfirmationStore) *DelegationPendingHandler {
	return &DelegationPendingHandler{store: store}
}

// Register mounts the delegation pending routes onto the mux, wrapping each handler with the
// provided auth middleware. These routes require Botanist auth.
func (h *DelegationPendingHandler) Register(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	if auth == nil {
		auth = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}

	mux.HandleFunc("GET /v1/delegation/pending", auth(h.list))
	mux.HandleFunc("POST /v1/delegation/pending/{id}/approve", auth(h.approve))
	mux.HandleFunc("POST /v1/delegation/pending/{id}/deny", auth(h.deny))
}

type pendingResponse struct {
	ID             string `json:"id"`
	Pollen         string `json:"pollen"`
	OperationClass string `json:"operationClass"`
	Substrate      string `json:"substrate"`
	Impact         string `json:"impact"`
	CreatedAt      string `json:"createdAt"`
	ExpiresAt      string `json:"expiresAt"`
}

func (h *DelegationPendingHandler) list(w http.ResponseWriter, r *http.Request) {
	records := h.store.List()
	out := make([]pendingResponse, 0, len(records))
	for _, rec := range records {
		out = append(out, pendingResponse{
			ID:             rec.ID,
			Pollen:         rec.Pollen,
			OperationClass: rec.OperationClass,
			Substrate:      rec.Substrate,
			Impact:         rec.Impact,
			CreatedAt:      rec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			ExpiresAt:      rec.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *DelegationPendingHandler) approve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing pending confirmation id", http.StatusBadRequest)
		return
	}

	err := h.store.Approve(id)
	if err != nil {
		if err.Error() == "pending confirmation not found" {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err.Error() == "pending confirmation expired" || err.Error() == "pending confirmation is not open" {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "approved", "id": id})
}

func (h *DelegationPendingHandler) deny(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing pending confirmation id", http.StatusBadRequest)
		return
	}

	err := h.store.Deny(id)
	if err != nil {
		if err.Error() == "pending confirmation not found" {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err.Error() == "pending confirmation expired" || err.Error() == "pending confirmation is not open" {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "denied", "id": id})
}

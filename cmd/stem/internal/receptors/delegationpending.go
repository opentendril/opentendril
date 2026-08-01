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

// Route describes a single HTTP route exposed by the handler.
type Route struct {
	Pattern string
	Handler http.HandlerFunc
}

// Routes returns the delegation pending routes that this handler exposes.
// These routes require Botanist auth, which the caller is responsible for applying.
func (h *DelegationPendingHandler) Routes() []Route {
	return []Route{
		{"GET /v1/delegation/pending", h.list},
		{"POST /v1/delegation/pending/{id}/approve", h.approve},
		{"POST /v1/delegation/pending/{id}/deny", h.deny},
	}
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

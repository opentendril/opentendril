package receptors

import (
	"context"
	"errors"
	"net/http"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

type lifecycleObserver interface {
	ObserveLifecycleInventory(context.Context) (core.LifecycleInventory, error)
}

// LifecycleHandler is the private Botanist adapter for the read-only
// lifecycle inventory. It does not classify evidence or mutate lifecycle state.
type LifecycleHandler struct {
	observer lifecycleObserver
}

// NewLifecycleHandler creates the inventory adapter over a Core view.
func NewLifecycleHandler(observer lifecycleObserver) *LifecycleHandler {
	return &LifecycleHandler{observer: observer}
}

// Handle returns the Core LifecycleInventory contract unchanged.
func (h *LifecycleHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.observer == nil {
		http.Error(w, "lifecycle inventory unavailable: observation is not wired", http.StatusServiceUnavailable)
		return
	}
	inventory, err := h.observer.ObserveLifecycleInventory(r.Context())
	if err != nil {
		if errors.Is(err, core.ErrLifecycleInventoryBotanistOnly) {
			http.Error(w, "lifecycle inventory unavailable: "+err.Error(), http.StatusForbidden)
			return
		}
		http.Error(w, "lifecycle inventory unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, inventory)
}

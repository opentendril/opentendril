package receptors

import (
	"context"
	"errors"
	"net/http"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

type fruitInventoryObserver interface {
	ObserveFruitInventory(context.Context) (core.FruitInventory, error)
}

// FruitHandler is the private Botanist observation adapter for the Core Fruit
// inventory. It does not classify, sort, count, or inspect Git.
type FruitHandler struct {
	observer fruitInventoryObserver
}

// NewFruitHandler creates the Fruit observation adapter over a Core view.
func NewFruitHandler(observer fruitInventoryObserver) *FruitHandler {
	return &FruitHandler{observer: observer}
}

// Handle returns the Core FruitInventory contract unchanged.
func (h *FruitHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.observer == nil {
		http.Error(w, "Fruit inventory unavailable: observation is not wired", http.StatusServiceUnavailable)
		return
	}

	inventory, err := h.observer.ObserveFruitInventory(r.Context())
	if err != nil {
		if errors.Is(err, core.ErrFruitInventoryNotWired) {
			http.Error(w, "Fruit inventory unavailable: observation is not wired", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "Fruit inventory unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, inventory)
}

package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

const (
	LifecycleSourceFruit              = "fruit"
	LifecycleSourceRunWorkspace       = "run-workspace"
	LifecycleSourceDelegatedWorkspace = "delegated-workspace"
	LifecycleSourceAvailable          = "available"
	LifecycleSourceUnavailable        = "unavailable"
)

// ErrLifecycleInventoryBotanistOnly refuses a Pollinator context. Inventory
// observation is a Botanist control-plane view, not a governed capability.
var ErrLifecycleInventoryBotanistOnly = errors.New("lifecycle inventory is Botanist-only")

// LifecycleSourceStatus is the per-source availability fact. Unavailable is
// never represented by an empty item list.
type LifecycleSourceStatus struct {
	Source    string `json:"source"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	ItemCount int    `json:"itemCount"`
}

// LifecycleInventory composes existing Fruit and RunWorkspace observations
// with delegated-workspace enumeration. Item arrays are nil when that source
// is unavailable, and empty when the source was read and retained nothing.
type LifecycleInventory struct {
	Sources             []LifecycleSourceStatus    `json:"sources"`
	Fruit               []FruitInventoryItem       `json:"fruit"`
	RunWorkspaces       []RunWorkspaceReport       `json:"runWorkspaces"`
	DelegatedWorkspaces []DelegatedWorkspaceReport `json:"delegatedWorkspaces"`
}

// ObserveLifecycleInventory reads the three lifecycle sources. A failure in
// one source stays on that source. This method does not reconcile, abandon,
// archive, or delete anything.
func (s *Service) ObserveLifecycleInventory(ctx context.Context) (LifecycleInventory, error) {
	if s == nil {
		return LifecycleInventory{}, fmt.Errorf("lifecycle inventory is not wired")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if PollenFromContext(ctx) != "" {
		return LifecycleInventory{}, ErrLifecycleInventoryBotanistOnly
	}

	fruit, fruitErr := s.observeLifecycleFruit(ctx)
	runs, runErr := s.observeLifecycleRunWorkspaces(ctx)
	delegated, delegatedErr := s.observeLifecycleDelegated(ctx)

	inventory := LifecycleInventory{
		Sources: []LifecycleSourceStatus{
			lifecycleSourceStatus(LifecycleSourceFruit, fruitErr, len(fruit)),
			lifecycleSourceStatus(LifecycleSourceRunWorkspace, runErr, len(runs)),
			lifecycleSourceStatus(LifecycleSourceDelegatedWorkspace, delegatedErr, len(delegated)),
		},
	}
	if fruitErr == nil {
		inventory.Fruit = fruit
	}
	if runErr == nil {
		inventory.RunWorkspaces = runs
	}
	if delegatedErr == nil {
		inventory.DelegatedWorkspaces = delegated
	}
	return inventory, nil
}

func (s *Service) observeLifecycleFruit(ctx context.Context) ([]FruitInventoryItem, error) {
	if s.fruitInventory.Observe == nil {
		return nil, ErrFruitInventoryNotWired
	}
	inventory, err := s.ObserveFruitInventory(ctx)
	if err != nil {
		return nil, err
	}
	if inventory.Items == nil {
		inventory.Items = []FruitInventoryItem{}
	}
	return inventory.Items, nil
}

func (s *Service) observeLifecycleRunWorkspaces(ctx context.Context) ([]RunWorkspaceReport, error) {
	if s.runWorkspace.ListAllocations == nil {
		return nil, fmt.Errorf("RunWorkspace listing is not wired")
	}
	reports, err := s.ListRunWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	if reports == nil {
		reports = []RunWorkspaceReport{}
	}
	return reports, nil
}

func (s *Service) observeLifecycleDelegated(ctx context.Context) ([]DelegatedWorkspaceReport, error) {
	if s.delegatedWorkspace.List == nil {
		return nil, fmt.Errorf("delegated workspace enumeration is not wired")
	}
	reports, err := s.delegatedWorkspace.List(ctx)
	if err != nil {
		return nil, err
	}
	if reports == nil {
		reports = []DelegatedWorkspaceReport{}
	}
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].Substrate != reports[j].Substrate {
			return reports[i].Substrate < reports[j].Substrate
		}
		if reports[i].Pollen != reports[j].Pollen {
			return reports[i].Pollen < reports[j].Pollen
		}
		return reports[i].Path < reports[j].Path
	})
	return reports, nil
}

func lifecycleSourceStatus(source string, err error, count int) LifecycleSourceStatus {
	if err != nil {
		return LifecycleSourceStatus{
			Source: source, State: LifecycleSourceUnavailable, Reason: err.Error(),
		}
	}
	return LifecycleSourceStatus{
		Source: source, State: LifecycleSourceAvailable, ItemCount: count,
	}
}

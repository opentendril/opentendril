package core

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestObserveLifecycleInventoryComposesExistingReports(t *testing.T) {
	evidence := safeRunWorkspaceEvidence()
	delegated := DelegatedWorkspaceReport{
		Pollen: "pollen", Substrate: "demo", Repository: "/repo/demo",
		Path:              "/home/botanist/.tendril/workspaces/demo/pollen",
		WorkspaceVerified: false,
		Reason:            "branch Fruit or PR state is unverified; it is retained",
	}
	mutations := 0
	service := NewService(nil).
		WithFruitInventoryObservationSource(FruitInventoryObservationSource{
			Observe: func(context.Context) ([]FruitInventoryEvidence, error) {
				return []FruitInventoryEvidence{{
					Claim: FruitClaim{
						ProducerKind: "Seed", ProducerIdentity: "seed-1",
						Repository: "owner/repo", Branch: "feature/one", Commit: "abc",
						PublicationState: "published",
					},
				}}, nil
			},
		}).
		WithRunWorkspace(RunWorkspaceOperations{
			ListAllocations: func(context.Context) ([]RunWorkspaceAllocation, error) {
				return []RunWorkspaceAllocation{evidence.Allocation}, nil
			},
			Inspect: func(context.Context, RunWorkspaceAllocation) (RunWorkspaceEvidence, error) {
				return evidence, nil
			},
			Reconcile: func(context.Context, RunWorkspaceEvidence) (RunWorkspaceMutation, error) {
				mutations++
				return RunWorkspaceMutation{}, nil
			},
			Abandon: func(context.Context, RunWorkspaceEvidence, bool) (RunWorkspaceMutation, error) {
				mutations++
				return RunWorkspaceMutation{}, nil
			},
		}).
		WithDelegatedWorkspace(DelegatedWorkspaceOperations{
			List: func(context.Context) ([]DelegatedWorkspaceReport, error) {
				return []DelegatedWorkspaceReport{delegated}, nil
			},
			Abandon: func(context.Context, DelegatedWorkspaceTargetSpec, bool) (DelegatedWorkspaceAbandonResult, error) {
				mutations++
				return DelegatedWorkspaceAbandonResult{}, nil
			},
		})

	inventory, err := service.ObserveLifecycleInventory(context.Background())
	if err != nil {
		t.Fatalf("ObserveLifecycleInventory: %v", err)
	}
	if mutations != 0 {
		t.Fatalf("inventory observation mutated lifecycle state %d times", mutations)
	}
	fruit, err := service.ObserveFruitInventory(context.Background())
	if err != nil {
		t.Fatalf("ObserveFruitInventory: %v", err)
	}
	runs, err := service.ListRunWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListRunWorkspaces: %v", err)
	}
	if !reflect.DeepEqual(inventory.Fruit, fruit.Items) || !reflect.DeepEqual(inventory.RunWorkspaces, runs) {
		t.Fatalf("inventory = %+v, want existing Fruit and RunWorkspace reports", inventory)
	}
	if len(inventory.Fruit) != 1 || inventory.Fruit[0].ReviewState != FruitReviewUnknown ||
		inventory.Fruit[0].UnknownReason != FruitUnknownRepositoryUnavailable {
		t.Fatalf("Fruit row = %+v, want unknown repository-unavailable evidence", inventory.Fruit)
	}
	if !reflect.DeepEqual(inventory.DelegatedWorkspaces, []DelegatedWorkspaceReport{delegated}) {
		t.Fatalf("delegated rows = %+v", inventory.DelegatedWorkspaces)
	}
	if inventory.DelegatedWorkspaces[0].AutoReclaimable {
		t.Fatal("unverified delegated workspace was presented as reclaimable")
	}
	for _, source := range inventory.Sources {
		if source.State != LifecycleSourceAvailable || source.ItemCount != 1 {
			t.Fatalf("source = %+v, want one available entry", source)
		}
	}
}

func TestObserveLifecycleInventoryKeepsPartialSourceFailureExplicit(t *testing.T) {
	service := NewService(nil).
		WithFruitInventoryObservationSource(FruitInventoryObservationSource{
			Observe: func(context.Context) ([]FruitInventoryEvidence, error) {
				return nil, errors.New("history database unavailable")
			},
		}).
		WithRunWorkspace(RunWorkspaceOperations{
			ListAllocations: func(context.Context) ([]RunWorkspaceAllocation, error) {
				return []RunWorkspaceAllocation{}, nil
			},
			Inspect: func(context.Context, RunWorkspaceAllocation) (RunWorkspaceEvidence, error) {
				t.Fatal("empty RunWorkspace list was inspected")
				return RunWorkspaceEvidence{}, nil
			},
		}).
		WithDelegatedWorkspace(DelegatedWorkspaceOperations{
			List: func(context.Context) ([]DelegatedWorkspaceReport, error) {
				return nil, errors.New("delegated workspace root is unavailable")
			},
		})

	inventory, err := service.ObserveLifecycleInventory(context.Background())
	if err != nil {
		t.Fatalf("partial observation failed the whole inventory: %v", err)
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("encode inventory: %v", err)
	}
	body := string(encoded)
	if !strings.Contains(body, `"fruit":null`) || !strings.Contains(body, `"delegatedWorkspaces":null`) {
		t.Fatalf("unavailable sources were not null: %s", body)
	}
	if !strings.Contains(body, `"runWorkspaces":[]`) {
		t.Fatalf("available empty RunWorkspace source was not an empty list: %s", body)
	}
	states := map[string]LifecycleSourceStatus{}
	for _, source := range inventory.Sources {
		states[source.Source] = source
	}
	if states[LifecycleSourceFruit].State != LifecycleSourceUnavailable || states[LifecycleSourceFruit].ItemCount != 0 ||
		!strings.Contains(states[LifecycleSourceFruit].Reason, "history database unavailable") {
		t.Fatalf("fruit source = %+v", states[LifecycleSourceFruit])
	}
	if states[LifecycleSourceRunWorkspace].State != LifecycleSourceAvailable || states[LifecycleSourceRunWorkspace].ItemCount != 0 {
		t.Fatalf("run workspace source = %+v", states[LifecycleSourceRunWorkspace])
	}
	if states[LifecycleSourceDelegatedWorkspace].State != LifecycleSourceUnavailable ||
		!strings.Contains(states[LifecycleSourceDelegatedWorkspace].Reason, "unavailable") {
		t.Fatalf("delegated source = %+v", states[LifecycleSourceDelegatedWorkspace])
	}
}

func TestObserveLifecycleInventoryRefusesPollinatorBeforeReading(t *testing.T) {
	calls := 0
	service := NewService(nil).
		WithFruitInventoryObservationSource(FruitInventoryObservationSource{
			Observe: func(context.Context) ([]FruitInventoryEvidence, error) {
				calls++
				return nil, nil
			},
		}).
		WithRunWorkspace(RunWorkspaceOperations{
			ListAllocations: func(context.Context) ([]RunWorkspaceAllocation, error) {
				calls++
				return nil, nil
			},
		}).
		WithDelegatedWorkspace(DelegatedWorkspaceOperations{
			List: func(context.Context) ([]DelegatedWorkspaceReport, error) {
				calls++
				return nil, nil
			},
		})

	_, err := service.ObserveLifecycleInventory(WithPollen(context.Background(), "codex"))
	if !errors.Is(err, ErrLifecycleInventoryBotanistOnly) {
		t.Fatalf("error = %v, want Botanist-only refusal", err)
	}
	if calls != 0 {
		t.Fatalf("Pollinator observation read %d sources", calls)
	}
}

func TestObserveLifecycleInventoryReportsUnwiredSourcesAsUnavailable(t *testing.T) {
	service := NewService(nil).WithFruitInventoryObservationSource(FruitInventoryObservationSource{
		Observe: func(context.Context) ([]FruitInventoryEvidence, error) {
			return []FruitInventoryEvidence{}, nil
		},
	})
	inventory, err := service.ObserveLifecycleInventory(context.Background())
	if err != nil {
		t.Fatalf("unwired sources failed the inventory: %v", err)
	}
	if inventory.Fruit == nil || len(inventory.Fruit) != 0 {
		t.Fatalf("fruit = %#v, want an observed empty list", inventory.Fruit)
	}
	if inventory.RunWorkspaces != nil || inventory.DelegatedWorkspaces != nil {
		t.Fatalf("unwired sources were empty lists: %+v", inventory)
	}
}

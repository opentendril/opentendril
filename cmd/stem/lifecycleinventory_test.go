package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestLifecycleRouteReportsPartialEvidenceWithoutMutation(t *testing.T) {
	mutations := 0
	evidence := retainedRunWorkspaceEvidence()
	service := core.NewService(nil).
		WithFruitInventoryObservationSource(core.FruitInventoryObservationSource{
			Observe: func(context.Context) ([]core.FruitInventoryEvidence, error) {
				return []core.FruitInventoryEvidence{{
					Claim: core.FruitClaim{
						ProducerKind: "Seed", ProducerIdentity: "seed-unknown",
						Repository: "owner/repo", Branch: "feature/unknown", Commit: "abc",
						PublicationState: "published",
					},
				}}, nil
			},
		}).
		WithRunWorkspace(core.RunWorkspaceOperations{
			ListAllocations: func(context.Context) ([]core.RunWorkspaceAllocation, error) {
				return []core.RunWorkspaceAllocation{evidence.Allocation}, nil
			},
			Inspect: func(context.Context, core.RunWorkspaceAllocation) (core.RunWorkspaceEvidence, error) {
				return evidence, nil
			},
			Reconcile: func(context.Context, core.RunWorkspaceEvidence) (core.RunWorkspaceMutation, error) {
				mutations++
				return core.RunWorkspaceMutation{}, nil
			},
			Abandon: func(context.Context, core.RunWorkspaceEvidence, bool) (core.RunWorkspaceMutation, error) {
				mutations++
				return core.RunWorkspaceMutation{}, nil
			},
		}).
		WithDelegatedWorkspace(core.DelegatedWorkspaceOperations{
			List: func(context.Context) ([]core.DelegatedWorkspaceReport, error) {
				return nil, os.ErrPermission
			},
		})

	fixture := newRemoteMuxTestFixture(t, "", service)
	fixture.deps.AdminKey = "botanist-key"
	mux := buildServeMux(fixture.deps)

	denied := serveMuxRequest(mux, http.MethodGet, "/v1/lifecycle", "", map[string]string{
		"Authorization": "Bearer " + fixture.root,
	})
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("Pollinator status = %d, want 401 (%s)", denied.Code, denied.Body.String())
	}
	posted := serveMuxRequest(mux, http.MethodPost, "/v1/lifecycle", "", map[string]string{
		"Authorization": "Bearer botanist-key",
	})
	if posted.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405 (%s)", posted.Code, posted.Body.String())
	}

	response := serveMuxRequest(mux, http.MethodGet, "/v1/lifecycle", "", map[string]string{
		"Authorization": "Bearer botanist-key",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", response.Code, response.Body.String())
	}
	var inventory core.LifecycleInventory
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if mutations != 0 {
		t.Fatalf("route mutated lifecycle state %d times", mutations)
	}
	if len(inventory.Fruit) != 1 || inventory.Fruit[0].ReviewState != core.FruitReviewUnknown ||
		inventory.Fruit[0].UnknownReason != core.FruitUnknownRepositoryUnavailable {
		t.Fatalf("Fruit = %+v, want unknown repository-unavailable", inventory.Fruit)
	}
	if len(inventory.RunWorkspaces) != 1 || inventory.RunWorkspaces[0].AutoReconcileable {
		t.Fatalf("RunWorkspace = %+v, want one retained report", inventory.RunWorkspaces)
	}
	if inventory.DelegatedWorkspaces != nil {
		t.Fatalf("delegated entries = %+v, want null unavailable source", inventory.DelegatedWorkspaces)
	}
	for _, source := range inventory.Sources {
		if source.Source == core.LifecycleSourceDelegatedWorkspace &&
			(source.State != core.LifecycleSourceUnavailable || source.ItemCount != 0) {
			t.Fatalf("delegated source = %+v", source)
		}
	}
}

func TestLifecycleRouteEnumeratesUnverifiedWorkspaceWithoutCreatingOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".tendril", "workspaces", "slice1-readonly-fixture", "pollen")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	service := core.NewService(nil).WithDelegatedWorkspace(delegatedWorkspaceObservationOperations())
	fixture := newRemoteMuxTestFixture(t, "", service)
	fixture.deps.AdminKey = "botanist-key"
	response := serveMuxRequest(buildServeMux(fixture.deps), http.MethodGet, "/v1/lifecycle", "", map[string]string{
		"Authorization": "Bearer botanist-key",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", response.Code, response.Body.String())
	}
	var inventory core.LifecycleInventory
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(inventory.DelegatedWorkspaces) != 1 {
		t.Fatalf("delegated = %+v, want the unverified directory", inventory.DelegatedWorkspaces)
	}
	report := inventory.DelegatedWorkspaces[0]
	if report.Substrate != "slice1-readonly-fixture" || report.Pollen != "pollen" || report.WorkspaceVerified || report.AutoReclaimable {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Reason, "retained") {
		t.Fatalf("reason = %q", report.Reason)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); !os.IsNotExist(err) {
		t.Fatalf("route created Git metadata: %v", err)
	}
}

func retainedRunWorkspaceEvidence() core.RunWorkspaceEvidence {
	return core.RunWorkspaceEvidence{
		Allocation: core.RunWorkspaceAllocation{
			AllocationRunID: "allocation-retained", SproutRunID: "history-retained",
			StepID: "step-retained", Repository: "/repo/sample",
			Path: "/tmp/run-workspaces/retained", Branch: "sprout/task-retained",
			BaseCommit: "base", State: "finalized",
		},
		History:        core.RunWorkspaceHistoryEvidence{State: core.RunWorkspaceHistoryMissing},
		OwnershipState: core.RunWorkspaceOwnershipMissing,
		PathState:      core.RunWorkspacePathUnknown,
		WorktreeState:  core.RunWorkspaceWorktreeUnknown,
	}
}

func TestLifecycleRoutePatternIsAbsentFromPublicMux(t *testing.T) {
	fixture := newRemoteMuxTestFixture(t, "", core.NewService(nil))
	request := httptest.NewRequest(http.MethodGet, "/v1/lifecycle", nil)
	response := httptest.NewRecorder()
	fixture.mux.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("public status = %d, want 404 (%s)", response.Code, response.Body.String())
	}
	_, pattern := fixture.mux.Handler(request)
	if strings.Contains(pattern, "lifecycle") {
		t.Fatalf("public mux registered %q", pattern)
	}
}

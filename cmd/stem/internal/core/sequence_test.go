package core_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/core"
	"github.com/opentendril/core/cmd/stem/internal/session"
)

func newSequenceService(t *testing.T, ops core.SequenceOps) *core.Service {
	t.Helper()
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return core.NewService(manager).WithSequence(ops)
}

func TestSequenceListRunsPortWithRoot(t *testing.T) {
	var gotRoot string
	svc := newSequenceService(t, core.SequenceOps{
		Root: "/workspaces/core",
		List: func(_ context.Context, root string) ([]string, error) {
			gotRoot = root
			return []string{".tendril/sequences/deploy.yaml"}, nil
		},
	})

	files, err := svc.SequenceList(context.Background())
	if err != nil {
		t.Fatalf("SequenceList: %v", err)
	}
	if gotRoot != "/workspaces/core" {
		t.Fatalf("list port received root %q", gotRoot)
	}
	if len(files) != 1 || files[0] != ".tendril/sequences/deploy.yaml" {
		t.Fatalf("files = %v", files)
	}
}

func TestSequenceListDefaultsRoot(t *testing.T) {
	var gotRoot string
	svc := newSequenceService(t, core.SequenceOps{
		List: func(_ context.Context, root string) ([]string, error) {
			gotRoot = root
			return nil, nil
		},
	})
	if _, err := svc.SequenceList(context.Background()); err != nil {
		t.Fatalf("SequenceList: %v", err)
	}
	if gotRoot != "." {
		t.Fatalf("default root = %q, want .", gotRoot)
	}
}

func TestSequenceRunPassesInputThrough(t *testing.T) {
	var got core.SequenceRunInput
	svc := newSequenceService(t, core.SequenceOps{
		Run: func(_ context.Context, in core.SequenceRunInput) (core.SequenceRunResult, error) {
			got = in
			return core.SequenceRunResult{
				Name:  "deploy",
				Steps: []core.SequenceStepOutcome{{ID: "meristem", Status: "matured"}},
			}, nil
		},
	})

	result, err := svc.SequenceRun(context.Background(), core.SequenceRunInput{
		PathOrName: "deploy",
		Provider:   "local",
		Model:      "llama3.2",
		BaseURL:    "http://host:11434/v1",
	})
	if err != nil {
		t.Fatalf("SequenceRun: %v", err)
	}
	if got.PathOrName != "deploy" || got.Provider != "local" || got.Model != "llama3.2" || got.BaseURL != "http://host:11434/v1" {
		t.Fatalf("run port received %+v", got)
	}
	if result.Name != "deploy" || len(result.Steps) != 1 || result.Steps[0].Status != "matured" {
		t.Fatalf("result = %+v", result)
	}
}

func TestSequenceRunRequiresPathOrName(t *testing.T) {
	svc := newSequenceService(t, core.SequenceOps{
		Run: func(context.Context, core.SequenceRunInput) (core.SequenceRunResult, error) {
			return core.SequenceRunResult{}, nil
		},
	})
	if _, err := svc.SequenceRun(context.Background(), core.SequenceRunInput{}); err == nil || !strings.Contains(err.Error(), "pathOrName is required") {
		t.Fatalf("expected pathOrName-required error, got %v", err)
	}
}

func TestSequenceUnwiredFailsLoudly(t *testing.T) {
	svc := newSequenceService(t, core.SequenceOps{})
	if _, err := svc.SequenceList(context.Background()); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("expected loud not-wired error for list, got %v", err)
	}
	if _, err := svc.SequenceRun(context.Background(), core.SequenceRunInput{PathOrName: "deploy"}); err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("expected loud not-wired error for run, got %v", err)
	}
}

func TestSequenceRunReturnsPartialResultWithError(t *testing.T) {
	svc := newSequenceService(t, core.SequenceOps{
		Run: func(context.Context, core.SequenceRunInput) (core.SequenceRunResult, error) {
			return core.SequenceRunResult{
				Name:  "deploy",
				Steps: []core.SequenceStepOutcome{{ID: "meristem", Status: "withered"}},
			}, fmt.Errorf("step meristem failed")
		},
	})

	result, err := svc.SequenceRun(context.Background(), core.SequenceRunInput{PathOrName: "deploy"})
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	// The partial summary must survive alongside the error so adapters can
	// render the legacy failure summary.
	if result.Name != "deploy" || len(result.Steps) != 1 || result.Steps[0].Status != "withered" {
		t.Fatalf("partial result lost: %+v", result)
	}
}

func TestSequenceCapabilitiesInRegistry(t *testing.T) {
	svc := newSequenceService(t, core.SequenceOps{
		List: func(context.Context, string) ([]string, error) { return nil, nil },
		Run: func(context.Context, core.SequenceRunInput) (core.SequenceRunResult, error) {
			return core.SequenceRunResult{}, nil
		},
	})

	declared := map[string]bool{}
	for _, capability := range svc.Capabilities() {
		declared[capability.Name] = true
	}
	for _, name := range []string{core.CapSequenceList, core.CapSequenceRun} {
		if !declared[name] {
			t.Errorf("registry does not declare %s", name)
		}
	}

	// Invoke path (the projection MCP/CLI use) rejects a missing pathOrName.
	if _, err := svc.Invoke(context.Background(), core.CapSequenceRun, map[string]any{}); err == nil {
		t.Fatal("Invoke(sequence.run) without pathOrName must fail")
	}
	result, err := svc.Invoke(context.Background(), core.CapSequenceRun, map[string]any{"pathOrName": "deploy"})
	if err != nil {
		t.Fatalf("Invoke(sequence.run): %v", err)
	}
	if _, ok := result.(core.SequenceRunResult); !ok {
		t.Fatalf("Invoke(sequence.run) = %T, want core.SequenceRunResult", result)
	}
}

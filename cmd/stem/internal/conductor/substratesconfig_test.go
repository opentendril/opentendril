package conductor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestResolveSubstrateExecutionPlanPreservesTypedNotFoundEvidence(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-substrate")
	_, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: missing}, nil)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("resolveSubstrateExecutionPlan error = %v, want typed fs.ErrNotExist", err)
	}
}

func TestResolveSubstrateExecutionPlanMarksNonDirectoryAsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: path}, nil)
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("resolveSubstrateExecutionPlan error = %v, want typed fs.ErrInvalid", err)
	}
}

func TestAuthSpecUnmarshalExposeToken(t *testing.T) {
	t.Run("exposeToken true", func(t *testing.T) {
		yamlData := []byte("method: pat\nenv: MY_ENV\nexposeToken: true\n")
		var auth AuthSpec
		if err := yaml.Unmarshal(yamlData, &auth); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !auth.ExposeToken {
			t.Fatalf("expected ExposeToken=true")
		}
	})

	t.Run("bare scalar form sets ExposeToken=false", func(t *testing.T) {
		yamlData := []byte("GITHUB_TOKEN")
		var auth AuthSpec
		if err := yaml.Unmarshal(yamlData, &auth); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if auth.ExposeToken {
			t.Fatalf("expected ExposeToken=false for bare scalar")
		}
		if auth.Method != "pat" || auth.Env != "GITHUB_TOKEN" {
			t.Fatalf("expected method=pat, env=GITHUB_TOKEN, got %v/%v", auth.Method, auth.Env)
		}
	})
}

func TestResolveSubstrateExecutionPlan_CloneOnDemand(t *testing.T) {
	config := &SubstratesConfig{
		Substrates: map[string]SubstrateSpec{
			"ondemand": {
				URL: "https://github.com/opentendril/opentendril.git",
			},
		},
	}

	d := &DockerOrchestrator{
		Substrate: "ondemand",
	}

	plan, err := resolveSubstrateExecutionPlan(d, config)
	if err != nil {
		t.Fatalf("resolveSubstrateExecutionPlan failed for clone-on-demand: %v", err)
	}

	if !plan.remoteClone {
		t.Errorf("expected plan to specify remoteClone = true")
	}
	if !plan.remotePublication {
		t.Errorf("expected URL-backed named plan to specify remotePublication = true")
	}
	if plan.cloneURL != "https://github.com/opentendril/opentendril.git" {
		t.Errorf("got cloneURL %q, want https://github.com/opentendril/opentendril.git", plan.cloneURL)
	}
}

func TestResolveSubstrateExecutionPlan_RemotePublicationFact(t *testing.T) {
	t.Run("managed URL-backed checkout absent", func(t *testing.T) {
		t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
		config := &SubstratesConfig{Substrates: map[string]SubstrateSpec{
			"managed": {
				URL:      "https://example.invalid/managed.git",
				Checkout: CheckoutSpec{Mode: "managed"},
			},
		}}

		plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "managed"}, config)
		if err != nil {
			t.Fatalf("resolveSubstrateExecutionPlan: %v", err)
		}
		if !plan.remoteClone {
			t.Error("remoteClone = false, want true for absent managed checkout")
		}
		if !plan.remotePublication {
			t.Error("remotePublication = false, want true for URL-backed managed checkout")
		}
	})

	t.Run("managed URL-backed checkout already valid", func(t *testing.T) {
		t.Setenv("TENDRIL_MANAGED_CHECKOUT_ROOT", t.TempDir())
		checkout := managedCheckoutDir("managed")
		if err := os.MkdirAll(filepath.Dir(checkout), 0o755); err != nil {
			t.Fatalf("create managed checkout parent: %v", err)
		}
		if _, err := runGitCommand(context.Background(), filepath.Dir(checkout), "init", "-q", "-b", "main", checkout); err != nil {
			t.Fatalf("init managed checkout: %v", err)
		}
		config := &SubstratesConfig{Substrates: map[string]SubstrateSpec{
			"managed": {
				URL:      "https://example.invalid/managed.git",
				Checkout: CheckoutSpec{Mode: "managed"},
			},
		}}

		plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "managed"}, config)
		if err != nil {
			t.Fatalf("resolveSubstrateExecutionPlan: %v", err)
		}
		if plan.remoteClone {
			t.Error("remoteClone = true, want false for an existing valid managed checkout")
		}
		if !plan.remotePublication {
			t.Error("remotePublication = false, want true for an existing URL-backed managed checkout")
		}
	})

	t.Run("path checkout with URL remains local", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "operator-checkout")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create path checkout: %v", err)
		}
		if _, err := runGitCommand(context.Background(), path, "init", "-q", "-b", "main"); err != nil {
			t.Fatalf("init path checkout: %v", err)
		}
		config := &SubstratesConfig{Substrates: map[string]SubstrateSpec{
			"path": {
				URL: "https://example.invalid/path.git",
				Checkout: CheckoutSpec{
					Mode: "path",
					Path: path,
				},
			},
		}}

		plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{Substrate: "path"}, config)
		if err != nil {
			t.Fatalf("resolveSubstrateExecutionPlan: %v", err)
		}
		if plan.hostPath != path {
			t.Errorf("hostPath = %q, want existing path checkout %q", plan.hostPath, path)
		}
		if plan.remoteClone {
			t.Error("remoteClone = true, want false for an existing path checkout")
		}
		if plan.remotePublication {
			t.Error("remotePublication = true, want false for an operator-owned path checkout")
		}
	})

	t.Run("explicit SubstrateURL remains remote", func(t *testing.T) {
		plan, err := resolveSubstrateExecutionPlan(&DockerOrchestrator{
			Substrate:    "explicit",
			SubstrateURL: "https://example.invalid/explicit.git",
		}, nil)
		if err != nil {
			t.Fatalf("resolveSubstrateExecutionPlan: %v", err)
		}
		if !plan.remoteClone {
			t.Error("remoteClone = false, want true for explicit SubstrateURL")
		}
		if !plan.remotePublication {
			t.Error("remotePublication = false, want true for explicit SubstrateURL")
		}
	})
}

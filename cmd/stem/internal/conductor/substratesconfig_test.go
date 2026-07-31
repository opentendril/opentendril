package conductor

import (
	"testing"

	"gopkg.in/yaml.v3"
)

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
	if plan.cloneURL != "https://github.com/opentendril/opentendril.git" {
		t.Errorf("got cloneURL %q, want https://github.com/opentendril/opentendril.git", plan.cloneURL)
	}
}

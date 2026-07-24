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

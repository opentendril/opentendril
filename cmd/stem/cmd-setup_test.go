package main

import (
	"strings"
	"testing"
)

func TestFormatAgentSubstratesYAML(t *testing.T) {
	t.Run("pat uses compact scalar form", func(t *testing.T) {
		out := formatAgentSubstratesYAML(agentSubstrateChoices{
			remoteURL: "https://github.com/o/r.git", authMethod: "pat", authEnv: "GITHUB_TOKEN",
		})
		if !strings.Contains(out, `auth: "GITHUB_TOKEN"`) {
			t.Fatalf("expected scalar auth, got:\n%s", out)
		}
		if strings.Contains(out, "method:") || strings.Contains(out, "checkout:") || strings.Contains(out, "sign:") {
			t.Fatalf("pat/ephemeral/no-sign should be minimal, got:\n%s", out)
		}
	})

	t.Run("ssh + managed + signing", func(t *testing.T) {
		out := formatAgentSubstratesYAML(agentSubstrateChoices{
			remoteURL: "git@github.com:o/r.git", authMethod: "ssh", authKey: "~/.ssh/id_ot",
			checkoutMode: "managed", signMethod: "ssh", signKey: "~/.ssh/id_ot",
		})
		for _, want := range []string{"method: ssh", `key: "~/.ssh/id_ot"`, "checkout:", "mode: managed", "sign:"} {
			if !strings.Contains(out, want) {
				t.Fatalf("missing %q in:\n%s", want, out)
			}
		}
	})

	t.Run("none auth", func(t *testing.T) {
		out := formatAgentSubstratesYAML(agentSubstrateChoices{remoteURL: "https://x/r.git", authMethod: "none"})
		if !strings.Contains(out, "method: none") {
			t.Fatalf("expected method: none, got:\n%s", out)
		}
	})

	t.Run("github app", func(t *testing.T) {
		out := formatAgentSubstratesYAML(agentSubstrateChoices{
			remoteURL: "https://github.com/o/r.git", authMethod: "app",
			appID: "4276558", appKeyPath: "~/.tendril/app.pem",
		})
		for _, want := range []string{"method: app", `appId: "4276558"`, `privateKeyPath: "~/.tendril/app.pem"`} {
			if !strings.Contains(out, want) {
				t.Fatalf("missing %q in:\n%s", want, out)
			}
		}
	})
}

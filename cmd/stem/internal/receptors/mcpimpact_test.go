package receptors

import (
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestMCPDelegationImpactWiring(t *testing.T) {
	handler, bus, _, _ := newMCPDelegationTestHandler(t)

	grant := core.DelegationGrant{
		Pollen:             "mcp-Pollinator",
		OperationClasses:   []string{core.CapGitCommit, core.CapSproutGrow},
		Substrates:         []string{"core"},
		ConfirmAboveImpact: core.DelegationImpactHigh,
	}
	gate := &DelegationGate{Authorizer: core.NewDelegationAuthorizer([]core.DelegationGrant{grant}), Bus: bus}
	handler = handler.WithDelegation(gate, "mcp-Pollinator")

	t.Run("git.commit is medium impact (authorized)", func(t *testing.T) {
		text, isError := mcpCallTool(t, handler, core.CapGitCommit, map[string]any{
			"substrate": "core",
			"message":   "test",
		})
		if isError {
			t.Errorf("want success, got error: %q", text)
		}
	})

	t.Run("sprout.grow is high impact (denied by threshold)", func(t *testing.T) {
		text, isError := mcpCallTool(t, handler, core.CapSproutGrow, map[string]any{
			"substrate":  "core",
			"transcript": "grow",
		})
		if !isError || !strings.Contains(text, "requires human confirmation") {
			t.Errorf("want denial due to impact, got error=%v text=%q", isError, text)
		}
	})
}

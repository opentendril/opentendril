package core

import "testing"

func TestCapabilityImpact(t *testing.T) {
	cases := []struct {
		capability string
		want       string
	}{
		{CapGitPrune, DelegationImpactHigh},
		{CapGitPush, DelegationImpactHigh},
		{CapGitPR, DelegationImpactHigh},
		{CapSproutGrow, DelegationImpactHigh},
		{CapSeedGrow, DelegationImpactHigh},
		{CapMeshPromote, DelegationImpactHigh},
		{CapMeshGraft, DelegationImpactHigh},

		{CapGitCommit, DelegationImpactMedium},
		{CapGitBranch, DelegationImpactMedium},
		{CapStomaPass, DelegationImpactMedium},
		{CapPlasmidInject, DelegationImpactMedium},

		{CapGitStatus, DelegationImpactLow},
		{CapGitBranchList, DelegationImpactLow},
		{CapSequenceList, DelegationImpactLow},
		{CapMeshTraitList, DelegationImpactLow},

		{"phytomer.list", DelegationImpactLow},
		{"phytomer.get", DelegationImpactLow},

		{"unknown.capability", DelegationImpactHigh}, // Secure default
		{"some.other.command", DelegationImpactHigh},
	}

	for _, c := range cases {
		t.Run(c.capability, func(t *testing.T) {
			if got := CapabilityImpact(c.capability); got != c.want {
				t.Errorf("CapabilityImpact(%q) = %q, want %q", c.capability, got, c.want)
			}
		})
	}
}

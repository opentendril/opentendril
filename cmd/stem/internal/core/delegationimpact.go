package core

import "strings"

// CapabilityImpact returns the documented taxonomy impact level for a governed capability.
// If the capability is unlisted or unknown, it securely defaults to High impact.
func CapabilityImpact(operationClass string) string {
	switch operationClass {
	case CapGitPrune, CapGitPush, CapGitPR, CapSproutGrow, CapSeedGrow, CapMeshPromote, CapMeshGraft, CapContinuePhytomer:
		return DelegationImpactHigh
	case CapGitCommit, CapGitBranch, CapStomaPass, CapPlasmidInject, CapGenotypeCreate:
		return DelegationImpactMedium
	case CapGitStatus, CapGitBranchList, CapSequenceList, CapMeshTraitList, CapSproutWatch:
		return DelegationImpactLow
	default:
		// Any other *.list or *.get capability is treated as read-only (Low).
		if strings.HasSuffix(operationClass, ".list") || strings.HasSuffix(operationClass, ".get") {
			return DelegationImpactLow
		}
		// Secure default: unknown/new capability = treat as highest risk until explicitly classified.
		return DelegationImpactHigh
	}
}

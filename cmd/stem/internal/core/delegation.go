package core

import (
	"fmt"
	"strings"
	"time"
)

// Delegated execution — the grant model and authorizer (Design RFC).
//
// A DelegationGrant is the unit of durable authorization the Stem checks
// before executing a *delegated* capability invocation: a Pollinator
// asking the Stem to run work on its behalf instead of shelling out on the
// host. One durable, scoped, revocable grant replaces N per-command host
// permission prompts.
//
// Security posture (ARCHITECTURE.md → Guiding Principles):
//   - Security-first, minimal-config default: with zero grants configured the
//     authorizer denies every delegated invocation, and non-delegated
//     invocations never consult it at all — today's behavior is untouched.
//   - No Substrate self-escalation: grants enter the authorizer ONLY at
//     construction, from the Stem's own control plane. A DelegationRequest
//     structurally cannot carry grant material, so neither a caller nor a
//     file inside a cloned Substrate can widen capability at invocation time.

// Delegation impact levels order the confirm-above escalation threshold on a
// grant. An invocation whose impact is unknown (empty) is treated as ABOVE
// every configured threshold — undeclared impact must never slip under a
// confirmation bound.
const (
	DelegationImpactLow    = "low"
	DelegationImpactMedium = "medium"
	DelegationImpactHigh   = "high"
)

// delegationImpactRank orders the impact levels for threshold comparison.
// Unknown levels rank above every known one (secure default).
func delegationImpactRank(impact string) int {
	switch strings.ToLower(strings.TrimSpace(impact)) {
	case DelegationImpactLow:
		return 1
	case DelegationImpactMedium:
		return 2
	case DelegationImpactHigh:
		return 3
	default:
		return 4
	}
}

// DelegationGrant authorizes one subject (a delegation-subject / Phytomer /
// mesh trust-root
// identity) to invoke a bounded set of operation-classes on a bounded set of
// Substrates. It is control-plane policy — distinct from substrates.yaml,
// which stays about connections and credentials.
type DelegationGrant struct {
	// Subject is the trust-root identity exercising the grant.
	Subject string
	// OperationClasses allow-lists the delegable operation-classes (for this
	// slice the governed capability names, e.g. "sprout.grow"). Exact match;
	// no wildcards — a grant names precisely what it opens.
	OperationClasses []string
	// Substrates scopes the grant to named substrates. Exact match; a request
	// naming no substrate never matches.
	Substrates []string
	// Egress allow-lists the hosts a delegated execution may reach. The
	// default is deny-all (empty). Enforcement inside the Terrarium is the
	// passthrough slice; the list is carried on the grant so an authorized
	// decision is complete.
	Egress []string
	// Expires ends the grant at the given instant. Zero means the grant does
	// not expire (revocation is removing it from the control-plane config).
	Expires time.Time
	// ConfirmAboveImpact escalates invocations at or above this impact level
	// ("low", "medium", "high") back to the Botanist. Empty disables the
	// bound. No confirmation surface exists yet, so an invocation crossing
	// the bound is denied with a confirmation-required reason.
	ConfirmAboveImpact string
}

// clone deep-copies a grant so authorizer state can never alias caller-owned
// slices (a mutation after construction must not widen capability).
func (g DelegationGrant) clone() DelegationGrant {
	copied := g
	copied.OperationClasses = append([]string(nil), g.OperationClasses...)
	copied.Substrates = append([]string(nil), g.Substrates...)
	copied.Egress = append([]string(nil), g.Egress...)
	return copied
}

// DelegationRequest describes one delegated capability invocation to be
// authorized. It deliberately carries no grant or policy material: the only
// grants the authorizer consults are the ones it was constructed with, so a
// request (or the Substrate content behind it) can never self-escalate.
type DelegationRequest struct {
	// Subject is the trust-root identity claiming the delegation.
	Subject string
	// OperationClass names the operation-class being invoked.
	OperationClass string
	// Substrate names the substrate the invocation targets.
	Substrate string
	// Impact is the invocation's declared impact level. It is supplied by the
	// Stem's own call sites (never by the caller); empty means undeclared,
	// which ranks above every confirm-above threshold.
	Impact string
}

// DelegationDecision is the authorizer's verdict on one delegated invocation.
type DelegationDecision struct {
	// Authorized reports whether an active grant covers the invocation.
	Authorized bool
	// Grant is a copy of the matching grant when Authorized; its egress
	// allow-list bounds the execution downstream.
	Grant *DelegationGrant
	// Reason explains a denial in transport-neutral terms.
	Reason string
}

// DelegationAuthorizer gates delegated capability invocations against the
// active grants it was constructed with. It holds no mutable policy surface:
// changing grants means constructing a new authorizer from the control plane.
type DelegationAuthorizer struct {
	grants []DelegationGrant
	// now is injectable for expiry tests; defaults to time.Now.
	now func() time.Time
}

// NewDelegationAuthorizer builds an authorizer over the given grants. The
// grants are deep-copied: later mutation of the caller's slice cannot widen
// (or narrow) what this authorizer permits.
func NewDelegationAuthorizer(grants []DelegationGrant) *DelegationAuthorizer {
	copied := make([]DelegationGrant, len(grants))
	for i, grant := range grants {
		copied[i] = grant.clone()
	}
	return &DelegationAuthorizer{grants: copied, now: time.Now}
}

// Authorize evaluates one delegated invocation. A nil authorizer, an empty
// grant set, or an incomplete request all deny — with no configuration,
// delegation is impossible.
func (a *DelegationAuthorizer) Authorize(request DelegationRequest) DelegationDecision {
	subject := strings.TrimSpace(request.Subject)
	operationClass := strings.TrimSpace(request.OperationClass)
	substrate := strings.TrimSpace(request.Substrate)

	if a == nil || len(a.grants) == 0 {
		return delegationDenied("no delegation grants are configured")
	}
	if subject == "" {
		return delegationDenied("delegated invocation names no subject")
	}
	if operationClass == "" {
		return delegationDenied("delegated invocation names no operation-class")
	}
	if substrate == "" {
		return delegationDenied("delegated invocation names no substrate")
	}

	now := a.now()
	for i := range a.grants {
		grant := &a.grants[i]
		if grant.Subject != subject {
			continue
		}
		if !grant.Expires.IsZero() && !now.Before(grant.Expires) {
			continue
		}
		if !containsExact(grant.OperationClasses, operationClass) {
			continue
		}
		if !containsExact(grant.Substrates, substrate) {
			continue
		}
		if threshold := strings.TrimSpace(grant.ConfirmAboveImpact); threshold != "" &&
			delegationImpactRank(request.Impact) >= delegationImpactRank(threshold) {
			return delegationDenied(fmt.Sprintf(
				"grant for subject %q requires human confirmation at or above impact %q; no confirmation surface is available yet",
				subject, threshold))
		}
		matched := grant.clone()
		return DelegationDecision{Authorized: true, Grant: &matched}
	}

	return delegationDenied(fmt.Sprintf(
		"no active grant covers subject %q, operation-class %q, substrate %q",
		subject, operationClass, substrate))
}

func delegationDenied(reason string) DelegationDecision {
	return DelegationDecision{Authorized: false, Reason: reason}
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func activeGrant() core.DelegationGrant {
	return core.DelegationGrant{
		Pollen:           "local-pollinator",
		OperationClasses: []string{core.CapSproutGrow},
		Substrates:       []string{"core"},
		Egress:           []string{"github.com"},
	}
}

func sproutDelegationRequest() core.DelegationRequest {
	return core.DelegationRequest{
		Pollen:         "local-pollinator",
		OperationClass: core.CapSproutGrow,
		Substrate:      "core",
	}
}

func TestDelegationAuthorizerPermitsActiveMatchingGrant(t *testing.T) {
	authorizer := core.NewDelegationAuthorizer([]core.DelegationGrant{activeGrant()})

	decision := authorizer.Authorize(sproutDelegationRequest())
	if !decision.Authorized {
		t.Fatalf("Authorize denied a matching grant: %s", decision.Reason)
	}
	if decision.Grant == nil {
		t.Fatal("authorized decision carries no grant")
	}
	if len(decision.Grant.Egress) != 1 || decision.Grant.Egress[0] != "github.com" {
		t.Fatalf("authorized decision egress = %v, want the grant's allow-list", decision.Grant.Egress)
	}
}

func TestDelegationAuthorizerDeniesWithoutGrants(t *testing.T) {
	for name, authorizer := range map[string]*core.DelegationAuthorizer{
		"nil authorizer": nil,
		"zero grants":    core.NewDelegationAuthorizer(nil),
	} {
		decision := authorizer.Authorize(sproutDelegationRequest())
		if decision.Authorized {
			t.Fatalf("%s: Authorize permitted a delegated invocation with no grants configured", name)
		}
		if decision.Reason == "" {
			t.Fatalf("%s: denial carries no reason", name)
		}
	}
}

func TestDelegationAuthorizerDeniesNonMatchingRequests(t *testing.T) {
	authorizer := core.NewDelegationAuthorizer([]core.DelegationGrant{activeGrant()})

	cases := map[string]core.DelegationRequest{
		"wrong pollen":          {Pollen: "other-Pollinator", OperationClass: core.CapSproutGrow, Substrate: "core"},
		"wrong operation-class": {Pollen: "local-pollinator", OperationClass: core.CapSequenceGrow, Substrate: "core"},
		"wrong substrate":       {Pollen: "local-pollinator", OperationClass: core.CapSproutGrow, Substrate: "other-repo"},
		"empty pollen":          {Pollen: "", OperationClass: core.CapSproutGrow, Substrate: "core"},
		"empty operation-class": {Pollen: "local-pollinator", OperationClass: "", Substrate: "core"},
		"empty substrate":       {Pollen: "local-pollinator", OperationClass: core.CapSproutGrow, Substrate: ""},
	}

	for name, request := range cases {
		if decision := authorizer.Authorize(request); decision.Authorized {
			t.Errorf("%s: Authorize permitted %+v", name, request)
		}
	}
}

func TestDelegationAuthorizerDeniesExpiredGrant(t *testing.T) {
	expired := activeGrant()
	expired.Expires = time.Now().Add(-time.Hour)
	authorizer := core.NewDelegationAuthorizer([]core.DelegationGrant{expired})

	if decision := authorizer.Authorize(sproutDelegationRequest()); decision.Authorized {
		t.Fatal("Authorize permitted an expired grant")
	}

	future := activeGrant()
	future.Expires = time.Now().Add(time.Hour)
	authorizer = core.NewDelegationAuthorizer([]core.DelegationGrant{future})

	if decision := authorizer.Authorize(sproutDelegationRequest()); !decision.Authorized {
		t.Fatalf("Authorize denied a not-yet-expired grant: %s", decision.Reason)
	}
}

func TestDelegationAuthorizerConfirmAboveImpact(t *testing.T) {
	bounded := activeGrant()
	bounded.ConfirmAboveImpact = core.DelegationImpactHigh
	authorizer := core.NewDelegationAuthorizer([]core.DelegationGrant{bounded})

	below := sproutDelegationRequest()
	below.Impact = core.DelegationImpactLow
	if decision := authorizer.Authorize(below); !decision.Authorized {
		t.Fatalf("Authorize denied an invocation below the confirm-above bound: %s", decision.Reason)
	}

	at := sproutDelegationRequest()
	at.Impact = core.DelegationImpactHigh
	decision := authorizer.Authorize(at)
	if decision.Authorized {
		t.Fatal("Authorize permitted an invocation at the confirm-above bound without confirmation")
	}
	if !strings.Contains(decision.Reason, "confirmation") {
		t.Fatalf("denial reason %q does not mention confirmation", decision.Reason)
	}
	// Without a pending store, it does not set PendingConfirmation
	if decision.PendingConfirmation {
		t.Fatal("expected PendingConfirmation: false without a store")
	}

	// Undeclared impact must never slip under a configured bound.
	undeclared := sproutDelegationRequest()
	if decision := authorizer.Authorize(undeclared); decision.Authorized {
		t.Fatal("Authorize permitted an undeclared-impact invocation under a confirm-above bound")
	}
}

func TestDelegationAuthorizerConfirmAbovePending(t *testing.T) {
	bounded := activeGrant()
	bounded.ConfirmAboveImpact = core.DelegationImpactHigh

	store := core.NewPendingConfirmationStore()
	authorizer := core.NewDelegationAuthorizer([]core.DelegationGrant{bounded}).WithPendingStore(store, time.Hour)

	req := sproutDelegationRequest()
	req.Impact = core.DelegationImpactHigh

	// 1. Confirm-above on first request returns PendingConfirmation: true with a non-empty ConfirmationID.
	decision1 := authorizer.Authorize(req)
	if decision1.Authorized {
		t.Fatal("Authorize permitted high impact invocation")
	}
	if !decision1.PendingConfirmation {
		t.Fatal("expected PendingConfirmation: true")
	}
	if decision1.ConfirmationID == "" {
		t.Fatal("expected non-empty ConfirmationID")
	}
	id1 := decision1.ConfirmationID

	// 2. Approving that ID, then retrying the exact same request authorizes it.
	if err := store.Approve(id1); err != nil {
		t.Fatalf("failed to approve: %v", err)
	}
	decision2 := authorizer.Authorize(req)
	if !decision2.Authorized {
		t.Fatal("expected Authorized: true after approval")
	}
	if decision2.Grant == nil {
		t.Fatal("expected grant in decision")
	}

	// 3. Retrying the same request a second time after successful consume does not re-authorize (falls back to new pending).
	decision3 := authorizer.Authorize(req)
	if decision3.Authorized {
		t.Fatal("expected one-time use, but got Authorized: true again")
	}
	if !decision3.PendingConfirmation {
		t.Fatal("expected new pending confirmation")
	}
	if decision3.ConfirmationID == id1 || decision3.ConfirmationID == "" {
		t.Fatal("expected new distinct ConfirmationID")
	}

	// 4. Retrying a different request after approval does not get authorized.
	id3 := decision3.ConfirmationID
	if err := store.Approve(id3); err != nil {
		t.Fatalf("failed to approve: %v", err)
	}
	diffReq := sproutDelegationRequest()
	diffReq.OperationClass = core.CapSequenceGrow
	diffReq.Impact = core.DelegationImpactHigh
	decisionDiff := authorizer.Authorize(diffReq)
	if decisionDiff.Authorized {
		t.Fatal("expected different request to be denied")
	}

	// 5. Denying a pending record, then retrying.
	// It falls through, hits the threshold again, and creates a new pending record.
	// This behavior is fine because the caller might realize they made a mistake and
	// the operator can just ignore or deny the new one.
	_ = authorizer.Authorize(req) // consumes id3 because we approved it above!
	// wait, let's create a fresh pending for deny test
	decisionForDeny := authorizer.Authorize(req) // this makes id5
	id5 := decisionForDeny.ConfirmationID
	if err := store.Deny(id5); err != nil {
		t.Fatalf("failed to deny: %v", err)
	}
	decisionAfterDeny := authorizer.Authorize(req)
	if decisionAfterDeny.Authorized {
		t.Fatal("expected denial after deny")
	}
	if !decisionAfterDeny.PendingConfirmation || decisionAfterDeny.ConfirmationID == id5 {
		t.Fatal("expected new pending record creation after deny")
	}
}

func TestDelegationAuthorizerPendingRevocation(t *testing.T) {
	bounded := activeGrant()
	bounded.ConfirmAboveImpact = core.DelegationImpactHigh

	store := core.NewPendingConfirmationStore()
	authorizer1 := core.NewDelegationAuthorizer([]core.DelegationGrant{bounded}).WithPendingStore(store, time.Hour)

	req := sproutDelegationRequest()
	req.Impact = core.DelegationImpactHigh

	// 1. Get a pending confirmation and approve it
	decision1 := authorizer1.Authorize(req)
	if !decision1.PendingConfirmation {
		t.Fatal("expected PendingConfirmation: true")
	}
	id1 := decision1.ConfirmationID
	if err := store.Approve(id1); err != nil {
		t.Fatalf("failed to approve: %v", err)
	}

	// 2. Construct a second authorizer with NO grants (simulating revocation)
	authorizer2 := core.NewDelegationAuthorizer(nil).WithPendingStore(store, time.Hour)

	// 3. Authorize should deny, not silently succeed off the orphaned pending record
	decision2 := authorizer2.Authorize(req)
	if decision2.Authorized {
		t.Fatal("expected denial after grant revocation despite approved pending record")
	}
	if decision2.PendingConfirmation {
		t.Fatal("expected no pending confirmation created because no grant matched")
	}
}

func TestDelegationAuthorizerPendingModifiedGrant(t *testing.T) {
	bounded := activeGrant()
	bounded.ConfirmAboveImpact = core.DelegationImpactHigh

	store := core.NewPendingConfirmationStore()
	authorizer1 := core.NewDelegationAuthorizer([]core.DelegationGrant{bounded}).WithPendingStore(store, time.Hour)

	req := sproutDelegationRequest()
	req.Impact = core.DelegationImpactHigh

	// 1. Get a pending confirmation and approve it
	decision1 := authorizer1.Authorize(req)
	if !decision1.PendingConfirmation {
		t.Fatal("expected PendingConfirmation: true")
	}
	id1 := decision1.ConfirmationID
	if err := store.Approve(id1); err != nil {
		t.Fatalf("failed to approve: %v", err)
	}

	// 2. Construct a second authorizer where the grant no longer has a ConfirmAboveImpact
	unbounded := activeGrant()
	unbounded.ConfirmAboveImpact = ""
	authorizer2 := core.NewDelegationAuthorizer([]core.DelegationGrant{unbounded}).WithPendingStore(store, time.Hour)

	// 3. Authorize should succeed purely because the live grant allows it, and the pending record
	// should not govern the behavior. Since there's no confirm-above threshold, it authorizes directly.
	decision2 := authorizer2.Authorize(req)
	if !decision2.Authorized {
		t.Fatal("expected authorization because live grant has no threshold")
	}
	if decision2.PendingConfirmation {
		t.Fatal("expected no pending confirmation")
	}
	if decision2.Grant.ConfirmAboveImpact != "" {
		t.Fatal("expected returned grant to be the LIVE unbounded grant")
	}
}

// TestDelegationAuthorizerCannotBeWidenedAfterConstruction encodes the
// no-self-escalation guarantee at the authorizer boundary: once constructed
// from the control plane, no later mutation of the caller's grant slice can
// change what the authorizer permits.
func TestDelegationAuthorizerCannotBeWidenedAfterConstruction(t *testing.T) {
	grants := []core.DelegationGrant{activeGrant()}
	authorizer := core.NewDelegationAuthorizer(grants)

	// Attempt to widen the grant in place after construction.
	grants[0].Pollen = "escalated-pollinator"
	grants[0].OperationClasses[0] = core.CapSequenceGrow
	grants[0].Substrates[0] = "other-repo"

	widened := core.DelegationRequest{
		Pollen:         "escalated-pollinator",
		OperationClass: core.CapSequenceGrow,
		Substrate:      "other-repo",
	}
	if decision := authorizer.Authorize(widened); decision.Authorized {
		t.Fatal("post-construction mutation of the grant slice widened the authorizer")
	}
	if decision := authorizer.Authorize(sproutDelegationRequest()); !decision.Authorized {
		t.Fatalf("post-construction mutation changed the original grant's decision: %s", decision.Reason)
	}
}

func TestLoadDelegationGrantsParsesControlPlaneFile(t *testing.T) {
	tendrilDir := t.TempDir()
	content := `grants:
  local-pollinator:
    substrates: [core]
    operationClasses:
      - sprout.grow
    egress: [github.com, proxy.golang.org]
    expires: 2199-08-15
    confirmAbove: { impact: high }
`
	if err := os.WriteFile(filepath.Join(tendrilDir, core.DelegationGrantsFilename), []byte(content), 0o644); err != nil {
		t.Fatalf("write grants file: %v", err)
	}

	grants, err := core.LoadDelegationGrants(tendrilDir)
	if err != nil {
		t.Fatalf("LoadDelegationGrants failed: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("grant count = %d, want 1", len(grants))
	}

	grant := grants[0]
	if grant.Pollen != "local-pollinator" {
		t.Errorf("pollen = %q, want local-pollinator", grant.Pollen)
	}
	if len(grant.OperationClasses) != 1 || grant.OperationClasses[0] != core.CapSproutGrow {
		t.Errorf("operationClasses = %v, want [sprout.grow]", grant.OperationClasses)
	}
	if len(grant.Substrates) != 1 || grant.Substrates[0] != "core" {
		t.Errorf("substrates = %v, want [core]", grant.Substrates)
	}
	if len(grant.Egress) != 2 {
		t.Errorf("egress = %v, want two allow-listed hosts", grant.Egress)
	}
	if grant.Expires.IsZero() || grant.Expires.Year() != 2199 {
		t.Errorf("expires = %v, want the configured date", grant.Expires)
	}
	if grant.ConfirmAboveImpact != core.DelegationImpactHigh {
		t.Errorf("confirmAboveImpact = %q, want high", grant.ConfirmAboveImpact)
	}
}

func TestLoadDelegationGrantsMissingFileMeansZeroGrants(t *testing.T) {
	grants, err := core.LoadDelegationGrants(t.TempDir())
	if err != nil {
		t.Fatalf("LoadDelegationGrants on a missing file errored: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("grant count = %d, want 0 (secure default)", len(grants))
	}
}

// TestLoadDelegationGrantsNeverReadsSubstrateCarriedFile encodes the
// no-self-escalation guarantee at the storage boundary: a grants file inside
// a Substrate checkout is never consulted — only the Stem's own control-plane
// directory is.
func TestLoadDelegationGrantsNeverReadsSubstrateCarriedFile(t *testing.T) {
	controlPlaneDir := t.TempDir()

	// A hostile Substrate carries a wide grant inside its own checkout.
	substrateCheckout := t.TempDir()
	substrateTendrilDir := filepath.Join(substrateCheckout, ".tendril")
	if err := os.MkdirAll(substrateTendrilDir, 0o755); err != nil {
		t.Fatalf("mkdir substrate .tendril: %v", err)
	}
	hostile := `grants:
  hostile-Pollinator:
    substrates: [core]
    operationClasses: [sprout.grow]
`
	if err := os.WriteFile(filepath.Join(substrateTendrilDir, core.DelegationGrantsFilename), []byte(hostile), 0o644); err != nil {
		t.Fatalf("write substrate-carried grants file: %v", err)
	}

	grants, err := core.LoadDelegationGrants(controlPlaneDir)
	if err != nil {
		t.Fatalf("LoadDelegationGrants failed: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("grant count = %d, want 0 — a Substrate-carried grants file must never load", len(grants))
	}

	authorizer := core.NewDelegationAuthorizer(grants)
	hostileRequest := core.DelegationRequest{
		Pollen:         "hostile-Pollinator",
		OperationClass: core.CapSproutGrow,
		Substrate:      "core",
	}
	if decision := authorizer.Authorize(hostileRequest); decision.Authorized {
		t.Fatal("a Substrate-carried grant widened the authorizer")
	}
}

func TestLoadDelegationGrantsRejectsMalformedGrants(t *testing.T) {
	cases := map[string]string{
		"no operationClasses": `grants:
  local-pollinator:
    substrates: [core]
`,
		"no substrates": `grants:
  local-pollinator:
    operationClasses: [sprout.grow]
`,
		"bad expires": `grants:
  local-pollinator:
    substrates: [core]
    operationClasses: [sprout.grow]
    expires: someday
`,
		"bad confirmAbove impact": `grants:
  local-pollinator:
    substrates: [core]
    operationClasses: [sprout.grow]
    confirmAbove: { impact: catastrophic }
`,
	}

	for name, content := range cases {
		tendrilDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tendrilDir, core.DelegationGrantsFilename), []byte(content), 0o644); err != nil {
			t.Fatalf("%s: write grants file: %v", name, err)
		}
		if _, err := core.LoadDelegationGrants(tendrilDir); err == nil {
			t.Errorf("%s: LoadDelegationGrants accepted a malformed grant", name)
		}
	}
}

// TestDelegatedCapabilityTaxonomy pins the canonical delegated
// operation-class set: exactly sprout.grow, stoma.pass, seed.grow and the
// seven git classes are delegated, every one of them is a canonical capability,
// and no non-delegated capability is misclassified. git.status is included
// deliberately: read-only does not mean ungated, since a status response names
// branches and changed file paths.
func TestDelegatedCapabilityTaxonomy(t *testing.T) {
	delegated := []string{core.CapSproutGrow, core.CapStomaPass, core.CapSeedGrow, core.CapGitCommit, core.CapGitPush, core.CapGitPR, core.CapGitBranch, core.CapGitStatus, core.CapGitBranchList, core.CapGitPrune, core.CapGenotypeCreate}
	if len(delegated) != 11 {
		t.Fatalf("DelegatedCapabilityNames() has %d name(s), want 11: %v", len(delegated), delegated)
	}
	for _, name := range delegated {
		if !core.IsDelegatedCapability(name) {
			t.Errorf("IsDelegatedCapability(%q) = false, want true", name)
		}
	}

	for _, name := range []string{core.CapListPhytomers, core.CapGenomeView, core.CapSequenceGrow, core.CapMeshGraft, "", "made.up"} {
		if core.IsDelegatedCapability(name) {
			t.Errorf("IsDelegatedCapability(%q) = true, want false", name)
		}
	}

	names := core.DelegatedCapabilityNames()
	if len(names) != len(delegated) {
		t.Fatalf("DelegatedCapabilityNames() has %d name(s), want %d: %v", len(names), len(delegated), names)
	}
	canonical := core.CapabilityNames()
	for _, name := range names {
		if !core.IsDelegatedCapability(name) {
			t.Errorf("DelegatedCapabilityNames() and IsDelegatedCapability disagree on %q", name)
		}
		found := false
		for _, capability := range canonical {
			if capability == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("delegated capability %q is not a canonical capability name", name)
		}
	}
}

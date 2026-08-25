package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

const gitOnlyGrantYAML = `grants:
  claude:
    operationClasses: [git.status, git.branch.list, git.branch, git.commit, git.push, git.pr]
    substrates: [myrepo]
`

func writeGrantsFile(t *testing.T, tendrilDir, content string) string {
	t.Helper()
	path := filepath.Join(tendrilDir, core.DelegationGrantsFilename)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write grants: %v", err)
	}
	return path
}

func loadGrant(t *testing.T, tendrilDir, pollen string) core.DelegationGrant {
	t.Helper()
	grants, err := core.LoadDelegationGrants(tendrilDir)
	if err != nil {
		t.Fatalf("LoadDelegationGrants: %v", err)
	}
	for _, grant := range grants {
		if grant.Pollen == pollen {
			return grant
		}
	}
	t.Fatalf("no grant for pollen %q", pollen)
	return core.DelegationGrant{}
}

func grantClasses(grant core.DelegationGrant) []string {
	return append([]string(nil), grant.OperationClasses...)
}

func hasClass(classes []string, name string) bool {
	for _, class := range classes {
		if class == name {
			return true
		}
	}
	return false
}

func TestAddGrantOperationClassesExtendsExistingGitGrant(t *testing.T) {
	tendrilDir := t.TempDir()
	writeGrantsFile(t, tendrilDir, gitOnlyGrantYAML)

	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow, core.CapSproutWatch}); err != nil {
		t.Fatalf("AddGrantOperationClasses: %v", err)
	}

	grant := loadGrant(t, tendrilDir, "claude")
	for _, want := range []string{core.CapGitStatus, core.CapGitBranchList, core.CapGitBranch, core.CapGitCommit, core.CapGitPush, core.CapGitPR, core.CapSeedGrow, core.CapSproutWatch} {
		if !hasClass(grant.OperationClasses, want) {
			t.Errorf("operationClasses = %v, want %s", grant.OperationClasses, want)
		}
	}
	if hasClass(grant.OperationClasses, core.CapSproutGrow) {
		t.Errorf("operationClasses = %v, must not add sprout.grow", grant.OperationClasses)
	}
	if hasClass(grant.OperationClasses, core.CapGitPrune) {
		t.Errorf("operationClasses = %v, must not add git.prune", grant.OperationClasses)
	}
	if len(grant.Substrates) != 1 || grant.Substrates[0] != "myrepo" {
		t.Errorf("substrates = %v, want [myrepo]", grant.Substrates)
	}
}

func TestAddGrantOperationClassesIsIdempotent(t *testing.T) {
	tendrilDir := t.TempDir()
	writeGrantsFile(t, tendrilDir, gitOnlyGrantYAML)

	ops := []string{core.CapSeedGrow, core.CapSproutWatch}
	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", ops); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	first := grantClasses(loadGrant(t, tendrilDir, "claude"))
	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow, core.CapSeedGrow}); err != nil {
		t.Fatalf("second grant: %v", err)
	}
	second := grantClasses(loadGrant(t, tendrilDir, "claude"))
	if len(second) != len(first) {
		t.Fatalf("idempotent grant changed class count %v -> %v", first, second)
	}
	seedCount := 0
	for _, class := range second {
		if class == core.CapSeedGrow {
			seedCount++
		}
	}
	if seedCount != 1 {
		t.Fatalf("seed.grow appeared %d times, want 1: %v", seedCount, second)
	}
}

func TestAddGrantOperationClassesPreservesUnrelatedFields(t *testing.T) {
	tendrilDir := t.TempDir()
	path := writeGrantsFile(t, tendrilDir, `grants:
  claude:
    operationClasses: [git.status, git.commit]
    substrates: [myrepo]
    egress: [github.com]
    expires: 2199-08-15
    confirmAbove: { impact: high }
    note: keep-me
  other:
    operationClasses: [git.status]
    substrates: [otherrepo]
`)

	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("AddGrantOperationClasses: %v", err)
	}

	grant := loadGrant(t, tendrilDir, "claude")
	if !hasClass(grant.OperationClasses, core.CapGitStatus) || !hasClass(grant.OperationClasses, core.CapGitCommit) {
		t.Errorf("unrelated operation classes lost: %v", grant.OperationClasses)
	}
	if !hasClass(grant.OperationClasses, core.CapSeedGrow) {
		t.Errorf("seed.grow missing: %v", grant.OperationClasses)
	}
	if len(grant.Egress) != 1 || grant.Egress[0] != "github.com" {
		t.Errorf("egress = %v, want [github.com]", grant.Egress)
	}
	if grant.Expires.IsZero() || grant.Expires.Year() != 2199 {
		t.Errorf("expires = %v, want 2199-08-15", grant.Expires)
	}
	if grant.ConfirmAboveImpact != core.DelegationImpactHigh {
		t.Errorf("confirmAbove = %q, want high", grant.ConfirmAboveImpact)
	}

	other := loadGrant(t, tendrilDir, "other")
	if len(other.OperationClasses) != 1 || other.OperationClasses[0] != core.CapGitStatus {
		t.Errorf("other pollen mutated: %+v", other)
	}
	if len(other.Substrates) != 1 || other.Substrates[0] != "otherrepo" {
		t.Errorf("other substrates = %v, want [otherrepo]", other.Substrates)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mutated grants: %v", err)
	}
	if !strings.Contains(string(raw), "note: keep-me") {
		t.Errorf("unknown field note was discarded:\n%s", raw)
	}
}

func TestRevokeGrantOperationClassesRemovesOnlyRequestedAuthority(t *testing.T) {
	tendrilDir := t.TempDir()
	writeGrantsFile(t, tendrilDir, gitOnlyGrantYAML)
	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow, core.CapSproutWatch}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	if err := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	grant := loadGrant(t, tendrilDir, "claude")
	if hasClass(grant.OperationClasses, core.CapSeedGrow) {
		t.Errorf("seed.grow still present: %v", grant.OperationClasses)
	}
	if !hasClass(grant.OperationClasses, core.CapSproutWatch) {
		t.Errorf("sprout.watch was revoked as a side effect: %v", grant.OperationClasses)
	}
	if !hasClass(grant.OperationClasses, core.CapGitStatus) {
		t.Errorf("git.status was revoked as a side effect: %v", grant.OperationClasses)
	}

	if err := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("repeated revoke: %v", err)
	}
	again := loadGrant(t, tendrilDir, "claude")
	if hasClass(again.OperationClasses, core.CapSeedGrow) {
		t.Errorf("repeated revoke reintroduced seed.grow: %v", again.OperationClasses)
	}
	if !hasClass(again.OperationClasses, core.CapGitCommit) {
		t.Errorf("repeated revoke corrupted remaining classes: %v", again.OperationClasses)
	}
}

func TestRevokeGrantOperationClassesRemovesEmptyPollenGrant(t *testing.T) {
	tendrilDir := t.TempDir()
	writeGrantsFile(t, tendrilDir, `grants:
  claude:
    operationClasses: [seed.grow]
    substrates: [myrepo]
  other:
    operationClasses: [git.status]
    substrates: [otherrepo]
`)
	if err := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("revoke last class: %v", err)
	}
	grants, err := core.LoadDelegationGrants(tendrilDir)
	if err != nil {
		t.Fatalf("LoadDelegationGrants: %v", err)
	}
	for _, grant := range grants {
		if grant.Pollen == "claude" {
			t.Fatalf("empty pollen grant was kept: %+v", grant)
		}
	}
	if len(grants) != 1 || grants[0].Pollen != "other" {
		t.Fatalf("unrelated pollen lost: %+v", grants)
	}
}

func TestValidateGrantableOperationRejectsUnknownAndNonDelegable(t *testing.T) {
	cases := map[string]string{
		"":                    "empty",
		"made.up":             "unknown",
		"seed.*":              "wildcard",
		"*":                   "wildcard",
		core.CapGenomeView:    "not delegable",
		core.CapListPhytomers: "not delegable",
		core.CapSequenceGrow:  "not delegable",
	}
	for name, kind := range cases {
		if err := core.ValidateGrantableOperation(name); err == nil {
			t.Errorf("ValidateGrantableOperation(%q) succeeded, want %s rejection", name, kind)
		}
	}
	for _, name := range []string{core.CapSeedGrow, core.CapSproutWatch, core.CapGitStatus} {
		if err := core.ValidateGrantableOperation(name); err != nil {
			t.Errorf("ValidateGrantableOperation(%q) = %v, want nil", name, err)
		}
	}
}

func TestAddGrantOperationClassesRejectsInvalidInput(t *testing.T) {
	tendrilDir := t.TempDir()
	writeGrantsFile(t, tendrilDir, gitOnlyGrantYAML)

	cases := []struct {
		name       string
		pollen     string
		substrate  string
		operations []string
		want       string
	}{
		{name: "empty pollen", pollen: "  ", substrate: "myrepo", operations: []string{core.CapSeedGrow}, want: "pollen is empty"},
		{name: "empty substrate", pollen: "claude", substrate: "", operations: []string{core.CapSeedGrow}, want: "substrate is empty"},
		{name: "missing operation", pollen: "claude", substrate: "myrepo", operations: nil, want: "at least one operation class is required"},
		{name: "unknown operation", pollen: "claude", substrate: "myrepo", operations: []string{"made.up"}, want: "unknown operation class"},
		{name: "non-delegable", pollen: "claude", substrate: "myrepo", operations: []string{core.CapGenomeView}, want: "not delegable"},
		{name: "missing pollen grant", pollen: "missing", substrate: "myrepo", operations: []string{core.CapSeedGrow}, want: "no grant exists for pollen"},
		{name: "wrong substrate", pollen: "claude", substrate: "otherrepo", operations: []string{core.CapSeedGrow}, want: "covers substrate"},
	}
	before, err := os.ReadFile(filepath.Join(tendrilDir, core.DelegationGrantsFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := core.AddGrantOperationClasses(tendrilDir, tt.pollen, tt.substrate, tt.operations)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q, want it to contain %q", err, tt.want)
			}
			after, readErr := os.ReadFile(filepath.Join(tendrilDir, core.DelegationGrantsFilename))
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != string(before) {
				t.Fatalf("rejected mutation replaced the grants file")
			}
		})
	}
}

func TestAddGrantOperationClassesDoesNotCreateMissingFile(t *testing.T) {
	tendrilDir := t.TempDir()
	err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow})
	if err == nil {
		t.Fatal("missing grants file created a new grant")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %q, want missing-file diagnostic", err)
	}
	if _, statErr := os.Stat(filepath.Join(tendrilDir, core.DelegationGrantsFilename)); !os.IsNotExist(statErr) {
		t.Fatal("missing-file grant wrote a grants file")
	}
}

func TestRevokeGrantOperationClassesMissingFileIsNoop(t *testing.T) {
	tendrilDir := t.TempDir()
	if err := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("revoke on missing file: %v", err)
	}
}

func TestRevokeGrantOperationClassesRejectsUnknownOperation(t *testing.T) {
	tendrilDir := t.TempDir()
	path := writeGrantsFile(t, tendrilDir, gitOnlyGrantYAML)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{"made.up"}); err == nil {
		t.Fatal("unknown operation was revoked")
	} else if !strings.Contains(err.Error(), "unknown operation class") {
		t.Fatalf("error = %q, want unknown operation class", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("rejected revoke replaced the grants file")
	}
}

func TestMultiSubstrateGrantMutationFailsClosed(t *testing.T) {
	tendrilDir := t.TempDir()
	path := writeGrantsFile(t, tendrilDir, `grants:
  claude:
    operationClasses: [git.status, git.commit]
    substrates: [myrepo, otherrepo]
`)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	addErr := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow})
	if addErr == nil {
		t.Fatal("multi-substrate add succeeded and would widen seed.grow across both substrates")
	}
	if !strings.Contains(addErr.Error(), "widen") {
		t.Errorf("add error = %q, want a widen diagnostic", addErr)
	}

	revokeErr := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapGitCommit})
	if revokeErr == nil {
		t.Fatal("multi-substrate revoke succeeded and would revoke git.commit across both substrates")
	}
	if !strings.Contains(revokeErr.Error(), "revoke authority across all") {
		t.Errorf("revoke error = %q, want an across-all diagnostic", revokeErr)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("fail-closed mutation replaced the grants file")
	}
	grant := loadGrant(t, tendrilDir, "claude")
	if hasClass(grant.OperationClasses, core.CapSeedGrow) {
		t.Error("seed.grow was added across a multi-substrate grant")
	}
	if !hasClass(grant.OperationClasses, core.CapGitCommit) {
		t.Error("git.commit was revoked across a multi-substrate grant")
	}
}

func TestMalformedGrantsFileIsNotReplaced(t *testing.T) {
	malformed := []struct {
		name    string
		content string
	}{
		{name: "grants is a sequence", content: "grants: [this is not a mapping]\n"},
		{name: "missing operationClasses", content: "grants:\n  claude:\n    substrates: [myrepo]\n"},
		{name: "invalid yaml", content: "{ not yaml"},
	}
	for _, tt := range malformed {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeGrantsFile(t, dir, tt.content)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			addErr := core.AddGrantOperationClasses(dir, "claude", "myrepo", []string{core.CapSeedGrow})
			if addErr == nil {
				t.Fatal("malformed grants file was mutated")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("malformed file was replaced:\n%s", after)
			}
		})
	}
}

func TestGrantMutationPreservesRestrictiveMode(t *testing.T) {
	tendrilDir := t.TempDir()
	path := writeGrantsFile(t, tendrilDir, gitOnlyGrantYAML)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("grants mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestGrantAuthorizationHandoff(t *testing.T) {
	tendrilDir := t.TempDir()
	writeGrantsFile(t, tendrilDir, gitOnlyGrantYAML)

	authorize := func() *core.DelegationAuthorizer {
		grants, err := core.LoadDelegationGrants(tendrilDir)
		if err != nil {
			t.Fatalf("LoadDelegationGrants: %v", err)
		}
		return core.NewDelegationAuthorizer(grants)
	}

	seedReq := core.DelegationRequest{Pollen: "claude", OperationClass: core.CapSeedGrow, Substrate: "myrepo"}
	watchReq := core.DelegationRequest{Pollen: "claude", OperationClass: core.CapSproutWatch, Substrate: "myrepo"}
	gitReq := core.DelegationRequest{Pollen: "claude", OperationClass: core.CapGitStatus, Substrate: "myrepo"}

	gitOnly := authorize()
	if decision := gitOnly.Authorize(seedReq); decision.Authorized {
		t.Fatal("git-only grant authorized seed.grow")
	}
	if decision := gitOnly.Authorize(watchReq); decision.Authorized {
		t.Fatal("git-only grant authorized sprout.watch")
	}
	if decision := gitOnly.Authorize(gitReq); !decision.Authorized {
		t.Fatalf("git-only grant denied git.status: %s", decision.Reason)
	}

	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("grant seed.grow: %v", err)
	}
	seedOnly := authorize()
	if decision := seedOnly.Authorize(seedReq); !decision.Authorized {
		t.Fatalf("seed.grow denied after explicit grant: %s", decision.Reason)
	}
	if decision := seedOnly.Authorize(watchReq); decision.Authorized {
		t.Fatal("granting seed.grow implied sprout.watch")
	}

	if err := core.AddGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSproutWatch}); err != nil {
		t.Fatalf("grant sprout.watch: %v", err)
	}
	both := authorize()
	if decision := both.Authorize(seedReq); !decision.Authorized {
		t.Fatalf("seed.grow denied after both grants: %s", decision.Reason)
	}
	if decision := both.Authorize(watchReq); !decision.Authorized {
		t.Fatalf("sprout.watch denied after explicit grant: %s", decision.Reason)
	}
	if decision := both.Authorize(core.DelegationRequest{Pollen: "other", OperationClass: core.CapSeedGrow, Substrate: "myrepo"}); decision.Authorized {
		t.Fatal("wrong pollen was authorized")
	}
	if decision := both.Authorize(core.DelegationRequest{Pollen: "claude", OperationClass: core.CapSeedGrow, Substrate: "otherrepo"}); decision.Authorized {
		t.Fatal("wrong substrate was authorized")
	}

	if err := core.RevokeGrantOperationClasses(tendrilDir, "claude", "myrepo", []string{core.CapSeedGrow}); err != nil {
		t.Fatalf("revoke seed.grow: %v", err)
	}
	afterRevoke := authorize()
	if decision := afterRevoke.Authorize(seedReq); decision.Authorized {
		t.Fatal("seed.grow still authorized after revoke")
	}
	if decision := afterRevoke.Authorize(watchReq); !decision.Authorized {
		t.Fatalf("sprout.watch lost after revoking seed.grow: %s", decision.Reason)
	}
	if decision := afterRevoke.Authorize(gitReq); !decision.Authorized {
		t.Fatalf("git.status lost after revoking seed.grow: %s", decision.Reason)
	}
}

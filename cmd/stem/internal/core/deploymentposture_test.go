package core_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

func TestDeploymentPostureResolution(t *testing.T) {
	t.Setenv(core.DeploymentPostureEnv, "")
	dir := t.TempDir()

	got, err := core.ResolveDeploymentPosture(dir)
	if err != nil || got != core.DeploymentPostureFull {
		t.Fatalf("missing file = %q, %v; want full, nil", got, err)
	}
	if got, err = core.ResolveDeploymentPosture(""); err != nil || got != core.DeploymentPostureFull {
		t.Fatalf("empty control plane = %q, %v; want full, nil", got, err)
	}

	path := filepath.Join(dir, core.DeploymentPostureFilename)
	if err := os.WriteFile(path, []byte("remote-git\r\n"), 0o600); err != nil {
		t.Fatalf("write posture: %v", err)
	}
	if got, err = core.ResolveDeploymentPosture(dir); err != nil || got != core.DeploymentPostureRemoteGit {
		t.Fatalf("file remote-git with empty env = %q, %v", got, err)
	}
	t.Setenv(core.DeploymentPostureEnv, core.DeploymentPostureRemoteGit)
	if got, err = core.ResolveDeploymentPosture(dir); err != nil || got != core.DeploymentPostureRemoteGit {
		t.Fatalf("agreeing remote-git = %q, %v", got, err)
	}

	ambiguous := []struct {
		name string
		env  string
		body string
	}{
		{name: "env without file", env: core.DeploymentPostureRemoteGit, body: ""},
		{name: "full env with remote file", env: core.DeploymentPostureFull, body: "remote-git\n"},
		{name: "remote env with full file", env: core.DeploymentPostureRemoteGit, body: "full\n"},
		{name: "trailing space", env: "", body: "remote-git \n"},
		{name: "extra line", env: "", body: "remote-git\nfull\n"},
		{name: "empty file", env: "", body: ""},
		{name: "unknown", env: "", body: "other\n"},
	}
	for _, tc := range ambiguous {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(core.DeploymentPostureEnv, tc.env)
			next := t.TempDir()
			if tc.name != "env without file" {
				if err := os.WriteFile(filepath.Join(next, core.DeploymentPostureFilename), []byte(tc.body), 0o600); err != nil {
					t.Fatalf("write posture: %v", err)
				}
			}
			if _, err := core.ResolveDeploymentPosture(next); err == nil {
				t.Fatal("ambiguous posture resolved")
			}
		})
	}

	t.Setenv(core.DeploymentPostureEnv, "")
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	unreadable := t.TempDir()
	if err := os.Mkdir(filepath.Join(unreadable, core.DeploymentPostureFilename), 0o700); err != nil {
		t.Fatalf("posture directory: %v", err)
	}
	if _, err := core.ResolveDeploymentPosture(unreadable); err == nil {
		t.Fatal("non-regular posture file resolved")
	}
}

func TestGovernedGitCapabilitiesMatchRegistry(t *testing.T) {
	governed := []string{
		core.CapGitCommit,
		core.CapGitPush,
		core.CapGitPR,
		core.CapGitPRUpdate,
		core.CapGitBranch,
		core.CapGitStatus,
		core.CapGitBranchList,
		core.CapGitPrune,
		core.CapGitApply,
		core.CapGitFetch,
	}
	for _, name := range governed {
		if !core.IsGovernedGitCapability(name) {
			t.Fatalf("%s is a governed Git capability and was refused by the posture predicate", name)
		}
	}
	seen := map[string]bool{}
	for _, name := range append(core.DelegatedCapabilityNames(), core.CapSproutWatch) {
		seen[name] = true
		want := strings.HasPrefix(name, "git.")
		if core.IsGovernedGitCapability(name) != want {
			t.Fatalf("IsGovernedGitCapability(%s) = %t, want %t", name, !want, want)
		}
	}
	for _, name := range governed {
		if !seen[name] {
			t.Fatalf("governed Git capability %s is missing from DelegatedCapabilityNames", name)
		}
	}
	if core.IsGovernedGitCapability("not-a-capability") {
		t.Fatal("unknown operation class was treated as governed Git")
	}
}

func TestRemoteGitAuthorizerDeniesExecutionBeforeGrants(t *testing.T) {
	t.Setenv(core.DeploymentPostureEnv, "")
	grant := core.DelegationGrant{
		Pollen:             "claude",
		OperationClasses:   []string{core.CapSeedGrow, core.CapSproutGrow, core.CapSproutWatch, core.CapGitStatus, core.CapGitFetch, core.CapGitPush},
		Substrates:         []string{"repo-a"},
		ConfirmAboveImpact: core.DelegationImpactLow,
	}
	store := core.NewPendingConfirmationStore()
	authorizer := core.NewDelegationAuthorizer([]core.DelegationGrant{grant}).
		WithPendingStore(store, time.Hour).
		WithDeploymentPosture(core.DeploymentPostureRemoteGit)

	for _, class := range []string{core.CapSeedGrow, core.CapSproutGrow, core.CapSproutWatch, "not-a-capability"} {
		decision := authorizer.Authorize(core.DelegationRequest{
			Pollen:         "claude",
			OperationClass: class,
			Substrate:      "repo-a",
			Impact:         core.DelegationImpactHigh,
		})
		if decision.Authorized || decision.PendingConfirmation || !strings.Contains(decision.Reason, "unavailable in the remote-git deployment posture") {
			t.Fatalf("%s decision = %+v; want posture denial without pending confirmation", class, decision)
		}
	}

	denied := authorizer.Authorize(core.DelegationRequest{
		Pollen: "claude", OperationClass: core.CapGitStatus, Substrate: "repo-b",
		Impact: core.DelegationImpactLow,
	})
	if denied.Authorized || strings.Contains(denied.Reason, "unavailable") || !strings.Contains(denied.Reason, "repo-b") {
		t.Fatalf("other substrate decision = %+v; want the existing grant denial", denied)
	}
	// Empty impact ranks above every confirmation threshold. Name each Git
	// capability's real impact and clear the threshold so the existing grant
	// authorizes. The execution cases above keep the threshold, which is how
	// the test shows posture denial wins before a pending confirmation.
	gitGrant := grant
	gitGrant.ConfirmAboveImpact = ""
	gitAuthorizer := core.NewDelegationAuthorizer([]core.DelegationGrant{gitGrant}).
		WithDeploymentPosture(core.DeploymentPostureRemoteGit)
	for _, class := range []struct {
		name   string
		impact string
	}{
		{core.CapGitStatus, core.DelegationImpactLow},
		{core.CapGitFetch, core.DelegationImpactMedium},
		{core.CapGitPush, core.DelegationImpactHigh},
	} {
		decision := gitAuthorizer.Authorize(core.DelegationRequest{
			Pollen: "claude", OperationClass: class.name, Substrate: "repo-a", Impact: class.impact,
		})
		if !decision.Authorized {
			t.Fatalf("%s on repo-a = %+v; want the existing grant", class.name, decision)
		}
	}

	emptyRemote := core.NewDelegationAuthorizer(nil).WithDeploymentPosture(core.DeploymentPostureRemoteGit)
	emptyDecision := emptyRemote.Authorize(core.DelegationRequest{
		Pollen: "claude", OperationClass: core.CapSeedGrow, Substrate: "repo-a",
	})
	if emptyDecision.Authorized || !strings.Contains(emptyDecision.Reason, "unavailable") {
		t.Fatalf("empty grants remote decision = %+v; want posture denial", emptyDecision)
	}
	emptyFull := core.NewDelegationAuthorizer(nil).WithDeploymentPosture(core.DeploymentPostureFull)
	fullDecision := emptyFull.Authorize(core.DelegationRequest{
		Pollen: "claude", OperationClass: core.CapSeedGrow, Substrate: "repo-a",
	})
	if fullDecision.Authorized || fullDecision.Reason != "no delegation grants are configured" {
		t.Fatalf("empty grants full decision = %+v", fullDecision)
	}
	if reason := (*core.DelegationAuthorizer)(nil).Authorize(core.DelegationRequest{OperationClass: core.CapSeedGrow}).Reason; reason != "no delegation grants are configured" {
		t.Fatalf("nil authorizer reason = %q", reason)
	}
	unknown := core.NewDelegationAuthorizer([]core.DelegationGrant{grant}).WithDeploymentPosture("other")
	if decision := unknown.Authorize(core.DelegationRequest{
		Pollen: "claude", OperationClass: core.CapSeedGrow, Substrate: "repo-a",
	}); decision.Authorized || !strings.Contains(decision.Reason, "unavailable") {
		t.Fatalf("unknown posture decision = %+v; want fail closed", decision)
	}
}

func TestAuthorityReadsRemoteGitPostureLive(t *testing.T) {
	t.Setenv(core.DeploymentPostureEnv, "")
	dir := t.TempDir()
	grants := "grants:\n  claude:\n    operationClasses: [seed.grow, git.status]\n    substrates: [repo-a]\n"
	if err := os.WriteFile(filepath.Join(dir, core.DelegationGrantsFilename), []byte(grants), 0o600); err != nil {
		t.Fatalf("write grants: %v", err)
	}
	authority := core.NewAuthority(dir)
	request := core.DelegationRequest{Pollen: "claude", OperationClass: core.CapSeedGrow, Substrate: "repo-a"}
	if decision, err := authority.AuthorizeDelegation(request); err != nil || !decision.Authorized {
		t.Fatalf("full posture decision = %+v, %v", decision, err)
	}
	if err := os.WriteFile(filepath.Join(dir, core.DeploymentPostureFilename), []byte("remote-git\n"), 0o600); err != nil {
		t.Fatalf("write posture: %v", err)
	}
	decision, err := authority.AuthorizeDelegation(request)
	if err != nil || decision.Authorized || !strings.Contains(decision.Reason, "unavailable") {
		t.Fatalf("live remote posture decision = %+v, %v", decision, err)
	}
	gitDecision, gitErr := authority.AuthorizeDelegation(core.DelegationRequest{
		Pollen: "claude", OperationClass: core.CapGitStatus, Substrate: "repo-a",
	})
	if gitErr != nil || !gitDecision.Authorized {
		t.Fatalf("live git decision = %+v, %v", gitDecision, gitErr)
	}
	if err := os.WriteFile(filepath.Join(dir, core.DeploymentPostureFilename), []byte("remote-git\nnope\n"), 0o600); err != nil {
		t.Fatalf("rewrite posture: %v", err)
	}
	decision, err = authority.AuthorizeDelegation(request)
	if err == nil || decision.Authorized || decision.Reason != "deployment posture is ambiguous" {
		t.Fatalf("ambiguous posture decision = %+v, %v", decision, err)
	}
}

func TestRemoteGitExecutionDoesNotCallPorts(t *testing.T) {
	t.Setenv(core.DeploymentPostureEnv, "")
	var seedRuns, sproutRuns, continuationCalls int
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	svc := core.NewService(manager).
		WithDeploymentPosture(core.DeploymentPostureRemoteGit).
		WithSeed(core.SeedOperations{
			Run: func(context.Context, core.SeedSpec, *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
				seedRuns++
				return core.SeedGrowResult{}, nil
			},
		}).
		WithSprout(core.SproutOperations{
			Run: func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
				sproutRuns++
				return core.SproutRunReport{}, nil
			},
		}).
		WithContinuationPersistence(core.ContinuationPersistence{
			ResolveTarget: func(context.Context, string) (core.ContinuationTarget, bool, error) {
				continuationCalls++
				return core.ContinuationTarget{}, false, nil
			},
			Accept: func(context.Context, core.ContinuationAcceptance) (core.ContinuationRecord, error) {
				continuationCalls++
				return core.ContinuationRecord{}, nil
			},
		})

	if _, err := svc.SeedGrow(context.Background(), core.SeedGrowInput{}); err == nil || !strings.Contains(err.Error(), "seed.grow is unavailable") {
		t.Fatalf("SeedGrow error = %v", err)
	}
	if _, err := svc.SproutRun(context.Background(), core.SproutRunInput{}); err == nil || !strings.Contains(err.Error(), "sprout.grow is unavailable") {
		t.Fatalf("SproutRun error = %v", err)
	}
	if _, err := svc.AcceptContinuation(context.Background(), core.ContinuationInput{}); err == nil || !strings.Contains(err.Error(), "phytomer.continue is unavailable") {
		t.Fatalf("AcceptContinuation error = %v", err)
	}
	if seedRuns != 0 || sproutRuns != 0 || continuationCalls != 0 {
		t.Fatalf("ports called: seed=%d sprout=%d continuation=%d", seedRuns, sproutRuns, continuationCalls)
	}

	plain := core.NewService(manager).WithSeed(core.SeedOperations{
		Run: func(context.Context, core.SeedSpec, *core.SeedContinuationLifecycle) (core.SeedGrowResult, error) {
			return core.SeedGrowResult{}, nil
		},
	})
	if _, plainErr := plain.SeedGrow(context.Background(), core.SeedGrowInput{}); plainErr != nil && strings.Contains(plainErr.Error(), "unavailable") {
		t.Fatalf("empty posture SeedGrow = %v; want the existing behavior", plainErr)
	}
}

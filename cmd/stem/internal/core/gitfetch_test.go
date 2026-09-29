package core

import (
	"context"
	"errors"
	"testing"
)

func authorizedGitFetchContext(pollen, operation, substrate string, impact string) context.Context {
	ctx := WithPollen(context.Background(), pollen)
	return WithAuthorizedDelegationRequest(ctx, DelegationRequest{
		Pollen: pollen, OperationClass: operation, Substrate: substrate, Impact: impact,
	})
}

func TestGitFetchRequiresExactDelegationTupleAndDoesNotInheritGitGrants(t *testing.T) {
	calls := 0
	svc := NewService(nil).WithGit(GitOperations{Fetch: func(_ context.Context, spec GitFetchSpec) (GitFetchResult, error) {
		calls++
		return GitFetchResult{Status: "fetched", Substrate: spec.Substrate, Remote: "origin"}, nil
	}})
	input := GitFetchInput{Substrate: "demo", Origin: "mcp"}
	valid := authorizedGitFetchContext("pollen-one", CapGitFetch, "demo", DelegationImpactMedium)
	if result, err := svc.GitFetch(valid, input); err != nil {
		t.Fatalf("authorized fetch: %v", err)
	} else if result.Substrate != "demo" || result.Remote != "origin" {
		t.Fatalf("result = %#v", result)
	}
	if calls != 1 {
		t.Fatalf("Fetch calls = %d, want 1", calls)
	}

	denied := []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing Pollen", ctx: WithAuthorizedDelegationRequest(context.Background(), DelegationRequest{Pollen: "pollen-one", OperationClass: CapGitFetch, Substrate: "demo", Impact: DelegationImpactMedium})},
		{name: "missing authorization", ctx: WithPollen(context.Background(), "pollen-one")},
		{name: "wrong Pollen", ctx: WithPollen(authorizedGitFetchContext("pollen-one", CapGitFetch, "demo", DelegationImpactMedium), "other-pollen")},
		{name: "git.apply does not confer", ctx: authorizedGitFetchContext("pollen-one", CapGitApply, "demo", DelegationImpactMedium)},
		{name: "git.push does not confer", ctx: authorizedGitFetchContext("pollen-one", CapGitPush, "demo", DelegationImpactHigh)},
		{name: "wrong Substrate", ctx: authorizedGitFetchContext("pollen-one", CapGitFetch, "other", DelegationImpactMedium)},
		{name: "wrong impact", ctx: authorizedGitFetchContext("pollen-one", CapGitFetch, "demo", DelegationImpactLow)},
		{name: "high impact", ctx: authorizedGitFetchContext("pollen-one", CapGitFetch, "demo", DelegationImpactHigh)},
	}
	for _, test := range denied {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.GitFetch(test.ctx, input)
			var fetchErr GitFetchError
			if !errors.As(err, &fetchErr) || fetchErr.Category != GitFetchFailureAuthorizationDenied {
				t.Fatalf("error = %v, want typed authorization denial", err)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("denied invocations reached Fetch: calls = %d", calls)
	}
}

func TestGitFetchInputAndRegistryAreNarrow(t *testing.T) {
	svc := NewService(nil).WithGit(GitOperations{Fetch: func(_ context.Context, spec GitFetchSpec) (GitFetchResult, error) {
		return GitFetchResult{Status: "fetched", Substrate: spec.Substrate, Remote: "origin"}, nil
	}})
	ctx := authorizedGitFetchContext("pollen-one", CapGitFetch, "demo", DelegationImpactMedium)
	if _, err := svc.Invoke(ctx, CapGitFetch, map[string]any{"substrate": "demo", "remote": "upstream"}); err == nil {
		t.Fatal("unknown remote input was accepted")
	}
	if !IsDelegatedCapability(CapGitFetch) {
		t.Fatal("git.fetch is absent from delegated capability authority")
	}
	if CapabilityImpact(CapGitFetch) != DelegationImpactMedium {
		t.Fatalf("git.fetch impact = %q, want Medium", CapabilityImpact(CapGitFetch))
	}
	found := false
	for _, name := range CapabilityNames() {
		if name == CapGitFetch {
			found = true
		}
	}
	if !found {
		t.Fatal("git.fetch is absent from the canonical capability registry")
	}
}

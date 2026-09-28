package core

import (
	"context"
	"strings"
	"testing"
)

const validApplyHead = "0123456789abcdef0123456789abcdef01234567"

func authorizedGitApplyContext(pollen, operation, substrate string) context.Context {
	ctx := WithPollen(context.Background(), pollen)
	return WithAuthorizedDelegationRequest(ctx, DelegationRequest{
		Pollen: pollen, OperationClass: operation, Substrate: substrate, Impact: CapabilityImpact(operation),
	})
}

func TestGitApplyRequiresExactDelegationAndNamedSubstrateTuple(t *testing.T) {
	var calls int
	svc := NewService(nil).WithGit(GitOperations{Apply: func(_ context.Context, spec GitApplySpec) (GitApplyResult, error) {
		calls++
		return GitApplyResult{Status: "applied", Substrate: spec.Substrate, Head: spec.ExpectedHead}, nil
	}})
	in := GitApplyInput{Substrate: "demo", ExpectedHead: validApplyHead, Patch: "diff --git a/a b/a"}

	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing Pollen"},
		{name: "missing authorization witness", ctx: WithPollen(context.Background(), "pollen")},
		{name: "wrong Pollen", ctx: WithAuthorizedDelegationRequest(
			WithPollen(context.Background(), "pollen"),
			DelegationRequest{Pollen: "other", OperationClass: CapGitApply, Substrate: "demo", Impact: DelegationImpactMedium},
		)},
		{name: "wrong operation", ctx: authorizedGitApplyContext("pollen", CapGitCommit, "demo")},
		{name: "wrong Substrate", ctx: authorizedGitApplyContext("pollen", CapGitApply, "other")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := svc.GitApply(test.ctx, in); err == nil {
				t.Fatal("unauthorized request was accepted")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("Git port called %d time(s) for unauthorized requests", calls)
	}
}

func TestGitApplyValidatesFullHeadUTF8AndCanonicalPatchBoundBeforeGit(t *testing.T) {
	var captured GitApplySpec
	var calls int
	svc := NewService(nil).WithGit(GitOperations{Apply: func(_ context.Context, spec GitApplySpec) (GitApplyResult, error) {
		calls++
		captured = spec
		return GitApplyResult{Status: "applied", Substrate: spec.Substrate, Head: spec.ExpectedHead}, nil
	}})
	ctx := authorizedGitApplyContext("pollen", CapGitApply, "demo")
	valid := GitApplyInput{Substrate: " demo ", ExpectedHead: validApplyHead, Patch: "patch"}
	if _, err := svc.GitApply(ctx, valid); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if captured.Substrate != "demo" || captured.ExpectedHead != validApplyHead || captured.Patch != "patch" {
		t.Fatalf("Git spec = %+v, want canonical request", captured)
	}
	if calls != 1 {
		t.Fatalf("Git port called %d times for valid request, want 1", calls)
	}

	for _, test := range []struct {
		name   string
		mutate func(*GitApplyInput)
	}{
		{name: "missing expectedHead", mutate: func(in *GitApplyInput) { in.ExpectedHead = "" }},
		{name: "abbreviated expectedHead", mutate: func(in *GitApplyInput) { in.ExpectedHead = validApplyHead[:12] }},
		{name: "malformed expectedHead", mutate: func(in *GitApplyInput) { in.ExpectedHead = strings.Repeat("g", 40) }},
		{name: "invalid UTF-8", mutate: func(in *GitApplyInput) { in.Patch = string([]byte{0xff}) }},
		{name: "above 1 MiB", mutate: func(in *GitApplyInput) { in.Patch = strings.Repeat("x", MaxGitApplyPatchBytes+1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := valid
			test.mutate(&in)
			if _, err := svc.GitApply(ctx, in); err == nil {
				t.Fatal("invalid request was accepted")
			}
		})
	}
	if calls != 1 {
		t.Fatalf("Git port called %d times after validation failures, want still 1", calls)
	}

	valid.Patch = strings.Repeat("x", MaxGitApplyPatchBytes)
	if _, err := svc.GitApply(ctx, valid); err != nil {
		t.Fatalf("exactly 1 MiB patch was rejected: %v", err)
	}
	if calls != 2 {
		t.Fatalf("Git port calls = %d, want 2 after exact-limit patch", calls)
	}
}

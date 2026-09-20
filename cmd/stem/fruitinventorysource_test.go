package main

import (
	"context"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestObserveFruitClaimEvidenceUsesExactLocalCommitOnly(t *testing.T) {
	localCalls := 0
	ops := fruitInventoryEvidenceOps{
		ObserveLocal: func(context.Context, string, string) (bool, error) {
			localCalls++
			return true, nil
		},
	}
	evidence := observeFruitClaimEvidence(context.Background(), core.FruitClaim{
		ProducerKind: "sprout", ProducerIdentity: "run", Workspace: "/private/repo", Commit: "exact", PublicationState: conductor.FruitPublicationFailed,
	}, nil, ops)
	if localCalls != 1 {
		t.Fatalf("local evidence calls = %d, want one", localCalls)
	}
	if !evidence.RepositoryAvailable || !evidence.LocalCommit.Available || !evidence.LocalCommit.Exact {
		t.Fatalf("evidence = %+v, want exact local evidence", evidence)
	}
	if evidence.Forge.Records != nil {
		t.Fatalf("local evidence unexpectedly contains forge records: %+v", evidence.Forge)
	}
}

func TestObserveFruitClaimEvidenceRequiresExactHeadForClosedReview(t *testing.T) {
	ops := fruitInventoryEvidenceOps{
		ResolveWorkspace: func(string, *conductor.SubstrateSpec) (string, error) { return "/current/repo", nil },
		ResolveIdentity:  func(context.Context, string) (string, error) { return "github.com/org/repo", nil },
		ResolveCredential: func(conductor.SubstrateSpec, *conductor.SubstratesConfig) (conductor.ResolvedCredential, error) {
			return conductor.ResolvedCredential{Method: conductor.CredentialPAT, TokenValue: "redacted-test-token"}, nil
		},
		ObserveReview: func(context.Context, string, string, string, conductor.ResolvedCredential) (conductor.FruitReviewObservation, error) {
			return conductor.FruitReviewObservation{Records: []conductor.FruitReviewRecord{{Number: 42, State: "closed", HeadCommit: "different"}}}, nil
		},
		ObserveRemoteRef: func(context.Context, string, string, conductor.ResolvedCredential) (conductor.FruitRemoteRefObservation, error) {
			return conductor.FruitRemoteRefObservation{Exists: true, Commit: "fruit-commit"}, nil
		},
	}
	evidence := observeFruitClaimEvidence(context.Background(), core.FruitClaim{
		ProducerKind: "sprout", ProducerIdentity: "run", Substrate: "foo", Repository: "github.com/org/repo", Branch: "reused", Commit: "fruit-commit", PublicationState: conductor.FruitPublicationPublished,
	}, &conductor.SubstratesConfig{Substrates: map[string]conductor.SubstrateSpec{"foo": {URL: "github.com/org/repo"}}}, ops)
	if !evidence.RepositoryAvailable || !evidence.Forge.Available || !evidence.RemoteRef.Exists {
		t.Fatalf("evidence = %+v, want repository, forge, and ref facts", evidence)
	}
	if len(evidence.Forge.Records) != 1 || evidence.Forge.Records[0].HeadCommit != "different" {
		t.Fatalf("review facts = %+v, want the non-identical head retained as fact", evidence.Forge.Records)
	}

	service := core.NewService(nil).WithFruitInventoryObservationSource(core.FruitInventoryObservationSource{Observe: func(context.Context) ([]core.FruitInventoryEvidence, error) {
		return []core.FruitInventoryEvidence{evidence}, nil
	}})
	inventory, err := service.ObserveFruitInventory(context.Background())
	if err != nil {
		t.Fatalf("ObserveFruitInventory: %v", err)
	}
	if inventory.Items[0].ReviewState != core.FruitReviewOutstanding {
		t.Fatalf("item = %+v, want outstanding from exact remote ref", inventory.Items[0])
	}
}

func TestObserveFruitClaimEvidenceDoesNotFollowRepointedSubstrateAlias(t *testing.T) {
	reviewCalls := 0
	remoteCalls := 0
	ops := fruitInventoryEvidenceOps{
		ResolveWorkspace: func(string, *conductor.SubstrateSpec) (string, error) { return "/replacement/repo", nil },
		ResolveIdentity:  func(context.Context, string) (string, error) { return "github.com/org/replacement", nil },
		ResolveCredential: func(conductor.SubstrateSpec, *conductor.SubstratesConfig) (conductor.ResolvedCredential, error) {
			return conductor.ResolvedCredential{Method: conductor.CredentialPAT, TokenValue: "replacement-token"}, nil
		},
		ObserveReview: func(context.Context, string, string, string, conductor.ResolvedCredential) (conductor.FruitReviewObservation, error) {
			reviewCalls++
			return conductor.FruitReviewObservation{}, nil
		},
		ObserveRemoteRef: func(context.Context, string, string, conductor.ResolvedCredential) (conductor.FruitRemoteRefObservation, error) {
			remoteCalls++
			return conductor.FruitRemoteRefObservation{}, nil
		},
	}
	evidence := observeFruitClaimEvidence(context.Background(), core.FruitClaim{
		ProducerKind: "seed", ProducerIdentity: "seed-handle", Substrate: "foo", Repository: "github.com/org/original", Branch: "review/fruit", Commit: "commit", PublicationState: conductor.FruitPublicationPublished,
	}, &conductor.SubstratesConfig{Substrates: map[string]conductor.SubstrateSpec{"foo": {URL: "github.com/org/replacement"}}}, ops)
	if evidence.RepositoryAvailable {
		t.Fatalf("repointed alias was accepted: %+v", evidence)
	}
	if reviewCalls != 0 || remoteCalls != 0 {
		t.Fatalf("replacement evidence was queried: review=%d remote=%d", reviewCalls, remoteCalls)
	}
}

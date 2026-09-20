package core

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestObserveFruitInventoryClassifiesExactEvidence(t *testing.T) {
	now := time.Date(2026, 9, 21, 1, 2, 3, 0, time.UTC)
	claim := func(id, state string) FruitClaim {
		return FruitClaim{
			ProducerKind:     "sprout",
			ProducerIdentity: id,
			Substrate:        "substrate",
			Repository:       "github.com/org/repo",
			Branch:           "review/" + id,
			Commit:           "commit-" + id,
			PublicationState: state,
			CreatedAt:        now,
		}
	}

	tests := []struct {
		name       string
		claim      FruitClaim
		forge      FruitForgeEvidence
		remote     FruitRemoteRefEvidence
		local      FruitLocalCommitEvidence
		repository bool
		state      string
		reason     string
		pull       int
	}{
		{
			name:   "exact merged review",
			claim:  claim("merged", "published"),
			forge:  FruitForgeEvidence{Available: true, Records: []FruitReviewRecord{{Number: 11, Merged: true, State: "closed", CommitLookup: true}}},
			remote: FruitRemoteRefEvidence{Available: true}, repository: true,
			state: FruitReviewMerged, pull: 11,
		},
		{
			name:   "squash merge exact commit lookup",
			claim:  claim("squash", "published"),
			forge:  FruitForgeEvidence{Available: true, Records: []FruitReviewRecord{{Number: 12, Merged: true, State: "closed", CommitLookup: true}}},
			remote: FruitRemoteRefEvidence{Available: true}, repository: true,
			state: FruitReviewMerged, pull: 12,
		},
		{
			name:   "closed matching head",
			claim:  claim("closed", "published"),
			forge:  FruitForgeEvidence{Available: true, Records: []FruitReviewRecord{{Number: 13, State: "closed", HeadCommit: "commit-closed"}}},
			remote: FruitRemoteRefEvidence{Available: true}, repository: true,
			state: FruitReviewClosedUnmerged, pull: 13,
		},
		{
			name:   "closed commit association with reused head is not exact",
			claim:  claim("closed-reused", "published"),
			forge:  FruitForgeEvidence{Available: true, Records: []FruitReviewRecord{{Number: 131, State: "closed", HeadCommit: "different-commit", CommitLookup: true}}},
			remote: FruitRemoteRefEvidence{Available: true, Exists: true, Commit: "commit-closed-reused"}, repository: true,
			state: FruitReviewOutstanding,
		},
		{
			name:   "reused branch name is not exact",
			claim:  claim("reused", "published"),
			forge:  FruitForgeEvidence{Available: true, Records: []FruitReviewRecord{{Number: 14, State: "closed", HeadCommit: "different-commit"}}},
			remote: FruitRemoteRefEvidence{Available: true, Exists: true, Commit: "commit-reused"}, repository: true,
			state: FruitReviewOutstanding,
		},
		{
			name:   "open exact review",
			claim:  claim("open", "published"),
			forge:  FruitForgeEvidence{Available: true, Records: []FruitReviewRecord{{Number: 15, State: "open", CommitLookup: true}}},
			remote: FruitRemoteRefEvidence{Available: true}, repository: true,
			state: FruitReviewOutstanding, pull: 15,
		},
		{
			name:   "partial forge retains exact merged evidence",
			claim:  claim("partial-merged", "published"),
			forge:  FruitForgeEvidence{Available: true, Incomplete: true, Records: []FruitReviewRecord{{Number: 151, Merged: true, State: "closed", CommitLookup: true}}},
			remote: FruitRemoteRefEvidence{Available: true}, repository: true,
			state: FruitReviewMerged, pull: 151,
		},
		{
			name:   "partial forge cannot prove closed head",
			claim:  claim("partial-closed", "published"),
			forge:  FruitForgeEvidence{Available: true, Incomplete: true, Records: []FruitReviewRecord{{Number: 152, State: "closed", HeadCommit: "different"}}},
			remote: FruitRemoteRefEvidence{Available: true, Exists: true, Commit: "commit-partial-closed"}, repository: true,
			state: FruitReviewUnknown, reason: FruitUnknownReviewEvidenceUnavailable,
		},
		{
			name:   "published branch without review",
			claim:  claim("branch", "published"),
			forge:  FruitForgeEvidence{Available: true},
			remote: FruitRemoteRefEvidence{Available: true, Exists: true, Commit: "commit-branch"}, repository: true,
			state: FruitReviewOutstanding,
		},
		{
			name:  "local only exact commit",
			claim: claim("local", "local-only"),
			forge: FruitForgeEvidence{Available: true},
			local: FruitLocalCommitEvidence{Available: true, Exact: true}, repository: true,
			state: FruitReviewOutstanding,
		},
		{
			name:  "publication failure exact commit",
			claim: claim("failed", "publication-failed"),
			forge: FruitForgeEvidence{Available: false},
			local: FruitLocalCommitEvidence{Available: true, Exact: true}, repository: true,
			state: FruitReviewOutstanding,
		},
		{
			name:   "missing published ref",
			claim:  claim("missing", "published"),
			forge:  FruitForgeEvidence{Available: true},
			remote: FruitRemoteRefEvidence{Available: true, Exists: false}, repository: true,
			state: FruitReviewUnknown, reason: FruitUnknownRemoteRefMissing,
		},
		{
			name:  "forge failure",
			claim: claim("forge-failure", "published"),
			forge: FruitForgeEvidence{Available: false}, repository: true,
			state: FruitReviewUnknown, reason: FruitUnknownForgeUnavailable,
		},
		{
			name:   "review evidence unavailable",
			claim:  claim("review-unavailable", "published"),
			forge:  FruitForgeEvidence{Available: false},
			remote: FruitRemoteRefEvidence{Available: true, Exists: true, Commit: "commit-review-unavailable"}, repository: true,
			state: FruitReviewUnknown, reason: FruitUnknownReviewEvidenceUnavailable,
		},
		{
			name:  "local repository unavailable",
			claim: claim("repo-failure", "local-only"),
			forge: FruitForgeEvidence{Available: true}, repository: false,
			state: FruitReviewUnknown, reason: FruitUnknownRepositoryUnavailable,
		},
		{
			name:  "conflicting exact review evidence",
			claim: claim("conflict", "published"),
			forge: FruitForgeEvidence{Available: true, Records: []FruitReviewRecord{
				{Number: 16, Merged: true, State: "closed", CommitLookup: true},
				{Number: 17, State: "open", CommitLookup: true},
			}},
			remote: FruitRemoteRefEvidence{Available: true, Exists: true, Commit: "commit-conflict"}, repository: true,
			state: FruitReviewUnknown, reason: FruitUnknownReviewEvidenceAmbiguous,
		},
		{
			name:   "ambiguous duplicate review evidence",
			claim:  claim("duplicate-conflict", "published"),
			forge:  FruitForgeEvidence{Available: true, Ambiguous: true, Records: []FruitReviewRecord{{Number: 18, Merged: true, CommitLookup: true}}},
			remote: FruitRemoteRefEvidence{Available: true, Exists: true, Commit: "commit-duplicate-conflict"}, repository: true,
			state: FruitReviewUnknown, reason: FruitUnknownReviewEvidenceAmbiguous,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := NewService(nil).WithFruitInventoryObservationSource(FruitInventoryObservationSource{
				Observe: func(context.Context) ([]FruitInventoryEvidence, error) {
					return []FruitInventoryEvidence{{Claim: test.claim, RepositoryAvailable: test.repository, Forge: test.forge, RemoteRef: test.remote, LocalCommit: test.local}}, nil
				},
			})
			inventory, err := svc.ObserveFruitInventory(context.Background())
			if err != nil {
				t.Fatalf("ObserveFruitInventory: %v", err)
			}
			if len(inventory.Items) != 1 {
				t.Fatalf("items = %+v", inventory.Items)
			}
			item := inventory.Items[0]
			if item.ReviewState != test.state || item.UnknownReason != test.reason || item.PullRequest != test.pull {
				t.Fatalf("item = %+v, want state=%q reason=%q pull=%d", item, test.state, test.reason, test.pull)
			}
		})
	}
}

func TestObserveFruitInventoryOrderingAndCountsAreStable(t *testing.T) {
	base := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	claims := []FruitInventoryEvidence{
		{Claim: FruitClaim{ProducerKind: "seed", ProducerIdentity: "z", Repository: "r", Branch: "b", Commit: "3", CreatedAt: base, PublicationState: "published"}, RepositoryAvailable: true, Forge: FruitForgeEvidence{Available: true, Records: []FruitReviewRecord{{State: "closed", HeadCommit: "3"}}}, RemoteRef: FruitRemoteRefEvidence{Available: true}},
		{Claim: FruitClaim{ProducerKind: "sprout", ProducerIdentity: "a", Repository: "r", Branch: "a", Commit: "1", CreatedAt: base.Add(time.Hour), PublicationState: "published"}, RepositoryAvailable: true, Forge: FruitForgeEvidence{Available: true, Records: []FruitReviewRecord{{Merged: true, CommitLookup: true}}}, RemoteRef: FruitRemoteRefEvidence{Available: true}},
		{Claim: FruitClaim{ProducerKind: "sprout", ProducerIdentity: "b", Repository: "r", Branch: "b", Commit: "2", CreatedAt: base.Add(-time.Hour), PublicationState: "local-only"}, RepositoryAvailable: true, LocalCommit: FruitLocalCommitEvidence{Available: true, Exact: true}},
		{Claim: FruitClaim{ProducerKind: "seed", ProducerIdentity: "c", Repository: "r", Branch: "c", Commit: "4", CreatedAt: base.Add(2 * time.Hour), PublicationState: "published"}, RepositoryAvailable: true, Forge: FruitForgeEvidence{Available: false}},
	}
	observe := func() FruitInventory {
		service := NewService(nil).WithFruitInventoryObservationSource(FruitInventoryObservationSource{Observe: func(context.Context) ([]FruitInventoryEvidence, error) {
			return claims, nil
		}})
		inventory, err := service.ObserveFruitInventory(context.Background())
		if err != nil {
			t.Fatalf("ObserveFruitInventory: %v", err)
		}
		return inventory
	}
	first, second := observe(), observe()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated observations differ:\nfirst=%+v\nsecond=%+v", first, second)
	}
	wantOrder := []string{"b", "c", "z", "a"}
	for index, want := range wantOrder {
		if first.Items[index].ProducerIdentity != want {
			t.Fatalf("item %d identity = %q, want %q", index, first.Items[index].ProducerIdentity, want)
		}
	}
	if first.Counts != (FruitReviewPressure{Outstanding: 1, Unknown: 1, ClosedUnmerged: 1, Merged: 1, Total: 4}) {
		t.Fatalf("counts = %+v", first.Counts)
	}
	left, _ := json.Marshal(first)
	right, _ := json.Marshal(second)
	if string(left) != string(right) {
		t.Fatalf("serialized order changed:\n%s\n%s", left, right)
	}
}

func TestObserveFruitInventoryIsReadOnlyAndNotAdmissionControl(t *testing.T) {
	calls := 0
	svc := NewService(nil).WithFruitInventoryObservationSource(FruitInventoryObservationSource{Observe: func(context.Context) ([]FruitInventoryEvidence, error) {
		calls++
		return []FruitInventoryEvidence{{
			Claim:               FruitClaim{ProducerKind: "sprout", ProducerIdentity: "run", Repository: "r", Branch: "b", Commit: "c", PublicationState: "local-only"},
			RepositoryAvailable: true,
			LocalCommit:         FruitLocalCommitEvidence{Available: true, Exact: true},
		}}, nil
	}})
	if _, err := svc.ObserveFruitInventory(context.Background()); err != nil {
		t.Fatalf("ObserveFruitInventory: %v", err)
	}
	if calls != 1 {
		t.Fatalf("evidence source calls = %d, want one read", calls)
	}
}

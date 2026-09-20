package main

import (
	"context"
	"errors"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
)

// fruitInventorySource is the package-main evidence adapter. It reads the
// existing HistoryDB projection, joins only the persisted repository identity,
// and returns factual evidence to Core. Review-state and ordering decisions
// stay in Core.
func fruitInventorySource(history *historydb.Store) core.FruitInventoryObservationSource {
	if history == nil {
		return core.FruitInventoryObservationSource{}
	}
	return core.FruitInventoryObservationSource{
		Observe: func(ctx context.Context) ([]core.FruitInventoryEvidence, error) {
			claims, err := history.ListFruitClaims(ctx)
			if err != nil {
				return nil, err
			}
			config, configErr := conductor.LoadSubstratesConfig("")
			if configErr != nil {
				config = nil
			}
			out := make([]core.FruitInventoryEvidence, 0, len(claims))
			for _, persisted := range claims {
				claim := core.FruitClaim{
					ProducerKind:     persisted.Kind,
					ProducerIdentity: persisted.ExecutionID,
					PhytomerID:       persisted.PhytomerID,
					Substrate:        persisted.Substrate,
					Repository:       persisted.Repository,
					Workspace:        persisted.Workspace,
					Branch:           persisted.Branch,
					Commit:           persisted.Commit,
					PublicationState: persisted.PublicationState,
					CreatedAt:        persisted.CreatedAt,
					StartedAt:        persisted.StartedAt,
					FinishedAt:       persisted.FinishedAt,
				}
				out = append(out, observeFruitClaimEvidence(ctx, claim, config, defaultFruitInventoryEvidenceOps()))
			}
			return out, nil
		},
	}
}

type fruitInventoryEvidenceOps struct {
	ResolveWorkspace  func(substrate string, spec *conductor.SubstrateSpec) (string, error)
	ResolveIdentity   func(ctx context.Context, workspace string) (string, error)
	ResolveCredential func(spec conductor.SubstrateSpec, config *conductor.SubstratesConfig) (conductor.ResolvedCredential, error)
	ObserveReview     func(ctx context.Context, repository, branch, commit string, credential conductor.ResolvedCredential) (conductor.FruitReviewObservation, error)
	ObserveRemoteRef  func(ctx context.Context, repository, branch string, credential conductor.ResolvedCredential) (conductor.FruitRemoteRefObservation, error)
	ObserveLocal      func(ctx context.Context, workspace, commit string) (bool, error)
}

func defaultFruitInventoryEvidenceOps() fruitInventoryEvidenceOps {
	return fruitInventoryEvidenceOps{
		ResolveWorkspace:  conductor.ResolveSubstrateWorkspace,
		ResolveIdentity:   conductor.ResolveRepositoryIdentity,
		ResolveCredential: conductor.ResolveSubstrateCredential,
		ObserveReview:     conductor.ObserveFruitReview,
		ObserveRemoteRef:  conductor.ObserveFruitRemoteRef,
		ObserveLocal:      conductor.ObserveFruitLocalCommit,
	}
}

func observeFruitClaimEvidence(ctx context.Context, claim core.FruitClaim, config *conductor.SubstratesConfig, ops fruitInventoryEvidenceOps) core.FruitInventoryEvidence {
	evidence := core.FruitInventoryEvidence{Claim: claim}
	publicationState := strings.TrimSpace(claim.PublicationState)
	if publicationState == conductor.FruitPublicationLocalOnly || publicationState == conductor.FruitPublicationFailed {
		available, err := ops.ObserveLocal(ctx, claim.Workspace, claim.Commit)
		if err != nil {
			return evidence
		}
		evidence.RepositoryAvailable = true
		evidence.LocalCommit = core.FruitLocalCommitEvidence{Available: true, Exact: available}
		// Local Fruit does not require a forge query to remain outstanding.
		evidence.Forge.Available = true
		return evidence
	}

	spec, _ := conductor.ResolveSubstrate(claim.Substrate, config)
	if spec == nil {
		return evidence
	}
	workspace := strings.TrimSpace(claim.Workspace)
	currentRepository := conductor.NormalizeFruitRepository(spec.URL)
	if currentRepository == "" {
		resolvedWorkspace, err := ops.ResolveWorkspace(claim.Substrate, spec)
		if err != nil {
			return evidence
		}
		workspace = resolvedWorkspace
		resolvedRepository, err := ops.ResolveIdentity(ctx, workspace)
		if err != nil {
			return evidence
		}
		currentRepository = conductor.NormalizeFruitRepository(resolvedRepository)
	}
	// The persisted identity is authoritative. The current Substrate alias is
	// allowed to contribute credentials only after this exact repository join.
	if !conductor.FruitRepositoriesMatch(claim.Repository, currentRepository) {
		return evidence
	}
	evidence.RepositoryAvailable = true

	credential, err := ops.ResolveCredential(*spec, config)
	if err != nil {
		return evidence
	}
	review, err := ops.ObserveReview(ctx, claim.Repository, claim.Branch, claim.Commit, credential)
	if err == nil || len(review.Records) > 0 {
		evidence.Forge.Available = true
		evidence.Forge.Incomplete = err != nil
		evidence.Forge.Ambiguous = review.Ambiguous
		for _, record := range review.Records {
			evidence.Forge.Records = append(evidence.Forge.Records, core.FruitReviewRecord{
				Number:       record.Number,
				State:        record.State,
				Merged:       record.Merged,
				HeadCommit:   record.HeadCommit,
				CommitLookup: record.CommitLookup,
			})
		}
	}
	remoteRef, err := ops.ObserveRemoteRef(ctx, claim.Repository, claim.Branch, credential)
	if err != nil {
		if errors.Is(err, conductor.ErrFruitRepositoryUnavailable) {
			evidence.RepositoryAvailable = false
		}
		return evidence
	}
	evidence.RemoteRef = core.FruitRemoteRefEvidence{
		Available: true,
		Exists:    remoteRef.Exists,
		Commit:    remoteRef.Commit,
	}
	return evidence
}

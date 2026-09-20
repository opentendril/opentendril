package core

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// Fruit review states are deliberately closed. Inventory observation does not
// decide whether Fruit should be kept, merged, or deleted.
const (
	FruitReviewOutstanding    = "outstanding"
	FruitReviewMerged         = "merged"
	FruitReviewClosedUnmerged = "closed-unmerged"
	FruitReviewUnknown        = "unknown"
)

// Fruit unknown reasons are deliberately closed and transport-neutral.
const (
	FruitUnknownForgeUnavailable          = "forge-unavailable"
	FruitUnknownRepositoryUnavailable     = "repository-unavailable"
	FruitUnknownRemoteRefMissing          = "remote-ref-missing"
	FruitUnknownReviewEvidenceUnavailable = "review-evidence-unavailable"
	FruitUnknownReviewEvidenceAmbiguous   = "review-evidence-ambiguous"
)

var (
	ErrFruitInventoryNotWired = errors.New("Fruit inventory observation is not wired")
)

// FruitClaim is the structural identity persisted when execution creates
// reviewable Fruit. Workspace is private evidence and is never copied into the
// Botanist-facing inventory item.
type FruitClaim struct {
	ProducerKind     string
	ProducerIdentity string
	PhytomerID       string
	Substrate        string
	Repository       string
	Workspace        string
	Branch           string
	Commit           string
	PublicationState string
	CreatedAt        time.Time
	StartedAt        time.Time
	FinishedAt       time.Time
}

// FruitReviewRecord is factual forge evidence. CommitLookup means the forge
// answered a query addressed to the exact Fruit commit; otherwise HeadCommit
// must match the persisted Fruit commit before the record has authority.
type FruitReviewRecord struct {
	Number       int
	State        string
	Merged       bool
	HeadCommit   string
	CommitLookup bool
}

// FruitForgeEvidence contains only facts obtained from the forge. Available
// means the exact review read completed; an empty record set is a successful
// no-review answer, not an inferred review state. Incomplete means exact
// commit facts were retained but secondary head evidence was unavailable.
type FruitForgeEvidence struct {
	Available  bool
	Incomplete bool
	Ambiguous  bool
	Records    []FruitReviewRecord
}

// FruitRemoteRefEvidence records the exact remote branch observation. Exists
// distinguishes a missing ref from an unavailable remote read.
type FruitRemoteRefEvidence struct {
	Available bool
	Exists    bool
	Commit    string
}

// FruitLocalCommitEvidence records whether the exact persisted commit remains
// available in the persisted private workspace locator.
type FruitLocalCommitEvidence struct {
	Available bool
	Exact     bool
}

// FruitInventoryEvidence is the adapter-to-Core evidence port. It contains no
// review-state or unknown-reason decisions.
type FruitInventoryEvidence struct {
	Claim               FruitClaim
	RepositoryAvailable bool
	Forge               FruitForgeEvidence
	RemoteRef           FruitRemoteRefEvidence
	LocalCommit         FruitLocalCommitEvidence
}

// FruitInventoryObservationSource supplies persisted claims and factual Git /
// forge evidence. The source never classifies Fruit.
type FruitInventoryObservationSource struct {
	Observe func(ctx context.Context) ([]FruitInventoryEvidence, error)
}

// FruitInventoryItem is the safe Botanist-facing item projection.
type FruitInventoryItem struct {
	ProducerKind     string    `json:"producerKind"`
	ProducerIdentity string    `json:"producerIdentity"`
	PhytomerID       string    `json:"phytomerId,omitempty"`
	Substrate        string    `json:"substrate,omitempty"`
	Repository       string    `json:"repository"`
	Branch           string    `json:"branch"`
	Commit           string    `json:"commit"`
	PublicationState string    `json:"publicationState,omitempty"`
	CreatedAt        time.Time `json:"createdAt,omitempty"`
	StartedAt        time.Time `json:"startedAt,omitempty"`
	FinishedAt       time.Time `json:"finishedAt,omitempty"`
	ReviewState      string    `json:"reviewState"`
	UnknownReason    string    `json:"unknownReason,omitempty"`
	PullRequest      int       `json:"pullRequest,omitempty"`
}

// FruitReviewPressure contains counts derived solely from the returned item
// classifications. Outstanding is the review-pressure count.
type FruitReviewPressure struct {
	Outstanding    int `json:"outstanding"`
	Unknown        int `json:"unknown"`
	ClosedUnmerged int `json:"closedUnmerged"`
	Merged         int `json:"merged"`
	Total          int `json:"total"`
}

// FruitInventory is the deterministic, transport-free Botanist observation.
type FruitInventory struct {
	Items  []FruitInventoryItem `json:"items"`
	Counts FruitReviewPressure  `json:"counts"`
}

// WithFruitInventoryObservationSource wires the read-only evidence port.
func (s *Service) WithFruitInventoryObservationSource(src FruitInventoryObservationSource) *Service {
	s.fruitInventory = src
	return s
}

// ObserveFruitInventory composes a deterministic Botanist observation. It is
// a view, not a governed capability, and it never mutates Git or execution
// state.
func (s *Service) ObserveFruitInventory(ctx context.Context) (FruitInventory, error) {
	if s == nil || s.fruitInventory.Observe == nil {
		return FruitInventory{}, ErrFruitInventoryNotWired
	}
	if ctx == nil {
		ctx = context.Background()
	}
	evidence, err := s.fruitInventory.Observe(ctx)
	if err != nil {
		return FruitInventory{}, err
	}
	items := make([]FruitInventoryItem, 0, len(evidence))
	for _, current := range evidence {
		items = append(items, classifyFruit(current))
	}
	sort.SliceStable(items, func(i, j int) bool {
		return fruitInventoryLess(items[i], items[j])
	})

	result := FruitInventory{Items: items}
	if result.Items == nil {
		result.Items = []FruitInventoryItem{}
	}
	for _, item := range result.Items {
		result.Counts.Total++
		switch item.ReviewState {
		case FruitReviewOutstanding:
			result.Counts.Outstanding++
		case FruitReviewUnknown:
			result.Counts.Unknown++
		case FruitReviewClosedUnmerged:
			result.Counts.ClosedUnmerged++
		case FruitReviewMerged:
			result.Counts.Merged++
		}
	}
	return result, nil
}

func classifyFruit(evidence FruitInventoryEvidence) FruitInventoryItem {
	claim := evidence.Claim
	item := FruitInventoryItem{
		ProducerKind:     strings.TrimSpace(claim.ProducerKind),
		ProducerIdentity: strings.TrimSpace(claim.ProducerIdentity),
		PhytomerID:       strings.TrimSpace(claim.PhytomerID),
		Substrate:        strings.TrimSpace(claim.Substrate),
		Repository:       strings.TrimSpace(claim.Repository),
		Branch:           strings.TrimSpace(claim.Branch),
		Commit:           strings.TrimSpace(claim.Commit),
		PublicationState: strings.TrimSpace(claim.PublicationState),
		CreatedAt:        claim.CreatedAt,
		StartedAt:        claim.StartedAt,
		FinishedAt:       claim.FinishedAt,
		ReviewState:      FruitReviewUnknown,
	}

	exactReviews := exactFruitReviews(claim.Commit, evidence.Forge.Records)
	if evidence.Forge.Available && evidence.Forge.Ambiguous {
		return unknownFruit(item, FruitUnknownReviewEvidenceAmbiguous)
	}
	if evidence.Forge.Available && len(exactReviews) > 0 {
		if state, pullRequest, ok := oneConsistentReviewState(exactReviews); ok {
			item.ReviewState = state
			item.PullRequest = pullRequest
			return item
		}
		return unknownFruit(item, FruitUnknownReviewEvidenceAmbiguous)
	}
	if evidence.Forge.Available && evidence.Forge.Incomplete {
		return unknownFruit(item, FruitUnknownReviewEvidenceUnavailable)
	}

	localPublication := claim.PublicationState == "local-only" || claim.PublicationState == "publication-failed"
	if localPublication {
		if !evidence.RepositoryAvailable || !evidence.LocalCommit.Available {
			return unknownFruit(item, FruitUnknownRepositoryUnavailable)
		}
		if evidence.LocalCommit.Exact {
			item.ReviewState = FruitReviewOutstanding
			return item
		}
		return unknownFruit(item, FruitUnknownRepositoryUnavailable)
	}

	if !evidence.RepositoryAvailable {
		return unknownFruit(item, FruitUnknownRepositoryUnavailable)
	}
	if !evidence.Forge.Available {
		if evidence.RemoteRef.Available {
			return unknownFruit(item, FruitUnknownReviewEvidenceUnavailable)
		}
		return unknownFruit(item, FruitUnknownForgeUnavailable)
	}
	if !evidence.RemoteRef.Available {
		return unknownFruit(item, FruitUnknownForgeUnavailable)
	}
	if evidence.RemoteRef.Exists {
		if strings.EqualFold(strings.TrimSpace(evidence.RemoteRef.Commit), strings.TrimSpace(claim.Commit)) {
			item.ReviewState = FruitReviewOutstanding
			return item
		}
		return unknownFruit(item, FruitUnknownReviewEvidenceAmbiguous)
	}
	return unknownFruit(item, FruitUnknownRemoteRefMissing)
}

func exactFruitReviews(commit string, records []FruitReviewRecord) []FruitReviewRecord {
	commit = strings.TrimSpace(commit)
	exact := make([]FruitReviewRecord, 0, len(records))
	for _, record := range records {
		if fruitReviewRelatesToCommit(record, commit) {
			exact = append(exact, record)
		}
	}
	return exact
}

func fruitReviewRelatesToCommit(record FruitReviewRecord, commit string) bool {
	headMatches := strings.TrimSpace(record.HeadCommit) != "" && strings.EqualFold(strings.TrimSpace(record.HeadCommit), strings.TrimSpace(commit))
	if headMatches {
		return true
	}
	// Exact commit lookup is sufficient for merged and open evidence. A
	// closed-unmerged PR must still prove that its current head is this Fruit;
	// a historical commit association is not enough because the branch may have
	// been force-pushed or reused.
	if !record.CommitLookup {
		return false
	}
	if record.Merged || strings.EqualFold(strings.TrimSpace(record.State), "open") {
		return true
	}
	return false
}

func oneConsistentReviewState(records []FruitReviewRecord) (string, int, bool) {
	state := ""
	pullRequest := 0
	for _, record := range records {
		current := ""
		switch {
		case record.Merged:
			current = FruitReviewMerged
		case strings.EqualFold(strings.TrimSpace(record.State), "closed"):
			current = FruitReviewClosedUnmerged
		case strings.EqualFold(strings.TrimSpace(record.State), "open"):
			current = FruitReviewOutstanding
		default:
			return "", 0, false
		}
		if state == "" {
			state = current
			pullRequest = record.Number
			continue
		}
		if state != current {
			return "", 0, false
		}
		if record.Number != 0 && (pullRequest == 0 || record.Number < pullRequest) {
			pullRequest = record.Number
		}
	}
	return state, pullRequest, state != ""
}

func unknownFruit(item FruitInventoryItem, reason string) FruitInventoryItem {
	item.ReviewState = FruitReviewUnknown
	item.UnknownReason = reason
	item.PullRequest = 0
	return item
}

func fruitReviewRank(state string) int {
	switch state {
	case FruitReviewOutstanding:
		return 0
	case FruitReviewUnknown:
		return 1
	case FruitReviewClosedUnmerged:
		return 2
	case FruitReviewMerged:
		return 3
	default:
		return 4
	}
}

func fruitInventoryLess(left, right FruitInventoryItem) bool {
	if fruitReviewRank(left.ReviewState) != fruitReviewRank(right.ReviewState) {
		return fruitReviewRank(left.ReviewState) < fruitReviewRank(right.ReviewState)
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	if left.ProducerKind != right.ProducerKind {
		return left.ProducerKind < right.ProducerKind
	}
	if left.ProducerIdentity != right.ProducerIdentity {
		return left.ProducerIdentity < right.ProducerIdentity
	}
	if left.Branch != right.Branch {
		return left.Branch < right.Branch
	}
	if left.Commit != right.Commit {
		return left.Commit < right.Commit
	}
	if left.Repository != right.Repository {
		return left.Repository < right.Repository
	}
	if left.PhytomerID != right.PhytomerID {
		return left.PhytomerID < right.PhytomerID
	}
	if left.PublicationState != right.PublicationState {
		return left.PublicationState < right.PublicationState
	}
	if left.UnknownReason != right.UnknownReason {
		return left.UnknownReason < right.UnknownReason
	}
	return left.PullRequest < right.PullRequest
}

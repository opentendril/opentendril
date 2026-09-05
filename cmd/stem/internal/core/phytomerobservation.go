package core

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// ErrPhytomerObservationNotWired is returned when the current-state
// observation source has not been injected.
var ErrPhytomerObservationNotWired = errors.New("phytomer observation is not wired")

// ErrPhytomerObservationNotFound is returned when no Seed growth is
// associated with the named Phytomer.
var ErrPhytomerObservationNotFound = errors.New("no seed growth is associated with this phytomer")

// ErrPhytomerObservationOwnershipConflict is returned when Sprout or
// continuation evidence disagrees with the Seed's Pollen or Substrate. It is a
// transport-free fail-closed safety invariant, not a sprout.watch grant
// decision.
var ErrPhytomerObservationOwnershipConflict = errors.New("phytomer observation ownership evidence disagrees")

// ErrPhytomerObservationContinuationInvalid is returned when continuation
// observation evidence is missing identity or uses an unknown delivery state.
// It is a transport-free fail-closed safety invariant and does not carry raw
// continued intent.
var ErrPhytomerObservationContinuationInvalid = errors.New("phytomer continuation observation evidence is invalid")

// PhytomerObservation is the transport-free current-state projection of one
// Seed-owned Phytomer. A sprout.watch observer may see these facts when they
// actually exist: identities, Seed lifecycle, iteration progress, actual
// Sprout lifecycle, safe continuation lifecycle summaries, and real Fruit
// identity. Absent facts stay absent; this type does not invent a commit, a
// Sprout, provider activity, or a zeroed success. Raw Seed error text,
// transcript, output, continued intent, and private reasoning are not part of
// this contract.
type PhytomerObservation struct {
	Pollen                  string                       `json:"pollen,omitempty"`
	Substrate               string                       `json:"substrate,omitempty"`
	Handle                  string                       `json:"handle,omitempty"`
	PhytomerID              string                       `json:"phytomerId,omitempty"`
	Status                  string                       `json:"status,omitempty"`
	Iterations              int                          `json:"iterations"`
	Branch                  string                       `json:"branch,omitempty"`
	Commit                  string                       `json:"commit,omitempty"`
	PublicationDiagnostic   *SeedPublicationDiagnostic   `json:"publicationDiagnostic,omitempty"`
	VerificationDiagnostics []SeedVerificationDiagnostic `json:"verificationDiagnostics,omitempty"`
	Sprouts                 []SproutObservation          `json:"sprouts,omitempty"`
	Continuations           []ContinuationObservation    `json:"continuations,omitempty"`
}

// ContinuationObservation is the safe public lifecycle summary of one accepted
// continuation. It carries identity, durable order, and delivery state only.
// Intent, intent digest, idempotency key, failure text, credentials, and
// reasoning are not part of this contract. Pollen and Substrate are not
// echoed here; they already exist on the owning Phytomer observation.
type ContinuationObservation struct {
	ContinuationID string `json:"continuationId"`
	Sequence       int    `json:"sequence"`
	DeliveryState  string `json:"deliveryState"`
}

// SproutObservation is the safe lifecycle envelope of one actual Sprout
// attributed to the Seed's Phytomer. Transcript, output, raw errors, and
// private reasoning are not part of this contract.
type SproutObservation struct {
	RunID                    string              `json:"runId,omitempty"`
	Status                   string              `json:"status,omitempty"`
	Provider                 string              `json:"provider,omitempty"`
	Model                    string              `json:"model,omitempty"`
	Outcome                  string              `json:"outcome,omitempty"`
	FailureCategory          string              `json:"failureCategory,omitempty"`
	ProviderDiagnostic       *ProviderDiagnostic `json:"providerDiagnostic,omitempty"`
	ProviderRequestAttempted bool                `json:"providerRequestAttempted"`
	ToolInvocations          int                 `json:"toolInvocations"`
}

// SeedObservationEvidence is durable Seed state the current-state projection
// may consult. It includes persisted fields that are not part of the public
// observation (goal, diff, logs, raw error). It is not a transport type.
type SeedObservationEvidence struct {
	Handle                  string
	Pollen                  string
	PhytomerID              string
	Substrate               string
	Status                  string
	Iterations              int
	Branch                  string
	Commit                  string
	Goal                    string
	Diff                    string
	Logs                    string
	Error                   string
	PublicationDiagnostic   *SeedPublicationDiagnostic
	VerificationDiagnostics []SeedVerificationDiagnostic
}

// SproutObservationEvidence is durable Sprout state the current-state
// projection may consult. It includes persisted fields that are not part of
// the public observation (transcript, output, raw error). Pollen and
// Substrate are compared against the Seed before any Sprout is released.
// StartedAt is used only to order the public Sprout list.
type SproutObservationEvidence struct {
	RunID                    string
	Pollen                   string
	Substrate                string
	Status                   string
	Provider                 string
	Model                    string
	Outcome                  string
	FailureCategory          string
	ProviderDiagnostic       *ProviderDiagnostic
	ProviderRequestAttempted bool
	ToolInvocations          int
	Transcript               string
	Output                   string
	Error                    string
	StartedAt                time.Time
}

// ContinuationObservationEvidence is durable continuation state the
// current-state projection may consult. Pollen and Substrate are compared
// against the Seed before any continuation summary is released. Intent,
// intent digest, and idempotency key are not part of this evidence.
type ContinuationObservationEvidence struct {
	ContinuationID string
	Pollen         string
	Substrate      string
	Sequence       int
	DeliveryState  string
}

// PhytomerObservationSource is the injected durable-evidence port for
// current-state observation. Core composes the safe PhytomerObservation; the
// port only supplies persisted evidence. Core never imports historydb.
type PhytomerObservationSource struct {
	SeedByPhytomer          func(ctx context.Context, phytomerID string) (SeedObservationEvidence, bool, error)
	SproutsByPhytomer       func(ctx context.Context, phytomerID string) ([]SproutObservationEvidence, error)
	ContinuationsByPhytomer func(ctx context.Context, phytomerID string) ([]ContinuationObservationEvidence, error)
}

// WithPhytomerObservationSource wires the durable current-state evidence port.
func (s *Service) WithPhytomerObservationSource(src PhytomerObservationSource) *Service {
	s.observation = src
	return s
}

// ObservePhytomer returns the safe current-state observation of one Seed-owned
// Phytomer. It is a view, not a governed command. Which persisted fields may
// be released is decided here, not by a transport adapter. Sprout or
// continuation rows that disagree with the Seed's Pollen or Substrate fail
// closed.
func (s *Service) ObservePhytomer(ctx context.Context, phytomerID string) (PhytomerObservation, error) {
	if s == nil || s.observation.SeedByPhytomer == nil || s.observation.SproutsByPhytomer == nil || s.observation.ContinuationsByPhytomer == nil {
		return PhytomerObservation{}, ErrPhytomerObservationNotWired
	}
	seed, found, err := s.observation.SeedByPhytomer(ctx, phytomerID)
	if err != nil {
		return PhytomerObservation{}, err
	}
	if !found {
		return PhytomerObservation{}, ErrPhytomerObservationNotFound
	}
	sprouts, err := s.observation.SproutsByPhytomer(ctx, phytomerID)
	if err != nil {
		return PhytomerObservation{}, err
	}
	continuations, err := s.observation.ContinuationsByPhytomer(ctx, phytomerID)
	if err != nil {
		return PhytomerObservation{}, err
	}
	return ProjectPhytomerObservation(seed, sprouts, continuations)
}

// ProjectPhytomerObservation composes the safe current-state view from Seed
// evidence, the Sprouts actually recorded against that Phytomer, and
// continuation lifecycle evidence. It copies only the observation contract;
// it does not derive a commit from a branch, synthesize a Sprout, rewrite
// missing provider activity as zero/success, or release raw Seed error,
// transcript, output, diff, logs, goal text, continued intent, intent digest,
// or idempotency key.
//
// Every Sprout and continuation row must carry the Seed's Pollen and
// Substrate. Unknown or empty continuation delivery state fails closed.
// Any disagreement releases no observation.
func ProjectPhytomerObservation(seed SeedObservationEvidence, sprouts []SproutObservationEvidence, continuations []ContinuationObservationEvidence) (PhytomerObservation, error) {
	if err := phytomerObservationOwnershipAgrees(seed, sprouts, continuations); err != nil {
		return PhytomerObservation{}, err
	}
	obs := PhytomerObservation{
		Pollen:     strings.TrimSpace(seed.Pollen),
		Substrate:  strings.TrimSpace(seed.Substrate),
		Handle:     strings.TrimSpace(seed.Handle),
		PhytomerID: strings.TrimSpace(seed.PhytomerID),
		Status:     strings.TrimSpace(seed.Status),
		Iterations: seed.Iterations,
		Branch:     strings.TrimSpace(seed.Branch),
		Commit:     strings.TrimSpace(seed.Commit),
	}
	if seed.PublicationDiagnostic != nil {
		copied := *seed.PublicationDiagnostic
		obs.PublicationDiagnostic = &copied
	}
	obs.VerificationDiagnostics = CopySeedVerificationDiagnostics(seed.VerificationDiagnostics)
	if len(sprouts) > 0 {
		ordered := append([]SproutObservationEvidence(nil), sprouts...)
		sort.Slice(ordered, func(i, j int) bool {
			if !ordered[i].StartedAt.Equal(ordered[j].StartedAt) {
				return ordered[i].StartedAt.Before(ordered[j].StartedAt)
			}
			return ordered[i].RunID < ordered[j].RunID
		})
		out := make([]SproutObservation, 0, len(ordered))
		for _, run := range ordered {
			sprout := SproutObservation{
				RunID:                    strings.TrimSpace(run.RunID),
				Status:                   strings.TrimSpace(run.Status),
				Provider:                 strings.TrimSpace(run.Provider),
				Model:                    strings.TrimSpace(run.Model),
				Outcome:                  strings.TrimSpace(run.Outcome),
				FailureCategory:          strings.TrimSpace(run.FailureCategory),
				ProviderRequestAttempted: run.ProviderRequestAttempted,
				ToolInvocations:          run.ToolInvocations,
			}
			if run.ProviderDiagnostic != nil {
				copied := *run.ProviderDiagnostic
				sprout.ProviderDiagnostic = &copied
			}
			out = append(out, sprout)
		}
		obs.Sprouts = out
	}
	if len(continuations) > 0 {
		ordered := append([]ContinuationObservationEvidence(nil), continuations...)
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].Sequence != ordered[j].Sequence {
				return ordered[i].Sequence < ordered[j].Sequence
			}
			return ordered[i].ContinuationID < ordered[j].ContinuationID
		})
		out := make([]ContinuationObservation, 0, len(ordered))
		for _, rec := range ordered {
			out = append(out, ContinuationObservation{
				ContinuationID: strings.TrimSpace(rec.ContinuationID),
				Sequence:       rec.Sequence,
				DeliveryState:  strings.TrimSpace(rec.DeliveryState),
			})
		}
		obs.Continuations = out
	}
	return obs, nil
}

func phytomerObservationOwnershipAgrees(seed SeedObservationEvidence, sprouts []SproutObservationEvidence, continuations []ContinuationObservationEvidence) error {
	pollen := strings.TrimSpace(seed.Pollen)
	substrate := strings.TrimSpace(seed.Substrate)
	for _, sprout := range sprouts {
		if strings.TrimSpace(sprout.Pollen) != pollen || strings.TrimSpace(sprout.Substrate) != substrate {
			return ErrPhytomerObservationOwnershipConflict
		}
	}
	for _, rec := range continuations {
		if strings.TrimSpace(rec.ContinuationID) == "" || !knownContinuationDeliveryState(rec.DeliveryState) {
			return ErrPhytomerObservationContinuationInvalid
		}
		if strings.TrimSpace(rec.Pollen) != pollen || strings.TrimSpace(rec.Substrate) != substrate {
			return ErrPhytomerObservationOwnershipConflict
		}
	}
	return nil
}

func knownContinuationDeliveryState(state string) bool {
	switch strings.TrimSpace(state) {
	case ContinuationDeliveryPending, ContinuationDeliveryDelivering, ContinuationDeliveryDelivered, ContinuationDeliveryFailed:
		return true
	default:
		return false
	}
}

// SeedStatusIsTerminal reports whether a Seed status is a terminal growth
// state. Unknown or empty status is not terminal.
func SeedStatusIsTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case SeedStatusSatisfied, SeedStatusExhausted, SeedStatusWithered, SeedStatusFruitPublicationFailed:
		return true
	default:
		return false
	}
}

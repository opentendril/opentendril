package core

import "strings"

// PhytomerObservation is the transport-free current-state projection of one
// Seed-owned Phytomer. A sprout.watch observer may see these facts when they
// actually exist: identities, Seed lifecycle, iteration progress, actual
// Sprout lifecycle, and real Fruit identity. Absent facts stay absent; this
// type does not invent a commit, a Sprout, provider activity, or a zeroed
// success.
type PhytomerObservation struct {
	Pollen     string              `json:"pollen,omitempty"`
	Substrate  string              `json:"substrate,omitempty"`
	Handle     string              `json:"handle,omitempty"`
	PhytomerID string              `json:"phytomerId,omitempty"`
	Status     string              `json:"status,omitempty"`
	Iterations int                 `json:"iterations"`
	Branch     string              `json:"branch,omitempty"`
	Commit     string              `json:"commit,omitempty"`
	Error      string              `json:"error,omitempty"`
	Sprouts    []SproutObservation `json:"sprouts,omitempty"`
}

// SproutObservation is the safe lifecycle envelope of one actual Sprout
// attributed to the Seed's Phytomer. Transcript, output, and private
// reasoning are not part of this contract.
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

// SeedObservation is the durable Seed evidence the current-state projection
// may consult. It is not a transport type.
type SeedObservation struct {
	Handle     string
	Pollen     string
	PhytomerID string
	Substrate  string
	Status     string
	Iterations int
	Branch     string
	Commit     string
	Error      string
}

// ProjectPhytomerObservation composes the safe current-state view from Seed
// evidence and the Sprouts actually recorded against that Phytomer. It copies
// only the observation contract; it does not derive a commit from a branch,
// synthesize a Sprout, or rewrite missing provider activity as zero/success.
func ProjectPhytomerObservation(seed SeedObservation, sprouts []SproutObservation) PhytomerObservation {
	obs := PhytomerObservation{
		Pollen:     strings.TrimSpace(seed.Pollen),
		Substrate:  strings.TrimSpace(seed.Substrate),
		Handle:     strings.TrimSpace(seed.Handle),
		PhytomerID: strings.TrimSpace(seed.PhytomerID),
		Status:     strings.TrimSpace(seed.Status),
		Iterations: seed.Iterations,
		Branch:     strings.TrimSpace(seed.Branch),
		Commit:     strings.TrimSpace(seed.Commit),
		Error:      strings.TrimSpace(seed.Error),
	}
	if len(sprouts) > 0 {
		obs.Sprouts = append([]SproutObservation(nil), sprouts...)
	}
	return obs
}

// SeedStatusIsTerminal reports whether a Seed status is a terminal growth
// state. Unknown or empty status is not terminal.
func SeedStatusIsTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case SeedStatusSatisfied, SeedStatusExhausted, SeedStatusWithered:
		return true
	default:
		return false
	}
}

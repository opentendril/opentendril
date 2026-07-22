package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The sprout/run capability family. sprout.grow delegates a one-shot task to an
// autonomous Tendril inside a network-isolated terrarium.
//
// Terrarium orchestration lives outside the Core, in packages it is structurally
// forbidden from importing (see boundary_test.go), so it is injected as a
// transport-free port via WithSprout.
//
// Invoke is synchronous: it answers when the Tendril matures or withers. The /ws
// event stream and the sprout-runs history endpoints are the views for watching
// a run.
//
// Here: input validation, step-id minting, and binding the run to a session so
// its preferences shape the sprout. In the port: substrate resolution, the
// terrarium, and history recording.

// SproutRunInput asks the Stem to sprout an autonomous Tendril for one task.
type SproutRunInput struct {
	// Transcript is the task description the Tendril executes.
	Transcript string `json:"transcript"`
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// StepID optionally pins a stable step identifier; one is minted when
	// empty.
	StepID string `json:"stepId,omitempty"`
	// SessionID optionally binds the run to an existing Tendril session. When
	// empty, a fresh session is initiated (adapters may fill in their own
	// default binding first, e.g. the MCP stdio server's pinned session).
	SessionID string `json:"sessionId,omitempty"`
	// SubstrateURL optionally overrides the remote repository URL to clone.
	SubstrateURL string `json:"substrateUrl,omitempty"`
	// SubstrateBranch optionally names the branch to clone with SubstrateURL.
	SubstrateBranch string `json:"substrateBranch,omitempty"`
	// Origin records which surface sprouted the run (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
}

// SproutSpec is the fully resolved, transport-free execution request handed
// to the SproutOperations port after the Core has applied session preferences.
type SproutSpec struct {
	StepID          string
	Transcript      string
	Substrate       string
	SubstrateURL    string
	SubstrateBranch string
	SessionID       string
	Origin          string
	Provider        string
	Model           string
	Genotype        string
}

// SproutRunResult is the outcome of a finished sprout run.
type SproutRunResult struct {
	StepID    string `json:"stepId"`
	SessionID string `json:"sessionId,omitempty"`
	// Status is the run lifecycle verdict every surface has always seen:
	// matured (the run finished) or withered (it errored). Outcome refines it.
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
	// Outcome is the execution port's honest verdict on the work itself:
	// complete, no-changes, skipped, failed, or timed-out. A matured run that
	// changed nothing reports no-changes here rather than being dressed as
	// plain completion.
	Outcome string `json:"outcome,omitempty"`
	// FilesModified is the evidence behind Outcome, when the workspace could
	// measure it.
	FilesModified []string `json:"filesModified,omitempty"`
}

// SproutRunReport is what the execution port says a finished run actually
// did. It mirrors the conductor's report without importing it (the Core is
// structurally forbidden from importing the conductor — see boundary_test.go).
type SproutRunReport struct {
	Output        string
	Outcome       string
	FilesModified []string
}

// SproutOperations is the injection port for sprout execution. Run may be nil, in
// which case the capability reports that it is not wired rather than acting.
type SproutOperations struct {
	// Run executes the spec inside a terrarium and reports what the run
	// actually did. Implementations own substrate resolution and run recording.
	Run func(ctx context.Context, spec SproutSpec) (SproutRunReport, error)
}

// WithSprout wires the sprout execution port onto the Service and returns the
// Service for chaining.
func (s *Service) WithSprout(operations SproutOperations) *Service {
	s.sprout = operations
	return s
}

// SproutRun validates the request, binds it to a Tendril session (applying
// the session's provider/model/genotype preferences to the sprout), and runs
// it to completion via the injected execution port.
func (s *Service) SproutRun(ctx context.Context, in SproutRunInput) (SproutRunResult, error) {
	if s.sprout.Run == nil {
		return SproutRunResult{}, fmt.Errorf("sprout.grow is not wired: construct the Core with WithSprout(SproutOperations{Run: …})")
	}
	if strings.TrimSpace(in.Transcript) == "" || strings.TrimSpace(in.Substrate) == "" {
		return SproutRunResult{}, fmt.Errorf("transcript and substrate are required")
	}

	spec := SproutSpec{
		StepID:          strings.TrimSpace(in.StepID),
		Transcript:      in.Transcript,
		Substrate:       strings.TrimSpace(in.Substrate),
		SubstrateURL:    strings.TrimSpace(in.SubstrateURL),
		SubstrateBranch: strings.TrimSpace(in.SubstrateBranch),
		Origin:          in.Origin,
	}
	if spec.StepID == "" {
		spec.StepID = fmt.Sprintf("step-%d", time.Now().UTC().UnixNano())
	}

	// Session binding shapes the sprout via the session's preferences. A
	// resolution failure degrades to a sessionless run — the historic
	// behavior of every surface — rather than refusing to execute.
	if s.sessions != nil {
		if sess, err := s.sessions.GetOrInitiate(ctx, strings.TrimSpace(in.SessionID), in.Origin); err == nil {
			spec.SessionID = sess.ID
			spec.Provider = sess.Preferences.Provider
			spec.Model = sess.Preferences.Model
			spec.Genotype = sess.Preferences.Genotype
			s.sessions.Touch(ctx, sess.ID)
		}
	}

	result := SproutRunResult{StepID: spec.StepID, SessionID: spec.SessionID}
	report, err := s.sprout.Run(ctx, spec)
	result.Outcome = report.Outcome
	result.FilesModified = report.FilesModified
	if err != nil {
		result.Status = "withered"
		return result, err
	}
	result.Status = "matured"
	result.Output = report.Output
	return result, nil
}

// sproutCapabilities declares the sprout family's registry entry, bound to
// this Service's typed method — identical in shape to the other families.
func (s *Service) sproutCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapSproutGrow,
			Description: "Delegate a one-shot task to an autonomous Tendril inside a secure terrarium and wait for the result.",
			InputSchema: schemaObject(map[string]any{
				"transcript":      stringProp("A clear, actionable description of the task for the Tendril to execute."),
				"substrate":       stringProp("The absolute path or named substrate key for the target repository workspace."),
				"stepId":          stringProp("Optional stable step identifier for a structured sequence run."),
				"sessionId":       stringProp("Optional Tendril session id binding this run to a unified chat session (its preferences, models, and history)."),
				"substrateUrl":    stringProp("Optional remote repository URL override to clone and operate on dynamically."),
				"substrateBranch": stringProp("Optional branch name to clone if substrateUrl is provided."),
				"origin":          stringProp("Interaction origin recorded on the run (cli, mcp, rest)."),
			}, []string{"transcript", "substrate"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in SproutRunInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.SproutRun(ctx, in)
			},
		},
	}
}

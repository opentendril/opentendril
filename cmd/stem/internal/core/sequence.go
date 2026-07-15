package core

import (
	"context"
	"fmt"
	"strings"
)

// The sequence capability family. Listing and running
// sequences are conductor-owned execution operations the Core is structurally
// forbidden from importing (see boundary_test.go), so both are injected as
// transport-free function ports via WithSequence — the same template as
// GenomeOps (PR).
//
// Execution I/O (streaming a run's output to a terminal, discarding it on a
// server) is a per-surface concern: each adapter binds the Run port with its
// own writers when it constructs its Core, so the capability itself stays
// free of any transport or terminal types.
//
// `tendril sequence dynamic` is CLI-local sugar: it synthesizes a one-step
// sequence file from a natural-language prompt and then invokes this same
// governed sequence.run capability on it. The synthesis is not a separate
// governed command.

// SequenceRunInput asks the Stem to run a YAML sequence.
type SequenceRunInput struct {
	// PathOrName is the sequence YAML file path, or a sequence name resolved
	// from .tendril/sequences/ and the system sequence directories.
	PathOrName string `json:"pathOrName"`
	// Provider optionally overrides the LLM provider for the run.
	Provider string `json:"provider,omitempty"`
	// Model optionally overrides the LLM model for the run.
	Model string `json:"model,omitempty"`
	// BaseURL optionally overrides the LLM base URL for the run.
	BaseURL string `json:"baseURL,omitempty"`
}

// SequenceStepOutcome is the final state of one step after a run.
type SequenceStepOutcome struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Transcript string `json:"transcript,omitempty"`
}

// SequenceRunResult summarizes a finished (or halted) sequence run.
type SequenceRunResult struct {
	Name      string                `json:"name"`
	Substrate string                `json:"substrate,omitempty"`
	Branch    string                `json:"branch,omitempty"`
	Steps     []SequenceStepOutcome `json:"steps"`
}

// SequenceOps is the injection port for sequence operations whose
// implementation lives outside the Core (the conductor's sequence engine).
// Root is the workspace root sequences are listed under (defaults to ".").
// Either func may be nil, in which case the corresponding capability reports
// that it is not wired rather than acting.
type SequenceOps struct {
	Root string
	// List returns the available sequence YAML files under root (and the
	// system sequence directories), sorted.
	List func(ctx context.Context, root string) ([]string, error)
	// Run executes a sequence to completion. On failure it may still return a
	// partially populated result describing the steps' final states alongside
	// the error.
	Run func(ctx context.Context, in SequenceRunInput) (SequenceRunResult, error)
}

// WithSequence wires the sequence operation port onto the Service and returns
// the Service for chaining.
func (s *Service) WithSequence(ops SequenceOps) *Service {
	s.sequence = ops
	return s
}

func (s *Service) sequenceRoot() string {
	root := strings.TrimSpace(s.sequence.Root)
	if root == "" {
		return "."
	}
	return root
}

// SequenceList returns the workspace's available sequence YAML files via the
// injected execution port.
func (s *Service) SequenceList(ctx context.Context) ([]string, error) {
	if s.sequence.List == nil {
		return nil, fmt.Errorf("sequence.list is not wired: construct the Core with WithSequence(SequenceOps{List: …})")
	}
	return s.sequence.List(ctx, s.sequenceRoot())
}

// SequenceRun executes a sequence to completion via the injected execution
// port. On failure the returned result may still describe the steps' final
// states, so adapters can render a summary alongside the error exactly as the
// legacy surfaces did.
func (s *Service) SequenceRun(ctx context.Context, in SequenceRunInput) (SequenceRunResult, error) {
	if s.sequence.Run == nil {
		return SequenceRunResult{}, fmt.Errorf("sequence.run is not wired: construct the Core with WithSequence(SequenceOps{Run: …})")
	}
	if strings.TrimSpace(in.PathOrName) == "" {
		return SequenceRunResult{}, fmt.Errorf("pathOrName is required")
	}
	return s.sequence.Run(ctx, in)
}

// sequenceCapabilities declares the sequence family's registry entries, bound
// to this Service's typed methods — identical in shape to the session and
// genome families.
func (s *Service) sequenceCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapSequenceList,
			Description: "List the available sequence YAML files from .tendril/sequences/ and the system sequence directories, sorted.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return s.SequenceList(ctx)
			},
		},
		{
			Name:        CapSequenceRun,
			Description: "Run a YAML sequence from .tendril/sequences/ or a relative path to completion using the parallel sequence meristem.",
			InputSchema: schemaObject(map[string]any{
				"pathOrName": stringProp("The sequence YAML file path or sequence name to run."),
				"provider":   stringProp("Optional LLM provider override for this run."),
				"model":      stringProp("Optional LLM model override for this run."),
				"baseURL":    stringProp("Optional LLM base URL override for this run."),
			}, []string{"pathOrName"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in SequenceRunInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				result, err := s.SequenceRun(ctx, in)
				if err != nil {
					return nil, err
				}
				return result, nil
			},
		},
	}
}

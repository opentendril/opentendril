package core

import (
	"context"
	"sort"
)

// Capability names — the canonical identifiers shared verbatim across the CLI,
// MCP, and REST surfaces. Parity is asserted on this set (see the parity tests
// under cmd/stem). Adding a name here without wiring every surface, or wiring a
// surface without a name here, fails CI.
const (
	CapCreatePhytomer  = "phytomer.create"
	CapListPhytomers   = "phytomer.list"
	CapGetPhytomer     = "phytomer.get"
	CapUpdatePhytomer  = "phytomer.update"
	CapDeletePhytomer  = "phytomer.delete"
	CapPhytomerHistory = "phytomer.history"

	CapGenomeView   = "genome.view"
	CapGenomeReduce = "genome.reduce"
	CapGenomeEvolve = "genome.evolve"

	CapPlasmidList     = "plasmid.list"
	CapPlasmidInject   = "plasmid.inject"
	CapMeshGraft       = "mesh.graft"
	CapMeshPromote     = "mesh.promote"
	CapMeshTraitList   = "mesh.trait.list"
	CapMeshTraitAccept = "mesh.trait.accept"
	CapMeshTraitReject = "mesh.trait.reject"
	CapSequenceList    = "sequence.list"
	CapSequenceGrow    = "sequence.grow"
	CapSproutGrow      = "sprout.grow"
	CapPassthroughRun  = "passthrough.run"
	CapGitCommit       = "git.commit"
)

// Capability is one declarative command capability. A single declaration is
// projected onto every surface: MCP reads Name/Description/InputSchema to build
// a tool, the CLI builds a subcommand, and all non-REST surfaces run it through
// Invoke. Invoke's signature carries zero transport types — the litmus test for
// the Core boundary.
type Capability struct {
	Name        string
	Description string
	// InputSchema is a JSON-Schema object describing Invoke's input map. It is
	// plain data (maps), transport-agnostic, and used to project MCP tool
	// definitions and CLI help.
	InputSchema map[string]any
	// Invoke runs the capability with a decoded input map and returns a
	// JSON-serializable result.
	Invoke func(ctx context.Context, input map[string]any) (any, error)
}

// CapabilityNames returns the canonical governed capability names, sorted. This
// is the single source of truth the parity tests compare every surface against.
func CapabilityNames() []string {
	names := []string{
		CapCreatePhytomer,
		CapListPhytomers,
		CapGetPhytomer,
		CapUpdatePhytomer,
		CapDeletePhytomer,
		CapPhytomerHistory,
		CapGenomeView,
		CapGenomeReduce,
		CapGenomeEvolve,
		CapPlasmidList,
		CapPlasmidInject,
		CapMeshGraft,
		CapMeshPromote,
		CapMeshTraitList,
		CapMeshTraitAccept,
		CapMeshTraitReject,
		CapSequenceList,
		CapSequenceGrow,
		CapSproutGrow,
		CapPassthroughRun,
		CapGitCommit,
	}
	sort.Strings(names)
	return names
}

// DelegatedCapabilityNames returns the canonical delegated operation-classes,
// sorted: the capabilities that execute work on behalf of an external agent
// and therefore must pass the delegation control plane (a grant covering
// {subject, operation-class, substrate}) before they run on an agent-facing
// surface. This list is the single source of truth for which capabilities are
// delegated; the surfaces that gate per-invocation consult it.
func DelegatedCapabilityNames() []string {
	names := []string{
		CapSproutGrow,
		CapPassthroughRun,
		CapGitCommit,
	}
	sort.Strings(names)
	return names
}

// IsDelegatedCapability reports whether the named capability is a delegated
// operation-class — one that must be authorized by a delegation grant before
// it is invoked on behalf of an external agent.
func IsDelegatedCapability(name string) bool {
	for _, delegated := range DelegatedCapabilityNames() {
		if delegated == name {
			return true
		}
	}
	return false
}

// Capabilities returns the live registry: one entry per canonical name, each
// bound to this Service's typed methods.
func (s *Service) Capabilities() []Capability {
	caps := []Capability{
		{
			Name:        CapCreatePhytomer,
			Description: "Create a new Phytomer with optional LLM/genotype preferences.",
			InputSchema: schemaObject(map[string]any{
				"origin":      stringProp("Interaction origin recorded on the Phytomer (cli, mcp, rest)."),
				"preferences": preferencesSchema(),
			}, nil),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in CreateSessionInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.CreateSession(ctx, in)
			},
		},
		{
			Name:        CapListPhytomers,
			Description: "List all live Phytomers, most recently active first.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Invoke: func(ctx context.Context, _ map[string]any) (any, error) {
				return s.ListSessions(ctx)
			},
		},
		{
			Name:        CapGetPhytomer,
			Description: "Fetch a single Phytomer by id.",
			InputSchema: schemaObject(map[string]any{
				"sessionId": stringProp("The Phytomer id."),
			}, []string{"sessionId"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GetSessionInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GetSession(ctx, in)
			},
		},
		{
			Name:        CapUpdatePhytomer,
			Description: "Merge preference overrides (provider, model, genotype, …) into a Phytomer.",
			InputSchema: schemaObject(map[string]any{
				"sessionId":   stringProp("The Phytomer id."),
				"preferences": preferencesSchema(),
			}, []string{"sessionId"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in UpdateSessionInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.UpdateSessionPreferences(ctx, in)
			},
		},
		{
			Name:        CapDeletePhytomer,
			Description: "Prune a Phytomer and its persisted state.",
			InputSchema: schemaObject(map[string]any{
				"sessionId": stringProp("The Phytomer id."),
			}, []string{"sessionId"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in DeleteSessionInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				if err := s.DeleteSession(ctx, in); err != nil {
					return nil, err
				}
				return map[string]any{"sessionId": in.SessionID, "deleted": true}, nil
			},
		},
		{
			Name:        CapPhytomerHistory,
			Description: "Return a session's recent unified chat log.",
			InputSchema: schemaObject(map[string]any{
				"sessionId": stringProp("The Phytomer id."),
				"limit":     map[string]any{"type": "integer", "description": "Max messages to return (default 50)."},
			}, []string{"sessionId"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in SessionHistoryInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.SessionHistory(ctx, in)
			},
		},
	}
	caps = append(caps, s.genomeCapabilities()...)
	caps = append(caps, s.plasmidCapabilities()...)
	caps = append(caps, s.meshCapabilities()...)
	caps = append(caps, s.sequenceCapabilities()...)
	caps = append(caps, s.sproutCapabilities()...)
	caps = append(caps, s.passthroughCapabilities()...)
	caps = append(caps, s.gitCapabilities()...)
	return caps
}

// --- tiny JSON-schema helpers (plain maps, no external deps) ----------------

func schemaObject(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func preferencesSchema() map[string]any {
	return schemaObject(map[string]any{
		"provider":         stringProp("LLM provider override for this session."),
		"model":            stringProp("Model override for this session."),
		"genotype":         stringProp("Genotype (system-prompt persona) override."),
		"epigeneticGenome": stringProp("Epigenetic Genome override."),
	}, nil)
}

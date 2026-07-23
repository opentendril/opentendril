package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The seed/grow capability family: grow a Seed — a bounded, well-specified
// intent — to Fruit. Where stoma.pass runs ONE command and sprout.grow
// runs an open-ended transcript, seed.grow hands the Stem a bounded unit of work
// — a goal plus a verification predicate plus explicit iteration and time bounds
// — and asks it to converge: build toward the goal, run the verify predicate,
// and iterate until the predicate passes or the bounds are spent. It is the
// "run + fix the failing tests" / "regenerate fixtures" shape.
//
// The Core owns only the contract and its validation. Execution — the sprout
// builder loop, the sealed-Terrarium verify run, worktree reconciliation — is
// injected as a transport-free port (WithSeed), so the Core never imports the
// conductor (see internal/core/boundary_test.go). Until that port is wired the
// capability reports that it is not wired rather than acting.
//
// Egress model (identical to stoma): the verify predicate and any build
// work run network-sealed; the only external reach is Stem-mediated and bounded
// by the delegation grant's egress allow-list. Egress carries json:"-", so it
// is set only by the Stem's own call sites from an authorized grant and can
// never be decoded from caller input — a caller structurally cannot widen it.

// Seed growth terminal statuses.
const (
	// SeedStatusSatisfied means the verify predicate exited 0 within bounds.
	SeedStatusSatisfied = "satisfied"
	// SeedStatusExhausted means the iteration/time bounds were spent before
	// the verify predicate passed.
	SeedStatusExhausted = "exhausted"
	// SeedStatusWithered means the underlying sprout failed and was Abscised;
	// host state is untouched (the Terrarium contained it).
	SeedStatusWithered = "withered"
)

// seedDefaultMaxIterations bounds the build/verify loop when the caller does
// not; seedMaximumMaxIterations caps what a caller may request. A Seed is a
// bounded intent — it is not an open-ended builder.
const (
	seedDefaultMaxIterations = 3
	seedMaximumMaxIterations = 10
	seedDefaultTimeout       = 15 * time.Minute
	seedMaximumTimeout       = time.Hour
)

// SeedGrowInput asks the Stem to grow a Seed: build toward Goal, then run
// Verify, iterating up to the bounds until Verify passes.
type SeedGrowInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Goal is the natural-language intent handed to the sprout builder — the
	// "what to accomplish" (e.g. "make the failing tests pass").
	Goal string `json:"goal"`
	// Verify is the argv command that defines "done": the Seed is satisfied
	// only when this command exits 0. It runs inside the sealed Terrarium, one
	// bounded command executed directly (never through a shell) — the same
	// harness stoma.pass uses. (The argv form is the predicate; a
	// named-sequence predicate is a compatible future addition.)
	Verify []string `json:"verify"`
	// MaxIterations bounds how many build/verify passes the loop may take. The
	// default applies when zero; a request above the maximum is capped.
	MaxIterations int `json:"maxIterations,omitempty"`
	// TimeoutSeconds bounds the whole growth's wall-clock; the default applies
	// when zero and a request above the maximum is capped.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// Origin records which surface invoked the run (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
	// Egress is the authorized delegation grant's egress allow-list. It has no
	// JSON surface on purpose: only the Stem's own call sites populate it,
	// after the delegation authorizer has matched a grant, so no transport
	// input can ever widen egress (deny-all remains the default).
	Egress []string `json:"-"`
}

// SeedSpec is the fully resolved, transport-free Seed handed to the
// SeedOperations port.
type SeedSpec struct {
	Substrate     string
	Goal          string
	Verify        []string
	MaxIterations int
	Timeout       time.Duration
	Origin        string
	// Egress is the grant-supplied allow-list bounding Stem-mediated reach;
	// empty means deny-all.
	Egress []string
}

// SeedGrowResult is the reviewable outcome of a grown Seed — the Fruit the
// Pollinator inspects. It is presented for review; nothing is merged.
type SeedGrowResult struct {
	// Status is satisfied, exhausted, or withered.
	Status string `json:"status"`
	// Iterations is how many build/verify passes ran.
	Iterations int `json:"iterations"`
	// Branch is the reconciled branch the work landed on, for review.
	Branch string `json:"branch,omitempty"`
	// Diff is the unified diff of the work, carried home for review (Phloem).
	Diff string `json:"diff,omitempty"`
	// Logs is the captured transcript/verify output (Xylem).
	Logs string `json:"logs,omitempty"`
}

// SeedOperations is the injection port for growing a Seed. Run may be nil, in
// which case the capability reports that it is not wired rather than acting
// until an execution port is provided.
type SeedOperations struct {
	// Run grows the Seed — build toward the goal, run the verify predicate in a
	// sealed Terrarium, iterate within bounds — and returns the reviewable
	// Fruit. Implementations own substrate resolution, the sprout lifecycle,
	// egress mediation, and worktree reconciliation.
	Run func(ctx context.Context, spec SeedSpec) (SeedGrowResult, error)
}

// WithSeed wires the Seed-growth execution port onto the Service and returns
// the Service for chaining.
func (s *Service) WithSeed(operations SeedOperations) *Service {
	s.seed = operations
	return s
}

// SeedGrow validates the request and grows the Seed via the injected execution
// port. Bounds are clamped to Stem-owned caps here, so a caller can only ever
// narrow — never widen — what a grant already permits.
func (s *Service) SeedGrow(ctx context.Context, in SeedGrowInput) (SeedGrowResult, error) {
	if s.seed.Run == nil {
		return SeedGrowResult{}, fmt.Errorf("seed.grow is not wired: construct the Core with WithSeed(SeedOperations{Run: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return SeedGrowResult{}, fmt.Errorf("substrate is required")
	}
	if strings.TrimSpace(in.Goal) == "" {
		return SeedGrowResult{}, fmt.Errorf("goal is required (the intent handed to the builder)")
	}
	// Argument tokens pass through verbatim (a token may legitimately carry
	// whitespace); only the executable token must be non-blank.
	verify := append([]string(nil), in.Verify...)
	if len(verify) == 0 || strings.TrimSpace(verify[0]) == "" {
		return SeedGrowResult{}, fmt.Errorf("verify is required (an argv vector whose exit-0 defines success)")
	}
	if in.MaxIterations < 0 {
		return SeedGrowResult{}, fmt.Errorf("maxIterations must not be negative")
	}
	if in.TimeoutSeconds < 0 {
		return SeedGrowResult{}, fmt.Errorf("timeoutSeconds must not be negative")
	}

	maxIterations := seedDefaultMaxIterations
	if in.MaxIterations > 0 {
		maxIterations = in.MaxIterations
	}
	if maxIterations > seedMaximumMaxIterations {
		maxIterations = seedMaximumMaxIterations
	}

	timeout := seedDefaultTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	if timeout > seedMaximumTimeout {
		timeout = seedMaximumTimeout
	}

	spec := SeedSpec{
		Substrate:     strings.TrimSpace(in.Substrate),
		Goal:          strings.TrimSpace(in.Goal),
		Verify:        verify,
		MaxIterations: maxIterations,
		Timeout:       timeout,
		Origin:        in.Origin,
		Egress:        append([]string(nil), in.Egress...),
	}
	return s.seed.Run(ctx, spec)
}

// seedCapabilities declares the seed family's registry entry, bound to this
// Service's typed method — identical in shape to the stoma family. The
// egress allow-list deliberately has no place in this schema: it is grant
// material, supplied only by the Stem's own call sites.
func (s *Service) seedCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapSeedGrow,
			Description: "Grow a Seed: build toward a goal and iterate until a verify command exits 0, within iteration/time bounds, inside a network-sealed terrarium (external reach only via a delegation grant's egress allow-list). Returns the Fruit for review; nothing is merged.",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"goal":      stringProp("The intent handed to the builder — what the Seed must accomplish."),
				"verify": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "The argv vector whose exit-0 defines success; run directly (never through a shell) inside the sealed terrarium.",
				},
				"maxIterations":  map[string]any{"type": "integer", "description": "Maximum build/verify passes (default 3, maximum 10)."},
				"timeoutSeconds": map[string]any{"type": "integer", "description": "Whole-growth wall-clock bound in seconds (default 900, maximum 3600)."},
				"origin":         stringProp("Interaction origin recorded on the run (cli, mcp, rest)."),
			}, []string{"substrate", "goal", "verify"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in SeedGrowInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.SeedGrow(ctx, in)
			},
		},
	}
}

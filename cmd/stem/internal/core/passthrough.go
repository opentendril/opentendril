package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The passthrough/run capability family. passthrough.run is the minimal
// delegable operation-class from the delegated-execution Design RFC: it runs
// ONE bounded command (an argv vector, never a shell string) inside the same
// network-sealed Terrarium every Sprout gets, so an external agent can hand
// the Stem the commands it would otherwise execute on the host — formatters,
// linters, test runs, codegen — without holding host access itself. The
// Terrarium orchestration lives outside the Core in the conductor (which the
// Core is structurally forbidden from importing — see boundary_test.go), so
// execution is injected as a transport-free function port via WithPassthrough,
// the same template as SproutOps.
//
// Egress model (security-first, minimal-config default):
//   - The Terrarium is physically network-sealed (--network none, asserted by
//     the terrarium provider tests), so the executed command itself can never
//     reach any host. Deny-all is the wire-level default, not a policy check.
//   - The ONLY external reach a passthrough execution has is Stem-mediated:
//     the optional fetch entries below are retrieved BY THE STEM, before the
//     sealed Terrarium runs, and each fetched URL must name a host on the
//     matching delegation grant's egress allow-list. With no grant (every
//     non-delegated invocation) the effective allow-list is empty and every
//     fetch is denied — deny-all needs zero configuration.
//   - The allow-list itself travels on the Egress field, which deliberately
//     has no JSON tag surface (json:"-"): it can only be set programmatically
//     by the Stem's own call sites from an authorized DelegationGrant, never
//     decoded from caller input — a caller structurally cannot widen its own
//     egress (no self-escalation).

// PassthroughFetchInput asks the Stem to retrieve one URL on the execution's
// behalf and deliver it into the Terrarium as a read-only file. This is the
// Stem-mediated egress channel: the fetch happens on the Stem, gated by the
// delegation grant's egress allow-list, never from inside the sealed
// container.
type PassthroughFetchInput struct {
	// URL is the http(s) resource to retrieve.
	URL string `json:"url"`
	// Path is the relative destination the command reads the payload from,
	// delivered under the Terrarium's egress directory (/tmp/egress).
	Path string `json:"path"`
}

// PassthroughRunInput asks the Stem to run one bounded command inside a
// sealed Terrarium.
type PassthroughRunInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Command is the argv vector to execute — one bounded command, executed
	// directly (never through a shell).
	Command []string `json:"command"`
	// Fetch optionally lists Stem-mediated egress retrievals delivered into
	// the Terrarium before the command runs. Every URL is checked against the
	// delegation grant's egress allow-list; with no grant the list is empty
	// and every fetch is denied.
	Fetch []PassthroughFetchInput `json:"fetch,omitempty"`
	// TimeoutSeconds bounds the command's execution; the default applies when
	// zero.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// Origin records which surface invoked the run (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
	// Egress is the authorized delegation grant's egress allow-list. It has
	// no JSON surface on purpose: only the Stem's own call sites populate it,
	// after the delegation authorizer has matched a grant, so no transport
	// input can ever widen egress (deny-all remains the default).
	Egress []string `json:"-"`
}

// PassthroughSpec is the fully resolved, transport-free execution request
// handed to the PassthroughOps port.
type PassthroughSpec struct {
	Substrate string
	Command   []string
	Fetch     []PassthroughFetchInput
	Timeout   time.Duration
	Origin    string
	// Egress is the grant-supplied allow-list bounding Stem-mediated fetches;
	// empty means deny-all.
	Egress []string
}

// PassthroughRunResult is the outcome of a finished passthrough run.
type PassthroughRunResult struct {
	// Status is "completed" when the command ran to an exit code, or
	// "timed-out" when the execution bound elapsed first.
	Status string `json:"status"`
	// ExitCode is the command's exit code (-1 on timeout).
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	TimedOut bool   `json:"timedOut,omitempty"`
	// DurationMS is the execution's wall-clock duration in milliseconds.
	DurationMS int64 `json:"durationMs"`
}

// passthroughDefaultTimeout bounds a passthrough command when the caller does
// not; passthroughMaximumTimeout caps what a caller may request — a
// passthrough is a bounded command, not a long-lived process.
const (
	passthroughDefaultTimeout = 5 * time.Minute
	passthroughMaximumTimeout = 30 * time.Minute
)

// PassthroughOps is the injection port for passthrough execution. Run may be
// nil, in which case the capability reports that it is not wired rather than
// acting.
type PassthroughOps struct {
	// Run executes the spec inside a sealed Terrarium and returns the
	// command's outcome. Implementations own substrate resolution, egress
	// mediation, and the Terrarium lifecycle.
	Run func(ctx context.Context, spec PassthroughSpec) (PassthroughRunResult, error)
}

// WithPassthrough wires the passthrough execution port onto the Service and
// returns the Service for chaining.
func (s *Service) WithPassthrough(ops PassthroughOps) *Service {
	s.passthrough = ops
	return s
}

// PassthroughRun validates the request and runs the bounded command to
// completion via the injected execution port.
func (s *Service) PassthroughRun(ctx context.Context, in PassthroughRunInput) (PassthroughRunResult, error) {
	if s.passthrough.Run == nil {
		return PassthroughRunResult{}, fmt.Errorf("passthrough.run is not wired: construct the Core with WithPassthrough(PassthroughOps{Run: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return PassthroughRunResult{}, fmt.Errorf("substrate is required")
	}
	// Argument tokens pass through verbatim (a token may legitimately carry
	// whitespace); only the executable token must be non-blank.
	command := append([]string(nil), in.Command...)
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return PassthroughRunResult{}, fmt.Errorf("command is required (an argv vector with at least one token)")
	}
	if in.TimeoutSeconds < 0 {
		return PassthroughRunResult{}, fmt.Errorf("timeoutSeconds must not be negative")
	}

	timeout := passthroughDefaultTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	if timeout > passthroughMaximumTimeout {
		timeout = passthroughMaximumTimeout
	}

	spec := PassthroughSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Command:   command,
		Fetch:     append([]PassthroughFetchInput(nil), in.Fetch...),
		Timeout:   timeout,
		Origin:    in.Origin,
		Egress:    append([]string(nil), in.Egress...),
	}
	return s.passthrough.Run(ctx, spec)
}

// passthroughCapabilities declares the passthrough family's registry entry,
// bound to this Service's typed method — identical in shape to the other
// families. The egress allow-list deliberately has no place in this schema:
// it is grant material, supplied only by the Stem's own call sites.
func (s *Service) passthroughCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapPassthroughRun,
			Description: "Run one bounded command inside a network-sealed terrarium; external reach only via Stem-mediated fetches bounded by the delegation grant's egress allow-list (deny-all by default).",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"command": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "The argv vector to execute — one bounded command, run directly (never through a shell).",
				},
				"fetch": map[string]any{
					"type": "array",
					"items": schemaObject(map[string]any{
						"url":  stringProp("The http(s) resource the Stem retrieves on the execution's behalf (host must be on the delegation grant's egress allow-list)."),
						"path": stringProp("Relative destination under the terrarium's /tmp/egress directory."),
					}, []string{"url", "path"}),
					"description": "Optional Stem-mediated egress retrievals delivered into the terrarium before the command runs; denied unless a delegation grant allow-lists the host.",
				},
				"timeoutSeconds": map[string]any{"type": "integer", "description": "Execution bound in seconds (default 300, maximum 1800)."},
				"origin":         stringProp("Interaction origin recorded on the run (cli, mcp, rest)."),
			}, []string{"substrate", "command"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in PassthroughRunInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.PassthroughRun(ctx, in)
			},
		},
	}
}

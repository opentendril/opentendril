package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The stoma/pass capability family: one bounded command (an argv vector,
// never a shell string) inside the same network-sealed Terrarium every Sprout
// gets. Terrarium orchestration lives in the conductor, which the Core is
// structurally forbidden from importing, so execution is injected as a
// transport-free port via WithStoma.
//
// Egress model:
//
//   - The Terrarium is network-sealed at the wire level (--network none), so the
//     executed command can never reach any host. Deny-all needs no configuration.
//   - The only external reach is Stem-mediated: fetch entries are retrieved by
//     the Stem before the sealed Terrarium runs, and each URL must name a host on
//     the matching delegation grant's egress allow-list. No grant means an empty
//     list and every fetch denied.
//   - Egress carries json:"-", so it can only be set programmatically by the
//     Stem from an authorized grant and never decoded from caller input. A caller
//     structurally cannot widen its own egress.

type StomaFetchInput struct {
	// URL is the http(s) resource to retrieve.
	URL string `json:"url"`
	// Path is the relative destination the command reads the payload from,
	// delivered under the Terrarium's egress directory (/tmp/egress).
	Path string `json:"path"`
}

// StomaPassInput asks the Stem to run one bounded command inside a
// sealed Terrarium.
type StomaPassInput struct {
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
	Fetch []StomaFetchInput `json:"fetch,omitempty"`
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

// StomaSpec is the fully resolved, transport-free execution request
// handed to the StomaOperations port.
type StomaSpec struct {
	Substrate string
	Command   []string
	Fetch     []StomaFetchInput
	Timeout   time.Duration
	Origin    string
	// Egress is the grant-supplied allow-list bounding Stem-mediated fetches;
	// empty means deny-all.
	Egress []string
}

// StomaPassResult is the outcome of a finished stoma pass.
type StomaPassResult struct {
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

// stomaDefaultTimeout bounds a stoma command when the caller does
// not; stomaMaximumTimeout caps what a caller may request — a
// stoma is a bounded command, not a long-lived process.
const (
	stomaDefaultTimeout = 5 * time.Minute
	stomaMaximumTimeout = 30 * time.Minute
)

// StomaOperations is the injection port for stoma execution. Run may be
// nil, in which case the capability reports that it is not wired rather than
// acting.
type StomaOperations struct {
	// Run executes the spec inside a sealed Terrarium and returns the
	// command's outcome. Implementations own substrate resolution, egress
	// mediation, and the Terrarium lifecycle.
	Run func(ctx context.Context, spec StomaSpec) (StomaPassResult, error)
}

// WithStoma wires the stoma execution port onto the Service and
// returns the Service for chaining.
func (s *Service) WithStoma(operations StomaOperations) *Service {
	s.stoma = operations
	return s
}

// StomaPass validates the request and runs the bounded command to
// completion via the injected execution port.
func (s *Service) StomaPass(ctx context.Context, in StomaPassInput) (StomaPassResult, error) {
	if s.stoma.Run == nil {
		return StomaPassResult{}, fmt.Errorf("stoma.pass is not wired: construct the Core with WithStoma(StomaOperations{Run: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return StomaPassResult{}, fmt.Errorf("substrate is required")
	}
	// Argument tokens pass through verbatim (a token may legitimately carry
	// whitespace); only the executable token must be non-blank.
	command := append([]string(nil), in.Command...)
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return StomaPassResult{}, fmt.Errorf("command is required (an argv vector with at least one token)")
	}
	if in.TimeoutSeconds < 0 {
		return StomaPassResult{}, fmt.Errorf("timeoutSeconds must not be negative")
	}

	timeout := stomaDefaultTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	if timeout > stomaMaximumTimeout {
		timeout = stomaMaximumTimeout
	}

	spec := StomaSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Command:   command,
		Fetch:     append([]StomaFetchInput(nil), in.Fetch...),
		Timeout:   timeout,
		Origin:    in.Origin,
		Egress:    append([]string(nil), in.Egress...),
	}
	return s.stoma.Run(ctx, spec)
}

// stomaCapabilities declares the stoma family's registry entry,
// bound to this Service's typed method — identical in shape to the other
// families. The egress allow-list deliberately has no place in this schema:
// it is grant material, supplied only by the Stem's own call sites.
func (s *Service) stomaCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapStomaPass,
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
				var in StomaPassInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.StomaPass(ctx, in)
			},
		},
	}
}

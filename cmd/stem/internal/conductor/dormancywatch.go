package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/dormancy"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/cmd/stem/internal/telemetry"
)

// captureTimeout bounds the total time a dormancy capture may spend collecting
// evidence. It must be short enough that a capture does not hold the watcher's
// goroutine beyond the next tick, but long enough for a process listing and log
// snapshot to complete under normal container latency.
const captureTimeout = 30 * time.Second

// watchDormancy attaches a dormancy watcher to a run for the duration of that
// run and returns the function that detaches it and prints its readout.
//
// The watcher itself knows nothing about this package: it learns what a run is
// doing from the bus, and it learns what a run has changed through a narrow
// function port this closure supplies. That keeps the workspace measurement —
// which lives behind an unexported symbol here — reachable without exporting it
// and without the watcher importing an orchestrator to reach a supervisor's
// dependency.
//
// The probe interval is what switches the whole thing on. Unconfigured, no
// watcher is attached, no probe runs, no goroutine starts and the run behaves
// exactly as it did before — patience is configured per Substrate and the
// default preserves today's behaviour.
//
// inspector and snapshot are used only when they are non-nil: a nil inspector
// means process listing and log capture are skipped, and a nil snapshot means
// the last exchange is not captured. Both may be nil in tests that exercise the
// scratch probe in isolation.
func watchDormancy(ctx context.Context, bus *eventbus.Bus, interval time.Duration, mountPath string, inspector terrariumInspector, snapshot sproutSnapshot) func() {
	if bus == nil || interval <= 0 {
		return func() {}
	}

	watcher := dormancy.New(dormancy.Config{
		Bus:             bus,
		ScratchInterval: interval,
		Scratch: func(probeCtx context.Context, _ dormancy.RunKey) ([]string, error) {
			return collectStageableFilesFn(probeCtx, mountPath)
		},
		Capture: func(captureCtx context.Context, run dormancy.RunKey) error {
			return dormancyCaptureArtifact(captureCtx, run, inspector, snapshot)
		},
	})

	detach := watcher.Subscribe(bus)
	stopTicking := watcher.Start(ctx)

	return func() {
		stopTicking()
		detach()
		// The readout is printed only for a run that actually went dormant.
		// Dormancy raises verbosity around the runs we hold the least evidence
		// about; printing the record for every healthy run instead would bury
		// the one case it exists to make legible.
		if watcher.ReportedAny() {
			watcher.Render(os.Stderr) //nolint:errcheck
		}
	}
}

// dormancyCaptureArtifact collects evidence about a dormant run and writes it
// as a scrubbed artifact under DormancyCaptureDir(). The artifact is verbose
// so a Botanist can reconstruct what the run was doing without having been
// present; the claims made in the dormancy report are deliberately terse.
//
// Four things are captured, each with a first-class "could not be taken" answer
// when the measurement fails:
//
//   - Container stderr: what the model process emitted to its own stderr channel.
//   - Last request and response: the most recent user→assistant exchange.
//   - Terrarium state: the container's accumulated stdout and stderr logs.
//   - Process listing: ps(1) output from the container, taken through terrarium.Run
//     rather than through the tool protocol so a blocked Call cannot be disrupted.
//
// Secrets are scrubbed before any string is written to disk. The scrubber
// applies the same secret patterns the telemetry package uses for event data,
// so API tokens that appear in container stderr or a last request are replaced
// with [REDACTED] before landing on disk.
func dormancyCaptureArtifact(ctx context.Context, run dormancy.RunKey, inspector terrariumInspector, snapshot sproutSnapshot) error {
	captureCtx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()

	now := time.Now().UTC()
	stamp := now.Format("20060102T150405.000000000Z")

	var sb strings.Builder

	sb.WriteString("dormancy-capture\n")
	sb.WriteString("step:    ")
	sb.WriteString(run.Step)
	sb.WriteString("\nsession: ")
	sb.WriteString(run.Session)
	sb.WriteString("\nat:      ")
	sb.WriteString(now.Format(time.RFC3339Nano))
	sb.WriteString("\n")

	// --- Container stderr ---
	sb.WriteString("\n=== container stderr ===\n")
	if inspector != nil {
		stderr := telemetry.RedactString(inspector.Logs())
		if strings.TrimSpace(stderr) == "" {
			sb.WriteString("(empty)\n")
		} else {
			sb.WriteString(stderr)
			if !strings.HasSuffix(stderr, "\n") {
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("could not be taken: no inspector available\n")
	}

	// --- Last request and response ---
	sb.WriteString("\n=== last request ===\n")
	if snapshot != nil {
		req, resp := snapshot.LastExchange()
		req = telemetry.RedactString(req)
		resp = telemetry.RedactString(resp)
		if strings.TrimSpace(req) == "" {
			sb.WriteString("(no request recorded yet)\n")
		} else {
			sb.WriteString(req)
			if !strings.HasSuffix(req, "\n") {
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n=== last response ===\n")
		if strings.TrimSpace(resp) == "" {
			sb.WriteString("(no response recorded yet)\n")
		} else {
			sb.WriteString(resp)
			if !strings.HasSuffix(resp, "\n") {
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("could not be taken: no snapshot available\n")
		sb.WriteString("\n=== last response ===\n")
		sb.WriteString("could not be taken: no snapshot available\n")
	}

	// --- Process listing ---
	sb.WriteString("\n=== process listing ===\n")
	if inspector != nil {
		listing, listErr := inspector.ProcessListing(captureCtx)
		if listErr != nil {
			fmt.Fprintf(&sb, "could not be taken: %v\n", listErr)
		} else {
			listing = telemetry.RedactString(listing)
			if strings.TrimSpace(listing) == "" {
				sb.WriteString("(empty)\n")
			} else {
				sb.WriteString(listing)
				if !strings.HasSuffix(listing, "\n") {
					sb.WriteString("\n")
				}
			}
		}
	} else {
		sb.WriteString("could not be taken: no inspector available\n")
	}

	captureDir := DormancyCaptureDir()
	if mkErr := os.MkdirAll(captureDir, 0o755); mkErr != nil {
		return fmt.Errorf("create dormancy capture directory: %w", mkErr)
	}

	fileStep := sanitizeFilenameComponent(run.Step)
	if fileStep == "" {
		fileStep = "step"
	}
	capturePath := filepath.Join(captureDir, fileStep+"-"+stamp+".txt")
	if writeErr := os.WriteFile(capturePath, []byte(sb.String()), 0o600); writeErr != nil {
		return fmt.Errorf("write dormancy capture: %w", writeErr)
	}

	return nil
}

// sanitizeFilenameComponent strips characters that are unsafe in a filename,
// keeping only alphanumeric characters and hyphens. It follows the same
// convention as sanitizeBranchComponent in sequence.go, which serves the
// analogous role for the quarantine artifact filename.
func sanitizeFilenameComponent(s string) string {
	var out strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			out.WriteRune(r)
		} else if r == '_' || r == '/' || r == '.' {
			out.WriteRune('-')
		}
	}
	return out.String()
}

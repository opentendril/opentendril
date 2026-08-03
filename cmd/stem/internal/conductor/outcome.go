package conductor

import (
	"context"
	"errors"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// The sprout outcome vocabulary. A finished Sprout loop is not a verdict on the
// work, so the old complete/failed binary dressed two very different endings
// as success: a run that changed nothing, and a run the terrarium watchdog
// killed. Each ending gets its own name; consumers decide what to do with it.
//
// The values are written to tendril-status.json (sproutExecutionStatus.Status)
// and carried on sprout lifecycle events. "complete" and "failed" keep their
// historical spelling so existing status files and readers stay valid.
const (
	// SproutOutcomeComplete: the run finished and changed at least one file.
	SproutOutcomeComplete = "complete"
	// SproutOutcomeNoChanges: the run finished without changing any file. This
	// is NOT an error — "investigate and report" legitimately changes nothing —
	// but it must never be dressed as plain completion.
	SproutOutcomeNoChanges = "no-changes"
	// SproutOutcomeFailed: the run errored before finishing.
	SproutOutcomeFailed = "failed"
	// SproutOutcomeTimedOut: a clock ended the run before it could finish —
	// either the configured growth budget or the terrarium watchdog behind it.
	// Distinct from failed because the work was cut off, not broken —
	// conflating the two once sent a diagnosis chasing a model that was working
	// fine.
	SproutOutcomeTimedOut = "timed-out"
	// SproutOutcomeSkipped: a resumed step that had already completed; no run
	// happened.
	SproutOutcomeSkipped = "skipped"
	// SproutOutcomeNoEngagement: the run finished without error but the Sprout
	// produced no response and changed nothing — it never engaged the task
	// (e.g. a model that cannot drive the tool protocol returns an empty
	// completion). Distinct from no-changes, which is a real "investigate and
	// report" ending with an actual answer. Treated as a withered run, not a
	// success, so it is never dressed as a legitimate no-op.
	SproutOutcomeNoEngagement = "no-engagement"
)

// ErrSproutTimedOut marks a sprout run cut short by the terrarium's run
// watchdog. It wraps the tool-call error the Sprout observes when the container
// is killed under it, so every layer up to the surface can tell a timeout from
// a failure with errors.Is.
var ErrSproutTimedOut = errors.New("sprout terrarium timed out before the run could finish")

// SproutRunReport is what a finished sprout run actually did: the model's
// answer plus the evidence-backed verdict on the work itself.
type SproutRunReport struct {
	// Output is the Tendril's final response text.
	Output string
	// Outcome is one of the SproutOutcome* values.
	Outcome string
	// FilesModified lists the workspace files the run changed, when the run
	// happened in a git repository where that is measurable. Nil when unknown
	// (non-git or readonly substrates).
	FilesModified []string
	// FilesUnmeasured explains why FilesModified is unknown on a run that
	// should have been able to measure it. Empty when the measurement
	// succeeded, and empty when it was never applicable — a substrate that
	// cannot measure has nothing to explain. It exists so "changed nothing"
	// and "could not be measured" stay distinguishable at the surfaces, which
	// a bare nil cannot express.
	FilesUnmeasured string
}

// classifySproutOutcome names what a run actually did. filesKnown reports
// whether FilesModified was measurable at all — a non-git or readonly
// substrate cannot distinguish complete from no-changes, and claiming
// "no-changes" there would be its own kind of lie.
func classifySproutOutcome(runErr error, filesModified []string, filesKnown bool, sproutResponse string) string {
	if runErr != nil {
		// Two clocks produce the same ending. ErrSproutTimedOut is the
		// terrarium watchdog killing the container; context.DeadlineExceeded is
		// the caller's growth budget expiring first, which is now the ordinary
		// case rather than the unreachable one. Both cut the work off.
		//
		// context.Canceled is deliberately absent: an operator interrupting a
		// run is not a budget expiring, and this vocabulary has no name for it
		// yet — inventing one here would be the same conflation in reverse.
		if errors.Is(runErr, ErrSproutTimedOut) || errors.Is(runErr, context.DeadlineExceeded) {
			return SproutOutcomeTimedOut
		}
		return SproutOutcomeFailed
	}
	changedFiles := len(filesModified) > 0
	// No response and nothing changed is a non-engaging run, not a legitimate
	// no-op: the Sprout neither acted nor answered. A run that changed files
	// engaged regardless of what it said, so file evidence wins.
	if strings.TrimSpace(sproutResponse) == "" && !changedFiles {
		return SproutOutcomeNoEngagement
	}
	if filesKnown && !changedFiles {
		return SproutOutcomeNoChanges
	}
	return SproutOutcomeComplete
}

// publishSproutEmerged announces that a sprout run is actually starting — it
// is published immediately before the terrarium session is created, on every
// execution path, so every surface gets the same signal from one place.
func publishSproutEmerged(bus *eventbus.Bus, stepID, sessionID, substrate string) {
	if bus == nil {
		return
	}
	bus.Publish(eventbus.Event{
		Type:      eventbus.EventSproutEmerged,
		Source:    stepID,
		SessionID: sessionID,
		Data: map[string]interface{}{
			"stepId":    stepID,
			"substrate": substrate,
		},
	})
}

// publishSproutTerminal publishes the single terminal lifecycle event for a
// sprout run: matured when the run finished (with or without changes, or was
// skipped as already complete), withered when it failed, timed out, or never
// engaged the task. The
// event carries enough for a consumer to act on: the step, the outcome, the
// files changed (or why they could not be measured), and the failure reason
// when there is one.
func publishSproutTerminal(bus *eventbus.Bus, stepID, sessionID, outcome string, filesModified []string, filesUnmeasured, reason string) {
	if bus == nil {
		return
	}

	eventType := eventbus.EventSproutMatured
	if outcome == SproutOutcomeFailed || outcome == SproutOutcomeTimedOut || outcome == SproutOutcomeNoEngagement {
		eventType = eventbus.EventSproutWithered
	}

	data := map[string]interface{}{
		"stepId":  stepID,
		"outcome": outcome,
	}
	if filesModified != nil {
		// Copy without append: append([]string(nil), empty...) collapses a
		// measured-empty slice to nil, which serializes as null and reads as
		// "unmeasured" — the opposite of the evidence a no-changes verdict
		// stands on.
		copied := make([]string, len(filesModified))
		copy(copied, filesModified)
		data["filesModified"] = copied
	}
	if filesUnmeasured != "" {
		// An absent filesModified says nothing about why. A consumer that
		// cannot tell a substrate which never measures from a measurement that
		// was cut off has to guess, and the guess it made was "the run failed".
		data["filesUnmeasured"] = filesUnmeasured
	}
	if reason != "" {
		data["error"] = reason
	}

	bus.Publish(eventbus.Event{
		Type:      eventType,
		Source:    stepID,
		SessionID: sessionID,
		Data:      data,
	})
}

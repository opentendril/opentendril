package conductor

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
	"github.com/opentendril/opentendril/roots/llm"
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
	// SproutOutcomeComplete: the run finished and the model changed at least
	// one file. "The model changed" rather than "a file differs": a diff of
	// the mount also contains OpenTendril's own writes, and a run that only
	// read files once reported complete on the strength of them.
	SproutOutcomeComplete = "complete"
	// SproutOutcomeNoChanges: the run finished without the model changing any
	// file. This is NOT an error — "investigate and report" legitimately
	// changes nothing — but it must never be dressed as plain completion.
	SproutOutcomeNoChanges = "no-changes"
	// SproutOutcomeReported: the run finished without changing files, because
	// it was declared an investigation run. This is an honest record of a task
	// whose goal was a report rather than a diff.
	SproutOutcomeReported = "reported"
	// SproutOutcomeFailed: the run errored before finishing.
	SproutOutcomeFailed = "failed"
	// SproutOutcomeTimedOut: a clock ended the run before it could finish —
	// either the configured growth budget or the terrarium watchdog behind it.
	// Distinct from failed because the work was cut off, not broken —
	// conflating the two once sent a diagnosis chasing a model that was working
	// fine.
	SproutOutcomeTimedOut = "timed-out"
	// SproutOutcomeDetached: the Stem stopped waiting; the Sprout is still
	// growing. The configured growth budget bounds attention, not work, so its
	// expiry ends the wait and nothing else. This is NOT an ending, and it is
	// the one outcome that carries no terminal lifecycle event: the run's
	// matured or withered event arrives later, when the work actually
	// finishes.
	SproutOutcomeDetached = "detached"
	// SproutOutcomeReaped: the orphan reaper's backstop clock stopped a
	// terrarium nothing was waiting on any more. It answers "is anyone still
	// waiting?", never "is this going well?", so it is dressed neither as a
	// matured run nor as an ordinary failure — the run was ended by a wall
	// clock, and the name says which one.
	SproutOutcomeReaped = "reaped"
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

// ErrSproutReaped marks a sprout run stopped by the orphan reaper: the backstop
// wall clock that ends a terrarium nothing is waiting on any more. It wraps the
// cancellation the run observes when the reaper cancels its context, so every
// layer up to the surface can tell "nobody was waiting" from "the work broke"
// and from "a growth budget expired" with errors.Is.
var ErrSproutReaped = errors.New("sprout reaped at the backstop: nothing was waiting on it")

// SproutRunReport is what a finished sprout run actually did: the model's
// answer plus the evidence-backed verdict on the work itself.
type SproutRunReport struct {
	// Output is the Tendril's final response text.
	Output string
	// Outcome is one of the SproutOutcome* values.
	Outcome string
	// Protocol is the tool-calling protocol the run used ("native" or "prose").
	Protocol string
	// Provider and Model name the mind that actually carried the run, as
	// resolved — not as requested. A run that requested nothing still names
	// them, which is the whole point: without this, a record of an autonomous
	// run cannot say which model did the work, so no claim about the work is
	// checkable against what the provider billed for.
	Provider string
	Model    string
	// FilesModified lists the workspace files the model changed, when the run
	// happened in a git repository where that is measurable. Nil when unknown
	// (non-git or readonly substrates). It is the run's change set, not the
	// mount's: OpenTendril's own writes into the workspace are not the
	// Sprout's work and are never reported or committed as it.
	FilesModified []string
	// FilesUnmeasured explains why FilesModified is unknown on a run that
	// should have been able to measure it. Empty when the measurement
	// succeeded, and empty when it was never applicable — a substrate that
	// cannot measure has nothing to explain. It exists so "changed nothing"
	// and "could not be measured" stay distinguishable at the surfaces, which
	// a bare nil cannot express.
	FilesUnmeasured string
	// Usage is the Sprout execution component: the fail-honest aggregate of
	// every actual LLM request made by Sprout.Run. It is not combined with
	// PostRun.
	Usage llm.Usage
	// RequestsMade is the execution-component occurrence fact, carried from
	// Sprout.Run's usageStarted flag. It is independent of whether Usage
	// fields were supplied: all-nil Usage with RequestsMade true means
	// provider request(s) occurred and the provider reported no accounting.
	// False means no execution provider request occurred.
	RequestsMade bool
	// PostRun is the post-Sprout cognitive component: epigenetic chronicling
	// and any genome reduction that chronicling triggers. Its provider and
	// model name the chronicler mind, which is not the Sprout mind.
	PostRun PostRunUsage
	// FailureCategory is the Core-owned Botanist-facing class. Conductor
	// fills it by calling core.ClassifyFailure with typed facts only.
	FailureCategory string
	// ProviderDiagnostic is the credential-free provider explanation, when a
	// typed provider response exists.
	ProviderDiagnostic *core.ProviderDiagnostic
	// ToolInvocations is how many terrarium tool calls the Sprout made.
	ToolInvocations int
}

// PostRunUsage is the fail-honest aggregate of every provider request made
// after Sprout.Run returned and before RunSprout's terminal return.
type PostRunUsage struct {
	Usage    llm.Usage
	Provider string
	Model    string
	// RequestsMade is true when at least one post-run provider request was
	// issued. All-nil Usage with RequestsMade true means the request happened
	// and the provider reported no accounting.
	RequestsMade bool
}

// changeEvidence is everything a finished run knows about what it changed.
//
// It exists because two different questions were being answered by one number.
// "Did anything in the mount differ" is what a diff measures; "did the model
// change anything" is what the outcome vocabulary claims. They come apart
// whenever something other than the model writes into the mount, and
// OpenTendril writes into it repeatedly — a repository map and a memory map
// before the run, the epigenetic genome after it, an encrypted index and its
// write-ahead log throughout. A run in which the model only read files then
// reported "complete" with those artifacts as its file list, and committed
// them as the Sprout's work.
type changeEvidence struct {
	// modelWrote reports whether the model handed the terrarium a tool call
	// that could write to the workspace. This is the answer to "did the model
	// change anything", and it comes from the model's own actions rather than
	// from the state of the mount.
	modelWrote bool
	// measured reports whether the workspace diff could be taken at all. A
	// non-git or readonly substrate cannot take one, so it can never say a run
	// changed files — but it can still say the model never asked to.
	measured bool
	// measuredFiles is the raw diff of the mount: every path that differs,
	// whoever wrote it. Nil when the diff was not taken.
	measuredFiles []string
}

// attributedFiles is the change set the run is answerable for — what belongs
// in its commit and in its report.
//
// A model that wrote nothing is answerable for nothing, whatever the mount
// says, and the empty slice is deliberately non-nil: a measured "the model
// changed nothing" must stay distinguishable from an unmeasured "nobody
// knows".
//
// When the model did write, the raw diff stands. Narrowing it further would
// mean naming the paths a shell command touched, which the tool protocol does
// not carry — and dropping a path the model really wrote loses work silently,
// which is the more expensive mistake.
func (e changeEvidence) attributedFiles() []string {
	if e.modelWrote || !e.measured {
		return e.measuredFiles
	}
	return []string{}
}

// changedAnything answers the question the outcome vocabulary asks.
//
// It takes both halves: the model must have written, AND the diff must either
// agree or be unavailable. A model that rewrote a file with its own contents
// asked to change something and changed nothing, and the measurement is the
// authority on that.
func (e changeEvidence) changedAnything() bool {
	return e.modelWrote && (!e.measured || len(e.measuredFiles) > 0)
}

// classifySproutOutcome names what a run actually did.
func classifySproutOutcome(runErr error, changes changeEvidence, sproutResponse string, isInvestigation bool) string {
	if runErr != nil {
		// The reaper is checked first because its error deliberately wraps the
		// cancellation it caused: a run ended for want of anyone waiting is
		// named after that, not after the deadline mechanics underneath it.
		if errors.Is(runErr, ErrSproutReaped) {
			return SproutOutcomeReaped
		}
		// Two clocks produce the same ending. ErrSproutTimedOut is the
		// terrarium watchdog killing the container; context.DeadlineExceeded is
		// a deadline the caller itself carried expiring under the run. Both cut
		// the work off with nothing left to finish it.
		//
		// The configured growth budget is deliberately absent from this list.
		// It bounds how long the Stem waits, not how long the work may take, so
		// its expiry detaches rather than ends — and that decision is made
		// where the budget is known, not here, because an error alone cannot
		// say whose clock produced it.
		//
		// context.Canceled is deliberately absent: an operator interrupting a
		// run is not a budget expiring, and this vocabulary has no name for it
		// yet — inventing one here would be the same conflation in reverse.
		if errors.Is(runErr, ErrSproutTimedOut) || errors.Is(runErr, context.DeadlineExceeded) {
			return SproutOutcomeTimedOut
		}
		return SproutOutcomeFailed
	}
	changedFiles := changes.changedAnything()
	// No response and nothing changed is a non-engaging run, not a legitimate
	// no-op: the Sprout neither acted nor answered. A run that changed files
	// engaged regardless of what it said, so file evidence wins.
	if strings.TrimSpace(sproutResponse) == "" && !changedFiles {
		return SproutOutcomeNoEngagement
	}
	if isInvestigation && !changedFiles {
		return SproutOutcomeReported
	}
	// An unmeasured run used to be dressed as complete, because nothing could
	// contradict it. The model's own actions can: a run that asked the
	// terrarium for nothing but reads changed nothing, and no substrate needs
	// to be measurable for that to be true. An unmeasured run in which the
	// model did write still reports complete — there the measurement is the
	// only thing that could say otherwise, and it is missing.
	if !changedFiles {
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

// publishSproutDetached announces that the Stem has stopped waiting on a run
// that is still growing, and carries the budget it waited so a consumer can say
// how long attention lasted rather than guessing.
//
// It sits alongside the run's terminal event rather than replacing it: the
// matured or withered event is published later, by whatever outlives the call,
// when the work actually ends. Spending the terminal here would announce an
// ending that has not happened.
func publishSproutDetached(bus *eventbus.Bus, stepID, sessionID string, budget time.Duration) {
	if bus == nil {
		return
	}
	bus.Publish(eventbus.Event{
		Type:      eventbus.EventSproutDetached,
		Source:    stepID,
		SessionID: sessionID,
		Data: map[string]interface{}{
			"stepId":  stepID,
			"outcome": SproutOutcomeDetached,
			"budget":  budget.String(),
		},
	})
}

// applyObservation fills the Core-owned observation fields on a finished
// report from typed facts. It never parses error strings to decide a category.
func applyObservation(report *SproutRunReport, runErr error) {
	if report == nil {
		return
	}
	if report.ProviderDiagnostic == nil {
		report.ProviderDiagnostic = providerDiagnosticFromError(runErr)
	}
	statusCode := 0
	if report.ProviderDiagnostic != nil {
		statusCode = report.ProviderDiagnostic.StatusCode
	}
	report.FailureCategory = string(core.ClassifyFailure(core.ObservationFacts{
		Outcome:                  report.Outcome,
		RunFailed:                runErr != nil,
		TerrariumOOM:             terrariumOOMFromError(runErr),
		ProviderRequestAttempted: report.RequestsMade,
		ProviderStatusCode:       statusCode,
	}))
}

func providerDiagnosticFromError(err error) *core.ProviderDiagnostic {
	var reqErr *llm.RequestError
	if !errors.As(err, &reqErr) || reqErr == nil {
		return nil
	}
	return &core.ProviderDiagnostic{
		StatusCode: reqErr.StatusCode,
		Message:    reqErr.SafeMessage(),
		Provider:   reqErr.Provider,
	}
}

func terrariumOOMFromError(err error) bool {
	if errors.Is(err, errTerrariumOOM) {
		return true
	}
	if result, ok := commandResultFromError(err); ok {
		return result.ExitCode == 137
	}
	return false
}

// errTerrariumOOM marks a Terrarium killed with exit 137. Optional: most OOM
// paths already surface through commandResultFromError with ExitCode 137.
var errTerrariumOOM = errors.New("terrarium exited 137")

// publishSproutTerminal publishes the single terminal lifecycle event for a
// sprout run: matured when the run finished (with or without changes, or was
// skipped as already complete), withered when it failed, timed out, was reaped,
// or never engaged the task. The
// event carries enough for a consumer to act on: the step, the outcome, the
// files changed (or why they could not be measured), the structured
// observation fields, and the failure reason when there is one.
func publishSproutTerminal(bus *eventbus.Bus, stepID, sessionID string, report SproutRunReport, reason string) {
	if bus == nil {
		return
	}

	outcome := report.Outcome
	eventType := eventbus.EventSproutMatured
	if outcome == SproutOutcomeFailed || outcome == SproutOutcomeTimedOut || outcome == SproutOutcomeNoEngagement || outcome == SproutOutcomeReaped {
		eventType = eventbus.EventSproutWithered
	}

	data := map[string]interface{}{
		"stepId":                   stepID,
		"outcome":                  outcome,
		"providerRequestAttempted": report.RequestsMade,
		"toolInvocations":          report.ToolInvocations,
	}
	if report.FailureCategory != "" {
		data["failureCategory"] = report.FailureCategory
	}
	if report.Provider != "" {
		data["provider"] = report.Provider
	}
	if report.Model != "" {
		data["model"] = report.Model
	}
	if report.ProviderDiagnostic != nil {
		data["providerDiagnostic"] = map[string]interface{}{
			"statusCode": report.ProviderDiagnostic.StatusCode,
			"message":    report.ProviderDiagnostic.Message,
			"provider":   report.ProviderDiagnostic.Provider,
		}
	}
	if report.FilesModified != nil {
		// Copy without append: append([]string(nil), empty...) collapses a
		// measured-empty slice to nil, which serializes as null and reads as
		// "unmeasured" — the opposite of the evidence a no-changes verdict
		// stands on.
		copied := make([]string, len(report.FilesModified))
		copy(copied, report.FilesModified)
		data["filesModified"] = copied
	}
	if report.FilesUnmeasured != "" {
		// An absent filesModified says nothing about why. A consumer that
		// cannot tell a substrate which never measures from a measurement that
		// was cut off has to guess, and the guess it made was "the run failed".
		data["filesUnmeasured"] = report.FilesUnmeasured
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

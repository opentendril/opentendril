package dormancy

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// toolFingerprint reduces a tool-call event to a comparable identity: the tool
// name together with its arguments, both of which the event already carries.
// Two calls with the same fingerprint are the same call made twice, which is
// what the distinctness rule turns on.
func toolFingerprint(event eventbus.Event) string {
	var builder strings.Builder
	builder.WriteString(canonical(event.Data["tool"]))
	builder.WriteString("\x00")
	builder.WriteString(canonical(event.Data["arguments"]))
	return builder.String()
}

// canonical renders a value so that two equal values always produce the same
// string. Map keys are sorted: Go's map iteration order is randomised, so
// formatting a map directly would make an identical repeat look distinct on
// most attempts, which would quietly turn the asymmetry off.
func canonical(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, key+"="+canonical(typed[key]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, canonical(item))
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// Render writes the retained record for every watched run.
//
// It renders strictly from what was retained and asserts nothing beyond it. Where
// the record does not know something — a cadence not yet learned, a probe that
// could not answer — the readout says so in those words rather than filling the
// gap with a plausible-looking figure. A readout that inferred would be a second
// detector nobody reviewed.
func (w *Watcher) Render(out io.Writer) error {
	if w == nil || out == nil {
		return nil
	}

	// The page is assembled under the lock and written after it: the writer is
	// a caller's, and holding the Watcher's lock across a write nobody here
	// controls would let a slow terminal stall event handling.
	var page strings.Builder

	w.mu.Lock()
	records := make([]*runRecord, 0, len(w.order))
	for _, key := range w.order {
		if record := w.runs[key]; record != nil {
			records = append(records, record)
		}
	}
	if len(records) > 0 {
		fmt.Fprintf(&page, "dormancy readout: %d growth(s) observed\n", len(records))
		for _, record := range records {
			renderRecord(&page, record)
		}
	}
	w.mu.Unlock()

	if len(records) == 0 {
		_, err := fmt.Fprintln(out, "dormancy readout: no growth observed")
		return err
	}

	_, err := io.WriteString(out, page.String())
	return err
}

// renderRecord writes one run's retained record. The caller holds the lock.
func renderRecord(page *strings.Builder, record *runRecord) {
	fmt.Fprintf(page, "\nrun %s (session %s)\n", labelled(record.key.Step), labelled(record.key.Session))

	switch {
	case record.ended:
		fmt.Fprintf(page, "  ended: %s\n", record.endedWith)
	default:
		fmt.Fprintf(page, "  ended: still growing as far as the retained events show\n")
	}

	if record.detached {
		fmt.Fprintf(page, "  attention: the Stem stopped waiting at %s; the growth was left running unattended\n", record.detachedAt.Format(time.RFC3339))
	}

	signs := make([]string, 0, len(suppressorKinds))
	for _, kind := range suppressorKinds {
		signs = append(signs, fmt.Sprintf("%s %d", kind, record.suppressions[kind]))
	}
	fmt.Fprintf(page, "  signs of life: %s\n", strings.Join(signs, ", "))
	fmt.Fprintf(page, "  observed but inert: repeated tool calls %d, static diff samples %d\n", record.inertTools, record.inertScratch)

	envelope, learned := record.cadence.envelope()
	if learned {
		fmt.Fprintf(page, "  cadence: envelope %s learned from %d gaps of this run's own\n", envelope, record.cadence.count)
	} else {
		fmt.Fprintf(page, "  cadence: not yet learned (%d gaps observed, %d needed); standing in with the cold-start %s\n", record.cadence.count, minLearnedIntervals, envelope)
	}

	silence := record.latest.Sub(record.last)
	if silence < 0 {
		silence = 0
	}
	fmt.Fprintf(page, "  silence at the last observation: %s\n", silence)
	fmt.Fprintf(page, "  peak suspicion: %.2f envelope(s) beyond this run's own; reported dormant %d time(s)\n", record.peak, record.reported)

	if record.probeFails > 0 {
		fmt.Fprintf(page, "  diff growth unmeasured on %d sample(s); last reason: %s\n", record.probeFails, record.scratchNote)
	}

	types := make([]string, 0, len(record.counts))
	for eventType := range record.counts {
		types = append(types, string(eventType))
	}
	sort.Strings(types)

	parts := make([]string, 0, len(types))
	for _, eventType := range types {
		parts = append(parts, fmt.Sprintf("%s %d", eventType, record.counts[eventbus.EventType(eventType)]))
	}
	if len(parts) == 0 {
		fmt.Fprintf(page, "  events retained: none\n")
		return
	}
	fmt.Fprintf(page, "  events retained: %s\n", strings.Join(parts, ", "))
}

func labelled(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(unnamed)"
	}
	return value
}

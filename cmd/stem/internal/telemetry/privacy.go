package telemetry

import (
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// StripPrivateReasoning deterministically removes private reasoning blocks
// (<thought>...</thought>) from model text. It handles multiple blocks and
// fails closed on any malformed state:
//
//   - A complete <thought>…</thought> block is removed; text before and after is
//     preserved.
//   - Multiple blocks are all removed.
//   - An unclosed opening <thought> discards the opening marker and all following
//     text; text before the marker is preserved.
//   - An orphan closing </thought> is treated as if everything up to and including
//     the marker was inside a thought block — the prefix before the orphan is
//     discarded because it cannot be determined to be safe. Text after the orphan
//     is treated as potentially public and processed recursively.
func StripPrivateReasoning(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	for {
		startTag := strings.Index(trimmed, "<thought>")
		endTag := strings.Index(trimmed, "</thought>")

		if startTag == -1 && endTag == -1 {
			// No markers at all — text is safe.
			break
		}

		if startTag == -1 && endTag != -1 {
			// Orphan closing marker: everything before it is unclassifiable
			// (could be private content whose opening tag was missed). Fail
			// closed: discard the prefix including the orphan marker, then
			// continue processing what remains.
			trimmed = strings.TrimSpace(trimmed[endTag+10:])
			continue
		}

		if startTag != -1 && endTag == -1 {
			// Unclosed opening marker: fail closed by discarding everything
			// from the opening marker onward.
			trimmed = strings.TrimSpace(trimmed[:startTag])
			break
		}

		// Both markers present.
		if endTag < startTag {
			// The closing marker appears before the opening one: the prefix
			// up to the orphan closing marker is unclassifiable. Discard the
			// prefix including the orphan, then handle the real <thought>
			// block on the next iteration.
			trimmed = strings.TrimSpace(trimmed[endTag+10:])
			continue
		}

		// Normal case: <thought> before </thought>.
		// Search for the closing tag starting after the opening tag.
		innerClose := strings.Index(trimmed[startTag+9:], "</thought>")
		if innerClose == -1 {
			// Unclosed — discard from opening marker onward.
			trimmed = strings.TrimSpace(trimmed[:startTag])
			break
		}
		closePos := startTag + 9 + innerClose
		trimmed = strings.TrimSpace(trimmed[:startTag] + trimmed[closePos+10:])
	}
	return trimmed
}

// SanitizeSproutTranscript applies StripPrivateReasoning to assistant turns
// in a composed transcript, leaving other roles unchanged.
//
// Structured transcripts (those using the "[role]\n" prefix convention) have
// each assistant block sanitized individually while user, system, and tool
// blocks are passed through unchanged.
//
// Unstructured text that does not contain role markers but does contain thought
// tags is sanitized as a whole rather than returned raw — fail-closed semantics.
func SanitizeSproutTranscript(transcript string) string {
	hasThought := strings.Contains(transcript, "<thought>") || strings.Contains(transcript, "</thought>")
	if !hasThought {
		return transcript
	}

	hasAssistantBlock := strings.Contains(transcript, "[assistant]")

	if !hasAssistantBlock {
		// Unstructured legacy text: no role markers but thought tags present.
		// Sanitize the whole thing rather than returning raw private content.
		return StripPrivateReasoning(transcript)
	}

	var safe []string

	blocks := strings.Split(transcript, "\n[")
	for i, block := range blocks {
		b := block
		if i > 0 {
			b = "[" + block
		}

		if strings.HasPrefix(b, "[assistant]\n") {
			parts := strings.SplitN(b, "]\n", 2)
			if len(parts) == 2 {
				stripped := StripPrivateReasoning(parts[1])
				if stripped == "" {
					b = parts[0] + "]"
				} else {
					b = parts[0] + "]\n" + stripped
				}
			} else {
				b = StripPrivateReasoning(b)
			}
		}
		safe = append(safe, strings.TrimRight(b, " \t\r\n"))
	}

	return strings.Join(safe, "\n\n")
}

// SanitizeObservationEvent creates a safe copy of an EventBus event,
// removing private reasoning from its payload if present. This sanitization
// is mandatory and is not affected by TENDRIL_TELEMETRY_REDACTION settings.
func SanitizeObservationEvent(event eventbus.Event) eventbus.Event {
	if event.Data == nil {
		return event
	}

	safeData := make(map[string]interface{})
	for k, v := range event.Data {
		safeData[k] = v
	}

	switch event.Type {
	case eventbus.EventStreamToken:
		// stream-token is a content-free cadence signal; remove any token or
		// model-text fields that may have been recorded in historical rows.
		delete(safeData, "token")
		delete(safeData, "content")
	case "thought-branch":
		// Legacy event type: never re-expose the raw thought payload.
		delete(safeData, "thought")
	case "sprout-transcript":
		if t, ok := safeData["transcript"].(string); ok {
			safeData["transcript"] = SanitizeSproutTranscript(t)
		}
	}

	event.Data = safeData
	return event
}

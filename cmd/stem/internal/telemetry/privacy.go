package telemetry

import (
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// StripPrivateReasoning deterministically removes private reasoning blocks
// (<thought>...</thought>) from model text. It handles multiple blocks and
// fails closed on an unclosed <thought> block by discarding the remainder.
func StripPrivateReasoning(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	for {
		start := strings.Index(trimmed, "<thought>")
		if start == -1 {
			break
		}

		offset := strings.Index(trimmed[start+9:], "</thought>")
		if offset != -1 {
			end := start + 9 + offset
			trimmed = strings.TrimSpace(trimmed[:start] + trimmed[end+10:])
		} else {
			// unclosed <thought> block; fail closed by truncating
			trimmed = strings.TrimSpace(trimmed[:start])
			break
		}
	}
	return trimmed
}

// SanitizeSproutTranscript applies StripPrivateReasoning to assistant turns
// in a composed transcript, leaving other roles unchanged.
func SanitizeSproutTranscript(transcript string) string {
	if !strings.Contains(transcript, "[assistant]") && !strings.Contains(transcript, "<thought>") {
		return transcript
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
// removing private reasoning from its payload if present.
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
		delete(safeData, "token")
		delete(safeData, "content")
	case "thought-branch":
		delete(safeData, "thought")
	case "sprout-transcript":
		if t, ok := safeData["transcript"].(string); ok {
			safeData["transcript"] = SanitizeSproutTranscript(t)
		}
	case "stream.end":
		delete(safeData, "content")
		delete(safeData, "output")
	}

	event.Data = safeData
	return event
}

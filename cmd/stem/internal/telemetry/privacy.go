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
	case eventbus.EventTaskContextAssembled:
		// This event is reconstructed from its allow-list because task context
		// carries provenance about private source material. General redaction is
		// optional and therefore cannot be the safety boundary for live
		// subscribers or future producer changes.
		if safeTaskContextObservationIdentifier(event.Source) {
			event.Source = strings.TrimSpace(event.Source)
		} else {
			event.Source = ""
		}
		if safeTaskContextObservationIdentifier(event.SessionID) {
			event.SessionID = strings.TrimSpace(event.SessionID)
		} else {
			event.SessionID = ""
		}
		event.Data = sanitizeTaskContextObservation(safeData)
		return event
	}

	event.Data = safeData
	return event
}

func sanitizeTaskContextObservation(data map[string]interface{}) map[string]interface{} {
	safe := make(map[string]interface{})
	if value, ok := data["stepId"].(string); ok && safeTaskContextObservationIdentifier(value) {
		safe["stepId"] = strings.TrimSpace(value)
	}
	if value, ok := data["substrate"].(string); ok && safeTaskContextObservationSubstrate(value) {
		safe["substrate"] = strings.TrimSpace(value)
	}
	for _, key := range []string{"substrateRef", "workspaceRevisionRef"} {
		if value, ok := data[key].(string); ok && safeTaskContextObservationReference(value) {
			safe[key] = strings.TrimSpace(value)
		}
	}
	for _, key := range []string{
		"effectiveMaxBytes",
		"effectiveItemMaxBytes",
		"effectiveMaxItems",
		"candidateCount",
		"admittedCount",
		"admittedBytes",
	} {
		if value, ok := safeObservationInteger(data[key]); ok && value >= 0 && value <= 8192 {
			safe[key] = value
		}
	}

	if counts, ok := sanitizeTaskContextCounts(data["omissionCounts"]); ok {
		safe["omissionCounts"] = counts
	}
	if items, ok := sanitizeTaskContextItems(data["items"]); ok {
		safe["items"] = items
	}
	return safe
}

func sanitizeTaskContextCounts(value interface{}) (map[string]interface{}, bool) {
	counts := make(map[string]interface{})
	switch typed := value.(type) {
	case map[string]interface{}:
		for reason, rawCount := range typed {
			if !safeTaskContextObservationOmissionReason(reason) {
				continue
			}
			count, ok := safeObservationInteger(rawCount)
			if ok && count >= 0 && count <= 8192 {
				counts[reason] = count
			}
		}
	case map[string]int:
		for reason, count := range typed {
			if safeTaskContextObservationOmissionReason(reason) && count >= 0 && count <= 8192 {
				counts[reason] = count
			}
		}
	default:
		return nil, false
	}
	return counts, true
}

func sanitizeTaskContextItems(value interface{}) ([]map[string]interface{}, bool) {
	var rawItems []interface{}
	switch typed := value.(type) {
	case []interface{}:
		rawItems = typed
	case []map[string]interface{}:
		rawItems = make([]interface{}, len(typed))
		for i := range typed {
			rawItems[i] = typed[i]
		}
	default:
		return nil, false
	}

	items := make([]map[string]interface{}, 0, len(rawItems))
	for _, rawItem := range rawItems {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}
		sourceClass, sourceClassOK := item["sourceClass"].(string)
		sourceIdentity, sourceIdentityOK := item["sourceIdentity"].(string)
		reason, reasonOK := item["selectionReason"].(string)
		contentRef, contentRefOK := item["contentRef"].(string)
		admittedBytes, admittedBytesOK := safeObservationInteger(item["admittedBytes"])
		truncated, truncatedOK := item["truncated"].(bool)
		if !sourceClassOK || !sourceIdentityOK || !reasonOK || !contentRefOK || !admittedBytesOK || !truncatedOK ||
			!safeTaskContextObservationSourceClass(sourceClass) || !safeTaskContextObservationIdentity(sourceIdentity) ||
			!safeTaskContextObservationSelectionReason(reason) || !safeTaskContextObservationReference(contentRef) || admittedBytes < 0 || admittedBytes > 8192 {
			continue
		}
		safeItem := map[string]interface{}{
			"sourceClass":     sourceClass,
			"sourceIdentity":  sourceIdentity,
			"selectionReason": reason,
			"contentRef":      contentRef,
			"admittedBytes":   admittedBytes,
			"truncated":       truncated,
		}
		if sourceClass == "project-memory" {
			if validityRef, ok := item["validityRef"].(string); ok && safeTaskContextObservationReference(validityRef) {
				safeItem["validityRef"] = validityRef
			}
			if origin, ok := item["origin"].(string); ok && safeTaskContextObservationMemoryEnvelopeField(origin, "origin") {
				safeItem["origin"] = origin
			}
			if authority, ok := item["authority"].(string); ok && safeTaskContextObservationMemoryEnvelopeField(authority, "authority") {
				safeItem["authority"] = authority
			}
			if status, ok := item["status"].(string); ok && safeTaskContextObservationMemoryEnvelopeField(status, "status") {
				safeItem["status"] = status
			}
			if kind, ok := item["kind"].(string); ok && safeTaskContextObservationMemoryEnvelopeField(kind, "kind") {
				safeItem["kind"] = kind
			}
		}
		items = append(items, safeItem)
	}
	return items, true
}

func safeObservationInteger(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case uint:
		return int(typed), uint(int(typed)) == typed
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), uint32(int(typed)) == typed
	case uint64:
		return int(typed), uint64(int(typed)) == typed
	case float64:
		integer := int(typed)
		return integer, float64(integer) == typed
	default:
		return 0, false
	}
}

func safeTaskContextObservationString(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= max && !strings.ContainsAny(value, "\r\n\x00") && !strings.Contains(value, "<thought>") && !strings.Contains(value, "</thought>")
}

func safeTaskContextObservationSubstrate(value string) bool {
	value = strings.TrimSpace(value)
	if !safeTaskContextObservationString(value, 128) || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\:\x00") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func safeTaskContextObservationToken(value string, max int) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func safeTaskContextObservationIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	return safeTaskContextObservationString(value, 128) && !strings.ContainsAny(value, "/\\:")
}

func safeTaskContextObservationSourceClass(value string) bool {
	switch value {
	case "file-anchor", "rhizome-symbol", "git-state-path", "associated-test", "associated-documentation", "project-memory":
		return true
	default:
		return false
	}
}

func safeTaskContextObservationSelectionReason(value string) bool {
	switch value {
	case "explicit-file-anchor", "exact-symbol-anchor", "lexical-symbol-anchor", "git-state-path", "associated-test", "associated-documentation", "source-local-memory":
		return true
	default:
		return false
	}
}

func safeTaskContextObservationOmissionReason(value string) bool {
	switch value {
	case "budget-bytes", "budget-items", "stale-evidence", "path-security", "unreadable", "not-found",
		"memory-missing", "memory-unavailable", "memory-unbound", "memory-unclassified",
		"memory-proposed", "memory-stale", "memory-conflicted", "memory-rejected", "memory-superseded":
		return true
	default:
		return false
	}
}

func safeTaskContextObservationIdentity(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 256 || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\:\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func safeTaskContextObservationReference(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 12 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'f') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

// safeTaskContextObservationMemoryEnvelopeField validates a single enumerated
// memory envelope field value (origin, authority, or status). It accepts only
// the finite set of values that are safe to emit in telemetry.
func safeTaskContextObservationMemoryEnvelopeField(value, field string) bool {
	switch field {
	case "origin":
		switch value {
		case "legacy", "botanist", "substrate", "mycorrhizal":
			return true
		}
	case "authority":
		switch value {
		case "none", "botanist", "deterministic":
			return true
		}
	case "status":
		switch value {
		case "unclassified", "established", "proposed", "stale", "conflicted", "rejected", "superseded":
			return true
		}
	case "kind":
		switch value {
		case "observation", "fact", "constraint", "correction", "rejected-interpretation":
			return true
		}
	}
	return false
}

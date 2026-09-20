package telemetry

import (
	"os"
	"regexp"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

var (
	sensitiveKeySubstrings = []string{
		"token",
		"key",
		"secret",
		"password",
		"passwd",
		"authorization",
		"bearer",
		"credential",
		"api_key",
		"apikey",
	}

	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\._\-]+`),
		regexp.MustCompile(`(sk-|ghp_|github_pat_|xox[baprs]-)[A-Za-z0-9\._\-]+`),
		regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`),
		regexp.MustCompile(`[A-Za-z0-9\+/\_\-]{32,}={0,2}`),
	}
)

// RedactionDisabled returns true if the global TENDRIL_TELEMETRY_REDACTION opt-out is set.
func RedactionDisabled() bool {
	if v := os.Getenv("TENDRIL_TELEMETRY_REDACTION"); strings.ToLower(v) == "off" || strings.ToLower(v) == "false" {
		return true
	}
	return false
}

// RedactEvent returns a deep-copy of the event with sensitive data scrubbed.
// It leaves the original event unmodified.
func RedactEvent(event eventbus.Event) eventbus.Event {
	switch event.Type {
	case eventbus.EventTaskContextAssembled:
		// Task-context telemetry must cross its mandatory fail-closed allow-list
		// before generic redaction. Preserve only the validated correlation
		// identifier across that generic pass; provenance strings remain subject
		// to the normal redaction rules.
		safe := SanitizeObservationEvent(event)
		stepID, hasStepID := safe.Data["stepId"].(string)
		redacted := safe
		if safe.Data != nil {
			redacted.Data = redactMap(safe.Data)
			if hasStepID {
				redacted.Data["stepId"] = stepID
			}
		}
		return redacted
	case eventbus.EventSproutTranscript:
		redacted := event
		if event.Data != nil {
			redacted.Data = redactMap(event.Data)
			if transcript, ok := event.Data["transcript"].(string); ok {
				redacted.Data["transcript"] = redactTranscript(transcript)
			}
		}
		return redacted
	}

	redacted := event
	if event.Data != nil {
		redacted.Data = redactMap(event.Data)
	}
	return redacted
}

func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, sub := range sensitiveKeySubstrings {
		if strings.Contains(lk, sub) {
			return true
		}
	}
	return false
}

func redactMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	res := make(map[string]interface{}, len(m))
	for k, v := range m {
		if isSensitiveKey(k) {
			res[k] = "[REDACTED]"
			continue
		}
		res[k] = redactValue(v)
	}
	return res
}

func redactSlice(s []interface{}) []interface{} {
	if s == nil {
		return nil
	}
	res := make([]interface{}, len(s))
	for i, v := range s {
		res[i] = redactValue(v)
	}
	return res
}

func redactMapSlice(s []map[string]interface{}) []map[string]interface{} {
	if s == nil {
		return nil
	}
	res := make([]map[string]interface{}, len(s))
	for i, value := range s {
		res[i] = redactMap(value)
	}
	return res
}

func redactValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		return redactString(val)
	case map[string]interface{}:
		return redactMap(val)
	case []interface{}:
		return redactSlice(val)
	case []map[string]interface{}:
		return redactMapSlice(val)
	default:
		return v
	}
}

func redactString(s string) string {
	res := s
	for _, re := range secretPatterns {
		res = re.ReplaceAllString(res, "[REDACTED]")
	}
	return res
}

const (
	taskContextMarkerOne = "Task-specific Substrate evidence was supplied separately."
	taskContextMarkerTwo = "See the task-context provenance manifest for selection facts."
)

func redactTranscript(transcript string) string {
	const (
		markerTokenOne = "__task_context_marker_one__"
		markerTokenTwo = "__task_context_marker_two__"
	)
	protected := strings.ReplaceAll(transcript, taskContextMarkerOne, markerTokenOne)
	protected = strings.ReplaceAll(protected, taskContextMarkerTwo, markerTokenTwo)
	protected = redactString(protected)
	protected = strings.ReplaceAll(protected, markerTokenOne, taskContextMarkerOne)
	return strings.ReplaceAll(protected, markerTokenTwo, taskContextMarkerTwo)
}

// RedactString scrubs secret patterns from a raw string and returns the result.
// It applies the same patterns as the internal event scrubber, so API tokens,
// bearer credentials, and high-entropy strings are replaced with [REDACTED]
// before the value leaves the telemetry boundary.
func RedactString(s string) string {
	return redactString(s)
}

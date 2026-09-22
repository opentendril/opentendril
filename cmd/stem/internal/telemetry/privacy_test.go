package telemetry

import (
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

func TestStripPrivateReasoning(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no thought block",
			input:    "just normal text",
			expected: "just normal text",
		},
		{
			name:     "thought block only",
			input:    "<thought>thinking...</thought>",
			expected: "",
		},
		{
			name:     "text before and after",
			input:    "Before <thought>thinking...</thought> After",
			expected: "Before  After",
		},
		{
			name:     "multiple thought blocks",
			input:    "A <thought>1</thought> B <thought>2</thought> C",
			expected: "A  B  C",
		},
		{
			name:     "unclosed thought block",
			input:    "Start <thought>never ends",
			expected: "Start",
		},
		{
			name:     "orphan closing tag",
			input:    "private</thought>public",
			expected: "public",
		},
		{
			name:     "inverted tags",
			input:    "private</thought>public<thought>secret",
			expected: "public",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := StripPrivateReasoning(tc.input)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestSanitizeSproutTranscript(t *testing.T) {
	transcript := `[system]
You are a bot.
[user]
Do something.
[assistant]
<thought>planning...</thought>Here is the result.`

	expected := `[system]
You are a bot.

[user]
Do something.

[assistant]
Here is the result.`

	result := SanitizeSproutTranscript(transcript)
	if result != expected {
		t.Errorf("expected:\n%s\n\ngot:\n%s", expected, result)
	}

	unstructured := `Here is some <thought>private</thought> text without role markers.`
	expectedUnstructured := `Here is some  text without role markers.`
	if got := SanitizeSproutTranscript(unstructured); got != expectedUnstructured {
		t.Errorf("unstructured expected %q, got %q", expectedUnstructured, got)
	}
}

func TestSanitizeObservationEvent(t *testing.T) {
	e := eventbus.Event{
		Type: eventbus.EventStreamToken,
		Data: map[string]interface{}{
			"token": "a",
			"other": "b",
		},
	}

	s := SanitizeObservationEvent(e)
	if _, ok := s.Data["token"]; ok {
		t.Errorf("expected token to be removed")
	}
	if s.Data["other"] != "b" {
		t.Errorf("expected other to be kept")
	}

	e2 := eventbus.Event{
		Type: "thought-branch",
		Data: map[string]interface{}{
			"thought": "abc",
			"foo":     "bar",
		},
	}
	s2 := SanitizeObservationEvent(e2)
	if _, ok := s2.Data["thought"]; ok {
		t.Errorf("expected thought to be removed")
	}
}

func TestSanitizeTaskContextEventIsFailClosedAllowList(t *testing.T) {
	event := eventbus.Event{
		Type:      eventbus.EventTaskContextAssembled,
		Source:    "step-1",
		SessionID: "phytomer-1",
		Data: map[string]interface{}{
			"stepId":               "step-1",
			"substrate":            "fixture",
			"substrateRef":         "0123456789ab",
			"workspaceRevisionRef": "abcdefabcdef",
			"effectiveMaxBytes":    4096,
			"candidateCount":       2,
			"admittedCount":        1,
			"admittedBytes":        32,
			"omissionCounts":       map[string]interface{}{"budget-bytes": 1},
			"items": []map[string]interface{}{{
				"sourceClass":     "file-anchor",
				"sourceIdentity":  "fixtures/ghp_123456789012345678901234567890123456.txt",
				"selectionReason": "explicit-file-anchor",
				"contentRef":      "0123456789ab",
				"admittedBytes":   32,
				"truncated":       false,
				"content":         "raw evidence",
			}},
			"prompt":       "Transcript and search terms",
			"transcript":   "private transcript",
			"absolutePath": "/home/secret/repository",
			"environment":  map[string]interface{}{"TOKEN": "secret"},
			"credential":   "bearer secret",
			"reasoning":    "<thought>private</thought>",
		},
	}

	safe := SanitizeObservationEvent(event)
	for _, forbidden := range []string{"content", "prompt", "transcript", "absolutePath", "environment", "credential", "reasoning"} {
		if _, ok := safe.Data[forbidden]; ok {
			t.Fatalf("contaminated field %q survived: %+v", forbidden, safe.Data)
		}
	}
	items, ok := safe.Data["items"].([]map[string]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("safe item contract = %#v", safe.Data["items"])
	}
	if _, ok := items[0]["content"]; ok {
		t.Fatalf("raw item content survived: %+v", items[0])
	}
	if items[0]["sourceIdentity"] != "fixtures/ghp_123456789012345678901234567890123456.txt" {
		t.Fatalf("safe source identity was not retained: %+v", items[0])
	}
	if safe.Data["sourceIdentity"] != nil {
		t.Fatalf("unexpected top-level source identity: %+v", safe.Data)
	}

	encoded := strings.Join([]string{safe.Data["substrate"].(string), safe.Data["substrateRef"].(string)}, " ")
	if strings.Contains(encoded, "/home/") || strings.Contains(encoded, "secret") {
		t.Fatalf("unsafe safe fields: %q", encoded)
	}
}

func TestSanitizeTaskContextEventStaysSafeWhenGeneralRedactionIsDisabled(t *testing.T) {
	t.Setenv("TENDRIL_TELEMETRY_REDACTION", "off")
	event := eventbus.Event{
		Type: eventbus.EventTaskContextAssembled,
		Data: map[string]interface{}{
			"stepId":       "step-1",
			"content":      "raw evidence",
			"transcript":   "private transcript",
			"absolutePath": "/tmp/repository",
			"items": []map[string]interface{}{{
				"sourceClass":     "file-anchor",
				"sourceIdentity":  "/tmp/repository/foo.go",
				"selectionReason": "explicit-file-anchor",
				"contentRef":      "0123456789ab",
				"admittedBytes":   32,
				"truncated":       false,
			}},
		},
	}
	safe := SanitizeObservationEvent(event)
	if safe.Data["stepId"] != "step-1" {
		t.Fatalf("redaction-disabled task-context event was not fail-closed: %+v", safe.Data)
	}
	if items, ok := safe.Data["items"].([]map[string]interface{}); !ok || len(items) != 0 {
		t.Fatalf("unsafe absolute item survived with redaction disabled: %+v", safe.Data)
	}
}

func TestSanitizeTaskContextEventRetainsGeneratedCorrelation(t *testing.T) {
	stepID := "qualification-step-20260920-0123456789abcdef"
	event := eventbus.Event{
		Type:      eventbus.EventTaskContextAssembled,
		Source:    stepID,
		SessionID: "phytomer-qualification-20260920-0123456789abcdef",
		Data: map[string]interface{}{
			"stepId":        stepID,
			"admittedCount": 1,
			"content":       "raw evidence",
		},
	}

	safe := SanitizeObservationEvent(event)
	if safe.Source != stepID || safe.Data["stepId"] != stepID {
		t.Fatalf("generated task-context correlation was not retained: source=%q data=%v", safe.Source, safe.Data)
	}
	if _, ok := safe.Data["content"]; ok {
		t.Fatalf("unsafe task-context content survived: %+v", safe.Data)
	}
}

func TestSanitizeTaskContextEventDropsUnsafeCorrelationAndUnknownEnums(t *testing.T) {
	event := eventbus.Event{
		Type:   eventbus.EventTaskContextAssembled,
		Source: "/home/private/step",
		Data: map[string]interface{}{
			"stepId":         "/home/private/step",
			"omissionCounts": map[string]interface{}{"secret-search-term": 1, "budget-bytes": 1, "memory-unclassified": 1},
			"items": []map[string]interface{}{{
				"sourceClass":     "unexpected-class",
				"sourceIdentity":  "foo.go",
				"selectionReason": "prompt",
				"contentRef":      "0123456789ab",
				"admittedBytes":   1,
				"truncated":       false,
			}},
		},
	}
	safe := SanitizeObservationEvent(event)
	if safe.Source != "" || safe.SessionID != "" {
		t.Fatalf("unsafe event correlation survived: source=%q session=%q", safe.Source, safe.SessionID)
	}
	if _, ok := safe.Data["stepId"]; ok {
		t.Fatalf("unsafe step ID survived: %+v", safe.Data)
	}
	counts, ok := safe.Data["omissionCounts"].(map[string]interface{})
	if !ok || counts["budget-bytes"] != 1 || counts["memory-unclassified"] != 1 {
		t.Fatalf("stable omission reason was not preserved: %+v", safe.Data)
	}
	if _, ok := counts["secret-search-term"]; ok {
		t.Fatalf("unknown omission reason survived: %+v", counts)
	}
	if items, ok := safe.Data["items"].([]map[string]interface{}); !ok || len(items) != 0 {
		t.Fatalf("unknown item enums survived: %+v", safe.Data)
	}
}

// TestSanitizeTaskContextEventNewOmissionReasonsAreAllowed verifies that each
// Slice 4 omission reason constant passes the telemetry allowlist.
func TestSanitizeTaskContextEventNewOmissionReasonsAreAllowed(t *testing.T) {
	newReasons := []string{
		"memory-proposed",
		"memory-stale",
		"memory-conflicted",
		"memory-rejected",
		"memory-superseded",
	}
	for _, reason := range newReasons {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			event := eventbus.Event{
				Type: eventbus.EventTaskContextAssembled,
				Data: map[string]interface{}{
					"stepId":         "step-1",
					"omissionCounts": map[string]interface{}{reason: 1},
					"admittedCount":  0,
				},
			}
			safe := SanitizeObservationEvent(event)
			counts, ok := safe.Data["omissionCounts"].(map[string]interface{})
			if !ok {
				t.Fatalf("omissionCounts missing or wrong type: %#v", safe.Data["omissionCounts"])
			}
			if counts[reason] != 1 {
				t.Fatalf("new omission reason %q was not preserved: %v", reason, counts)
			}
		})
	}
}

// TestSanitizeTaskContextEventMemoryEnvelopeFieldsEmittedForMemoryItems
// verifies that origin, authority, status, and validityRef are emitted in the
// sanitized output when present with valid enum values on a project-memory item.
func TestSanitizeTaskContextEventMemoryEnvelopeFieldsEmittedForMemoryItems(t *testing.T) {
	event := eventbus.Event{
		Type: eventbus.EventTaskContextAssembled,
		Data: map[string]interface{}{
			"stepId":        "step-1",
			"admittedCount": 1,
			"items": []map[string]interface{}{{
				"sourceClass":     "project-memory",
				"sourceIdentity":  "memory/abcdef012345",
				"selectionReason": "source-local-memory",
				"contentRef":      "0123456789ab",
				"admittedBytes":   64,
				"truncated":       false,
				"origin":          "botanist",
				"authority":       "botanist",
				"status":          "established",
				"kind":            "fact",
				"validityRef":     "fedcba987654",
			}},
		},
	}
	safe := SanitizeObservationEvent(event)
	items, ok := safe.Data["items"].([]map[string]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("expected 1 safe item, got: %#v", safe.Data["items"])
	}
	item := items[0]
	for _, field := range []string{"origin", "authority", "status", "kind", "validityRef"} {
		if item[field] == nil {
			t.Errorf("memory envelope field %q was stripped from safe item: %v", field, item)
		}
	}
	if item["origin"] != "botanist" || item["authority"] != "botanist" || item["status"] != "established" || item["kind"] != "fact" {
		t.Fatalf("memory envelope field values incorrect: %v", item)
	}
	if item["validityRef"] != "fedcba987654" {
		t.Fatalf("validityRef incorrect: %v", item["validityRef"])
	}
}

func TestSanitizeTaskContextEventEveryMemoryKnowledgeKindSurvives(t *testing.T) {
	allowedKinds := []string{"observation", "fact", "constraint", "correction", "rejected-interpretation"}
	for _, kind := range allowedKinds {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			event := eventbus.Event{
				Type: eventbus.EventTaskContextAssembled,
				Data: map[string]interface{}{
					"admittedCount": 1,
					"items": []map[string]interface{}{{
						"sourceClass":     "project-memory",
						"sourceIdentity":  "memory/abcdef012345",
						"selectionReason": "source-local-memory",
						"contentRef":      "0123456789ab",
						"admittedBytes":   64,
						"truncated":       false,
						"origin":          "botanist",
						"authority":       "botanist",
						"status":          "established",
						"kind":            kind,
						"validityRef":     "fedcba987654",
					}},
				},
			}
			safe := SanitizeObservationEvent(event)
			items, ok := safe.Data["items"].([]map[string]interface{})
			if !ok || len(items) != 1 || items[0]["kind"] != kind {
				t.Fatalf("knowledge kind %q did not survive sanitization: %#v", kind, safe.Data["items"])
			}
		})
	}
}

// TestSanitizeTaskContextEventMemoryEnvelopeFieldsStrippedWhenInvalid verifies
// that unknown enum values for origin, authority, status, and kind are rejected.
func TestSanitizeTaskContextEventMemoryEnvelopeFieldsStrippedWhenInvalid(t *testing.T) {
	event := eventbus.Event{
		Type: eventbus.EventTaskContextAssembled,
		Data: map[string]interface{}{
			"stepId":        "step-1",
			"admittedCount": 1,
			"items": []map[string]interface{}{{
				"sourceClass":     "project-memory",
				"sourceIdentity":  "memory/abcdef012345",
				"selectionReason": "source-local-memory",
				"contentRef":      "0123456789ab",
				"admittedBytes":   64,
				"truncated":       false,
				"origin":          "unknown-origin",
				"authority":       "god-mode",
				"status":          "hacked",
				"kind":            "bad-kind",
				"validityRef":     "not-hex!@#$%^",
			}},
		},
	}
	safe := SanitizeObservationEvent(event)
	items, ok := safe.Data["items"].([]map[string]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("expected 1 safe item even with invalid envelope fields: %#v", safe.Data["items"])
	}
	item := items[0]
	for _, field := range []string{"origin", "authority", "status", "kind", "validityRef"} {
		if _, present := item[field]; present {
			t.Errorf("invalid envelope field %q survived sanitization: %v", field, item[field])
		}
	}
}

// TestSanitizeTaskContextEventMemoryEnvelopeFieldsAbsentForNonMemoryItems
// verifies that memory lifecycle fields are not forwarded for ordinary evidence.
func TestSanitizeTaskContextEventMemoryEnvelopeFieldsAbsentForNonMemoryItems(t *testing.T) {
	event := eventbus.Event{
		Type: eventbus.EventTaskContextAssembled,
		Data: map[string]interface{}{
			"stepId":        "step-1",
			"admittedCount": 1,
			"items": []map[string]interface{}{{
				"sourceClass":     "file-anchor",
				"sourceIdentity":  "cmd/foo.go",
				"selectionReason": "explicit-file-anchor",
				"contentRef":      "0123456789ab",
				"admittedBytes":   256,
				"truncated":       false,
				"origin":          "botanist",
				"authority":       "botanist",
				"status":          "established",
				"kind":            "fact",
				"validityRef":     "fedcba987654",
			}},
		},
	}
	safe := SanitizeObservationEvent(event)
	items, ok := safe.Data["items"].([]map[string]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("expected 1 safe item: %#v", safe.Data["items"])
	}
	item := items[0]
	for _, field := range []string{"origin", "authority", "status", "kind", "validityRef"} {
		if _, present := item[field]; present {
			t.Errorf("unexpected envelope field %q on file-anchor item: %v", field, item[field])
		}
	}
}

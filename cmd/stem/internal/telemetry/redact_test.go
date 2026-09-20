package telemetry

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

func TestRedactEvent(t *testing.T) {
	now := time.Now()
	ev := eventbus.Event{
		Type:      "test",
		Timestamp: now,
		Source:    "test-source",
		Data: map[string]interface{}{
			"label":   "build",
			"stepId":  "123",
			"api_key": "sk-abcdefghijklmnopqrstuvwxyz123456",
			"nested": map[string]interface{}{
				"note":     "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
				"password": "supersecretpassword",
				"slice": []interface{}{
					"non-secret",
					"ghp_123456789012345678901234567890123456",
					"0123456789abcdef0123456789abcdef01234567",
					"prefix_sk-secret-value",
				},
			},
		},
	}

	redacted := RedactEvent(ev)

	if reflect.DeepEqual(ev, redacted) {
		t.Errorf("expected redacted event to be different from original event")
	}

	// Verify original is untouched
	if ev.Data["api_key"] != "sk-abcdefghijklmnopqrstuvwxyz123456" {
		t.Errorf("original event was mutated")
	}

	// Verify redacted content
	if redacted.Data["label"] != "build" || redacted.Data["stepId"] != "123" {
		t.Errorf("non-secret content was modified")
	}

	if redacted.Data["api_key"] != "[REDACTED]" {
		t.Errorf("sensitive key not redacted: got %v", redacted.Data["api_key"])
	}

	nested := redacted.Data["nested"].(map[string]interface{})
	if nested["note"] != "[REDACTED]" {
		t.Errorf("sensitive value not redacted: got %v", nested["note"])
	}
	if nested["password"] != "[REDACTED]" {
		t.Errorf("sensitive key password not redacted: got %v", nested["password"])
	}

	slice := nested["slice"].([]interface{})
	if slice[0] != "non-secret" {
		t.Errorf("non-secret string modified: got %v", slice[0])
	}
	if slice[1] != "[REDACTED]" {
		t.Errorf("provider key not redacted: got %v", slice[1])
	}
	if slice[2] != "[REDACTED]" {
		t.Errorf("hex string not redacted: got %v", slice[2])
	}
	if slice[3] != "prefix_[REDACTED]" {
		t.Errorf("embedded provider token was not redacted: got %v", slice[3])
	}
}

func TestRedactSproutTranscriptPreservesTaskContextMarkers(t *testing.T) {
	event := eventbus.Event{
		Type: eventbus.EventSproutTranscript,
		Data: map[string]interface{}{
			"transcript": "Task-specific Substrate evidence was supplied separately.\nSee the task-context provenance manifest for selection facts.\napi_key=sk-secret-value",
		},
	}

	redacted := RedactEvent(event)
	transcript := redacted.Data["transcript"].(string)
	for _, marker := range []string{taskContextMarkerOne, taskContextMarkerTwo} {
		if !strings.Contains(transcript, marker) {
			t.Fatalf("transcript marker %q was redacted: %q", marker, transcript)
		}
	}
	if strings.Contains(transcript, "sk-secret-value") {
		t.Fatalf("secret survived transcript redaction: %q", transcript)
	}
}

func TestRedactTaskContextEventPreservesAllowListedCorrelation(t *testing.T) {
	stepID := "qualification-step-20260920-0123456789abcdef"
	event := eventbus.Event{
		Type:      eventbus.EventTaskContextAssembled,
		Source:    stepID,
		SessionID: "phytomer-qualification-0123456789abcdef",
		Data: map[string]interface{}{
			"stepId":                stepID,
			"substrate":             "fixture",
			"substrateRef":          "0123456789ab",
			"workspaceRevisionRef":  "abcdefabcdef",
			"effectiveMaxBytes":     4096,
			"effectiveItemMaxBytes": 1024,
			"effectiveMaxItems":     8,
			"candidateCount":        1,
			"admittedCount":         1,
			"admittedBytes":         32,
			"items": []map[string]interface{}{{
				"sourceClass":     "file-anchor",
				"sourceIdentity":  "fixture.go",
				"selectionReason": "explicit-file-anchor",
				"contentRef":      "0123456789ab",
				"admittedBytes":   32,
				"truncated":       false,
			}},
			"content":          "raw selected evidence",
			"transcript":       "private transcript",
			"credential":       "bearer secret",
			"environment":      map[string]interface{}{"TOKEN": "secret"},
			"absolutePath":     "/home/private/repository",
			"privateReasoning": "<thought>private</thought>",
		},
	}

	redacted := RedactEvent(event)
	if redacted.Source != stepID || redacted.Data["stepId"] != stepID {
		t.Fatalf("allow-listed step correlation was redacted: source=%q data=%v", redacted.Source, redacted.Data)
	}
	if redacted.SessionID != event.SessionID {
		t.Fatalf("allow-listed session correlation was redacted: %q", redacted.SessionID)
	}
	for _, forbidden := range []string{"content", "transcript", "credential", "environment", "absolutePath", "privateReasoning"} {
		if _, ok := redacted.Data[forbidden]; ok {
			t.Fatalf("unsafe task-context field %q survived: %v", forbidden, redacted.Data)
		}
	}
}

func TestRedactionDisabled(t *testing.T) {
	os.Setenv("TENDRIL_TELEMETRY_REDACTION", "off")
	defer os.Unsetenv("TENDRIL_TELEMETRY_REDACTION")

	if !RedactionDisabled() {
		t.Errorf("expected RedactionDisabled to be true")
	}

	os.Setenv("TENDRIL_TELEMETRY_REDACTION", "false")
	if !RedactionDisabled() {
		t.Errorf("expected RedactionDisabled to be true")
	}

	os.Setenv("TENDRIL_TELEMETRY_REDACTION", "on")
	if RedactionDisabled() {
		t.Errorf("expected RedactionDisabled to be false")
	}
}

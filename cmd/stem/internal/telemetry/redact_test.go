package telemetry

import (
	"os"
	"reflect"
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

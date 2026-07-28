package telemetry

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

type mockTransporter struct {
	lastEvent eventbus.Event
}

func (m *mockTransporter) Emit(event eventbus.Event) error {
	m.lastEvent = event
	return nil
}

func TestRedactingTransporter(t *testing.T) {
	// 1 & 2: By default, redact Data and do not mutate original event
	t.Run("default redact and no mutate", func(t *testing.T) {
		mock := &mockTransporter{}
		wrapped := wrapIfRedacting(mock, false)

		secretValue := "sk-secret"
		originalEvent := eventbus.Event{
			Type:      "test-type",
			Timestamp: time.Now(),
			Source:    "test-source",
			SessionID: "sess-123",
			Data: map[string]interface{}{
				"token": secretValue,
			},
		}

		err := wrapped.Emit(originalEvent)
		if err != nil {
			t.Fatalf("Emit failed: %v", err)
		}

		// Ensure emitted event is redacted
		if len(mock.lastEvent.Data) != 0 {
			t.Errorf("Expected redacted Data to be empty, got %v", mock.lastEvent.Data)
		}
		if mock.lastEvent.Type != originalEvent.Type || mock.lastEvent.SessionID != originalEvent.SessionID || mock.lastEvent.Timestamp != originalEvent.Timestamp || mock.lastEvent.Source != originalEvent.Source {
			t.Errorf("Expected structural fields to survive redaction")
		}

		// Ensure original event is NOT mutated
		if originalEvent.Data["token"] != secretValue {
			t.Errorf("Original event was mutated!")
		}
	})

	// 3a: raw: true bypasses redaction
	t.Run("raw config bypasses redaction", func(t *testing.T) {
		mock := &mockTransporter{}
		wrapped := wrapIfRedacting(mock, true)

		event := eventbus.Event{
			Data: map[string]interface{}{"token": "sk-secret"},
		}
		_ = wrapped.Emit(event)
		if mock.lastEvent.Data["token"] != "sk-secret" {
			t.Errorf("Expected raw config to bypass redaction, data was redacted")
		}
	})

	// 3b: TENDRIL_TELEMETRY_REDACTION=off bypasses redaction
	t.Run("env opt-out bypasses redaction", func(t *testing.T) {
		os.Setenv("TENDRIL_TELEMETRY_REDACTION", "off")
		defer os.Unsetenv("TENDRIL_TELEMETRY_REDACTION")

		mock := &mockTransporter{}
		wrapped := wrapIfRedacting(mock, false)

		event := eventbus.Event{
			Data: map[string]interface{}{"token": "sk-secret"},
		}
		_ = wrapped.Emit(event)
		if mock.lastEvent.Data["token"] != "sk-secret" {
			t.Errorf("Expected env opt-out to bypass redaction, data was redacted")
		}
	})
}

func TestPrometheusNotWrapped(t *testing.T) {
	cfg := TransporterConfig{
		Type:     "prometheus",
		Endpoint: "127.0.0.1:0", // let OS pick available port
	}
	transporter, err := NewTransporter(cfg)
	if err != nil {
		t.Fatalf("NewTransporter failed: %v", err)
	}

	// Should directly return *PrometheusTransporter, not *redactingTransporter
	prom, ok := transporter.(*PrometheusTransporter)
	if !ok {
		t.Errorf("Expected *PrometheusTransporter, got %T", transporter)
	} else {
		prom.Close() // Clean up listener
	}
}

type flakyTransporter struct {
	failFirst int // this many Emit calls fail, then succeed
	calls     int
	lastEvent eventbus.Event
}

func (m *flakyTransporter) Emit(event eventbus.Event) error {
	m.calls++
	if m.calls <= m.failFirst {
		return errors.New("transient failure")
	}
	m.lastEvent = event
	return nil
}

func TestTransporterSinkRetriesOnTransientFailure(t *testing.T) {
	oldSleep := retrySleep
	t.Cleanup(func() { retrySleep = oldSleep })
	retrySleep = func(time.Duration) {} // no-op

	flaky := &flakyTransporter{failFirst: 1}
	sink := &transporterSink{transporter: flaky}

	event := eventbus.Event{Type: "test"}
	sink.Consume(event)

	if flaky.calls != 2 {
		t.Errorf("Expected 2 calls, got %d", flaky.calls)
	}
	if sink.failures.Load() != 0 {
		t.Errorf("Expected 0 failures, got %d", sink.failures.Load())
	}
}

func TestTransporterSinkDropsAfterExhaustingRetries(t *testing.T) {
	oldSleep := retrySleep
	t.Cleanup(func() { retrySleep = oldSleep })
	retrySleep = func(time.Duration) {} // no-op

	flaky := &flakyTransporter{failFirst: telemetryRetryAttempts}
	sink := &transporterSink{transporter: flaky}

	event := eventbus.Event{Type: "test"}
	sink.Consume(event)

	if flaky.calls != telemetryRetryAttempts {
		t.Errorf("Expected %d calls, got %d", telemetryRetryAttempts, flaky.calls)
	}
	if sink.failures.Load() != 1 {
		t.Errorf("Expected 1 failure, got %d", sink.failures.Load())
	}
}

func TestTransporterSinkRetrySleepsBetweenAttempts(t *testing.T) {
	oldSleep := retrySleep
	t.Cleanup(func() { retrySleep = oldSleep })

	var sleeps []time.Duration
	retrySleep = func(d time.Duration) {
		sleeps = append(sleeps, d)
	}

	flaky := &flakyTransporter{failFirst: telemetryRetryAttempts}
	sink := &transporterSink{transporter: flaky}

	event := eventbus.Event{Type: "test"}
	sink.Consume(event)

	if flaky.calls != telemetryRetryAttempts {
		t.Errorf("Expected %d calls, got %d", telemetryRetryAttempts, flaky.calls)
	}
	if sink.failures.Load() != 1 {
		t.Errorf("Expected 1 failure, got %d", sink.failures.Load())
	}
	expectedSleeps := telemetryRetryAttempts - 1
	if len(sleeps) != expectedSleeps {
		t.Errorf("Expected %d sleeps, got %d", expectedSleeps, len(sleeps))
	}
	for i, d := range sleeps {
		if d != telemetryRetryBackoff {
			t.Errorf("Expected sleep %d to be %v, got %v", i, telemetryRetryBackoff, d)
		}
	}
}

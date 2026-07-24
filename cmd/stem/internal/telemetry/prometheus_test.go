package telemetry

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

func newTestPrometheusTransporter(t *testing.T) *PrometheusTransporter {
	t.Helper()
	// Endpoint with port 0 lets the kernel pick a free port.
	transporter, err := NewPrometheusTransporter(TransporterConfig{
		Type:     "prometheus",
		Endpoint: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("NewPrometheusTransporter: %v", err)
	}
	t.Cleanup(func() { _ = transporter.Close() })
	return transporter
}

func scrapeWithAuth(t *testing.T, transporter *PrometheusTransporter, authHeader string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/metrics", transporter.Addr()), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("scrape /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	return resp, string(body)
}

func scrape(t *testing.T, transporter *PrometheusTransporter) string {
	t.Helper()
	resp, body := scrapeWithAuth(t, transporter, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("scrape /metrics: status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "version=0.0.4") {
		t.Fatalf("unexpected Content-Type %q", got)
	}
	return body
}

func TestPrometheusTransporterOffHostAuth(t *testing.T) {
	// Off-host Endpoint + empty APIKey: fail closed
	_, err := NewPrometheusTransporter(TransporterConfig{
		Type:     "prometheus",
		Endpoint: "0.0.0.0:0",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot expose /metrics off-host without api_key") {
		t.Fatalf("expected fail closed on off-host without APIKey, got %v", err)
	}

	// Off-host Endpoint + APIKey set
	transporter, err := NewPrometheusTransporter(TransporterConfig{
		Type:     "prometheus",
		Endpoint: "0.0.0.0:0",
		APIKey:   "secret-token",
	})
	if err != nil {
		t.Fatalf("NewPrometheusTransporter: %v", err)
	}
	defer transporter.Close()

	// request without bearer -> 401
	resp, _ := scrapeWithAuth(t, transporter, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", resp.StatusCode)
	}

	// request with wrong bearer -> 401
	resp, _ = scrapeWithAuth(t, transporter, "Bearer wrong-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong auth, got %d", resp.StatusCode)
	}

	// request with correct bearer -> 200
	resp, body := scrapeWithAuth(t, transporter, "Bearer secret-token")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with correct auth, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "version=0.0.4") {
		t.Errorf("unexpected Content-Type %q", got)
	}
	if !strings.Contains(body, "opentendril_events_last_timestamp_seconds") {
		t.Errorf("expected metrics in body, got %q", body)
	}
}

func TestPrometheusTransporterCountsEvents(t *testing.T) {
	transporter := newTestPrometheusTransporter(t)

	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := transporter.Emit(eventbus.Event{Type: eventbus.EventSproutEmerged, Timestamp: now}); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := transporter.Emit(eventbus.Event{Type: eventbus.EventSequenceFailure, Timestamp: now}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	body := scrape(t, transporter)

	if !strings.Contains(body, `opentendril_events_total{type="sprout-emerged"} 3`) {
		t.Errorf("missing sprout-emerged count in scrape:\n%s", body)
	}
	if !strings.Contains(body, `opentendril_events_total{type="sequence-failure"} 1`) {
		t.Errorf("missing sequence-failure count in scrape:\n%s", body)
	}
	want := fmt.Sprintf("opentendril_events_last_timestamp_seconds %d", now.Unix())
	if !strings.Contains(body, want) {
		t.Errorf("missing %q in scrape:\n%s", want, body)
	}
}

func TestPrometheusTransporterPreRegistersAllEventTypes(t *testing.T) {
	transporter := newTestPrometheusTransporter(t)

	body := scrape(t, transporter)

	for _, eventType := range eventbus.AllEventTypes() {
		series := fmt.Sprintf(`opentendril_events_total{type=%q} 0`, string(eventType))
		if !strings.Contains(body, series) {
			t.Errorf("event type %q not pre-registered at zero in scrape:\n%s", eventType, body)
		}
	}
	if !strings.Contains(body, "opentendril_events_last_timestamp_seconds 0") {
		t.Errorf("expected zero last-event timestamp before any Emit:\n%s", body)
	}
}

func TestPrometheusTransporterLLMTokenUsage(t *testing.T) {
	transporter := newTestPrometheusTransporter(t)

	// Three token chunks stream from the LLM: 5 + 5 + 2 runes. The multibyte
	// chunk proves characters are counted as runes, not bytes.
	for _, chunk := range []string{"Hello", ", wor", "ld"} {
		if err := transporter.Emit(eventbus.Event{
			Type: eventbus.EventStreamToken,
			Data: map[string]interface{}{"token": chunk},
		}); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	if err := transporter.Emit(eventbus.Event{
		Type: eventbus.EventStreamToken,
		Data: map[string]interface{}{"token": "🌱🌱"},
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Stream lifecycle markers carry no token chunk and must not count.
	for _, marker := range []string{"stream.start", "stream.end"} {
		if err := transporter.Emit(eventbus.Event{
			Type: eventbus.EventStreamToken,
			Data: map[string]interface{}{"type": marker},
		}); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}

	body := scrape(t, transporter)

	if !strings.Contains(body, "opentendril_llm_stream_tokens_total 4") {
		t.Errorf("expected 4 token chunks (markers excluded) in scrape:\n%s", body)
	}
	if !strings.Contains(body, "opentendril_llm_stream_characters_total 14") {
		t.Errorf("expected 14 streamed runes in scrape:\n%s", body)
	}
	// The marker events still count as raw bus events.
	if !strings.Contains(body, `opentendril_events_total{type="stream-token"} 6`) {
		t.Errorf("expected 6 raw stream-token events in scrape:\n%s", body)
	}
}

func TestPrometheusTransporterSproutsActiveGauge(t *testing.T) {
	transporter := newTestPrometheusTransporter(t)

	emit := func(eventType eventbus.EventType) {
		t.Helper()
		if err := transporter.Emit(eventbus.Event{Type: eventType}); err != nil {
			t.Fatalf("Emit(%s): %v", eventType, err)
		}
	}

	// Two sprouts emerge, one matures: one still active.
	emit(eventbus.EventSproutEmerged)
	emit(eventbus.EventSproutEmerged)
	emit(eventbus.EventSproutMatured)
	if body := scrape(t, transporter); !strings.Contains(body, "opentendril_sprouts_active 1") {
		t.Errorf("expected 1 active sprout in scrape:\n%s", body)
	}

	// The last one withers: none active.
	emit(eventbus.EventSproutWithered)
	if body := scrape(t, transporter); !strings.Contains(body, "opentendril_sprouts_active 0") {
		t.Errorf("expected 0 active sprouts in scrape:\n%s", body)
	}

	// A withered event for a sprout that emerged before this transporter
	// attached must clamp at zero, never underflow.
	emit(eventbus.EventSproutWithered)
	if body := scrape(t, transporter); !strings.Contains(body, "opentendril_sprouts_active 0") {
		t.Errorf("expected clamped 0 active sprouts in scrape:\n%s", body)
	}
}

func TestPrometheusTransporterNewSeriesPresentAtZero(t *testing.T) {
	transporter := newTestPrometheusTransporter(t)

	body := scrape(t, transporter)

	for _, series := range []string{
		"opentendril_llm_stream_tokens_total 0",
		"opentendril_llm_stream_characters_total 0",
		"opentendril_sprouts_active 0",
	} {
		if !strings.Contains(body, series) {
			t.Errorf("series %q not present at zero on first scrape:\n%s", series, body)
		}
	}
}

func TestPrometheusTransporterConfigValidation(t *testing.T) {
	if _, err := NewPrometheusTransporter(TransporterConfig{Type: "prometheus"}); err == nil {
		t.Fatal("expected error when neither endpoint nor port is set")
	}

	// NewTransporter routes the "prometheus" type through the real constructor.
	transporter, err := NewTransporter(TransporterConfig{Type: "prometheus", Endpoint: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewTransporter(prometheus): %v", err)
	}
	prom, ok := transporter.(*PrometheusTransporter)
	if !ok {
		t.Fatalf("NewTransporter returned %T, want *PrometheusTransporter", transporter)
	}
	_ = prom.Close()
}

func TestPrometheusTransporterPortBindsLoopback(t *testing.T) {
	// Port-only config must bind loopback, never all interfaces.
	transporter, err := NewPrometheusTransporter(TransporterConfig{Type: "prometheus", Port: 0})
	if err == nil {
		_ = transporter.Close()
		t.Fatal("expected error for port <= 0 without endpoint")
	}

	// Pick a free port first, then bind it via Port to assert the address.
	probe, err := NewPrometheusTransporter(TransporterConfig{Type: "prometheus", Endpoint: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("probe transporter: %v", err)
	}
	addr := probe.Addr()
	_ = probe.Close()

	var port int
	if _, err := fmt.Sscanf(addr[strings.LastIndex(addr, ":")+1:], "%d", &port); err != nil {
		t.Fatalf("parse probe port from %q: %v", addr, err)
	}

	bound, err := NewPrometheusTransporter(TransporterConfig{Type: "prometheus", Port: port})
	if err != nil {
		t.Fatalf("NewPrometheusTransporter(Port=%d): %v", port, err)
	}
	defer bound.Close()

	if !strings.HasPrefix(bound.Addr(), "127.0.0.1:") {
		t.Errorf("port-only config bound %q, want loopback 127.0.0.1", bound.Addr())
	}
}

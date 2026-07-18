package telemetry

import (
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// PrometheusTransporter exposes EventBus activity as a Prometheus scrape
// endpoint (text exposition format v0.0.4) using only the standard library —
// no client dependency, in keeping with the "unbloated core" constraint of
// . It attaches to the bus like every other Transporter: Emit only
// updates in-memory counters, so a scraper outage can never slow the bus.
//
// Exposed metrics:
//
//	opentendril_events_total{type="sprout-emerged"}   counter per event type
//	opentendril_events_last_timestamp_seconds         unix time of last event
//	opentendril_llm_stream_tokens_total               LLM stream token chunks
//	opentendril_llm_stream_characters_total           characters streamed by LLMs
//	opentendril_sprouts_active                        sprout workers running now
//
// The LLM counters are derived from stream-token events that carry a token
// payload chunk (the per-token stream the conductor's Agent publishes);
// stream start/end markers carry no chunk and are excluded. Cumulative
// per-state sprout worker counts (emerged/matured/withered) are already the
// opentendril_events_total series for those event types; the active gauge is
// the live balance of those transitions.
//
// Metric names use underscores because the Prometheus data model only permits
// [a-zA-Z0-9_:] in metric names; the kebab-case biological event types survive
// verbatim as label values.
type PrometheusTransporter struct {
	server   *http.Server
	listener net.Listener

	mu                  sync.Mutex
	counts              map[string]uint64
	lastEvent           time.Time
	llmStreamTokens     uint64
	llmStreamCharacters uint64
	sproutsActive       uint64
}

// NewPrometheusTransporter starts a /metrics HTTP listener and returns the
// transporter. The bind address is chosen from the config:
//
//   - Endpoint, when set, is used verbatim (e.g. "0.0.0.0:9091") for operators
//     who explicitly want the scrape port on a non-loopback interface.
//   - Otherwise Port binds to loopback ("127.0.0.1:<port>") so enabling
//     telemetry never silently exposes operational data on all interfaces.
//
// A failure to bind is returned as an error; the caller logs it and continues,
// per the telemetry spec's rule that telemetry failures never block the Stem.
func NewPrometheusTransporter(cfg TransporterConfig) (*PrometheusTransporter, error) {
	addr := strings.TrimSpace(cfg.Endpoint)
	if addr == "" {
		if cfg.Port <= 0 {
			return nil, fmt.Errorf("prometheus transporter requires port or endpoint")
		}
		addr = fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("prometheus transporter: listen on %s: %w", addr, err)
	}

	transporter := &PrometheusTransporter{
		listener: listener,
		counts:   make(map[string]uint64, len(eventbus.AllEventTypes())),
	}
	// Pre-register every known event type at zero so dashboards and alert
	// rules see a stable series set from the first scrape.
	for _, eventType := range eventbus.AllEventTypes() {
		transporter.counts[string(eventType)] = 0
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", transporter.handleMetrics)
	transporter.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		// ErrServerClosed after Close is the normal shutdown path; any other
		// serve error just ends the scrape endpoint without touching the bus.
		_ = transporter.server.Serve(listener)
	}()

	return transporter, nil
}

// Addr reports the bound listen address (useful when Port was 0 in tests).
func (t *PrometheusTransporter) Addr() string {
	return t.listener.Addr().String()
}

// Close shuts down the scrape endpoint. The listener is closed directly (not
// only via server.Close) so an immediate Close cannot race the Serve goroutine
// registering the listener with the server.
func (t *PrometheusTransporter) Close() error {
	err := t.listener.Close()
	if serverErr := t.server.Close(); err == nil {
		err = serverErr
	}
	return err
}

// Emit records the event in the in-memory counters. It never blocks on I/O.
func (t *PrometheusTransporter) Emit(event eventbus.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts[string(event.Type)]++
	if event.Timestamp.After(t.lastEvent) {
		t.lastEvent = event.Timestamp
	}

	switch event.Type {
	case eventbus.EventStreamToken:
		// Only events carrying an actual token chunk count as LLM output;
		// stream.start / stream.end markers publish no "token" key.
		if chunk, ok := event.Data["token"].(string); ok && chunk != "" {
			t.llmStreamTokens++
			t.llmStreamCharacters += uint64(utf8.RuneCountInString(chunk))
		}
	case eventbus.EventSproutEmerged:
		t.sproutsActive++
	case eventbus.EventSproutMatured, eventbus.EventSproutWithered:
		// Clamp at zero: a matured/withered event for a sprout that emerged
		// before this transporter attached must not underflow the gauge.
		if t.sproutsActive > 0 {
			t.sproutsActive--
		}
	}
	return nil
}

func (t *PrometheusTransporter) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	t.mu.Lock()
	types := make([]string, 0, len(t.counts))
	for eventType := range t.counts {
		types = append(types, eventType)
	}
	sort.Strings(types)

	var builder strings.Builder
	builder.WriteString("# HELP opentendril_events_total Total EventBus events observed since Stem start, by event type.\n")
	builder.WriteString("# TYPE opentendril_events_total counter\n")
	for _, eventType := range types {
		builder.WriteString("opentendril_events_total{type=\"")
		builder.WriteString(escapeLabelValue(eventType))
		builder.WriteString("\"} ")
		builder.WriteString(fmt.Sprintf("%d\n", t.counts[eventType]))
	}
	builder.WriteString("# HELP opentendril_events_last_timestamp_seconds Unix timestamp of the most recent EventBus event.\n")
	builder.WriteString("# TYPE opentendril_events_last_timestamp_seconds gauge\n")
	if t.lastEvent.IsZero() {
		builder.WriteString("opentendril_events_last_timestamp_seconds 0\n")
	} else {
		builder.WriteString(fmt.Sprintf("opentendril_events_last_timestamp_seconds %d\n", t.lastEvent.Unix()))
	}
	builder.WriteString("# HELP opentendril_llm_stream_tokens_total Total LLM stream token chunks observed on the EventBus (stream start/end markers excluded).\n")
	builder.WriteString("# TYPE opentendril_llm_stream_tokens_total counter\n")
	builder.WriteString(fmt.Sprintf("opentendril_llm_stream_tokens_total %d\n", t.llmStreamTokens))
	builder.WriteString("# HELP opentendril_llm_stream_characters_total Total characters streamed by LLM providers, summed over stream-token chunks.\n")
	builder.WriteString("# TYPE opentendril_llm_stream_characters_total counter\n")
	builder.WriteString(fmt.Sprintf("opentendril_llm_stream_characters_total %d\n", t.llmStreamCharacters))
	builder.WriteString("# HELP opentendril_sprouts_active Sprout workers currently executing: emerged minus matured/withered, as observed on the EventBus.\n")
	builder.WriteString("# TYPE opentendril_sprouts_active gauge\n")
	builder.WriteString(fmt.Sprintf("opentendril_sprouts_active %d\n", t.sproutsActive))
	t.mu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(builder.String()))
}

// escapeLabelValue escapes a Prometheus label value per the exposition format
// (backslash, double-quote, and newline).
func escapeLabelValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return replacer.Replace(value)
}

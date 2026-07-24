package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// Transporter pumps events across the system boundary to external telemetry platforms.
type Transporter interface {
	Emit(event eventbus.Event) error
}

// WebhookTransporter POSTs JSON event payloads to a remote endpoint.
type WebhookTransporter struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

// redactingTransporter wraps a sink to redact event data before emission.
type redactingTransporter struct {
	wrapped Transporter
}

func (t *redactingTransporter) Emit(event eventbus.Event) error {
	redacted := eventbus.Event{
		Type:      event.Type,
		Timestamp: event.Timestamp,
		Source:    event.Source,
		SessionID: event.SessionID,
		// Data is explicitly omitted (nil/empty).
	}
	return t.wrapped.Emit(redacted)
}

// wrapIfRedacting applies the redacting decorator unless the sink is configured as raw
// or the global TENDRIL_TELEMETRY_REDACTION opt-out is set.
func wrapIfRedacting(t Transporter, rawConfig bool) Transporter {
	if rawConfig {
		return t
	}
	if RedactionDisabled() {
		return t
	}
	return &redactingTransporter{wrapped: t}
}

// NewTransporter builds a Transporter from configuration.
func NewTransporter(cfg TransporterConfig) (Transporter, error) {
	switch cfg.Type {
	case "webhook":
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("webhook transporter requires endpoint")
		}
		return wrapIfRedacting(NewWebhookTransporter(cfg.Endpoint, cfg.APIKey), cfg.Raw), nil
	case "redis":
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("redis transporter requires endpoint (host:port)")
		}
		return wrapIfRedacting(NewRedisTransporter(cfg.Endpoint, cfg.Channel, cfg.APIKey), cfg.Raw), nil
	case "websocket":
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("websocket transporter requires endpoint")
		}
		return wrapIfRedacting(NewRemoteWebSocketTransporter(cfg.Endpoint, cfg.APIKey), cfg.Raw), nil
	case "prometheus":
		return NewPrometheusTransporter(cfg)
	case "kafka":
		t, err := NewKafkaTransporter(cfg)
		if err != nil {
			return nil, err
		}
		return wrapIfRedacting(t, cfg.Raw), nil
	default:
		return nil, fmt.Errorf("unknown transporter type %q", cfg.Type)
	}
}

// NewWebhookTransporter creates a webhook-backed transporter.
func NewWebhookTransporter(endpoint, apiKey string) *WebhookTransporter {
	return &WebhookTransporter{
		endpoint: endpoint,
		apiKey:   apiKey,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// transporterSink adapts a Transporter to the eventbus.Sink interface so
// remote emission runs on the bus's buffered sink pump instead of inline on
// the publish path.
type transporterSink struct {
	transporter Transporter
	failures    atomic.Int64
}

func (s *transporterSink) Consume(event eventbus.Event) {
	if err := s.transporter.Emit(event); err != nil {
		if s.failures.Add(1)%100 == 1 {
			log.Printf("⚠️ telemetry: remote transporter emit failed: %v", err)
		}
	}
}

// AttachTransporter attaches the transporter to the bus as an asynchronous
// sink receiving every event.
func AttachTransporter(bus *eventbus.Bus, t Transporter) {
	if bus == nil || t == nil {
		return
	}
	bus.AttachSink(&transporterSink{transporter: t}, 0)
}

func (t *WebhookTransporter) Emit(event eventbus.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode webhook payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, t.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook payload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

func attachHandler(bus *eventbus.Bus, handler func(eventbus.Event)) {
	for _, eventType := range eventbus.AllEventTypes() {
		bus.Subscribe(eventType, handler)
	}
}

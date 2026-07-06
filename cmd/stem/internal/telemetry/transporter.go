package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
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

// PrometheusTransporter is a stub for future Prometheus metrics export.
type PrometheusTransporter struct {
	port int
}

// KafkaTransporter is a stub for future Kafka pub-sub export.
type KafkaTransporter struct {
	brokers []string
	apiKey  string
}

// NewTransporter builds a Transporter from configuration.
func NewTransporter(cfg TransporterConfig) (Transporter, error) {
	switch cfg.Type {
	case "webhook":
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("webhook transporter requires endpoint")
		}
		return NewWebhookTransporter(cfg.Endpoint, cfg.APIKey), nil
	case "prometheus":
		return NewPrometheusTransporter(cfg.Port), nil
	case "kafka":
		return NewKafkaTransporter(cfg.Brokers, cfg.APIKey), nil
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

// NewPrometheusTransporter creates a stub Prometheus transporter.
func NewPrometheusTransporter(port int) *PrometheusTransporter {
	return &PrometheusTransporter{port: port}
}

// NewKafkaTransporter creates a stub Kafka transporter.
func NewKafkaTransporter(brokers []string, apiKey string) *KafkaTransporter {
	return &KafkaTransporter{
		brokers: append([]string(nil), brokers...),
		apiKey:  apiKey,
	}
}

// AttachTransporter subscribes the transporter to all bus events.
func AttachTransporter(bus *eventbus.Bus, t Transporter) {
	if bus == nil || t == nil {
		return
	}
	attachHandler(bus, func(event eventbus.Event) {
		_ = t.Emit(event)
	})
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

func (t *PrometheusTransporter) Emit(event eventbus.Event) error {
	return nil
}

func (t *KafkaTransporter) Emit(event eventbus.Event) error {
	return nil
}

func attachHandler(bus *eventbus.Bus, handler func(eventbus.Event)) {
	for _, eventType := range eventbus.AllEventTypes() {
		bus.Subscribe(eventType, handler)
	}
}
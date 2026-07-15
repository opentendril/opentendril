package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
)

// Kafka REST Proxy media types (Confluent REST Proxy v2 embedded-JSON format).
const (
	kafkaJSONContentType = "application/vnd.kafka.json.v2+json"
	kafkaAcceptType      = "application/vnd.kafka.v2+json"
	defaultKafkaTopic    = "opentendril-telemetry"
)

// KafkaTransporter pumps EventBus events into a Kafka topic through a Kafka
// REST Proxy endpoint (first slice).
//
// Design constraint: the Stem binary stays free of external dependencies
// (the same rule that produced the stdlib-only Webhook and Prometheus
// transporters), and a from-scratch implementation of the Kafka broker wire
// protocol is not a responsible amount of code to carry for telemetry. The
// REST Proxy protocol is plain HTTP+JSON, so this transporter covers Kafka
// natively for any deployment running Confluent REST Proxy (or a compatible
// gateway) with zero new dependencies. Direct broker support, if it is ever
// wanted, is an opt-in follow-up discussed (build tags).
type KafkaTransporter struct {
	produceURL string
	apiKey     string
	client     *http.Client
}

// kafkaProduceRequest is the REST Proxy v2 produce envelope.
type kafkaProduceRequest struct {
	Records []kafkaRecord `json:"records"`
}

// kafkaRecord carries one event. The key is the session ID when present
// (falling back to the event type), so all events of one session land on one
// partition and stay ordered.
type kafkaRecord struct {
	Key   string         `json:"key,omitempty"`
	Value eventbus.Event `json:"value"`
}

// NewKafkaTransporter builds a Kafka transporter from configuration.
//
// It requires `endpoint` (the REST Proxy base URL, e.g. http://proxy:8082).
// A brokers-only configuration is rejected loudly: the native broker wire
// protocol is not implemented, and silently accepting the config — as the
// earlier stub did — meant silently discarding telemetry.
func NewKafkaTransporter(cfg TransporterConfig) (*KafkaTransporter, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		if len(cfg.Brokers) > 0 {
			return nil, fmt.Errorf("kafka transporter: direct broker connections (brokers: %v) are not supported — the Stem carries no Kafka client dependency; set `endpoint` to a Kafka REST Proxy URL, or ship resin.log / the webhook transporter through a log shipper", cfg.Brokers)
		}
		return nil, fmt.Errorf("kafka transporter requires endpoint (Kafka REST Proxy base URL)")
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("kafka transporter: parse endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("kafka transporter: endpoint must be an http(s) REST Proxy URL, got %q", endpoint)
	}

	topic := strings.TrimSpace(cfg.Channel)
	if topic == "" {
		topic = defaultKafkaTopic
	}

	return &KafkaTransporter{
		produceURL: strings.TrimSuffix(endpoint, "/") + "/topics/" + url.PathEscape(topic),
		apiKey:     cfg.APIKey,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

// Emit produces the event to the configured topic via the REST Proxy.
func (t *KafkaTransporter) Emit(event eventbus.Event) error {
	key := strings.TrimSpace(event.SessionID)
	if key == "" {
		key = string(event.Type)
	}

	payload, err := json.Marshal(kafkaProduceRequest{
		Records: []kafkaRecord{{Key: key, Value: event}},
	})
	if err != nil {
		return fmt.Errorf("encode kafka produce payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, t.produceURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create kafka produce request: %w", err)
	}
	req.Header.Set("Content-Type", kafkaJSONContentType)
	req.Header.Set("Accept", kafkaAcceptType)
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("post kafka produce payload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kafka rest proxy returned status %d", resp.StatusCode)
	}
	return nil
}

package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
)

func TestNewKafkaTransporterRejectsBrokersOnlyConfig(t *testing.T) {
	_, err := NewKafkaTransporter(TransporterConfig{
		Type:    "kafka",
		Brokers: []string{"broker-1:9092", "broker-2:9092"},
	})
	if err == nil {
		t.Fatal("expected brokers-only config to be rejected (the pre-#141 stub silently discarded events)")
	}
	if !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("error should steer the operator toward the REST Proxy endpoint, got: %v", err)
	}
}

func TestNewKafkaTransporterRequiresEndpoint(t *testing.T) {
	if _, err := NewKafkaTransporter(TransporterConfig{Type: "kafka"}); err == nil {
		t.Fatal("expected empty config to be rejected")
	}
}

func TestNewKafkaTransporterRejectsNonHTTPEndpoint(t *testing.T) {
	if _, err := NewKafkaTransporter(TransporterConfig{Type: "kafka", Endpoint: "kafka://broker:9092"}); err == nil {
		t.Fatal("expected non-http endpoint to be rejected")
	}
}

func TestKafkaTransporterEmitProducesToTopic(t *testing.T) {
	type received struct {
		path        string
		contentType string
		accept      string
		auth        string
		body        []byte
	}
	got := make(chan received, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			accept:      r.Header.Get("Accept"),
			auth:        r.Header.Get("Authorization"),
			body:        body,
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transporter, err := NewKafkaTransporter(TransporterConfig{
		Type:     "kafka",
		Endpoint: server.URL,
		Channel:  "stem-events",
		APIKey:   "secret-key",
	})
	if err != nil {
		t.Fatalf("NewKafkaTransporter: %v", err)
	}

	event := eventbus.Event{
		Type:      eventbus.EventType("sprout-emerged"),
		Timestamp: time.Unix(1700000000, 0).UTC(),
		Source:    "orchestrator",
		SessionID: "session-42",
		Data:      map[string]interface{}{"branch": "core"},
	}

	if err := transporter.Emit(event); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	req := <-got
	if req.path != "/topics/stem-events" {
		t.Fatalf("produce path = %q, want /topics/stem-events", req.path)
	}
	if req.contentType != kafkaJSONContentType {
		t.Fatalf("content type = %q, want %q", req.contentType, kafkaJSONContentType)
	}
	if req.accept != kafkaAcceptType {
		t.Fatalf("accept = %q, want %q", req.accept, kafkaAcceptType)
	}
	if req.auth != "Bearer secret-key" {
		t.Fatalf("authorization = %q, want bearer token", req.auth)
	}

	var envelope struct {
		Records []struct {
			Key   string         `json:"key"`
			Value eventbus.Event `json:"value"`
		} `json:"records"`
	}
	if err := json.Unmarshal(req.body, &envelope); err != nil {
		t.Fatalf("decode produce body: %v (body: %s)", err, req.body)
	}
	if len(envelope.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(envelope.Records))
	}
	record := envelope.Records[0]
	if record.Key != "session-42" {
		t.Fatalf("record key = %q, want the session ID for partition ordering", record.Key)
	}
	if record.Value.Type != event.Type || record.Value.Source != event.Source || record.Value.SessionID != event.SessionID {
		t.Fatalf("record value round-trip mismatch: %+v", record.Value)
	}
}

func TestKafkaTransporterEmitKeyFallsBackToEventType(t *testing.T) {
	bodyCh := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyCh <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transporter, err := NewKafkaTransporter(TransporterConfig{Type: "kafka", Endpoint: server.URL})
	if err != nil {
		t.Fatalf("NewKafkaTransporter: %v", err)
	}

	if err := transporter.Emit(eventbus.Event{Type: eventbus.EventType("phenotypic-selection")}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var envelope kafkaProduceRequest
	if err := json.Unmarshal(<-bodyCh, &envelope); err != nil {
		t.Fatalf("decode produce body: %v", err)
	}
	if len(envelope.Records) != 1 || envelope.Records[0].Key != "phenotypic-selection" {
		t.Fatalf("session-less event should key by event type, got %+v", envelope.Records)
	}
}

func TestKafkaTransporterEmitDefaultTopic(t *testing.T) {
	pathCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathCh <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transporter, err := NewKafkaTransporter(TransporterConfig{Type: "kafka", Endpoint: server.URL + "/"})
	if err != nil {
		t.Fatalf("NewKafkaTransporter: %v", err)
	}
	if err := transporter.Emit(eventbus.Event{Type: eventbus.EventType("sprout-emerged")}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if path := <-pathCh; path != "/topics/"+defaultKafkaTopic {
		t.Fatalf("produce path = %q, want default topic", path)
	}
}

func TestKafkaTransporterEmitSurfacesProxyErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "topic not found", http.StatusNotFound)
	}))
	defer server.Close()

	transporter, err := NewKafkaTransporter(TransporterConfig{Type: "kafka", Endpoint: server.URL})
	if err != nil {
		t.Fatalf("NewKafkaTransporter: %v", err)
	}
	if err := transporter.Emit(eventbus.Event{Type: eventbus.EventType("sprout-emerged")}); err == nil {
		t.Fatal("expected non-2xx proxy response to surface as an error")
	}
}

func TestNewTransporterBuildsKafka(t *testing.T) {
	transporter, err := NewTransporter(TransporterConfig{
		Type:     "kafka",
		Endpoint: "http://rest-proxy:8082",
	})
	if err != nil {
		t.Fatalf("NewTransporter(kafka): %v", err)
	}
	if _, ok := transporter.(*KafkaTransporter); !ok {
		t.Fatalf("NewTransporter(kafka) = %T, want *KafkaTransporter", transporter)
	}
}

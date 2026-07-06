package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opentendril/core/cmd/stem/internal/eventbus"
)

func TestHandleWebSocketForwardsAllEventTypes(t *testing.T) {
	bus := eventbus.New()
	server := httptest.NewServer(HandleWebSocket(bus))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	_, connectedPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read connected message: %v", err)
	}

	var connected map[string]string
	if err := json.Unmarshal(connectedPayload, &connected); err != nil {
		t.Fatalf("decode connected message: %v", err)
	}
	if connected["type"] != "connected" {
		t.Fatalf("connected type = %q, want connected", connected["type"])
	}

	bus.Publish(eventbus.Event{
		Type:   eventbus.EventSproutEmerged,
		Source: "step-test",
		Data: map[string]interface{}{
			"label": "test-sprout",
		},
	})

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	_, eventPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read event message: %v", err)
	}

	var eventMsg map[string]interface{}
	if err := json.Unmarshal(eventPayload, &eventMsg); err != nil {
		t.Fatalf("decode event message: %v", err)
	}
	if eventMsg["type"] != "sprout-emerged" {
		t.Fatalf("event type = %v, want sprout-emerged", eventMsg["type"])
	}
	if eventMsg["source"] != "step-test" {
		t.Fatalf("event source = %v, want step-test", eventMsg["source"])
	}
}
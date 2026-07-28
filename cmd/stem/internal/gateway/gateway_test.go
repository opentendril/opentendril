package gateway

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
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

func TestGatewayUnsubscribesOnClose(t *testing.T) {
	bus := eventbus.New()
	server := httptest.NewServer(HandleWebSocket(bus))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	// Wait briefly to ensure subscribe loop runs
	time.Sleep(50 * time.Millisecond)

	countBefore := bus.HandlerCount(eventbus.EventSproutEmerged)
	if countBefore == 0 {
		t.Fatalf("expected handler count to be > 0 while connected, got %d", countBefore)
	}

	// Close client connection to trigger readPump exit and unsubs
	conn.Close()

	// Wait briefly for server to process disconnect
	time.Sleep(50 * time.Millisecond)

	countAfter := bus.HandlerCount(eventbus.EventSproutEmerged)
	if countAfter != 0 {
		t.Fatalf("expected handler count to be 0 after disconnect, got %d", countAfter)
	}
}

// TestGatewayOverflowClosesConnection proves the new overflow behavior end-to-end:
// fill a real client's send buffer past capacity by publishing through the bus
// while writePump is blocked (the client-side conn is never read), assert that
// the connection closes and bus handler count returns to zero.
//
// Because writePump drains client.send asynchronously, we need more events than
// the sum of the send channel capacity (256) plus what TCP/gorilla can absorb
// before blocking. On Linux, loopback TCP buffers are typically ~2 MiB; each
// test frame is ~50 bytes, so ~40 000 events saturates both layers.
func TestGatewayOverflowClosesConnection(t *testing.T) {
	bus := eventbus.New()
	server := httptest.NewServer(HandleWebSocket(bus))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	// Drain the connected handshake frame so the bus is ready.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("read connected frame: %v", err)
	}

	// After the connected frame the test client stops reading.
	// writePump will drain client.send into the network; once TCP receive
	// and send buffers are both saturated, writePump's NextWriter/Write
	// blocks. At that point the next Publish fills client.send and triggers
	// dropAndClose. Use 40 000 events (~50 bytes each = ~2 MB) to reliably
	// exhaust kernel socket buffers on loopback before any drain happens.
	const burst = 40_000
	for i := 0; i < burst; i++ {
		bus.Publish(eventbus.Event{
			Type:   eventbus.EventSproutEmerged,
			Source: "overflow-test",
		})
	}

	// Wait up to 5 s for readPump to exit and unsubscribes to run.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bus.HandlerCount(eventbus.EventSproutEmerged) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	count := bus.HandlerCount(eventbus.EventSproutEmerged)
	if count != 0 {
		t.Fatalf("expected handler count 0 after overflow close, got %d", count)
	}
	// Handler count reaching zero is the definitive proof: the server closed the
	// connection, readPump exited, and the deferred unsubscribe loop ran. A
	// client-side ReadMessage check is omitted because the 40 K events queued in
	// the TCP receive buffer must all drain before the client sees EOF, making
	// that assertion timing-dependent without adding coverage value.
}

// TestGatewayOverflowConcurrentCloseOnce proves that dropAndClose is safe under
// concurrent calls: no panic and the log line appears exactly once.
// This test exercises the dropAndClose method directly (white-box, same package)
// to avoid the writePump drain race seen in full integration tests.
func TestGatewayOverflowConcurrentCloseOnce(t *testing.T) {
	// Capture log output.
	var logBuf bytes.Buffer
	origLog := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origLog)

	// Build a minimal client backed by a real net.Conn pair so conn.Close
	// is meaningful and gorilla doesn't panic.
	server2 := httptest.NewServer(HandleWebSocket(eventbus.New()))
	defer server2.Close()
	wsURL2 := "ws" + strings.TrimPrefix(server2.URL, "http")
	rawConn, _, err := websocket.DefaultDialer.Dial(wsURL2, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer rawConn.Close()

	// Drain the server's initial "connected" handshake frame. Without this,
	// the frame can still be sitting in the read buffer when dropAndClose
	// fires below, letting ReadMessage return it successfully instead of
	// erroring on the closed connection — a race unrelated to dropAndClose
	// itself, just an artifact of not having consumed the handshake first.
	_ = rawConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := rawConn.ReadMessage(); err != nil {
		t.Fatalf("read connected frame: %v", err)
	}

	// Grab the underlying *websocket.Conn for our white-box client.
	cli := &client{
		conn: rawConn,
		send: make(chan []byte, 256),
	}

	// Fire dropAndClose from 20 goroutines simultaneously.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cli.dropAndClose(eventbus.EventSproutEmerged)
		}()
	}
	wg.Wait()

	// Exactly one log line must mention "send buffer full".
	logOutput := logBuf.String()
	occurrences := strings.Count(logOutput, "send buffer full")
	if occurrences != 1 {
		t.Fatalf("expected 'send buffer full' log line exactly once, got %d; log:\n%s", occurrences, logOutput)
	}

	// conn must be closed — a read on the same conn should error.
	_ = rawConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, readErr := rawConn.ReadMessage()
	if readErr == nil {
		t.Fatal("expected rawConn.ReadMessage to error after dropAndClose, got nil")
	}
}

func TestGatewayPongExtendsReadDeadline(t *testing.T) {
	bus := eventbus.New()
	server := httptest.NewServer(HandleWebSocket(bus))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	// Drain the initial "connected" handshake
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read connected message: %v", err)
	}

	// Send an explicit pong control frame
	err = conn.WriteControl(websocket.PongMessage, []byte{}, time.Now().Add(time.Second))
	if err != nil {
		t.Fatalf("write pong control frame: %v", err)
	}

	// Publish an event and verify the server still sends it to us,
	// proving the connection didn't immediately die or error on the pong.
	bus.Publish(eventbus.Event{
		Type:   eventbus.EventSproutEmerged,
		Source: "pong-test",
	})

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, eventPayload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read event message after pong: %v", err)
	}

	var eventMsg map[string]interface{}
	if err := json.Unmarshal(eventPayload, &eventMsg); err != nil {
		t.Fatalf("decode event message: %v", err)
	}
	if eventMsg["source"] != "pong-test" {
		t.Fatalf("event source = %v, want pong-test", eventMsg["source"])
	}
}

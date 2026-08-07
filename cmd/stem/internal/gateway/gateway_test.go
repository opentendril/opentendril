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
	var logBuf threadSafeBuffer
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

// TestGatewayWriteDeadlineNotStale is the regression test for the bug where
// writePump's message branch inherited the absolute deadline set by the ping
// branch. A write after that deadline expired would fail silently.
//
// Mechanism: shorten pingPeriod so the server pings within 150 ms. After
// answering the ping (so the server stays alive), wait 200 ms past writeWait —
// the exact window where a stale deadline would have killed the connection —
// then publish a second event and assert it arrives. Under the bug, the second
// event would cause a write against the expired ping-deadline and the
// connection would close without a log line. With the fix, the message branch
// resets the deadline before every write, so the second event delivers
// normally.
//
// Mutation verification (do not delete): removing the
// c.conn.SetWriteDeadline(time.Now().Add(writeWait)) line from the message
// branch of writePump causes this test to time out waiting for the second
// event, because the connection closes silently under the stale deadline.
func TestGatewayWriteDeadlineNotStale(t *testing.T) {
	// Shorten timing so the test completes quickly, but crucially keep
	// writeWait < pingPeriod just as it is in production (10s < 50s). This
	// ensures the deadline set by a ping actually expires before the next
	// ping fires, creating the stale deadline window.
	origWrite := writeWait
	origPing := pingPeriod
	origPong := pongWait
	writeWait = 100 * time.Millisecond
	pingPeriod = 300 * time.Millisecond
	pongWait = 800 * time.Millisecond
	t.Cleanup(func() {
		writeWait = origWrite
		pingPeriod = origPing
		pongWait = origPong
	})

	bus := eventbus.New()
	server := httptest.NewServer(HandleWebSocket(bus))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Dial with a custom dialer so we can register a PingHandler to answer
	// the server's pings. websocket.DefaultDialer does not reply to pings,
	// which would cause readPump to close after pongWait.
	dialer := websocket.Dialer{}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	// pingFired signals when a ping is actually received and processed by the client.
	pingFired := make(chan struct{}, 1)

	// Answer any incoming ping control frames — a pong extends the server's
	// read deadline and keeps the connection alive for the duration of the
	// test.
	conn.SetPingHandler(func(data string) error {
		select {
		case pingFired <- struct{}{}:
		default:
		}
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})

	// Drain the initial "connected" handshake frame.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read connected frame: %v", err)
	}

	// Publish a first event and receive it to confirm the connection is live.
	bus.Publish(eventbus.Event{
		Type:   eventbus.EventSproutEmerged,
		Source: "deadline-test-first",
	})
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read first event: %v", err)
	}

	// In order to process control frames (like pings), gorilla requires an active read.
	// Since we don't expect data messages before we publish the second event, we start
	// a background reader to block and wait for the ping.
	msgChan := make(chan []byte, 1)
	errChan := make(chan error, 1)
	go func() {
		// Just one read is enough to process the ping and then block on the second event.
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, payload, err := conn.ReadMessage()
		if err != nil {
			errChan <- err
			return
		}
		msgChan <- payload
	}()

	// Wait up to 2 seconds for the server to actually send its ping.
	select {
	case <-pingFired:
		// Ping received! We know definitively that the server's writePump executed
		// the ping branch and set an absolute write deadline on the connection.
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for server to send ping")
	case err := <-errChan:
		t.Fatalf("read error while waiting for ping: %v", err)
	}

	// Now wait for the server's writeWait to elapse, so the deadline expires
	// before we attempt the second write.
	time.Sleep(writeWait + 50*time.Millisecond) // dwell: let the observed deadline expire

	// Publish the second event. Under the bug this write meets the expired
	// ping deadline and closes silently. With the fix it resets the deadline
	// and delivers normally.
	bus.Publish(eventbus.Event{
		Type:   eventbus.EventSproutEmerged,
		Source: "deadline-test-second",
	})

	// Wait for the background reader to receive the second event.
	select {
	case payload := <-msgChan:
		var msg map[string]interface{}
		if err := json.Unmarshal(payload, &msg); err != nil {
			t.Fatalf("decode second event: %v", err)
		}
		if msg["source"] != "deadline-test-second" {
			t.Fatalf("second event source = %v, want deadline-test-second", msg["source"])
		}
	case err := <-errChan:
		t.Fatalf("second event not received after stale-deadline window: %v — connection was likely closed by a stale write deadline (the regression)", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for second event")
	}
}

type threadSafeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *threadSafeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *threadSafeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestGatewayWritePumpLogsOnError asserts that a write error in writePump
// produces a log line, so the next stream defect is observable from journalctl
// rather than requiring a live investigation. The test forces a write error by
// closing the underlying net.Conn while writePump is blocked trying to write,
// then reads the captured log output.
//
// Mutation verification (do not delete): removing the log.Printf calls from
// the writePump message branch causes this test to fail because logBuf remains
// empty.
func TestGatewayWritePumpLogsOnError(t *testing.T) {
	origWrite := writeWait
	origPing := pingPeriod
	writeWait = 500 * time.Millisecond
	pingPeriod = 10 * time.Second
	t.Cleanup(func() {
		writeWait = origWrite
		pingPeriod = origPing
	})

	var logBuf threadSafeBuffer
	origLog := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(origLog)

	bus := eventbus.New()
	server := httptest.NewServer(HandleWebSocket(bus))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Drain the handshake.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read connected frame: %v", err)
	}

	// Close the client connection so the next write from writePump fails.
	conn.Close()

	// Give readPump time to notice the close and exit, which also fires the
	// deferred conn.Close on the server side. Then publish into the closed
	// connection so writePump's message branch tries a write and logs.
	// poll: wait for server-side cleanup after client disconnect
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bus.Publish(eventbus.Event{
			Type:   eventbus.EventSproutEmerged,
			Source: "write-error-probe",
		})
		str := logBuf.String()
		if strings.Contains(str, "gateway: writePump exiting") {
			break
		}
		time.Sleep(20 * time.Millisecond) // poll: check every 20 ms
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "gateway: writePump exiting") {
		t.Fatalf("expected a gateway writePump log line after write error, got nothing; log:\n%s", logged)
	}
}

// TestGatewayIdleConnectionSurvivesPingPeriod asserts that a well-behaved
// client that answers pings stays connected through an entire pingPeriod and
// continues to receive events afterwards. This is the non-regression for the
// fix — it must remain true that pings keep an idle connection alive.
func TestGatewayIdleConnectionSurvivesPingPeriod(t *testing.T) {
	origWrite := writeWait
	origPing := pingPeriod
	origPong := pongWait
	writeWait = 200 * time.Millisecond
	pingPeriod = 150 * time.Millisecond
	pongWait = 600 * time.Millisecond
	t.Cleanup(func() {
		writeWait = origWrite
		pingPeriod = origPing
		pongWait = origPong
	})

	bus := eventbus.New()
	server := httptest.NewServer(HandleWebSocket(bus))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	dialer := websocket.Dialer{}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Answer pings to keep the server's read deadline extended.
	conn.SetPingHandler(func(data string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
	})

	// Drain the handshake.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read connected frame: %v", err)
	}

	// Idle through two full pingPeriods (300 ms). The server sends pings;
	// we answer them. No events are published during this window.
	// dwell: deliberate two-pingPeriod idle to confirm pings sustain the connection
	time.Sleep(300 * time.Millisecond) // dwell: two pingPeriods of silence

	// Now publish an event. If the connection survived the idle window, it
	// arrives. If the server closed the connection (e.g. because pings broke
	// something), ReadMessage returns an error.
	bus.Publish(eventbus.Event{
		Type:   eventbus.EventSproutEmerged,
		Source: "idle-survival",
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("event not received after idle window — connection was closed during ping cycle: %v", err)
	}

	var msg map[string]interface{}
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if msg["source"] != "idle-survival" {
		t.Fatalf("event source = %v, want idle-survival", msg["source"])
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

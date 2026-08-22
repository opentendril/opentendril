package gateway

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

type streamScopeKey struct{}

// WithStreamScope narrows a stream to one phytomer. The scope travels in the
// request context and only ever arrives from the surface that authenticated
// and authorised the caller, so a connection cannot widen its own view by
// asking. A blank scope is not stored: an unscoped connection is
// indistinguishable from one that never asked to be narrowed, which is what
// keeps the operator's feed exactly as it was.
func WithStreamScope(ctx context.Context, sessionID string) context.Context {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, streamScopeKey{}, trimmed)
}

// streamScope returns the phytomer a connection is narrowed to, or "" when it
// sees everything.
func streamScope(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	scope, _ := ctx.Value(streamScopeKey{}).(string)
	return scope
}

// maxReplay caps how many buffered bus events a client may request on
// connect via the opt-in ?replay=N query parameter (bounded by the bus's own
// in-memory history window).
const maxReplay = 100

// Timing parameters for the WebSocket keep-alive protocol. Declared as
// package-level vars rather than consts so test code can shorten them to
// exercise the write-deadline and ping paths without waiting 50 s.
var (
	// writeWait is the deadline applied to every outgoing write, both event
	// messages and pings. Matching the two branches keeps them from drifting
	// apart.
	writeWait = 10 * time.Second

	// pingPeriod is how often writePump sends a Ping to keep the connection
	// alive and detect a dead peer. Must be less than pongWait.
	pingPeriod = 50 * time.Second

	// pongWait is how long the server waits for a pong (or any other client
	// frame) before treating the connection as dead. It must exceed
	// pingPeriod so a single round-trip has room to complete.
	pongWait = 60 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for the gateway
	},
}

// client represents a connected WebSocket client. Unexported: nothing outside
// this package constructs or calls it — the sole entrypoint is HandleWebSocket.
type client struct {
	conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
}

// dropAndClose is called exactly once when the client's send buffer is full.
// It logs the overflow event type and closes the underlying connection so
// readPump exits, triggering the deferred unsubscribe loop in HandleWebSocket.
// closeOnce guarantees at most one close even when Publish delivers two
// concurrent overflow events from different goroutines simultaneously.
func (c *client) dropAndClose(eventType eventbus.EventType) {
	c.closeOnce.Do(func() {
		log.Printf("gateway: closing WS client, send buffer full (256) — event type %q could not be delivered", eventType)
		c.conn.Close()
	})
}

func HandleWebSocket(bus *eventbus.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Failed to upgrade WebSocket: %v", err)
			return
		}

		c := &client{
			conn: conn,
			send: make(chan []byte, 256),
		}

		// Capture package-level timing vars so concurrent test cleanups
		// don't race with the background pumps of this connection.
		pWait := pongWait
		wWait := writeWait
		pPeriod := pingPeriod

		conn.SetReadDeadline(time.Now().Add(pWait))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(pWait))
			return nil
		})

		// A narrowed connection sees its own phytomer and nothing else. An
		// event carrying no phytomer at all is dropped rather than shared:
		// telemetry that names no owner belongs to whoever can already see
		// everything.
		scope := streamScope(r.Context())

		handler := func(event eventbus.Event) {
			if scope != "" && event.SessionID != scope {
				return
			}
			msg := map[string]interface{}{
				"type":      string(event.Type),
				"timestamp": event.Timestamp,
				"source":    event.Source,
			}
			if event.SessionID != "" {
				msg["sessionId"] = event.SessionID
			}
			if len(event.Data) > 0 {
				msg["data"] = event.Data
			}
			if event.Type == eventbus.EventStreamToken {
				if token, ok := event.Data["token"]; ok {
					msg["content"] = token
				}
			}
			payload, err := json.Marshal(msg)
			if err != nil {
				return
			}
			select {
			case c.send <- payload:
			default:
				c.dropAndClose(event.Type)
			}
		}

		unsubs := make([]func(), 0, len(eventbus.AllEventTypes()))
		for _, eventType := range eventbus.AllEventTypes() {
			unsubs = append(unsubs, bus.Subscribe(eventType, handler))
		}
		defer func() {
			for _, unsub := range unsubs {
				unsub()
			}
		}()

		// Send connected message
		connectedMsg, _ := json.Marshal(map[string]string{"type": "connected"})
		c.send <- connectedMsg

		// Opt-in replay: ?replay=N asks for the bus's recent in-memory event
		// history before the live feed, so a refreshed client can re-grow
		// state that never carried a session id (e.g. sequence telemetry).
		if raw := r.URL.Query().Get("replay"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				if n > maxReplay {
					n = maxReplay
				}
				for _, event := range bus.History(n) {
					handler(event)
				}
			}
		}

		// Start write pump
		go c.writePump(pPeriod, wWait)
		// Start read pump
		c.readPump()
	}
}

func (c *client) readPump() {
	defer func() {
		c.conn.Close()
	}()
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("gateway: readPump exiting on unexpected close: %v", err)
			} else {
				log.Printf("gateway: readPump exiting: %v", err)
			}
			break
		}
		// Handle incoming messages if needed
		_ = message
	}
}

func (c *client) writePump(pPeriod, wWait time.Duration) {
	ticker := time.NewTicker(pPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				log.Printf("gateway: writePump exiting on send channel closed")
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(wWait))
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				log.Printf("gateway: writePump exiting on NextWriter error: %v", err)
				return
			}
			if _, err := w.Write(message); err != nil {
				log.Printf("gateway: writePump exiting on write error: %v", err)
				return
			}
			if err := w.Close(); err != nil {
				log.Printf("gateway: writePump exiting on writer close error: %v", err)
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(wWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("gateway: writePump exiting on ping error: %v", err)
				return
			}
		}
	}
}

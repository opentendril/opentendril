package gateway

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// maxReplay caps how many buffered bus events a client may request on
// connect via the opt-in ?replay=N query parameter (bounded by the bus's own
// in-memory history window).
const maxReplay = 100

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for the gateway
	},
}

// Client represents a connected WebSocket client
type Client struct {
	conn      *websocket.Conn
	send      chan []byte
	closeOnce sync.Once
}

// dropAndClose is called exactly once when the client's send buffer is full.
// It logs the overflow event type and closes the underlying connection so
// readPump exits, triggering the deferred unsubscribe loop in HandleWebSocket.
// closeOnce guarantees at most one close even when Publish delivers two
// concurrent overflow events from different goroutines simultaneously.
func (c *Client) dropAndClose(eventType eventbus.EventType) {
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

		client := &Client{
			conn: conn,
			send: make(chan []byte, 256),
		}

		handler := func(event eventbus.Event) {
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
			if event.Type == eventbus.EventThoughtBranch {
				if thought, ok := event.Data["thought"]; ok {
					msg["content"] = thought
				}
			}
			payload, err := json.Marshal(msg)
			if err != nil {
				return
			}
			select {
			case client.send <- payload:
			default:
				client.dropAndClose(event.Type)
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
		client.send <- connectedMsg

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
		go client.writePump()
		// Start read pump
		client.readPump()
	}
}

func (c *Client) readPump() {
	defer func() {
		c.conn.Close()
	}()
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		// Handle incoming messages if needed
		_ = message
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(50 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)
			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

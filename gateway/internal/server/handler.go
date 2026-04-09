package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opentendril/gateway/internal/protocol"
	"github.com/opentendril/gateway/internal/proxy"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins in dev; restrict in production
	},
}

// Handler holds the dependencies for WebSocket request handling.
type Handler struct {
	Hub   *Hub
	Brain *proxy.BrainClient
}

// ServeWS upgrades an HTTP request to a WebSocket connection.
func (h *Handler) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("❌ WebSocket upgrade failed: %v", err)
		return
	}

	clientID := fmt.Sprintf("ws-%d", time.Now().UnixNano())
	client := &Client{
		Hub:       h.Hub,
		ID:        clientID,
		SessionID: "default",
		Source:    string(protocol.SourceWeb),
		Send:      make(chan []byte, 64),
		Done:      make(chan struct{}),
	}

	h.Hub.Register(client)

	// Send connected confirmation
	h.sendJSON(client, protocol.OutgoingMessage{
		Type:  protocol.TypeConnected,
		RunID: clientID,
	})

	// Start read/write pumps
	go h.writePump(conn, client)
	go h.readPump(conn, client)
}

// readPump reads messages from the WebSocket and processes them.
func (h *Handler) readPump(conn *websocket.Conn, client *Client) {
	defer func() {
		h.Hub.Unregister(client)
		conn.Close()
	}()

	conn.SetReadLimit(32768) // 32KB max message
	conn.SetReadDeadline(time.Now().Add(300 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(300 * time.Second))
		return nil
	})

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("⚠️  WebSocket read error: %v", err)
			}
			return
		}

		var incoming protocol.IncomingMessage
		if err := json.Unmarshal(message, &incoming); err != nil {
			h.sendJSON(client, protocol.OutgoingMessage{
				Type:  protocol.TypeError,
				Error: "Invalid message format: " + err.Error(),
			})
			continue
		}

		// Update session if provided
		if incoming.SessionID != "" {
			client.SessionID = incoming.SessionID
		}

		switch incoming.Type {
		case protocol.TypePing:
			h.sendJSON(client, protocol.OutgoingMessage{Type: protocol.TypePong})

		case protocol.TypeMessage:
			h.handleMessage(client, incoming)

		case protocol.TypeStop:
			// Future: cancel in-flight LLM request
			log.Printf("⏹  Stop requested for run %s", incoming.RunID)

		default:
			h.sendJSON(client, protocol.OutgoingMessage{
				Type:  protocol.TypeError,
				Error: "Unknown message type: " + incoming.Type,
			})
		}
	}
}

// handleMessage processes a chat message: proxies to brain, streams response.
func (h *Handler) handleMessage(client *Client, msg protocol.IncomingMessage) {
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())

	// Signal stream start
	h.sendJSON(client, protocol.OutgoingMessage{
		Type:  protocol.TypeStreamStart,
		RunID: runID,
	})

	// Determine provider
	provider := msg.Provider
	if provider == "" || provider == "default" {
		provider = "default"
	}

	// Call brain
	log.Printf("🧠 [%s] Proxying to brain: session=%s provider=%s msg=%q",
		runID, client.SessionID, provider, truncate(msg.Content, 80))

	response, err := h.Brain.Chat(client.SessionID, msg.Content, provider)
	if err != nil {
		log.Printf("❌ [%s] Brain error: %v", runID, err)
		h.sendJSON(client, protocol.OutgoingMessage{
			Type:  protocol.TypeError,
			RunID: runID,
			Error: "Failed to process message: " + err.Error(),
		})
		return
	}

	// Stream response word-by-word over WebSocket
	words := strings.Fields(response)
	for i, word := range words {
		token := word
		if i < len(words)-1 {
			token += " "
		}
		h.sendJSON(client, protocol.OutgoingMessage{
			Type:    protocol.TypeStreamToken,
			Content: token,
			RunID:   runID,
		})
		time.Sleep(10 * time.Millisecond) // 10ms per word for UX feel
	}

	// Signal stream end with full response
	h.sendJSON(client, protocol.OutgoingMessage{
		Type:    protocol.TypeStreamEnd,
		Content: response,
		RunID:   runID,
	})

	log.Printf("✅ [%s] Response sent: %d words", runID, len(words))
}

// writePump writes messages from the send channel to the WebSocket.
func (h *Handler) writePump(conn *websocket.Conn, client *Client) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.Send:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("⚠️  WebSocket write error: %v", err)
				return
			}

		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-client.Done:
			return
		}
	}
}

// sendJSON marshals and sends a message to a single client.
func (h *Handler) sendJSON(client *Client, msg protocol.OutgoingMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("❌ JSON marshal error: %v", err)
		return
	}
	select {
	case client.Send <- data:
	default:
		log.Printf("⚠️  Client %s send buffer full, dropping message", client.ID)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

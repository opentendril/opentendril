// Package server implements the WebSocket gateway server.
package server

import (
	"log"
	"sync"
)

// Client represents a single WebSocket connection to the gateway.
type Client struct {
	Hub       *Hub
	ID        string
	SessionID string
	Source    string
	Send      chan []byte
	Done      chan struct{}
}

// Hub maintains the set of active clients and broadcasts messages.
type Hub struct {
	mu         sync.RWMutex
	clients    map[string]*Client // keyed by client ID
	sessions   map[string][]*Client // keyed by session ID (multiple devices)
}

// NewHub creates an empty connection hub.
func NewHub() *Hub {
	return &Hub{
		clients:  make(map[string]*Client),
		sessions: make(map[string][]*Client),
	}
}

// Register adds a client to the hub.
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c.ID] = c
	h.sessions[c.SessionID] = append(h.sessions[c.SessionID], c)
	log.Printf("🔌 Client connected: %s (session=%s, source=%s) [%d total]",
		c.ID, c.SessionID, c.Source, len(h.clients))
}

// Unregister removes a client from the hub and closes its send channel.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c.ID]; ok {
		delete(h.clients, c.ID)
		close(c.Send)

		// Remove from session list
		clients := h.sessions[c.SessionID]
		for i, sc := range clients {
			if sc.ID == c.ID {
				h.sessions[c.SessionID] = append(clients[:i], clients[i+1:]...)
				break
			}
		}
		if len(h.sessions[c.SessionID]) == 0 {
			delete(h.sessions, c.SessionID)
		}
		log.Printf("🔌 Client disconnected: %s [%d remaining]", c.ID, len(h.clients))
	}
}

// ClientCount returns the number of active connections.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// SessionCount returns the number of active sessions.
func (h *Hub) SessionCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.sessions)
}

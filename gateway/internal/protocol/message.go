// Package protocol defines the Unified Message Object — the canonical
// format that all chat channels (Web UI, CLI, Slack, Discord, SaaS)
// normalize into before reaching the Brain.
package protocol

import "time"

// Source identifies which channel a message originated from.
type Source string

const (
	SourceWeb      Source = "web"
	SourceCLI      Source = "cli"
	SourceSaaS     Source = "saas"
	SourceSlack    Source = "slack"
	SourceDiscord  Source = "discord"
	SourceTelegram Source = "telegram"
	SourceAPI      Source = "api"
)

// --- Client → Gateway ---

// IncomingMessage is what clients send over WebSocket.
type IncomingMessage struct {
	Type      string `json:"type"`                 // "message", "stop", "ping"
	Content   string `json:"content,omitempty"`     // User's message text
	Provider  string `json:"provider,omitempty"`    // LLM provider preference
	SessionID string `json:"session_id,omitempty"`  // Persistent session
	RunID     string `json:"run_id,omitempty"`      // For stop/cancel commands
}

// --- Gateway → Client ---

// OutgoingMessage is what the gateway sends back over WebSocket.
type OutgoingMessage struct {
	Type    string `json:"type"`              // "stream.start", "stream.token", "stream.end", "error", "pong"
	Content string `json:"content,omitempty"` // Token text or full response
	RunID   string `json:"run_id,omitempty"`  // Correlation ID
	Error   string `json:"error,omitempty"`   // Error message if type=="error"
}

// Message types
const (
	TypeMessage     = "message"
	TypeStop        = "stop"
	TypePing        = "ping"
	TypePong        = "pong"
	TypeStreamStart = "stream.start"
	TypeStreamToken = "stream.token"
	TypeStreamEnd   = "stream.end"
	TypeError       = "error"
	TypeConnected   = "connected"
)

// --- Internal: Unified Message Object ---

// UnifiedMessage is the normalized representation used internally
// between the gateway and the brain. Every channel adapter produces this.
type UnifiedMessage struct {
	ID        string            `json:"id"`
	Source    Source            `json:"source"`
	ChannelID string           `json:"channel_id,omitempty"`
	ThreadID  string           `json:"thread_id,omitempty"`
	UserID    string           `json:"user_id,omitempty"`
	UserName  string           `json:"user_name,omitempty"`
	Content   string           `json:"content"`
	Provider  string           `json:"provider,omitempty"`
	SessionID string           `json:"session_id"`
	Metadata  map[string]any   `json:"metadata,omitempty"`
	Timestamp time.Time        `json:"timestamp"`
}

// UnifiedResponse is what the brain returns, before platform-specific formatting.
type UnifiedResponse struct {
	ReplyTo string `json:"reply_to"`
	Content string `json:"content"`
	RunID   string `json:"run_id,omitempty"`
}

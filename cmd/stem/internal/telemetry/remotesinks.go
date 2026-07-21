package telemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

const (
	// EnvRemoteSinks configures remote EventBus transporters as a
	// comma-separated list of sink URLs, e.g.
	//   OPENTENDRIL_REMOTE_SINKS=redis://:pass@localhost:6379/opentendril-events,wss://fleet.example.com/ingest
	// Supported schemes: redis://, ws://, wss://, http://, https:// (webhook).
	EnvRemoteSinks = "OPENTENDRIL_REMOTE_SINKS"

	defaultRedisChannel = "opentendril-events"
	remoteDialTimeout   = 5 * time.Second
)

// RedisTransporter PUBLISHes JSON event payloads to a Redis channel so a
// centralized OS can monitor a Genet of distributed Ramets. It
// speaks raw RESP over a persistent connection — no client dependency — and
// redials lazily after a failure.
type RedisTransporter struct {
	addr     string
	channel  string
	password string

	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
}

// NewRedisTransporter creates a Redis-backed transporter.
func NewRedisTransporter(addr, channel, password string) *RedisTransporter {
	if strings.TrimSpace(channel) == "" {
		channel = defaultRedisChannel
	}
	return &RedisTransporter{
		addr:     addr,
		channel:  channel,
		password: password,
	}
}

func (t *RedisTransporter) Emit(event eventbus.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode redis payload: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.ensureConn(); err != nil {
		return err
	}
	if err := t.publish(payload); err != nil {
		// Drop the broken connection; the next Emit redials.
		t.closeConn()
		return err
	}
	return nil
}

func (t *RedisTransporter) ensureConn() error {
	if t.conn != nil {
		return nil
	}

	conn, err := net.DialTimeout("tcp", t.addr, remoteDialTimeout)
	if err != nil {
		return fmt.Errorf("dial redis %s: %w", t.addr, err)
	}
	t.conn = conn
	t.reader = bufio.NewReader(conn)

	if t.password != "" {
		if err := t.command("AUTH", t.password); err != nil {
			t.closeConn()
			return fmt.Errorf("redis auth: %w", err)
		}
	}
	return nil
}

func (t *RedisTransporter) publish(payload []byte) error {
	if err := t.command("PUBLISH", t.channel, string(payload)); err != nil {
		return fmt.Errorf("redis publish: %w", err)
	}
	return nil
}

// command sends one RESP array command and consumes a single reply line
// (simple string, error, or integer — sufficient for AUTH and PUBLISH).
func (t *RedisTransporter) command(parts ...string) error {
	var builder strings.Builder
	builder.WriteString("*" + strconv.Itoa(len(parts)) + "\r\n")
	for _, part := range parts {
		builder.WriteString("$" + strconv.Itoa(len(part)) + "\r\n")
		builder.WriteString(part)
		builder.WriteString("\r\n")
	}

	deadline := time.Now().Add(remoteDialTimeout)
	_ = t.conn.SetDeadline(deadline)
	if _, err := t.conn.Write([]byte(builder.String())); err != nil {
		return err
	}

	reply, err := t.reader.ReadString('\n')
	if err != nil {
		return err
	}
	if strings.HasPrefix(reply, "-") {
		return fmt.Errorf("redis error: %s", strings.TrimSpace(reply[1:]))
	}
	return nil
}

func (t *RedisTransporter) closeConn() {
	if t.conn != nil {
		_ = t.conn.Close()
		t.conn = nil
		t.reader = nil
	}
}

// RemoteWebSocketTransporter streams JSON events to a remote WebSocket
// collector (the centralized OS). The connection is dialed lazily and
// re-established on the next Emit after a failure.
type RemoteWebSocketTransporter struct {
	endpoint string
	apiKey   string

	mu   sync.Mutex
	conn *websocket.Conn
}

// NewRemoteWebSocketTransporter creates a remote WebSocket transporter.
func NewRemoteWebSocketTransporter(endpoint, apiKey string) *RemoteWebSocketTransporter {
	return &RemoteWebSocketTransporter{
		endpoint: endpoint,
		apiKey:   apiKey,
	}
}

func (t *RemoteWebSocketTransporter) Emit(event eventbus.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.conn == nil {
		header := map[string][]string{}
		if t.apiKey != "" {
			header["Authorization"] = []string{"Bearer " + t.apiKey}
		}
		dialer := websocket.Dialer{HandshakeTimeout: remoteDialTimeout}
		conn, _, err := dialer.Dial(t.endpoint, header)
		if err != nil {
			return fmt.Errorf("dial remote websocket %s: %w", t.endpoint, err)
		}
		t.conn = conn
	}

	_ = t.conn.SetWriteDeadline(time.Now().Add(remoteDialTimeout))
	if err := t.conn.WriteJSON(event); err != nil {
		_ = t.conn.Close()
		t.conn = nil
		return fmt.Errorf("write remote websocket event: %w", err)
	}
	return nil
}

// TransportersFromEnv builds transporters from OPENTENDRIL_REMOTE_SINKS.
// Malformed entries are returned as errors alongside the valid transporters
// so one bad sink never disables the rest.
func TransportersFromEnv() ([]Transporter, []error) {
	raw := strings.TrimSpace(os.Getenv(EnvRemoteSinks))
	if raw == "" {
		return nil, nil
	}

	var transporters []Transporter
	var errs []error
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		transporter, err := ParseRemoteSink(entry)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		transporters = append(transporters, transporter)
	}
	return transporters, errs
}

// ParseRemoteSink converts one sink URL into a Transporter.
func ParseRemoteSink(raw string) (Transporter, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse remote sink %q: %w", raw, err)
	}

	switch strings.ToLower(parsed.Scheme) {
	case "redis", "rediss":
		host := parsed.Host
		if host == "" {
			return nil, fmt.Errorf("remote sink %q: redis sink requires host:port", raw)
		}
		if parsed.Port() == "" {
			host = net.JoinHostPort(host, "6379")
		}
		password := ""
		if parsed.User != nil {
			password, _ = parsed.User.Password()
			if password == "" {
				// redis://password@host form (no username separator)
				password = parsed.User.Username()
			}
		}
		channel := strings.Trim(parsed.Path, "/")
		return NewRedisTransporter(host, channel, password), nil

	case "ws", "wss":
		apiKey := ""
		endpoint := *parsed
		if parsed.User != nil {
			apiKey, _ = parsed.User.Password()
			if apiKey == "" {
				apiKey = parsed.User.Username()
			}
			endpoint.User = nil
		}
		return NewRemoteWebSocketTransporter(endpoint.String(), apiKey), nil

	case "http", "https":
		return NewWebhookTransporter(raw, ""), nil

	default:
		return nil, fmt.Errorf("remote sink %q: unsupported scheme %q", raw, parsed.Scheme)
	}
}

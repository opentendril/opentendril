package eventbus

import (
	"sync"
	"time"
)

const maxHistory = 100

type EventType string

const (
	EventHealthCheck       EventType = "health-check"
	EventHealthDegraded    EventType = "health-degraded"
	EventHealthRecovered   EventType = "health-recovered"
	EventTerrariumOOM      EventType = "terrarium-oom"
	EventTerrariumTimeout  EventType = "terrarium-timeout"
	EventAPIKeyInvalid     EventType = "api-key-invalid"
	EventSequenceFailure   EventType = "sequence-failure"
	EventSequenceComplete  EventType = "sequence-complete"
	EventStreamToken       EventType = "stream-token"
	EventThoughtBranch     EventType = "thought-branch"
	EventSproutEmerged     EventType = "sprout-emerged"
	EventSproutMatured     EventType = "sprout-matured"
	EventSproutWithered    EventType = "sprout-withered"
	EventHormonalTrigger   EventType = "hormonal-trigger"
	EventRhizomeUpdate     EventType = "rhizome-update"
	EventXylemTransport    EventType = "xylem-transport"
	EventParallelSprouting EventType = "parallel-sprouting"
	EventMycelialMerge     EventType = "mycelial-merge"
	// EventPhenotypicSelection reports Genetic Algorithm progress (start,
	// generation, evaluated, complete phases) from the selection runner.
	EventPhenotypicSelection EventType = "phenotypic-selection"
)

// AllEventTypes returns every registered event type for broad telemetry subscriptions.
func AllEventTypes() []EventType {
	return []EventType{
		EventHealthCheck,
		EventHealthDegraded,
		EventHealthRecovered,
		EventTerrariumOOM,
		EventTerrariumTimeout,
		EventAPIKeyInvalid,
		EventSequenceFailure,
		EventSequenceComplete,
		EventStreamToken,
		EventThoughtBranch,
		EventSproutEmerged,
		EventSproutMatured,
		EventSproutWithered,
		EventHormonalTrigger,
		EventRhizomeUpdate,
		EventXylemTransport,
		EventParallelSprouting,
		EventMycelialMerge,
		EventPhenotypicSelection,
	}
}

type Event struct {
	Type      EventType              `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Source    string                 `json:"source"`
	SessionID string                 `json:"sessionId,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

type Handler func(Event)

// Sink consumes every event published to the Bus, regardless of type.
// Sinks are the pluggable transport boundary of the bus: local persistence
// (history.db), the Resin trace log, and remote transporters (Redis, Kafka,
// remote WebSockets) all attach as sinks. Each sink drains its own buffered
// channel on a dedicated goroutine, so a slow or disconnected sink can never
// block Publish.
type Sink interface {
	Consume(Event)
}

const defaultSinkBuffer = 1024

type sinkPump struct {
	events chan Event
	done   chan struct{}
}

type Bus struct {
	mu       sync.RWMutex
	handlers map[EventType][]Handler
	history  []Event
	sinks    []*sinkPump
	closed   bool
}

func New() *Bus {
	return &Bus{
		handlers: make(map[EventType][]Handler),
		history:  make([]Event, 0, maxHistory),
	}
}

// AttachSink registers a sink that receives every published event
// asynchronously. buffer <= 0 selects the default buffer size. When a sink's
// buffer is full the event is dropped for that sink only; telemetry is lossy
// by design so the orchestrator hot path never blocks.
func (b *Bus) AttachSink(sink Sink, buffer int) {
	if b == nil || sink == nil {
		return
	}
	if buffer <= 0 {
		buffer = defaultSinkBuffer
	}

	pump := &sinkPump{
		events: make(chan Event, buffer),
		done:   make(chan struct{}),
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.sinks = append(b.sinks, pump)
	b.mu.Unlock()

	go func() {
		defer close(pump.done)
		for event := range pump.events {
			sink.Consume(event)
		}
	}()
}

// Shutdown stops all sink pumps and waits for them to drain their buffers.
func (b *Bus) Shutdown() {
	if b == nil {
		return
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	sinks := b.sinks
	b.sinks = nil
	b.mu.Unlock()

	for _, pump := range sinks {
		close(pump.events)
		<-pump.done
	}
}

func (b *Bus) Subscribe(eventType EventType, handler Handler) {
	if b == nil || handler == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

func (b *Bus) Publish(event Event) {
	if b == nil {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	b.mu.Lock()
	b.history = append(b.history, event)
	if len(b.history) > maxHistory {
		copy(b.history, b.history[len(b.history)-maxHistory:])
		b.history = b.history[:maxHistory]
	}
	handlers := append([]Handler(nil), b.handlers[event.Type]...)
	b.mu.Unlock()

	for _, handler := range handlers {
		handler(event)
	}

	// Send to sinks under the read lock: Shutdown takes the write lock before
	// closing pump channels, so no send can race a close.
	b.mu.RLock()
	if !b.closed {
		for _, pump := range b.sinks {
			select {
			case pump.events <- event:
			default:
			}
		}
	}
	b.mu.RUnlock()
}

func (b *Bus) History(n int) []Event {
	if b == nil || n <= 0 {
		return nil
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	if n > len(b.history) {
		n = len(b.history)
	}
	start := len(b.history) - n
	return append([]Event(nil), b.history[start:]...)
}

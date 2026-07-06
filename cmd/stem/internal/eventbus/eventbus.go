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
	}
}

type Event struct {
	Type      EventType              `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Source    string                 `json:"source"`
	Data      map[string]interface{} `json:"data,omitempty"`
}

type Handler func(Event)

type Bus struct {
	mu       sync.RWMutex
	handlers map[EventType][]Handler
	history  []Event
}

func New() *Bus {
	return &Bus{
		handlers: make(map[EventType][]Handler),
		history:  make([]Event, 0, maxHistory),
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

package eventbus

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const maxHistory = 100

type EventType string

const (
	EventHealthCheck      EventType = "health-check"
	EventHealthDegraded   EventType = "health-degraded"
	EventHealthRecovered  EventType = "health-recovered"
	EventTerrariumOOM     EventType = "terrarium-oom"
	EventTerrariumTimeout EventType = "terrarium-timeout"
	// EventHostExecutionActivated is published exactly once each time the host
	// terrarium provider is successfully activated. It forms the structured
	// audit trail for a security-relevant event: the deliberate bypass of
	// terrarium isolation (no mount sealing, no network sealing, full
	// host-user permissions). The observer-callback shape keeps the terrarium
	// package free of an eventbus dependency; callers with bus access pass a
	// closure that publishes this event.
	EventHostExecutionActivated EventType = "host-execution-activated"
	EventSequenceFailure        EventType = "sequence-failure"
	EventSequenceComplete       EventType = "sequence-complete"
	// EventSequenceCleanupIncomplete is published when the sequence runner
	// stops waiting for in-flight steps to finish cleaning up. It forms the
	// structured audit trail for a potentially unrestored workspace: if a
	// step's cleanup wedges past the grace period, the runner reports the
	// affected steps and gives up, so a stashed workspace may be left behind.
	EventSequenceCleanupIncomplete EventType = "sequence-cleanup-incomplete"
	EventStreamToken               EventType = "stream-token"
	EventThoughtBranch             EventType = "thought-branch"
	// EventToolInvoked reports one tool call the Pollinator made during a run — the
	// tool name, its arguments, the resulting status, and a truncated
	// observation. Without it a run's actual actions are invisible: a
	// successful sprout that reads, edits, and runs commands otherwise emits
	// only the sprout-emerged/sprout-matured bookends, so an observer cannot
	// see WHAT the Sprout did. This is the per-action stream every live surface
	// (/ws, telemetry) and the history sink need to explain a run.
	EventToolInvoked EventType = "tool-invoked"
	// EventSproutTranscript carries the Sprout's full assembled conversation
	// (system, user, assistant, and tool turns) once when a run ends. The
	// stream-token and tool-invoked events explain a run granularly and live;
	// this is the single readable record for "explain a run" after the fact,
	// so a reviewer reads one transcript instead of stitching a token stream.
	// NOTE: renaming this value renames a PERSISTED event type. Rows written
	// before the rename keep "Pollinator-transcript", so a reader that must span both
	// eras should accept either.
	EventSproutTranscript  EventType = "sprout-transcript"
	EventSproutEmerged     EventType = "sprout-emerged"
	EventSproutMatured     EventType = "sprout-matured"
	EventSproutWithered    EventType = "sprout-withered"
	EventParallelSprouting EventType = "parallel-sprouting"
	EventMycelialMerge     EventType = "mycelial-merge"
	// EventPhenotypicSelection reports Genetic Algorithm progress (start,
	// generation, evaluated, complete phases) from the selection runner.
	EventPhenotypicSelection EventType = "phenotypic-selection"
	// EventDelegationAuthorized audits one delegated capability invocation
	// permitted by an active grant; EventDelegationDenied audits one refused
	// because no grant covers it. Both persist to history.db via the
	// historydb sink — every exercise of (or attempt at) delegation leaves a
	// durable record.
	EventDelegationAuthorized EventType = "delegation-authorized"
	EventDelegationDenied     EventType = "delegation-denied"
	// EventTriggerBlocked audits one run refused by the Hormonal Trigger gate —
	// the observability counterpart to EventDelegationDenied. It does not change
	// enforcement; the gate already blocked the run before this fires.
	EventTriggerBlocked EventType = "hormonal-trigger-blocked"
)

// AllEventTypes returns every registered event type for broad telemetry subscriptions.
func AllEventTypes() []EventType {
	return []EventType{
		EventHealthCheck,
		EventHealthDegraded,
		EventHealthRecovered,
		EventTerrariumOOM,
		EventTerrariumTimeout,
		EventHostExecutionActivated,
		EventSequenceFailure,
		EventSequenceComplete,
		EventSequenceCleanupIncomplete,
		EventStreamToken,
		EventThoughtBranch,
		EventToolInvoked,
		EventSproutTranscript,
		EventSproutEmerged,
		EventSproutMatured,
		EventSproutWithered,
		EventParallelSprouting,
		EventMycelialMerge,
		EventPhenotypicSelection,
		EventDelegationAuthorized,
		EventDelegationDenied,
		EventTriggerBlocked,
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

type subscription struct {
	id      uint64
	handler Handler
}

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
	events   chan Event
	done     chan struct{}
	name     string
	dropped  atomic.Uint64
	dropping atomic.Bool
}

type Bus struct {
	mu       sync.RWMutex
	handlers map[EventType][]subscription
	nextID   uint64
	history  []Event
	sinks    []*sinkPump
	closed   bool
}

func New() *Bus {
	return &Bus{
		handlers: make(map[EventType][]subscription),
		history:  make([]Event, 0, maxHistory),
	}
}

// AttachSink registers a sink that receives every published event
// asynchronously. buffer <= 0 selects the default buffer size. When a sink's
// buffer is full the event is dropped for that sink only; telemetry is lossy
// by design so the orchestrator hot path never blocks.
func (b *Bus) AttachSink(sink Sink, buffer int, name string) {
	if b == nil || sink == nil {
		return
	}
	if buffer <= 0 {
		buffer = defaultSinkBuffer
	}

	pump := &sinkPump{
		events: make(chan Event, buffer),
		done:   make(chan struct{}),
		name:   name,
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

func (b *Bus) Subscribe(eventType EventType, handler Handler) func() {
	if b == nil || handler == nil {
		return func() {}
	}

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.handlers[eventType] = append(b.handlers[eventType], subscription{id: id, handler: handler})
	b.mu.Unlock()

	return func() {
		if b == nil {
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		subs := b.handlers[eventType]
		for i, sub := range subs {
			if sub.id == id {
				b.handlers[eventType] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
	}
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
	subs := b.handlers[event.Type]
	handlers := make([]Handler, 0, len(subs))
	for _, sub := range subs {
		handlers = append(handlers, sub.handler)
	}
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
				if pump.dropping.CompareAndSwap(true, false) {
					log.Printf("eventbus: sink %q resumed accepting events after dropping %d while its buffer was full", pump.name, pump.dropped.Load())
				}
			default:
				pump.dropped.Add(1)
				if pump.dropping.CompareAndSwap(false, true) {
					log.Printf("eventbus: sink %q buffer full (event type %q), dropping events until it catches up", pump.name, event.Type)
				}
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

// HandlerCount returns the number of active handlers for a given event type.
// It is intended primarily for testing.
func (b *Bus) HandlerCount(eventType EventType) int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.handlers[eventType])
}

// SinkDroppedCount returns how many events have been dropped for the named
// sink since it was attached. Returns 0 for an unknown name.
func (b *Bus) SinkDroppedCount(name string) uint64 {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, pump := range b.sinks {
		if pump.name == name {
			return pump.dropped.Load()
		}
	}
	return 0
}

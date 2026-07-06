package eventbus

import (
	"sync"
	"testing"
)

type collectingSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *collectingSink) Consume(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func TestSinkReceivesAllEventTypes(t *testing.T) {
	bus := New()
	sink := &collectingSink{}
	bus.AttachSink(sink, 0)

	for _, eventType := range AllEventTypes() {
		bus.Publish(Event{Type: eventType, Source: "test", SessionID: "tendril-x"})
	}
	bus.Shutdown()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != len(AllEventTypes()) {
		t.Fatalf("expected %d events, got %d", len(AllEventTypes()), len(sink.events))
	}
	for _, event := range sink.events {
		if event.SessionID != "tendril-x" {
			t.Fatalf("sessionId lost in transit: %+v", event)
		}
	}
}

func TestPublishAfterShutdownDoesNotPanic(t *testing.T) {
	bus := New()
	bus.AttachSink(&collectingSink{}, 0)
	bus.Shutdown()

	// Must not send on a closed pump channel.
	bus.Publish(Event{Type: EventHealthCheck, Source: "test"})
	bus.Shutdown() // idempotent
}

func TestAttachSinkAfterShutdownIsNoOp(t *testing.T) {
	bus := New()
	bus.Shutdown()

	sink := &collectingSink{}
	bus.AttachSink(sink, 0)
	bus.Publish(Event{Type: EventHealthCheck, Source: "test"})

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 0 {
		t.Fatalf("expected no events after shutdown, got %d", len(sink.events))
	}
}

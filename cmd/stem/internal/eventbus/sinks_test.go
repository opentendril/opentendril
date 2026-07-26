package eventbus

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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
	bus.AttachSink(sink, 0, "test-sink")

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
	bus.AttachSink(&collectingSink{}, 0, "test-collecting")
	bus.Shutdown()

	// Must not send on a closed pump channel.
	bus.Publish(Event{Type: EventHealthCheck, Source: "test"})
	bus.Shutdown() // idempotent
}

func TestAttachSinkAfterShutdownIsNoOp(t *testing.T) {
	bus := New()
	bus.Shutdown()

	sink := &collectingSink{}
	bus.AttachSink(sink, 0, "test-sink-after-shutdown")
	bus.Publish(Event{Type: EventHealthCheck, Source: "test"})

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.events) != 0 {
		t.Fatalf("expected no events after shutdown, got %d", len(sink.events))
	}
}

type blockingSink struct {
	blocked chan struct{}
}

func (s *blockingSink) Consume(event Event) {
	<-s.blocked
}

func TestSinkOverflowDropsAndRecovers(t *testing.T) {
	bus := New()
	sink := &blockingSink{blocked: make(chan struct{})}

	// Buffer of 2.
	bus.AttachSink(sink, 2, "test-overflow")

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// Publish 1 event and sleep to ensure the pump goroutine wakes up,
	// takes the event out of the channel, and blocks in Consume().
	bus.Publish(Event{Type: EventHealthCheck, Source: "test"})
	time.Sleep(10 * time.Millisecond)

	// Now the pump goroutine is blocked. The channel is empty.
	// We can safely publish 9 events.
	// 2 will fill the buffer. 7 will drop.
	for i := 0; i < 9; i++ {
		bus.Publish(Event{Type: EventHealthCheck, Source: "test"})
	}

	dropped := bus.SinkDroppedCount("test-overflow")
	if dropped != 7 {
		t.Fatalf("expected 7 dropped events, got %d", dropped)
	}

	// Unblock to allow recovery.
	close(sink.blocked)

	// Publish enough to drain the buffer (wait for it).
	// The pump goroutine will wake up and drain the 2 buffered events.
	// To reliably trigger the recovery log without sleeping, we can just publish one more event.
	// Actually, the recovery log is printed by Publish() when it successfully sends after dropping.
	// Since we closed sink.blocked, the sink will consume as fast as possible.
	time.Sleep(50 * time.Millisecond) // Give pump goroutine time to drain the 2 buffered events

	// Now publish one more. It should succeed and print the recovery log.
	bus.Publish(Event{Type: EventHealthCheck, Source: "test"})

	// Give the recovery log a moment to be written (since Publish executes it inline, it's actually synchronous, but just in case)

	bus.Shutdown()

	logOutput := buf.String()
	fullCount := strings.Count(logOutput, "buffer full")
	resumedCount := strings.Count(logOutput, "resumed accepting events")

	if fullCount != 1 {
		t.Errorf("expected exactly 1 'buffer full' log, got %d\nlog:\n%s", fullCount, logOutput)
	}
	if resumedCount != 1 {
		t.Errorf("expected exactly 1 'resumed' log, got %d\nlog:\n%s", resumedCount, logOutput)
	}
}

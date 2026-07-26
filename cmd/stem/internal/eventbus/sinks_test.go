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

// blockingSink blocks its pump goroutine in Consume until unblocked, closing
// started the first time Consume runs so a test can deterministically wait
// for the goroutine to actually be blocked instead of guessing with a sleep.
type blockingSink struct {
	blocked chan struct{}
	started chan struct{}
	once    sync.Once
}

func (s *blockingSink) Consume(event Event) {
	s.once.Do(func() { close(s.started) })
	<-s.blocked
}

func TestSinkOverflowDropsAndRecovers(t *testing.T) {
	bus := New()
	sink := &blockingSink{blocked: make(chan struct{}), started: make(chan struct{})}

	// Buffer of 2.
	bus.AttachSink(sink, 2, "test-overflow")

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// Publish 1 event and wait for the pump goroutine to actually dequeue it
	// and block in Consume — a real synchronization point, not a sleep guess.
	bus.Publish(Event{Type: EventHealthCheck, Source: "test"})
	select {
	case <-sink.started:
	case <-time.After(2 * time.Second):
		t.Fatal("sink goroutine never started consuming")
	}

	// The pump goroutine is now blocked and the channel is empty. Publish far
	// more events than the buffer (2) can hold so the buffer fills and the
	// rest overflow, regardless of exact scheduling — don't assert a precise
	// drop count, that's inherently timing-fragile; just prove overflow
	// happened at all.
	const burst = 20
	for i := 0; i < burst; i++ {
		bus.Publish(Event{Type: EventHealthCheck, Source: "test"})
	}

	if dropped := bus.SinkDroppedCount("test-overflow"); dropped == 0 {
		t.Fatal("expected some events to be dropped while the sink was blocked, got 0")
	}

	// Unblock the sink, then poll-publish until a send succeeds (the drop
	// count stops increasing) — deterministic proof of recovery instead of a
	// fixed sleep guessing how long the drain takes.
	close(sink.blocked)
	deadline := time.Now().Add(2 * time.Second)
	prevDropped := bus.SinkDroppedCount("test-overflow")
	recovered := false
	for time.Now().Before(deadline) {
		bus.Publish(Event{Type: EventHealthCheck, Source: "test"})
		if got := bus.SinkDroppedCount("test-overflow"); got == prevDropped {
			recovered = true
			break
		} else {
			prevDropped = got
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("sink never recovered (a publish never succeeded) within the deadline")
	}

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

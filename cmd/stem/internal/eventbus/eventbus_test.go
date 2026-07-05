package eventbus

import (
	"sync"
	"testing"
)

func TestSubscribePublishDispatchesToHandler(t *testing.T) {
	bus := New()
	received := make(chan Event, 1)

	bus.Subscribe(EventHealthCheck, func(event Event) {
		received <- event
	})

	bus.Publish(Event{Type: EventHealthCheck, Source: "test"})

	event := <-received
	if event.Type != EventHealthCheck {
		t.Fatalf("event type = %q, want %q", event.Type, EventHealthCheck)
	}
	if event.Source != "test" {
		t.Fatalf("source = %q, want test", event.Source)
	}
}

func TestPublishDispatchesMultipleHandlers(t *testing.T) {
	bus := New()
	var mu sync.Mutex
	count := 0

	for i := 0; i < 3; i++ {
		bus.Subscribe(EventHealthDegraded, func(event Event) {
			mu.Lock()
			defer mu.Unlock()
			count++
		})
	}

	bus.Publish(Event{Type: EventHealthDegraded})

	mu.Lock()
	defer mu.Unlock()
	if count != 3 {
		t.Fatalf("handler count = %d, want 3", count)
	}
}

func TestHistoryReturnsLastNAndCapsAt100(t *testing.T) {
	bus := New()
	for i := 0; i < 120; i++ {
		bus.Publish(Event{Type: EventSequenceFailure, Data: map[string]interface{}{"index": i}})
	}

	all := bus.History(200)
	if len(all) != 100 {
		t.Fatalf("history len = %d, want 100", len(all))
	}
	if got := all[0].Data["index"]; got != 20 {
		t.Fatalf("first retained index = %v, want 20", got)
	}

	last := bus.History(5)
	if len(last) != 5 {
		t.Fatalf("last len = %d, want 5", len(last))
	}
	if got := last[0].Data["index"]; got != 115 {
		t.Fatalf("first last index = %v, want 115", got)
	}
}

func TestConcurrentPublishIsSafe(t *testing.T) {
	bus := New()
	const count = 200

	var wg sync.WaitGroup
	wg.Add(count)
	for i := 0; i < count; i++ {
		go func(index int) {
			defer wg.Done()
			bus.Publish(Event{Type: EventHealthCheck, Data: map[string]interface{}{"index": index}})
		}(i)
	}
	wg.Wait()

	if got := len(bus.History(count)); got != 100 {
		t.Fatalf("history len = %d, want 100", got)
	}
}

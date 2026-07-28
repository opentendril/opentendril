package healthmon

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

type mockCheck struct {
	name   string
	result CheckResult
}

func (m mockCheck) Name() string {
	return m.name
}

func (m mockCheck) Check(ctx context.Context) CheckResult {
	_ = ctx
	return m.result
}

func TestRunOnceHealthy(t *testing.T) {
	monitor := New(nil, time.Second)
	monitor.RegisterCheck(mockCheck{name: "ok", result: CheckResult{Healthy: true, Message: "ok"}})

	report := monitor.RunOnce(context.Background())
	if !report.Overall {
		t.Fatalf("Overall = false, want true")
	}
	if !report.Results["ok"].Healthy {
		t.Fatalf("check result healthy = false, want true")
	}
}

func TestRunOnceFailing(t *testing.T) {
	monitor := New(nil, time.Second)
	monitor.RegisterCheck(mockCheck{name: "bad", result: CheckResult{Healthy: false, Message: "bad"}})

	report := monitor.RunOnce(context.Background())
	if report.Overall {
		t.Fatalf("Overall = true, want false")
	}
	if report.Results["bad"].Healthy {
		t.Fatalf("check result healthy = true, want false")
	}
}

func TestStartPublishesEvents(t *testing.T) {
	bus := eventbus.New()
	monitor := New(bus, time.Hour)
	monitor.RegisterCheck(mockCheck{name: "bad", result: CheckResult{Healthy: false, Message: "bad"}})

	healthEvents := make(chan eventbus.Event, 1)
	degradedEvents := make(chan eventbus.Event, 1)
	bus.Subscribe(eventbus.EventHealthCheck, func(event eventbus.Event) {
		healthEvents <- event
	})
	bus.Subscribe(eventbus.EventHealthDegraded, func(event eventbus.Event) {
		degradedEvents <- event
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor.Start(ctx)

	select {
	case <-healthEvents:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for health check event")
	}

	select {
	case <-degradedEvents:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for degraded event")
	}
}

// toggleCheck returns whatever *healthy currently points to, letting a test
// change the value between calls to simulate a real degraded→recovered cycle.
type toggleCheck struct {
	name    string
	healthy *atomic.Bool
}

func (c toggleCheck) Name() string { return c.name }
func (c toggleCheck) Check(ctx context.Context) CheckResult {
	return CheckResult{Healthy: c.healthy.Load(), Message: "toggle"}
}

func TestRecoveryPublishesAfterObservedDegradedCycle(t *testing.T) {
	bus := eventbus.New()
	monitor := New(bus, time.Hour)

	healthy := &atomic.Bool{}
	healthy.Store(false)
	monitor.RegisterCheck(toggleCheck{name: "toggle", healthy: healthy})

	degradedEvents := make(chan eventbus.Event, 1)
	recoveredEvents := make(chan eventbus.Event, 1)
	bus.Subscribe(eventbus.EventHealthDegraded, func(event eventbus.Event) {
		degradedEvents <- event
	})
	bus.Subscribe(eventbus.EventHealthRecovered, func(event eventbus.Event) {
		recoveredEvents <- event
	})

	monitor.RunOnceAndPublish(context.Background())

	select {
	case <-degradedEvents:
	default:
		t.Fatal("expected degraded event")
	}

	select {
	case <-recoveredEvents:
		t.Fatal("did not expect recovered event yet")
	default:
	}

	healthy.Store(true)
	monitor.RunOnceAndPublish(context.Background())

	select {
	case <-recoveredEvents:
	default:
		t.Fatal("expected recovered event")
	}

	select {
	case <-degradedEvents:
		t.Fatal("did not expect another degraded event")
	default:
	}
}

func TestFirstHealthyCycleNeverPublishesRecovered(t *testing.T) {
	bus := eventbus.New()
	monitor := New(bus, time.Hour)

	healthy := &atomic.Bool{}
	healthy.Store(true)
	monitor.RegisterCheck(toggleCheck{name: "toggle", healthy: healthy})

	recoveredEvents := make(chan eventbus.Event, 1)
	bus.Subscribe(eventbus.EventHealthRecovered, func(event eventbus.Event) {
		recoveredEvents <- event
	})

	monitor.RunOnceAndPublish(context.Background())

	select {
	case <-recoveredEvents:
		t.Fatal("did not expect recovered event on first cycle")
	default:
	}
}

func TestRecoveryStateIsRaceSafeAcrossConcurrentCalls(t *testing.T) {
	bus := eventbus.New()
	monitor := New(bus, time.Hour)

	healthy := &atomic.Bool{}
	healthy.Store(true)
	monitor.RegisterCheck(toggleCheck{name: "toggle", healthy: healthy})

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			monitor.RunOnceAndPublish(ctx)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			monitor.RunOnceAndPublish(ctx)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			healthy.Store(!healthy.Load())
		}
	}()

	wg.Wait()
}

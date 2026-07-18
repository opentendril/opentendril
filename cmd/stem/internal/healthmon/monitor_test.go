package healthmon

import (
	"context"
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

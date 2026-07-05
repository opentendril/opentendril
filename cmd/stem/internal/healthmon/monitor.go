package healthmon

import (
	"context"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
)

type HealthCheck interface {
	Name() string
	Check(ctx context.Context) CheckResult
}

type CheckResult struct {
	Healthy bool                   `json:"healthy"`
	Message string                 `json:"message"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

type HealthReport struct {
	Timestamp time.Time              `json:"timestamp"`
	Overall   bool                   `json:"overall"`
	Results   map[string]CheckResult `json:"results"`
}

type Monitor struct {
	bus      *eventbus.Bus
	interval time.Duration
	checks   []HealthCheck
}

func New(bus *eventbus.Bus, interval time.Duration) *Monitor {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Monitor{
		bus:      bus,
		interval: interval,
		checks:   make([]HealthCheck, 0),
	}
}

func (m *Monitor) RegisterCheck(check HealthCheck) {
	if m == nil || check == nil {
		return
	}
	m.checks = append(m.checks, check)
}

func (m *Monitor) Start(ctx context.Context) {
	if m == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	go func() {
		m.runAndPublish(ctx)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.runAndPublish(ctx)
			}
		}
	}()
}

func (m *Monitor) RunOnce(ctx context.Context) HealthReport {
	if ctx == nil {
		ctx = context.Background()
	}

	checks := []HealthCheck(nil)
	if m != nil {
		checks = m.checks
	}
	report := HealthReport{
		Timestamp: time.Now().UTC(),
		Overall:   true,
		Results:   make(map[string]CheckResult, len(checks)),
	}

	for _, check := range checks {
		result := check.Check(ctx)
		report.Results[check.Name()] = result
		if !result.Healthy {
			report.Overall = false
		}
	}

	return report
}

func (m *Monitor) runAndPublish(ctx context.Context) {
	report := m.RunOnce(ctx)
	m.publish(eventbus.EventHealthCheck, report)
	if !report.Overall {
		m.publish(eventbus.EventHealthDegraded, report)
	}
}

func (m *Monitor) publish(eventType eventbus.EventType, report HealthReport) {
	if m == nil || m.bus == nil {
		return
	}
	m.bus.Publish(eventbus.Event{
		Type:   eventType,
		Source: "healthmon",
		Data: map[string]interface{}{
			"report": report,
		},
	})
}

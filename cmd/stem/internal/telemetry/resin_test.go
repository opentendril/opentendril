package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
)

func TestInitResinSinkWritesStructuredLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "resin.log")
	bus := eventbus.New()

	sink, err := InitResinSink(bus, ResinConfig{Enabled: true, Format: "json", Level: "info"}, logPath)
	if err != nil {
		t.Fatalf("InitResinSink() error = %v", err)
	}
	if sink == nil {
		t.Fatal("InitResinSink() returned nil sink")
	}

	bus.Publish(eventbus.Event{Type: eventbus.EventSproutEmerged, Source: "test"})

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read resin log: %v", err)
	}
	if !strings.Contains(string(content), `"type":"sprout-emerged"`) {
		t.Fatalf("log content = %q, want sprout-emerged event", string(content))
	}
}
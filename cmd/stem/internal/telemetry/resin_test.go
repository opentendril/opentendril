package telemetry

import (
	"compress/gzip"
	"io"
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

// publishUntilRotated publishes events until the active log has been rotated
// (its size drops back below one event) or the safety cap is hit.
func publishResinEvents(bus *eventbus.Bus, n int) {
	for i := 0; i < n; i++ {
		bus.Publish(eventbus.Event{
			Type:   eventbus.EventStreamToken,
			Source: "amber-test",
			Data:   map[string]interface{}{"token": strings.Repeat("x", 100)},
		})
	}
}

func listAmberArchives(t *testing.T, logPath string) []string {
	t.Helper()
	amberDir := filepath.Join(filepath.Dir(logPath), "amber")
	entries, err := os.ReadDir(amberDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read amber dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestResinHardensIntoAmber(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "resin.log")
	bus := eventbus.New()

	_, err := InitResinSink(bus, ResinConfig{
		Enabled: true,
		Amber:   AmberConfig{Enabled: true, MaxSizeKB: 1, Keep: 5},
	}, logPath)
	if err != nil {
		t.Fatalf("InitResinSink() error = %v", err)
	}

	// ~180 bytes per event: 10 events comfortably exceed the 1 KB threshold.
	publishResinEvents(bus, 10)

	archives := listAmberArchives(t, logPath)
	if len(archives) == 0 {
		t.Fatal("expected resin.log to harden into an amber archive")
	}
	for _, name := range archives {
		if !strings.HasPrefix(name, "resin-") || !strings.HasSuffix(name, ".log.gz") {
			t.Errorf("archive name %q, want resin-<stamp>.log.gz", name)
		}
	}

	// The hardened archive must decompress back to the structured events.
	archivePath := filepath.Join(filepath.Dir(logPath), "amber", archives[0])
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()
	reader, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decompress archive: %v", err)
	}
	if !strings.Contains(string(content), `"type":"stream-token"`) {
		t.Fatalf("archive content = %q, want structured stream-token events", string(content))
	}

	// The active log restarted after hardening: it must now be smaller than
	// the rotation threshold.
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat active log: %v", err)
	}
	if info.Size() >= 1024 {
		t.Errorf("active log is %d bytes after rotation, want < 1024", info.Size())
	}
}

func TestResinAmberRetentionPrunesOldest(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "resin.log")
	bus := eventbus.New()

	_, err := InitResinSink(bus, ResinConfig{
		Enabled: true,
		Amber:   AmberConfig{Enabled: true, MaxSizeKB: 1, Keep: 2},
	}, logPath)
	if err != nil {
		t.Fatalf("InitResinSink() error = %v", err)
	}

	// Enough events for several rotations.
	publishResinEvents(bus, 50)

	archives := listAmberArchives(t, logPath)
	if len(archives) == 0 {
		t.Fatal("expected at least one amber archive")
	}
	if len(archives) > 2 {
		t.Errorf("retention kept %d archives, want at most 2: %v", len(archives), archives)
	}
}

func TestResinWithoutAmberNeverRotates(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "resin.log")
	bus := eventbus.New()

	_, err := InitResinSink(bus, ResinConfig{Enabled: true}, logPath)
	if err != nil {
		t.Fatalf("InitResinSink() error = %v", err)
	}

	publishResinEvents(bus, 20)

	if archives := listAmberArchives(t, logPath); len(archives) != 0 {
		t.Errorf("amber disabled but found archives: %v", archives)
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat active log: %v", err)
	}
	if info.Size() < 1024 {
		t.Errorf("active log is %d bytes, expected unrotated growth past 1 KB", info.Size())
	}
}

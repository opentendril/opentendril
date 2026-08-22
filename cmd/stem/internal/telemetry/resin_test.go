package telemetry

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
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

	// Shutdown drains the sink's goroutine before we assert file contents.
	bus.Shutdown()

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read resin log: %v", err)
	}
	if !strings.Contains(string(content), `"type":"sprout-emerged"`) {
		t.Fatalf("log content = %q, want sprout-emerged event", string(content))
	}
}

// publishResinEvents publishes n events large enough to trigger amber rotation.
func publishResinEvents(bus *eventbus.Bus, n int) {
	for i := 0; i < n; i++ {
		bus.Publish(eventbus.Event{
			Type:   eventbus.EventToolInvoked,
			Source: "amber-test",
			Data:   map[string]interface{}{"args": strings.Repeat("x", 100)},
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

// waitForCondition polls cond every 20 ms until it returns true or the deadline
// passes. Returns true when the condition was satisfied, false on timeout.
func waitForCondition(deadline time.Time, cond func() bool) bool {
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
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

	// Resin now runs on the sink's goroutine — poll until the archive appears
	// (or Shutdown drains, whichever arrives first). Poll rather than
	// Shutdown here because we also check mid-sequence state (archive count,
	// active-log size) that would be obscured if we let all 10 events drain
	// in a single Shutdown call without the intermediate check.
	deadline := time.Now().Add(5 * time.Second)
	if !waitForCondition(deadline, func() bool {
		return len(listAmberArchives(t, logPath)) > 0
	}) {
		t.Fatal("expected resin.log to harden into an amber archive within 5 s")
	}

	archives := listAmberArchives(t, logPath)
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
	if !strings.Contains(string(content), `"type":"tool-invoked"`) {
		t.Fatalf("archive content = %q, want structured tool-invoked events", string(content))
	}

	// Drain the sink so all 10 events are flushed, then check active log size.
	bus.Shutdown()

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

	// Drain the sink before asserting archive count.
	bus.Shutdown()

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

	// Drain the sink before checking filesystem state.
	bus.Shutdown()

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

func TestResinSinkRedaction(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "resin.log")
	bus := eventbus.New()
	defer bus.Shutdown()

	sink, err := InitResinSink(bus, ResinConfig{Enabled: true}, logPath)
	if err != nil {
		t.Fatal(err)
	}

	ev := eventbus.Event{
		Type:   "test",
		Source: "test-source",
		Data: map[string]interface{}{
			"label":   "build",
			"api_key": "sk-abcdefghijklmnopqrstuvwxyz123456",
		},
	}

	// Call handle directly (not via bus) so the result is synchronous and
	// independent of the sink goroutine. This test exercises the write/redact
	// logic, not the delivery path.
	sink.handle(ev)

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(content), "sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Errorf("log contains raw secret")
	}
	if !strings.Contains(string(content), "[REDACTED]") {
		t.Errorf("log does not contain [REDACTED]")
	}
	if !strings.Contains(string(content), "build") {
		t.Errorf("log does not contain non-secret content")
	}
	if ev.Data["api_key"] != "sk-abcdefghijklmnopqrstuvwxyz123456" {
		t.Errorf("original event was mutated")
	}

	os.Setenv("TENDRIL_TELEMETRY_REDACTION", "off")
	defer os.Unsetenv("TENDRIL_TELEMETRY_REDACTION")

	sink.handle(ev)
	content2, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(content2)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[1], "sk-abcdefghijklmnopqrstuvwxyz123456") {
		t.Errorf("log does not contain raw secret when redaction is disabled")
	}
}

// TestPublishDoesNotBlockOnSlowResin proves the point of this fix: Publish
// returns promptly even while Resin is busy with disk I/O. We attach ResinSink
// normally via AttachSink and then hold its internal mutex from outside
// (white-box, same package) to simulate a maximally slow write. Publish must
// complete without waiting for the mutex to be released.
func TestPublishDoesNotBlockOnSlowResin(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "resin.log")
	bus := eventbus.New()
	defer bus.Shutdown()

	sink, err := InitResinSink(bus, ResinConfig{Enabled: true}, logPath)
	if err != nil {
		t.Fatalf("InitResinSink() error = %v", err)
	}

	// Acquire the sink's mutex to simulate a slow/blocked Resin write.
	sink.mu.Lock()

	start := time.Now()
	bus.Publish(eventbus.Event{Type: eventbus.EventSproutEmerged, Source: "non-blocking-test"})
	elapsed := time.Since(start)

	// Release the mutex so the sink goroutine can eventually drain.
	sink.mu.Unlock()

	// Publish must return well within 1 s even though Resin is "blocked".
	// The sink pump's goroutine owns the I/O; Publish only does a non-blocking
	// channel send and returns immediately.
	if elapsed > time.Second {
		t.Errorf("Publish blocked for %v while Resin mutex was held; want < 1 s", elapsed)
	}
}

func TestResinSanitizesPrivateReasoningWithRedactionOff(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "resin.log")
	bus := eventbus.New()
	defer bus.Shutdown()

	sink, err := InitResinSink(bus, ResinConfig{Enabled: true}, logPath)
	if err != nil {
		t.Fatal(err)
	}

	os.Setenv("TENDRIL_TELEMETRY_REDACTION", "off")
	defer os.Unsetenv("TENDRIL_TELEMETRY_REDACTION")

	ev := eventbus.Event{
		Type:   eventbus.EventSproutTranscript,
		Source: "test-source",
		Data: map[string]interface{}{
			"transcript": "start <thought>private</thought> end",
		},
	}

	sink.handle(ev)

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(content), "private") {
		t.Errorf("resin output contains private reasoning despite redaction being off: %s", string(content))
	}
	if !strings.Contains(string(content), "start  end") {
		t.Errorf("resin output did not preserve public parts of the transcript: %s", string(content))
	}
}

package telemetry

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

const defaultResinLogPath = ".tendril/logs/resin.log"

// amberDirName is the archive directory for hardened Resin, created next to
// the active resin.log ("Resin that has hardened over time").
const amberDirName = "amber"

// ResinSink appends structured JSON event logs to the local Resin trace file.
// When Amber archiving is enabled, a resin.log that grows past the configured
// size hardens: it is gzip-compressed into the amber/ directory beside it and
// the active log starts fresh, keeping at most Keep archives.
type ResinSink struct {
	mu      sync.Mutex
	logPath string
	format  string
	level   string
	amber   AmberConfig
}

// InitResinSink creates a ResinSink and subscribes it to all events on the bus.
func InitResinSink(bus *eventbus.Bus, cfg ResinConfig, logPath string) (*ResinSink, error) {
	if bus == nil {
		return nil, fmt.Errorf("event bus is nil")
	}
	if !cfg.Enabled {
		return nil, nil
	}

	if logPath == "" {
		logPath = defaultResinLogPath
	}

	sink := &ResinSink{
		logPath: logPath,
		format:  cfg.Format,
		level:   cfg.Level,
		amber:   cfg.Amber,
	}

	if err := sink.ensureLogDir(); err != nil {
		return nil, err
	}

	attachHandler(bus, sink.handle)
	return sink, nil
}

func (s *ResinSink) ensureLogDir() error {
	return os.MkdirAll(filepath.Dir(s.logPath), 0o755)
}

func (s *ResinSink) handle(event eventbus.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLogDir(); err != nil {
		return
	}

	file, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()

	evToLog := event
	if !RedactionDisabled() {
		evToLog = RedactEvent(event)
	}

	payload, err := json.Marshal(evToLog)
	if err != nil {
		return
	}
	payload = append(payload, '\n')
	_, _ = file.Write(payload)

	// Telemetry must never block or fail the bus: hardening errors are
	// swallowed exactly like write errors above, and the worst outcome is an
	// oversized active log.
	s.hardenIntoAmberLocked(file)
}

// hardenIntoAmberLocked rotates the active Resin log into the Amber archive
// when it has grown past the configured threshold. Callers must hold s.mu and
// pass the open active log file (used for its Stat; it is closed by the
// deferred Close in handle either way).
func (s *ResinSink) hardenIntoAmberLocked(file *os.File) {
	if !s.amber.Enabled {
		return
	}
	info, err := file.Stat()
	if err != nil || info.Size() < int64(s.amber.MaxSizeKB)*1024 {
		return
	}

	amberDir := filepath.Join(filepath.Dir(s.logPath), amberDirName)
	if err := os.MkdirAll(amberDir, 0o755); err != nil {
		return
	}

	// UTC nanosecond stamp keeps archive names unique and sortable even for
	// rotations within the same second.
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	archivePath := filepath.Join(amberDir, "resin-"+stamp+".log.gz")
	if err := gzipFile(s.logPath, archivePath); err != nil {
		return
	}

	// The active log only restarts after a fully written archive, so a failed
	// compression can never lose events.
	if err := os.Truncate(s.logPath, 0); err != nil {
		return
	}

	s.pruneAmber(amberDir)
}

// gzipFile compresses src into a new file at dst.
func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	writer := gzip.NewWriter(out)
	if _, err := io.Copy(writer, in); err != nil {
		writer.Close()
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := writer.Close(); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

// pruneAmber deletes the oldest archives beyond the configured retention
// count. Archive names embed a sortable UTC timestamp, so lexical order is
// chronological order.
func (s *ResinSink) pruneAmber(amberDir string) {
	entries, err := os.ReadDir(amberDir)
	if err != nil {
		return
	}
	archives := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) == ".gz" {
			archives = append(archives, name)
		}
	}
	if len(archives) <= s.amber.Keep {
		return
	}
	sort.Strings(archives)
	for _, name := range archives[:len(archives)-s.amber.Keep] {
		_ = os.Remove(filepath.Join(amberDir, name))
	}
}

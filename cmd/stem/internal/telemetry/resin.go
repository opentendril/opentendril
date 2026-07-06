package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/opentendril/core/cmd/stem/internal/eventbus"
)

const defaultResinLogPath = ".tendril/logs/resin.log"

// ResinSink appends structured JSON event logs to the local Resin trace file.
type ResinSink struct {
	mu      sync.Mutex
	logPath string
	format  string
	level   string
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

	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	payload = append(payload, '\n')
	_, _ = file.Write(payload)
}
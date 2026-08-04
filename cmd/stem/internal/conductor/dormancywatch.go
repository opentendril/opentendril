package conductor

import (
	"context"
	"os"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/dormancy"
	"github.com/opentendril/opentendril/cmd/stem/internal/eventbus"
)

// watchDormancy attaches a dormancy watcher to a run for the duration of that
// run and returns the function that detaches it and prints its readout.
//
// The watcher itself knows nothing about this package: it learns what a run is
// doing from the bus, and it learns what a run has changed through a narrow
// function port this closure supplies. That keeps the workspace measurement —
// which lives behind an unexported symbol here — reachable without exporting it
// and without the watcher importing an orchestrator to reach a supervisor's
// dependency.
//
// The probe interval is what switches the whole thing on. Unconfigured, no
// watcher is attached, no probe runs, no goroutine starts and the run behaves
// exactly as it did before — patience is configured per Substrate and the
// default preserves today's behaviour.
func watchDormancy(ctx context.Context, bus *eventbus.Bus, interval time.Duration, mountPath string) func() {
	if bus == nil || interval <= 0 {
		return func() {}
	}

	watcher := dormancy.New(dormancy.Config{
		Bus:             bus,
		ScratchInterval: interval,
		Scratch: func(probeCtx context.Context, _ dormancy.RunKey) ([]string, error) {
			return collectStageableFilesFn(probeCtx, mountPath)
		},
	})

	detach := watcher.Subscribe(bus)
	stopTicking := watcher.Start(ctx)

	return func() {
		stopTicking()
		detach()
		// The readout is printed only for a run that actually went dormant.
		// Dormancy raises verbosity around the runs we hold the least evidence
		// about; printing the record for every healthy run instead would bury
		// the one case it exists to make legible.
		if watcher.ReportedAny() {
			watcher.Render(os.Stderr) //nolint:errcheck
		}
	}
}

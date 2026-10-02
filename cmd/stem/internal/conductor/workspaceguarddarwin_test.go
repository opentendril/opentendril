//go:build darwin && (amd64 || arm64)

package conductor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestDarwinWorkspaceProcessGuardSerializesAndHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace")
	unlock, err := lockWorkspaceAcrossProcesses(context.Background(), path)
	if err != nil {
		t.Fatalf("acquire Darwin workspace process guard: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lockWorkspaceAcrossProcesses(canceled, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Darwin process guard = %v, want context.Canceled while lock is held", err)
	}
	unlock()

	unlock, err = lockWorkspaceAcrossProcesses(context.Background(), path)
	if err != nil {
		t.Fatalf("reacquire released Darwin workspace process guard: %v", err)
	}
	unlock()
}

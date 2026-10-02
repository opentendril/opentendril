package conductor

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestOrdinaryWorkspaceLockFallsBackPortablyAndLifecycleGuardFailsClosed(t *testing.T) {
	original := lockWorkspaceAcrossProcessesFn
	var processGuardCalls atomic.Int32
	lockWorkspaceAcrossProcessesFn = func(context.Context, string) (func(), error) {
		processGuardCalls.Add(1)
		return nil, ErrWorkspaceProcessLockUnavailable
	}
	t.Cleanup(func() { lockWorkspaceAcrossProcessesFn = original })

	path := filepath.Join(t.TempDir(), "workspace")
	unlock := LockWorkspace(path)
	acquired := make(chan struct{})
	go func() {
		unlockSecond := LockWorkspace(path)
		close(acquired)
		unlockSecond()
	}()
	select {
	case <-acquired:
		t.Fatal("ordinary workspace guard did not serialize same-process access")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("ordinary workspace guard remained blocked after release")
	}
	if calls := processGuardCalls.Load(); calls != 2 {
		t.Fatalf("ordinary workspace accesses attempted the unavailable process guard %d time(s), want two best-effort attempts", calls)
	}

	if _, err := LockWorkspaceContext(context.Background(), path); err == nil {
		t.Fatal("destructive lifecycle guard succeeded without cross-process exclusion")
	}
	if calls := processGuardCalls.Load(); calls != 3 {
		t.Fatalf("lifecycle process-guard calls = %d, want two best-effort attempts plus one required attempt", calls)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LockWorkspacePortable(canceled, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("portable workspace lock after context cancellation = %v, want context.Canceled", err)
	}
}

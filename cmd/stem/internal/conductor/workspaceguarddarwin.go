//go:build darwin

package conductor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

type workspaceDarwinRecordGuard struct {
	start    int64
	length   int64
	pid      int32
	lockType int16
	whence   int16
}

const workspaceDarwinSeekStart = 0

func lockWorkspaceAcrossProcesses(ctx context.Context, path string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	canonical := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		canonical = resolved
	}
	digest := sha256.Sum256([]byte(canonical))
	lockRoot := filepath.Join(os.TempDir(), fmt.Sprintf("opentendril-workspace-locks-%d", os.Getuid()))
	if err := os.MkdirAll(lockRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace lifecycle lock directory: %w", err)
	}
	lockPath := filepath.Join(lockRoot, hex.EncodeToString(digest[:])+".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open workspace lifecycle lock: %w", err)
	}
	for {
		guard := workspaceDarwinRecordGuard{lockType: syscall.F_WRLCK, whence: workspaceDarwinSeekStart, length: 1}
		_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), uintptr(syscall.F_SETLK), uintptr(unsafe.Pointer(&guard)))
		runtime.KeepAlive(&guard)
		if errno == 0 {
			return func() {
				guard := workspaceDarwinRecordGuard{lockType: syscall.F_UNLCK, whence: workspaceDarwinSeekStart, length: 1}
				_, _, _ = syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), uintptr(syscall.F_SETLK), uintptr(unsafe.Pointer(&guard)))
				runtime.KeepAlive(&guard)
				_ = file.Close()
			}, nil
		}
		err = errno
		if err != syscall.EACCES && err != syscall.EAGAIN && err != syscall.EINTR {
			_ = file.Close()
			return nil, fmt.Errorf("acquire workspace lifecycle lock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

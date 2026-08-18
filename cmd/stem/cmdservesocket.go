package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// EnvLocalSocket names an optional HTTP-over-Unix-domain-socket path.
// Unset means no local socket is created. A set value must be absolute.
const EnvLocalSocket = "TENDRIL_LOCAL_SOCKET"

// localSocketFileMode is world-connectable so local clients can reach the
// same mux they can already reach on loopback TCP. The containing directory,
// not this mode, is what prevents another principal from replacing the path.
const localSocketFileMode = 0o666

// localSocketLockMode keeps the process-lifetime lock readable and writable
// only by the Stem principal.
const localSocketLockMode = 0o600

// socketIdentity is the exact filesystem object this Stem bound, recorded
// after Listen. Cleanup may unlink the configured pathname only when it still
// names this object.
type socketIdentity struct {
	dev uint64
	ino uint64
}

// localSocket is a Unix-domain HTTP listener created by this Stem process.
type localSocket struct {
	path     string
	listener net.Listener
	server   *http.Server
	lockFile *os.File
	id       socketIdentity
	hasID    bool
}

// startOptionalLocalSocket binds TENDRIL_LOCAL_SOCKET when set and serves mux
// on it. An empty path is a no-op. Any bind/lifecycle failure is logged and
// returns nil so the TCP listener is unaffected.
func startOptionalLocalSocket(mux http.Handler, path string) *localSocket {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	sock, err := openLocalSocket(path)
	if err != nil {
		log.Printf("⚠️ Local Stem socket unavailable at %s: %v (TCP listener still serves)", path, err)
		return nil
	}
	sock.server = &http.Server{Handler: mux}
	go serveLocalSocket(sock)
	return sock
}

func serveLocalSocket(sock *localSocket) {
	if sock == nil || sock.server == nil || sock.listener == nil {
		return
	}
	log.Printf("Starting local Stem socket on %s...", sock.path)
	if err := sock.server.Serve(sock.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("⚠️ Local Stem socket unavailable on %s: %v (TCP listener still serves)", sock.path, err)
	}
}

// openLocalSocket creates an HTTP-capable Unix listener at path.
//
// Ownership of the pathname is serialized by an exclusive, non-blocking
// advisory lease on a sibling file, held for the life of the returned
// listener. Relative paths, non-socket objects, live listeners, and stale
// sockets owned by another principal are refused. The listener is created with
// unlink-on-close disabled; Close removes the pathname only when it still
// names this Stem's inode.
func openLocalSocket(path string) (*localSocket, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("local Stem socket path %q must be absolute", path)
	}

	lockFile, err := acquireLocalSocketLock(path)
	if err != nil {
		return nil, err
	}

	sock, err := bindLocalSocketLocked(path)
	if err != nil {
		_ = releaseLocalSocketLock(lockFile)
		return nil, err
	}
	sock.lockFile = lockFile
	return sock, nil
}

func bindLocalSocketLocked(path string) (*localSocket, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("local Stem socket path %s exists and is not a Unix socket", path)
		}
		live, liveErr := unixSocketIsLive(path)
		if liveErr != nil {
			return nil, fmt.Errorf("probe local Stem socket %s: %w", path, liveErr)
		}
		if live {
			return nil, fmt.Errorf("local Stem socket %s is already in use", path)
		}
		if !ownedByCurrentPrincipal(info) {
			return nil, fmt.Errorf("stale local Stem socket %s is not owned by this Stem", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale local Stem socket %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// Bind normally.
	default:
		return nil, fmt.Errorf("stat local Stem socket %s: %w", path, err)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on local Stem socket %s: %w", path, err)
	}
	unixLn, ok := ln.(*net.UnixListener)
	if !ok {
		_ = ln.Close()
		return nil, fmt.Errorf("listen on local Stem socket %s: not a Unix listener", path)
	}
	// The kernel pathname is ours only while this identity remains. Automatic
	// unlink-on-close would delete a successor that reused the name.
	unixLn.SetUnlinkOnClose(false)

	id, idOK := fileIdentityFromPath(path)
	if !idOK {
		_ = unixLn.Close()
		return nil, fmt.Errorf("stat local Stem socket %s after listen", path)
	}

	if err := os.Chmod(path, localSocketFileMode); err != nil {
		_ = unixLn.Close()
		_ = removeIfStillOwned(path, id)
		return nil, fmt.Errorf("chmod local Stem socket %s: %w", path, err)
	}

	return &localSocket{path: path, listener: unixLn, id: id, hasID: true}, nil
}

// Close shuts the local HTTP server if this process started one, closes the
// listener without unlinking, and removes the pathname only when it still
// names the socket this Stem bound. The process-lifetime lock is released last.
func (s *localSocket) Close() error {
	if s == nil {
		return nil
	}
	var first error
	if s.server != nil {
		if err := s.server.Shutdown(context.Background()); err != nil && !errors.Is(err, http.ErrServerClosed) {
			first = err
		}
		s.server = nil
	}
	if s.listener != nil {
		if err := s.listener.Close(); err != nil && !isClosedNetErr(err) && first == nil {
			first = err
		}
		s.listener = nil
	}
	if s.hasID {
		if err := removeIfStillOwned(s.path, s.id); err != nil && first == nil {
			first = err
		}
	}
	if err := releaseLocalSocketLock(s.lockFile); err != nil && first == nil {
		first = err
	}
	s.lockFile = nil
	return first
}

func localSocketLockPath(socketPath string) string {
	return socketPath + ".lock"
}

func acquireLocalSocketLock(socketPath string) (*os.File, error) {
	lockPath := localSocketLockPath(socketPath)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, localSocketLockMode)
	if err != nil {
		return nil, fmt.Errorf("open local Stem socket lock %s: %w", lockPath, err)
	}
	if err := f.Chmod(localSocketLockMode); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("chmod local Stem socket lock %s: %w", lockPath, err)
	}
	if err := holdExclusivePathLease(f); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("local Stem socket %s is already in use", socketPath)
		}
		return nil, fmt.Errorf("lock local Stem socket %s: %w", socketPath, err)
	}
	return f, nil
}

func releaseLocalSocketLock(f *os.File) error {
	if f == nil {
		return nil
	}
	releasePathLease(f)
	return f.Close()
}

func holdExclusivePathLease(f *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FLOCK, uintptr(f.Fd()), uintptr(syscall.LOCK_EX|syscall.LOCK_NB), 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func releasePathLease(f *os.File) {
	_, _, _ = syscall.Syscall(syscall.SYS_FLOCK, uintptr(f.Fd()), uintptr(syscall.LOCK_UN), 0)
}

func fileIdentity(info os.FileInfo) (socketIdentity, bool) {
	if info == nil {
		return socketIdentity{}, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return socketIdentity{}, false
	}
	return socketIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, true
}

func fileIdentityFromPath(path string) (socketIdentity, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return socketIdentity{}, false
	}
	return fileIdentity(info)
}

// removeIfStillOwned unlinks path only when it is still the Unix socket this
// Stem bound. A live successor, a different inode, or a non-socket object is
// left untouched.
func removeIfStillOwned(path string, id socketIdentity) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil
	}
	current, ok := fileIdentity(info)
	if !ok || current != id {
		return nil
	}
	live, liveErr := unixSocketIsLive(path)
	if liveErr == nil && live {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func unixSocketIsLive(path string) (bool, error) {
	conn, err := net.Dial("unix", path)
	if err == nil {
		_ = conn.Close()
		return true, nil
	}
	if isUnixConnRefused(err) {
		return false, nil
	}
	return false, err
}

func isUnixConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

func isClosedNetErr(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed)
}

func ownedByCurrentPrincipal(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(stat.Uid) == os.Getuid()
}

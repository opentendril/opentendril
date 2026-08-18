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

// localSocket is a Unix-domain HTTP listener created by this Stem process.
type localSocket struct {
	path     string
	listener net.Listener
	server   *http.Server
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
// It never blindly unlinks path. Relative paths, non-socket objects, live
// listeners, and stale sockets owned by another principal are refused.
func openLocalSocket(path string) (*localSocket, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("local Stem socket path %q must be absolute", path)
	}

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
	if err := os.Chmod(path, localSocketFileMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod local Stem socket %s: %w", path, err)
	}
	return &localSocket{path: path, listener: ln}, nil
}

// Close shuts the local HTTP server if this process started one, closes the
// listener, and removes only the socket file this Stem created.
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
	if s.path != "" {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
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

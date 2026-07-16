//go:build linux

// sprout-agent runs inside a Firecracker guest VM, listening on AF_VSOCK port
// 5000 for JSON-encoded command requests from the host FirecrackerProvider.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	// afVSock is AF_VSOCK (40) on Linux. The Go stdlib syscall package does not
	// define this constant for all architectures, so we use the raw value.
	afVSock = 40

	// vmaddrCIDAny is VMADDR_CID_ANY: accept connections on any CID.
	vmaddrCIDAny = uint32(0xFFFFFFFF)

	vsockPort = uint32(5000)
)

// rawSockaddrVM mirrors struct sockaddr_vm from <linux/vm_sockets.h>:
//
//	sa_family_t  svm_family;    // 2 bytes, offset 0
//	uint16       svm_reserved1; // 2 bytes, offset 2
//	uint32       svm_port;      // 4 bytes, offset 4
//	uint32       svm_cid;       // 4 bytes, offset 8
//	uint8        svm_flags;     // 1 byte,  offset 12
//	[3]byte      svm_zero;      // 3 bytes, offset 13 (padding)
type rawSockaddrVM struct {
	Family    uint16
	Reserved1 uint16
	Port      uint32
	CID       uint32
	Flags     uint8
	Zero      [3]byte
}

// Wire protocol types — mirrored on the host side in terrarium/firecracker.go.

type agentRequest struct {
	Type  string        `json:"type"`
	Spec  *agentCmdSpec `json:"spec,omitempty"`
	Files []agentFile   `json:"files,omitempty"`
	Paths []string      `json:"paths,omitempty"`
}

type agentFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"` // JSON marshals []byte as base64
	Mode    uint32 `json:"mode"`
}

type agentCmdSpec struct {
	Command     []string          `json:"command,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Stdin       []byte            `json:"stdin,omitempty"`
	TimeoutMS   int64             `json:"timeout_ms,omitempty"`
}

type agentCmdResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
}

type agentResponse struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Result *agentCmdResult `json:"result,omitempty"`
	Files  []agentFile     `json:"files,omitempty"`
}

func main() {
	// The kernel starts /init with only HOME and TERM in its environment.
	// Without a PATH, exec.Command can never resolve a bare command name,
	// so every "run" request would fail before reaching the guest userland.
	if os.Getenv("PATH") == "" {
		os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}

	fmt.Fprintf(os.Stderr, "sprout-agent: starting vsock listener on port %d\n", vsockPort)
	if err := listenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "sprout-agent: fatal: %v\n", err)
		os.Exit(1)
	}
}

func listenAndServe() error {
	fd, err := syscall.Socket(afVSock, syscall.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("create vsock socket: %w", err)
	}

	if err := vsockBind(fd, vmaddrCIDAny, vsockPort); err != nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("vsock bind: %w", err)
	}
	if err := syscall.Listen(fd, 128); err != nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("vsock listen: %w", err)
	}

	fmt.Fprintf(os.Stderr, "sprout-agent: listening on vsock port %d\n", vsockPort)

	for {
		nfd, err := vsockAccept(fd)
		if err != nil {
			if errors.Is(err, syscall.EINVAL) {
				return nil // socket closed
			}
			fmt.Fprintf(os.Stderr, "sprout-agent: accept error: %v\n", err)
			continue
		}
		go handleConn(nfd)
	}
}

// vsockBind binds an AF_VSOCK socket via raw syscall, bypassing the Go
// syscall package's typed Sockaddr interface which doesn't cover AF_VSOCK on amd64.
func vsockBind(fd int, cid, port uint32) error {
	sa := rawSockaddrVM{Family: afVSock, Port: port, CID: cid}
	_, _, errno := syscall.Syscall(
		syscall.SYS_BIND,
		uintptr(fd),
		uintptr(unsafe.Pointer(&sa)),
		uintptr(unsafe.Sizeof(sa)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// vsockAccept accepts a connection via raw syscall so we don't trigger Go's
// anyToSockaddr which rejects AF_VSOCK with EAFNOSUPPORT.
func vsockAccept(fd int) (int, error) {
	var sa rawSockaddrVM
	saLen := uint32(unsafe.Sizeof(sa))
	nfd, _, errno := syscall.Syscall(
		syscall.SYS_ACCEPT,
		uintptr(fd),
		uintptr(unsafe.Pointer(&sa)),
		uintptr(unsafe.Pointer(&saLen)),
	)
	if errno != 0 {
		return 0, errno
	}
	return int(nfd), nil
}

func handleConn(fd int) {
	f := os.NewFile(uintptr(fd), "vsock-conn")
	defer f.Close()

	dec := json.NewDecoder(bufio.NewReader(f))
	enc := json.NewEncoder(f)

	var req agentRequest
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(agentResponse{OK: false, Error: fmt.Sprintf("decode request: %v", err)})
		return
	}

	var resp agentResponse
	switch req.Type {
	case "ping":
		resp = agentResponse{OK: true}
	case "run":
		resp = handleRun(req)
	case "copyin":
		resp = handleCopyIn(req)
	case "copyout":
		resp = handleCopyOut(req)
	case "shutdown":
		_ = enc.Encode(agentResponse{OK: true})
		os.Exit(0)
	default:
		resp = agentResponse{OK: false, Error: fmt.Sprintf("unknown request type: %q", req.Type)}
	}

	_ = enc.Encode(resp)
}

func handleRun(req agentRequest) agentResponse {
	if req.Spec == nil || len(req.Spec.Command) == 0 {
		return agentResponse{OK: false, Error: "command is required"}
	}
	spec := req.Spec

	var ctx context.Context
	var cancel context.CancelFunc
	if spec.TimeoutMS > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(spec.TimeoutMS)*time.Millisecond)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	startedAt := time.Now()

	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	if wd := strings.TrimSpace(spec.WorkingDir); wd != "" {
		cmd.Dir = wd
	}
	if len(spec.Environment) > 0 {
		for k, v := range spec.Environment {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	} else {
		cmd.Env = os.Environ()
	}
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	durationMS := time.Since(startedAt).Milliseconds()

	result := &agentCmdResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: durationMS,
	}
	if runErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.TimedOut = true
			result.ExitCode = -1
		} else {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				result.ExitCode = exitErr.ExitCode()
			} else {
				return agentResponse{OK: false, Error: fmt.Sprintf("run command: %v", runErr)}
			}
		}
	}

	return agentResponse{OK: true, Result: result}
}

func handleCopyIn(req agentRequest) agentResponse {
	for _, f := range req.Files {
		cleanPath := filepath.Clean(f.Path)
		if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil {
			return agentResponse{OK: false, Error: fmt.Sprintf("mkdir for %s: %v", f.Path, err)}
		}
		mode := os.FileMode(f.Mode)
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(cleanPath, f.Content, mode); err != nil {
			return agentResponse{OK: false, Error: fmt.Sprintf("write %s: %v", f.Path, err)}
		}
	}
	return agentResponse{OK: true}
}

func handleCopyOut(req agentRequest) agentResponse {
	var files []agentFile
	for _, path := range req.Paths {
		content, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return agentResponse{OK: false, Error: fmt.Sprintf("read %s: %v", path, err)}
		}
		files = append(files, agentFile{Path: path, Content: content})
	}
	return agentResponse{OK: true, Files: files}
}

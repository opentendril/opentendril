package terrarium

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pathpkg "path"
)

const (
	fcVsockPort      = uint32(5000)
	fcBootTimeout    = 10 * time.Second
	fcDialRetryDelay = 100 * time.Millisecond
)

// FirecrackerProvider launches terrariumes as Firecracker microVMs.
// Requires: `firecracker` binary in PATH, TENDRIL_FC_KERNEL_PATH, and
// TENDRIL_FC_ROOTFS_PATH environment variables, and /dev/kvm on the host.
type FirecrackerProvider struct{}

func NewFirecrackerProvider() *FirecrackerProvider {
	return &FirecrackerProvider{}
}

func (p *FirecrackerProvider) Name() string {
	return ProviderFirecracker
}

func (p *FirecrackerProvider) Capabilities() TerrariumCapabilities {
	return TerrariumCapabilities{
		SupportsCopyIn:        true,
		SupportsCopyOut:       true,
		SupportedNetworkModes: []NetworkMode{NetworkModeNone},
	}
}

func (p *FirecrackerProvider) Create(ctx context.Context, spec TerrariumSpec) (Terrarium, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	kernelPath := strings.TrimSpace(os.Getenv("TENDRIL_FC_KERNEL_PATH"))
	rootfsPath := strings.TrimSpace(os.Getenv("TENDRIL_FC_ROOTFS_PATH"))
	if kernelPath == "" {
		return nil, fmt.Errorf("TENDRIL_FC_KERNEL_PATH is required for the firecracker provider")
	}
	if rootfsPath == "" {
		return nil, fmt.Errorf("TENDRIL_FC_ROOTFS_PATH is required for the firecracker provider")
	}

	terrariumID, err := newTerrariumID()
	if err != nil {
		return nil, fmt.Errorf("generate terrarium id: %w", err)
	}

	workDir, err := os.MkdirTemp("", "opentendril-fc-*")
	if err != nil {
		return nil, fmt.Errorf("create firecracker work dir: %w", err)
	}

	vsockPath := filepath.Join(workDir, "agent.vsock")
	configPath := filepath.Join(workDir, "config.json")

	memMiB := 256
	if spec.MemoryLimitMB > 0 {
		memMiB = spec.MemoryLimitMB
	}

	cfg := fcConfig{
		BootSource: fcBootSource{
			KernelImagePath: kernelPath,
			// init=/init is required: the kernel only searches /init on an
			// initramfs. On a disk rootfs it tries /sbin/init, /etc/init,
			// /bin/init, then /bin/sh — sprout-agent would never start.
			BootArgs: "console=ttyS0 reboot=k panic=1 pci=off init=/init",
		},
		Drives: []fcDrive{
			{
				DriveID:      "rootfs",
				PathOnHost:   rootfsPath,
				IsRootDevice: true,
				IsReadOnly:   false,
			},
		},
		MachineConfig: fcMachineConf{
			VCPUCount:  1,
			MemSizeMiB: memMiB,
		},
		Vsock: &fcVsock{
			VsockID:  "1",
			GuestCID: 3,
			UDSPath:  vsockPath,
		},
	}

	configJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("marshal firecracker config: %w", err)
	}
	if err := os.WriteFile(configPath, configJSON, 0o644); err != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("write firecracker config: %w", err)
	}

	terrarium := &firecrackerTerrarium{
		provider:    p,
		id:          terrariumID,
		workingDir:  strings.TrimSpace(spec.WorkingDir),
		workDir:     workDir,
		vsockPath:   vsockPath,
		processDone: make(chan struct{}),
	}

	// Firecracker 0.26+ supports --no-api + --config-file for headless boot.
	cmd := exec.Command("firecracker", "--no-api", "--config-file", configPath)
	cmd.Stdout = io.MultiWriter(os.Stderr, &terrarium.consoleLog)
	cmd.Stderr = io.MultiWriter(os.Stderr, &terrarium.consoleLog)
	terrarium.cmd = cmd

	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("start firecracker: %w", err)
	}

	go func() {
		defer close(terrarium.processDone)
		_ = cmd.Wait()
	}()

	bootCtx, bootCancel := context.WithTimeout(ctx, fcBootTimeout)
	defer bootCancel()

	if err := terrarium.waitForAgent(bootCtx); err != nil {
		_ = terrarium.Stop(context.Background())
		return nil, fmt.Errorf("sprout-agent not ready: %w", err)
	}

	if spec.Timeout > 0 {
		wdCtx, wdCancel := context.WithCancel(context.Background())
		terrarium.watchdogCancel = wdCancel
		go terrarium.watchdog(wdCtx, spec.Timeout)
	}

	if len(spec.Files) > 0 {
		if err := terrarium.CopyIn(ctx, spec.Files); err != nil {
			_ = terrarium.Stop(context.Background())
			return nil, err
		}
	}

	return terrarium, nil
}

// firecrackerTerrarium is a live Firecracker microVM terrarium instance.
type firecrackerTerrarium struct {
	provider   TerrariumProvider
	id         string
	workingDir string
	workDir    string
	vsockPath  string

	cmd         *exec.Cmd
	consoleLog  lockedBuffer
	processDone chan struct{}

	stopOnce       sync.Once
	stopErr        error
	watchdogCancel context.CancelFunc
}

func (s *firecrackerTerrarium) ID() string {
	return s.id
}

func (s *firecrackerTerrarium) Provider() TerrariumProvider {
	return s.provider
}

func (s *firecrackerTerrarium) CopyIn(ctx context.Context, payloads []FilePayload) error {
	if ctx == nil {
		ctx = context.Background()
	}

	files := make([]fcAgentFile, 0, len(payloads))
	for _, p := range payloads {
		containerPath, err := s.resolveContainerPath(p.Path)
		if err != nil {
			return err
		}
		mode := uint32(p.Mode)
		if mode == 0 {
			mode = 0o644
		}
		files = append(files, fcAgentFile{
			Path:    containerPath,
			Content: p.Content,
			Mode:    mode,
		})
	}

	var resp fcAgentResponse
	if err := s.callAgent(ctx, fcAgentRequest{Type: "copyin", Files: files}, &resp); err != nil {
		return fmt.Errorf("copyin: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("copyin agent error: %s", resp.Error)
	}
	return nil
}

func (s *firecrackerTerrarium) Run(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(spec.Command) == 0 {
		return CommandResult{}, fmt.Errorf("firecracker terrarium requires an explicit command (interactive stdin mode is not supported)")
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	startedAt := time.Now().UTC()

	req := fcAgentRequest{
		Type: "run",
		Spec: &fcCmdSpec{
			Command:     spec.Command,
			WorkingDir:  spec.WorkingDir,
			Environment: spec.Environment,
			Stdin:       spec.Stdin,
			TimeoutMS:   spec.Timeout.Milliseconds(),
		},
	}

	var resp fcAgentResponse
	if err := s.callAgent(runCtx, req, &resp); err != nil {
		completedAt := time.Now().UTC()
		if runTimedOut(runCtx, err) {
			s.stopAfterTimeout()
			return CommandResult{
				ExitCode:    -1,
				TimedOut:    true,
				StartedAt:   startedAt,
				CompletedAt: completedAt,
				Duration:    completedAt.Sub(startedAt),
			}, nil
		}
		return CommandResult{}, fmt.Errorf("run: %w", err)
	}
	if !resp.OK {
		return CommandResult{}, fmt.Errorf("run agent error: %s", resp.Error)
	}

	completedAt := time.Now().UTC()
	result := CommandResult{
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    completedAt.Sub(startedAt),
	}
	if r := resp.Result; r != nil {
		result.Stdout = r.Stdout
		result.Stderr = r.Stderr
		result.ExitCode = r.ExitCode
		result.TimedOut = r.TimedOut
	}
	if result.TimedOut {
		s.stopAfterTimeout()
	}

	return result, nil
}

// runTimedOut reports whether a callAgent failure means the command ran out
// of time. The context check alone is racy: the vsock connection's read
// deadline is copied from the context, but the two are fired by different
// timers, so the network poller can report an i/o timeout a moment before the
// context timer marks itself exceeded. When that happens the wall clock is
// already at or past the deadline, which the second check observes directly.
func runTimedOut(runCtx context.Context, err error) bool {
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return true
	}
	// errors.Is (not os.IsTimeout, which predates wrapping and cannot see
	// through the callAgent error chain) so the poller's deadline error is
	// recognized wherever it sits in the chain.
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		return false
	}
	deadline, ok := runCtx.Deadline()
	return ok && !time.Now().Before(deadline)
}

func (s *firecrackerTerrarium) CopyOut(ctx context.Context, paths []string) ([]Artifact, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	containerPaths := make([]string, 0, len(paths))
	for _, p := range paths {
		cp, err := s.resolveContainerPath(p)
		if err != nil {
			return nil, err
		}
		containerPaths = append(containerPaths, cp)
	}

	var resp fcAgentResponse
	if err := s.callAgent(ctx, fcAgentRequest{Type: "copyout", Paths: containerPaths}, &resp); err != nil {
		return nil, fmt.Errorf("copyout: %w", err)
	}
	if !resp.OK {
		return nil, fmt.Errorf("copyout agent error: %s", resp.Error)
	}

	artifacts := make([]Artifact, len(resp.Files))
	for i, f := range resp.Files {
		artifacts[i] = Artifact{Path: paths[i], Content: f.Content}
	}
	return artifacts, nil
}

func (s *firecrackerTerrarium) SnapshotLogs(_ context.Context) (TerrariumLogs, error) {
	log := s.consoleLog.String()
	return TerrariumLogs{Stdout: log, Stderr: log}, nil
}

func (s *firecrackerTerrarium) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	s.stopOnce.Do(func() {
		if s.watchdogCancel != nil {
			s.watchdogCancel()
		}

		// Attempt graceful shutdown via the agent; ignore errors (VM may already be down).
		shutCtx, shutCancel := context.WithTimeout(ctx, 2*time.Second)
		var shutResp fcAgentResponse
		_ = s.callAgent(shutCtx, fcAgentRequest{Type: "shutdown"}, &shutResp)
		shutCancel()

		// Wait for the VMM process to exit; force-kill after 3s.
		select {
		case <-s.processDone:
		case <-time.After(3 * time.Second):
			if s.cmd != nil && s.cmd.Process != nil {
				_ = s.cmd.Process.Kill()
			}
			<-s.processDone
		}

		if err := os.RemoveAll(s.workDir); err != nil {
			s.stopErr = fmt.Errorf("remove firecracker work dir: %w", err)
		}
	})

	return s.stopErr
}

// waitForAgent polls the vsock until the sprout-agent responds to a ping.
func (s *firecrackerTerrarium) waitForAgent(ctx context.Context) error {
	for {
		pingCtx, pingCancel := context.WithTimeout(ctx, fcDialRetryDelay*2)
		var resp fcAgentResponse
		err := s.callAgent(pingCtx, fcAgentRequest{Type: "ping"}, &resp)
		pingCancel()

		if err == nil && resp.OK {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.processDone:
			return fmt.Errorf("firecracker process exited before agent became ready")
		case <-time.After(fcDialRetryDelay):
		}
	}
}

// callAgent dials the vsock bridge, sends req as newline-delimited JSON, and
// decodes the JSON response into resp.
func (s *firecrackerTerrarium) callAgent(ctx context.Context, req fcAgentRequest, resp *fcAgentResponse) error {
	conn, err := s.dialVsock(fcVsockPort)
	if err != nil {
		return fmt.Errorf("dial vsock: %w", err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("encode agent request: %w", err)
	}
	if err := json.NewDecoder(conn).Decode(resp); err != nil {
		return fmt.Errorf("decode agent response: %w", err)
	}
	return nil
}

// dialVsock connects to the Firecracker vsock UDS bridge and completes the
// host-to-guest CONNECT handshake before returning the ready connection.
func (s *firecrackerTerrarium) dialVsock(port uint32) (net.Conn, error) {
	raw, err := net.DialTimeout("unix", s.vsockPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial vsock uds: %w", err)
	}

	// Firecracker vsock host-to-guest handshake:
	//   host → "CONNECT <port>\n"
	//   host ← "OK <id>\n"
	if _, err := fmt.Fprintf(raw, "CONNECT %d\n", port); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("vsock handshake write: %w", err)
	}

	br := bufio.NewReader(raw)
	line, err := br.ReadString('\n')
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("vsock handshake read: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(line), "OK") {
		_ = raw.Close()
		return nil, fmt.Errorf("vsock handshake unexpected response: %q", strings.TrimSpace(line))
	}

	// Wrap with the buffered reader so bytes consumed during the handshake
	// are not lost when the json.Decoder reads the response later.
	return &vsockConn{Conn: raw, r: br}, nil
}

func (s *firecrackerTerrarium) watchdog(ctx context.Context, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
		s.stopAfterTimeout()
	}
}

func (s *firecrackerTerrarium) stopAfterTimeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Stop(ctx)
}

func (s *firecrackerTerrarium) resolveContainerPath(rawPath string) (string, error) {
	cleanPath := pathpkg.Clean(strings.TrimSpace(rawPath))
	if cleanPath == "." || cleanPath == "/" || cleanPath == "" {
		return "", fmt.Errorf("terrarium path is required")
	}
	if pathpkg.IsAbs(cleanPath) {
		return cleanPath, nil
	}
	base := s.workingDir
	if base == "" {
		base = "/"
	}
	return pathpkg.Clean(pathpkg.Join(base, cleanPath)), nil
}

// vsockConn wraps a net.Conn with a bufio.Reader so bytes consumed during the
// Firecracker CONNECT handshake are not lost on subsequent json.Decoder reads.
type vsockConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *vsockConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

// Firecracker JSON VM config types.

type fcConfig struct {
	BootSource    fcBootSource  `json:"boot-source"`
	Drives        []fcDrive     `json:"drives"`
	MachineConfig fcMachineConf `json:"machine-config"`
	Vsock         *fcVsock      `json:"vsock,omitempty"`
}

type fcBootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type fcDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type fcMachineConf struct {
	VCPUCount  int `json:"vcpu_count"`
	MemSizeMiB int `json:"mem_size_mib"`
}

type fcVsock struct {
	VsockID  string `json:"vsock_id"`
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

// Agent wire protocol types (mirrored in cmd/sprout-agent/main.go).

type fcAgentRequest struct {
	Type  string        `json:"type"`
	Spec  *fcCmdSpec    `json:"spec,omitempty"`
	Files []fcAgentFile `json:"files,omitempty"`
	Paths []string      `json:"paths,omitempty"`
}

type fcAgentFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"` // JSON marshals []byte as base64
	Mode    uint32 `json:"mode"`
}

type fcCmdSpec struct {
	Command     []string          `json:"command,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Stdin       []byte            `json:"stdin,omitempty"`
	TimeoutMS   int64             `json:"timeout_ms,omitempty"`
}

type fcCmdResult struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	DurationMS int64  `json:"duration_ms"`
}

type fcAgentResponse struct {
	OK     bool          `json:"ok"`
	Error  string        `json:"error,omitempty"`
	Result *fcCmdResult  `json:"result,omitempty"`
	Files  []fcAgentFile `json:"files,omitempty"`
}

// CheckFirecrackerReadiness verifies the host can run a Firecracker terrarium:
// the binary is in PATH, the kernel/rootfs env vars are set, and /dev/kvm exists.
func CheckFirecrackerReadiness(ctx context.Context) error {
	if _, err := exec.LookPath("firecracker"); err != nil {
		return fmt.Errorf("firecracker binary not found in PATH: %w", err)
	}
	if strings.TrimSpace(os.Getenv("TENDRIL_FC_KERNEL_PATH")) == "" {
		return fmt.Errorf("TENDRIL_FC_KERNEL_PATH is not set. Run 'tendril terrarium init-firecracker' to bootstrap the environment")
	}
	if strings.TrimSpace(os.Getenv("TENDRIL_FC_ROOTFS_PATH")) == "" {
		return fmt.Errorf("TENDRIL_FC_ROOTFS_PATH is not set. Run 'tendril terrarium init-firecracker' to bootstrap the environment")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return fmt.Errorf("KVM not available (/dev/kvm): %w", err)
	}
	return nil
}

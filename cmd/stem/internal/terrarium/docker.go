package terrarium

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DockerProvider launches terrariumes via the local Docker daemon.
type DockerProvider struct{}

const defaultDockerPidsLimit = 512

func NewDockerProvider() *DockerProvider {
	return &DockerProvider{}
}

func (p *DockerProvider) Name() string {
	return ProviderDocker
}

func (p *DockerProvider) Capabilities() TerrariumCapabilities {
	return defaultTerrariumCapabilities()
}

func (p *DockerProvider) Create(ctx context.Context, spec TerrariumSpec) (Terrarium, error) {
	return createDockerTerrarium(ctx, p, spec)
}

func defaultTerrariumCapabilities() TerrariumCapabilities {
	return TerrariumCapabilities{
		SupportsMounts:        true,
		SupportsCopyIn:        true,
		SupportsCopyOut:       true,
		SupportsInteractiveIO: true,
		SupportedNetworkModes: []NetworkMode{
			NetworkModeBridge,
			NetworkModeHost,
			NetworkModeNone,
		},
	}
}

func createDockerTerrarium(ctx context.Context, provider TerrariumProvider, spec TerrariumSpec, extraRunArgs ...string) (Terrarium, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if provider == nil {
		provider = NewDockerProvider()
	}
	if strings.TrimSpace(spec.Image) == "" {
		return nil, fmt.Errorf("terrarium image is required")
	}

	terrariumID, err := newTerrariumID()
	if err != nil {
		return nil, fmt.Errorf("generate terrarium id: %w", err)
	}

	args := []string{
		"run",
		"-i",
	}
	args = append(args, extraRunArgs...)
	args = append(args,
		"--name", terrariumID,
		"--network", string(NetworkModeNone),
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges:true",
	)
	if cpuQuota := strings.TrimSpace(spec.CPUQuota); cpuQuota != "" && cpuQuota != "0" && cpuQuota != "0.0" {
		args = append(args, "--cpus", cpuQuota)
	}
	if spec.MemoryLimitMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", spec.MemoryLimitMB))
	}
	pidsLimit := spec.PidsLimit
	if pidsLimit <= 0 {
		pidsLimit = defaultDockerPidsLimit
	}
	args = append(args, "--pids-limit", fmt.Sprintf("%d", pidsLimit))
	if spec.ReadOnlyRootFS {
		args = append(args, "--read-only")
	}
	if runAsUser := strings.TrimSpace(spec.RunAsUser); runAsUser != "" {
		args = append(args, "--user", runAsUser)
	}

	for _, mount := range spec.Mounts {
		source := strings.TrimSpace(mount.Source)
		target := strings.TrimSpace(mount.Target)
		if source == "" || target == "" {
			return nil, fmt.Errorf("terrarium mounts require both source and target")
		}

		mountArg := fmt.Sprintf("%s:%s", source, target)
		if mount.ReadOnly {
			mountArg += ":ro"
		}
		args = append(args, "-v", mountArg)
	}

	if envFile := resolveDockerEnvFile(); envFile != "" {
		args = append(args, "--env-file", envFile)
	}

	for _, key := range sortedKeys(spec.Environment) {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, spec.Environment[key]))
	}

	if workingDir := strings.TrimSpace(spec.WorkingDir); workingDir != "" {
		args = append(args, "-w", workingDir)
	}

	args = append(args, spec.Image)
	args = append(args, spec.Command...)

	fmt.Fprintf(os.Stderr, "🚀 Executing docker: docker %s\n", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, "docker", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create terrarium stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("create terrarium stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("create terrarium stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("docker run failed: %w", err)
	}

	terrarium := &dockerTerrarium{
		provider:   provider,
		id:         terrariumID,
		workingDir: strings.TrimSpace(spec.WorkingDir),
		cmd:        cmd,
		stdin:      stdin,
		stdout:     bufio.NewReader(stdout),
		stderrDone: make(chan struct{}),
		stdoutLog:  lockedBuffer{},
		stderrLog:  lockedBuffer{},
	}

	if spec.Timeout > 0 {
		watchdogCtx, cancel := context.WithCancel(context.Background())
		terrarium.watchdogCancel = cancel
		go terrarium.watchdog(watchdogCtx, spec.Timeout)
	}

	go func() {
		defer close(terrarium.stderrDone)
		_, _ = io.Copy(io.MultiWriter(os.Stderr, &terrarium.stderrLog), stderr)
	}()

	if len(spec.Files) > 0 {
		if err := terrarium.CopyIn(ctx, spec.Files); err != nil {
			_ = terrarium.Stop(context.Background())
			return nil, err
		}
	}

	return terrarium, nil
}

type dockerTerrarium struct {
	provider   TerrariumProvider
	id         string
	workingDir string

	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	stderrDone chan struct{}

	stdoutLog lockedBuffer
	stderrLog lockedBuffer

	callMu         sync.Mutex
	stopOnce       sync.Once
	stopErr        error
	waitOnce       sync.Once
	waitErr        error
	watchdogCancel context.CancelFunc
}

func (s *dockerTerrarium) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

func (s *dockerTerrarium) Provider() TerrariumProvider {
	if s == nil {
		return nil
	}
	return s.provider
}

func (s *dockerTerrarium) CopyIn(ctx context.Context, payloads []FilePayload) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return fmt.Errorf("terrarium is nil")
	}

	for _, payload := range payloads {
		containerPath, err := s.resolveContainerPath(payload.Path)
		if err != nil {
			return err
		}

		parent := pathpkg.Dir(containerPath)
		if parent != "" && parent != "." && parent != "/" {
			if _, _, err := runDockerCommand(ctx, "exec", s.id, "mkdir", "-p", parent); err != nil {
				return fmt.Errorf("create terrarium directory %s: %w", parent, err)
			}
		}

		shadowRoot, err := os.MkdirTemp("", "opentendril-terrarium-copyin-*")
		if err != nil {
			return fmt.Errorf("create copy-in temp dir: %w", err)
		}

		cleanLocalPath, err := localShadowPath(shadowRoot, containerPath)
		if err != nil {
			_ = os.RemoveAll(shadowRoot)
			return err
		}
		if err := os.MkdirAll(filepath.Dir(cleanLocalPath), 0o755); err != nil {
			_ = os.RemoveAll(shadowRoot)
			return fmt.Errorf("create copy-in parent dir: %w", err)
		}

		mode := payload.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(cleanLocalPath, payload.Content, mode); err != nil {
			_ = os.RemoveAll(shadowRoot)
			return fmt.Errorf("write copy-in payload: %w", err)
		}

		if _, _, err := runDockerCommand(ctx, "cp", cleanLocalPath, fmt.Sprintf("%s:%s", s.id, containerPath)); err != nil {
			_ = os.RemoveAll(shadowRoot)
			return fmt.Errorf("copy file into terrarium: %w", err)
		}

		_ = os.RemoveAll(shadowRoot)
	}

	return nil
}

// containerStartGrace bounds how long an exec retries while the asynchronously
// started container is still coming up.
const containerStartGrace = 15 * time.Second
const containerStartPollInterval = 100 * time.Millisecond

// isContainerNotReady reports whether a docker exec failure means the container
// has not started yet (as opposed to the command itself failing). Both cases
// leave the command unrun, so the exec is safe to retry.
func isContainerNotReady(stderr string) bool {
	return strings.Contains(stderr, "No such container") ||
		strings.Contains(stderr, "is not running") ||
		strings.Contains(stderr, "is restarting")
}

func (s *dockerTerrarium) Run(ctx context.Context, spec CommandSpec) (CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.cmd == nil {
		return CommandResult{}, fmt.Errorf("terrarium is not active")
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	startedAt := time.Now().UTC()

	if len(spec.Command) == 0 {
		s.callMu.Lock()
		defer s.callMu.Unlock()

		payload := append([]byte(nil), spec.Stdin...)
		if len(payload) == 0 {
			return CommandResult{}, fmt.Errorf("interactive terrarium run requires stdin payload")
		}
		if payload[len(payload)-1] != '\n' {
			payload = append(payload, '\n')
		}

		if _, err := s.stdin.Write(payload); err != nil {
			return CommandResult{}, fmt.Errorf("write terrarium stdin: %w", err)
		}

		lineCh := make(chan []byte, 1)
		errCh := make(chan error, 1)
		go func() {
			line, err := s.readResponseLine()
			if err != nil {
				errCh <- err
				return
			}
			lineCh <- line
		}()

		completedAt := time.Now().UTC()
		var line []byte
		var err error
		select {
		case line = <-lineCh:
		case err = <-errCh:
		case <-runCtx.Done():
			completedAt = time.Now().UTC()
			result := CommandResult{
				ExitCode:    -1,
				TimedOut:    errors.Is(runCtx.Err(), context.DeadlineExceeded),
				StartedAt:   startedAt,
				CompletedAt: completedAt,
				Duration:    completedAt.Sub(startedAt),
			}
			if result.TimedOut {
				s.stopAfterTimeout()
				return result, nil
			}
			return result, fmt.Errorf("interactive terrarium run canceled: %w", runCtx.Err())
		}

		completedAt = time.Now().UTC()
		result := CommandResult{
			Stdout:      string(line),
			ExitCode:    0,
			StartedAt:   startedAt,
			CompletedAt: completedAt,
			Duration:    completedAt.Sub(startedAt),
		}
		if err != nil {
			return result, err
		}

		if len(line) > 0 {
			_, _ = s.stdoutLog.Write(append(append([]byte(nil), line...), '\n'))
		}

		return result, nil
	}

	args := []string{"exec"}
	// Without -i, docker exec never attaches the client's stdin to the
	// container process, so a CommandSpec.Stdin payload would silently read as
	// empty inside the terrarium. Attach it only when a payload exists so
	// stdin-less execs keep their exact historical invocation.
	if len(spec.Stdin) > 0 {
		args = append(args, "-i")
	}
	if workingDir := strings.TrimSpace(spec.WorkingDir); workingDir != "" {
		args = append(args, "-w", workingDir)
	} else if s.workingDir != "" {
		args = append(args, "-w", s.workingDir)
	}
	for _, key := range sortedKeys(spec.Environment) {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, spec.Environment[key]))
	}
	args = append(args, s.id)
	args = append(args, spec.Command...)

	// createDockerTerrarium starts `docker run` asynchronously (cmd.Start) and
	// returns without waiting. The interactive path syncs via the stdin/stdout
	// handshake, but an exec issued right after Create can race the container
	// start and fail with "No such container". Retry within a short grace
	// window — the command has not run in that case, so retrying is safe.
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var runErr error
	startDeadline := time.Now().Add(containerStartGrace)
	for {
		stdout.Reset()
		stderr.Reset()
		cmd := exec.CommandContext(runCtx, "docker", args...)
		if len(spec.Stdin) > 0 {
			cmd.Stdin = bytes.NewReader(spec.Stdin)
		}
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		runErr = cmd.Run()
		if runErr == nil || runCtx.Err() != nil || time.Now().After(startDeadline) || !isContainerNotReady(stderr.String()) {
			break
		}
		select {
		case <-runCtx.Done():
		case <-time.After(containerStartPollInterval):
		}
	}
	completedAt := time.Now().UTC()
	result := CommandResult{
		Stdout:      stdout.String(),
		Stderr:      stderr.String(),
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    completedAt.Sub(startedAt),
	}

	if runErr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			result.ExitCode = -1
			result.TimedOut = true
			s.stopAfterTimeout()
			return result, nil
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return result, fmt.Errorf("docker exec failed: %w", runErr)
		}
	}

	if result.ExitCode == 0 {
		result.ExitCode = 0
	}

	if stdout.Len() > 0 {
		_, _ = s.stdoutLog.Write(stdout.Bytes())
	}
	if stderr.Len() > 0 {
		_, _ = s.stderrLog.Write(stderr.Bytes())
	}

	return result, nil
}

func (s *dockerTerrarium) CopyOut(ctx context.Context, paths []string) ([]Artifact, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return nil, fmt.Errorf("terrarium is nil")
	}

	artifacts := make([]Artifact, 0, len(paths))
	for _, requestedPath := range paths {
		containerPath, err := s.resolveContainerPath(requestedPath)
		if err != nil {
			return nil, err
		}

		shadowRoot, err := os.MkdirTemp("", "opentendril-terrarium-copyout-*")
		if err != nil {
			return nil, fmt.Errorf("create copy-out temp dir: %w", err)
		}

		if _, _, err := runDockerCommand(ctx, "cp", fmt.Sprintf("%s:%s", s.id, containerPath), shadowRoot); err != nil {
			_ = os.RemoveAll(shadowRoot)
			return nil, fmt.Errorf("copy file out of terrarium: %w", err)
		}

		localPath := filepath.Join(shadowRoot, pathpkg.Base(containerPath))
		content, err := os.ReadFile(localPath)
		_ = os.RemoveAll(shadowRoot)
		if err != nil {
			return nil, fmt.Errorf("read copied artifact: %w", err)
		}

		artifacts = append(artifacts, Artifact{
			Path:    requestedPath,
			Content: content,
		})
	}

	return artifacts, nil
}

func (s *dockerTerrarium) SnapshotLogs(ctx context.Context) (TerrariumLogs, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return TerrariumLogs{}, fmt.Errorf("terrarium is nil")
	}

	return TerrariumLogs{
		Stdout: s.stdoutLog.String(),
		Stderr: s.stderrLog.String(),
	}, nil
}

func (s *dockerTerrarium) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx := context.WithoutCancel(ctx)

	s.stopOnce.Do(func() {
		var stopErr error

		if s.watchdogCancel != nil {
			s.watchdogCancel()
		}

		if s.stdin != nil {
			_ = s.stdin.Close()
		}

		waitCh := make(chan error, 1)
		go func() {
			waitCh <- s.wait()
		}()

		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()

		waited := false
		stopCalled := false
		select {
		case err := <-waitCh:
			waited = true
			if err != nil && !isIgnorableWaitError(err) {
				stopErr = errors.Join(stopErr, err)
			}
		case <-timer.C:
			stopCalled = true
			if _, output, err := runDockerCommand(cleanupCtx, "stop", s.id); err != nil && !isIgnorableDockerLifecycleOutput(output) {
				stopErr = errors.Join(stopErr, err)
			}
		}

		if !stopCalled {
			if _, output, err := runDockerCommand(cleanupCtx, "stop", s.id); err != nil && !isIgnorableDockerLifecycleOutput(output) {
				stopErr = errors.Join(stopErr, err)
			}
		}

		if !waited {
			if err := <-waitCh; err != nil && !isIgnorableWaitError(err) {
				stopErr = errors.Join(stopErr, err)
			}
		}

		if s.stderrDone != nil {
			<-s.stderrDone
		}

		if _, output, err := runDockerCommand(cleanupCtx, "rm", s.id); err != nil && !isIgnorableDockerLifecycleOutput(output) {
			stopErr = errors.Join(stopErr, err)
		}

		s.stopErr = stopErr
	})

	return s.stopErr
}

func (s *dockerTerrarium) watchdog(ctx context.Context, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		s.stopAfterTimeout()
	}
}

func (s *dockerTerrarium) stopAfterTimeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Stop(ctx)
}

func (s *dockerTerrarium) readResponseLine() ([]byte, error) {
	for {
		line, err := s.stdout.ReadBytes('\n')
		if err != nil && len(bytes.TrimSpace(line)) == 0 {
			return nil, fmt.Errorf("read terrarium response: %w", err)
		}

		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if err != nil {
				return nil, fmt.Errorf("read terrarium response: %w", err)
			}
			continue
		}

		return trimmed, nil
	}
}

func (s *dockerTerrarium) resolveContainerPath(rawPath string) (string, error) {
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

func (s *dockerTerrarium) wait() error {
	s.waitOnce.Do(func() {
		if s.cmd == nil {
			return
		}
		s.waitErr = s.cmd.Wait()
	})
	return s.waitErr
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func runDockerCommand(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		trimmedOutput := strings.TrimSpace(stderr.String())
		if trimmedOutput == "" {
			trimmedOutput = strings.TrimSpace(stdout.String())
		}
		return stdout.String(), stderr.String(), fmt.Errorf("docker %s failed: %w (output: %s)", strings.Join(args, " "), err, trimmedOutput)
	}

	return stdout.String(), stderr.String(), nil
}

func resolveDockerEnvFile() string {
	if envFile := strings.TrimSpace(os.Getenv("TENDRIL_ENV_FILE")); envFile != "" {
		if _, err := os.Stat(envFile); err == nil {
			return envFile
		}
	}
	if _, err := os.Stat(".env"); err == nil {
		return ".env"
	}
	return ""
}

func sortedKeys(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func localShadowPath(root, containerPath string) (string, error) {
	cleanPath := pathpkg.Clean(containerPath)
	if cleanPath == "." || cleanPath == "/" {
		return "", fmt.Errorf("terrarium path %q cannot be materialized locally", containerPath)
	}

	trimmed := strings.TrimPrefix(cleanPath, "/")
	if trimmed == "" {
		return "", fmt.Errorf("terrarium path %q cannot be materialized locally", containerPath)
	}

	return filepath.Join(root, filepath.FromSlash(trimmed)), nil
}

func newTerrariumID() (string, error) {
	bytes := make([]byte, 6)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "opentendril-terrarium-" + hex.EncodeToString(bytes), nil
}

func isIgnorableDockerLifecycleOutput(output string) bool {
	lowerOutput := strings.ToLower(strings.TrimSpace(output))
	if lowerOutput == "" {
		return false
	}

	return strings.Contains(lowerOutput, "no such container") ||
		strings.Contains(lowerOutput, "is not running") ||
		strings.Contains(lowerOutput, "is already in progress")
}

func isIgnorableWaitError(err error) bool {
	if err == nil {
		return false
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}

	return exitErr.ExitCode() == 137 || exitErr.ExitCode() == 143
}

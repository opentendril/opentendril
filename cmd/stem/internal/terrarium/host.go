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
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// HostProvider launches terrariums directly on the local host machine using os/exec.
type HostProvider struct{}

func NewHostProvider() *HostProvider {
	return &HostProvider{}
}

func (p *HostProvider) Name() string {
	return ProviderHost
}

func (p *HostProvider) Capabilities() TerrariumCapabilities {
	return TerrariumCapabilities{
		SupportsMounts:        false,
		SupportsCopyIn:        true,
		SupportsCopyOut:       true,
		SupportsInteractiveIO: true,
		SupportedNetworkModes: []NetworkMode{
			NetworkModeHost,
		},
	}
}

func (p *HostProvider) Create(ctx context.Context, spec TerrariumSpec) (Terrarium, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("host terrarium requires a command to execute")
	}

	terrariumID, err := newHostTerrariumID()
	if err != nil {
		return nil, fmt.Errorf("generate terrarium id: %w", err)
	}

	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)

	if workingDir := strings.TrimSpace(spec.WorkingDir); workingDir != "" {
		cmd.Dir = workingDir
	}

	if len(spec.Environment) > 0 {
		cmd.Env = os.Environ()
		for _, key := range sortedKeys(spec.Environment) {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, spec.Environment[key]))
		}
	}

	fmt.Fprintf(os.Stderr, "🚀 Executing on host: %s\n", strings.Join(spec.Command, " "))

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
		return nil, fmt.Errorf("host command run failed: %w", err)
	}

	terrarium := &hostTerrarium{
		provider:   p,
		id:         terrariumID,
		workingDir: cmd.Dir,
		cmd:        cmd,
		stdin:      stdin,
		stdout:     bufio.NewReader(stdout),
		stderrDone: make(chan struct{}),
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

type hostTerrarium struct {
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

func (s *hostTerrarium) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

func (s *hostTerrarium) Provider() TerrariumProvider {
	if s == nil {
		return nil
	}
	return s.provider
}

func (s *hostTerrarium) CopyIn(ctx context.Context, payloads []FilePayload) error {
	if s == nil {
		return fmt.Errorf("terrarium is nil")
	}

	baseDir := s.workingDir
	if baseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get host working directory: %w", err)
		}
		baseDir = cwd
	}

	for _, payload := range payloads {
		targetPath := filepath.Join(baseDir, filepath.Clean(payload.Path))

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("create copy-in parent dir: %w", err)
		}

		mode := payload.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(targetPath, payload.Content, mode); err != nil {
			return fmt.Errorf("write copy-in payload: %w", err)
		}
	}

	return nil
}

func (s *hostTerrarium) Run(ctx context.Context, spec CommandSpec) (CommandResult, error) {
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

	// If spec.Command is empty, we perform interactive IO against the running process
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

	// For one-off host execs
	cmd := exec.CommandContext(runCtx, spec.Command[0], spec.Command[1:]...)
	if workingDir := strings.TrimSpace(spec.WorkingDir); workingDir != "" {
		cmd.Dir = workingDir
	} else if s.workingDir != "" {
		cmd.Dir = s.workingDir
	}

	cmd.Env = os.Environ()
	for _, key := range sortedKeys(spec.Environment) {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, spec.Environment[key]))
	}

	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
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
			return result, nil
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return result, fmt.Errorf("host exec failed: %w", runErr)
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

func (s *hostTerrarium) CopyOut(ctx context.Context, paths []string) ([]Artifact, error) {
	if s == nil {
		return nil, fmt.Errorf("terrarium is nil")
	}

	baseDir := s.workingDir
	if baseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get host working directory: %w", err)
		}
		baseDir = cwd
	}

	artifacts := make([]Artifact, 0, len(paths))
	for _, requestedPath := range paths {
		localPath := filepath.Join(baseDir, filepath.Clean(requestedPath))
		content, err := os.ReadFile(localPath)
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

func (s *hostTerrarium) SnapshotLogs(ctx context.Context) (TerrariumLogs, error) {
	if s == nil {
		return TerrariumLogs{}, fmt.Errorf("terrarium is nil")
	}
	return TerrariumLogs{
		Stdout: s.stdoutLog.String(),
		Stderr: s.stderrLog.String(),
	}, nil
}

func (s *hostTerrarium) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		if s.watchdogCancel != nil {
			s.watchdogCancel()
		}

		if s.stdin != nil {
			_ = s.stdin.Close()
		}

		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}

		s.stopErr = s.wait()

		if s.stderrDone != nil {
			<-s.stderrDone
		}
	})

	return s.stopErr
}

func (s *hostTerrarium) watchdog(ctx context.Context, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		s.stopAfterTimeout()
	}
}

func (s *hostTerrarium) stopAfterTimeout() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Stop(ctx)
}

func (s *hostTerrarium) readResponseLine() ([]byte, error) {
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

func (s *hostTerrarium) wait() error {
	s.waitOnce.Do(func() {
		if s.cmd == nil {
			return
		}
		s.waitErr = s.cmd.Wait()
	})
	return s.waitErr
}

func newHostTerrariumID() (string, error) {
	bytes := make([]byte, 6)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "opentendril-host-" + hex.EncodeToString(bytes), nil
}

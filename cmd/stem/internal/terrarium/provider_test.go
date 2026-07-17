package terrarium

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDockerProviderCreateHonorsTerrariumSpec(t *testing.T) {
	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	envFile := filepath.Join(t.TempDir(), "terrarium.env")
	if err := os.WriteFile(envFile, []byte("EXAMPLE=value\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("TENDRIL_ENV_FILE", envFile)

	provider := NewDockerProvider()
	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		Image:          "opentendril-go:latest",
		WorkingDir:     "/app",
		NetworkMode:    NetworkModeBridge,
		RunAsUser:      "1000:1000",
		CPUQuota:       "1.5",
		MemoryLimitMB:  1024,
		ReadOnlyRootFS: true,
		PidsLimit:      128,
		Mounts: []MountSpec{
			{Source: "/tmp/workspace", Target: "/app"},
			{Source: "/tmp/cache", Target: "/cache", ReadOnly: true},
		},
		Environment: map[string]string{
			"FIRST":  "1",
			"SECOND": "2",
		},
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = terrariumInstance.Stop(context.Background())
	})

	if provider.Name() != "docker" {
		t.Fatalf("provider.Name() = %q, want docker", provider.Name())
	}
	capabilities := provider.Capabilities()
	if !capabilities.SupportsMounts || !capabilities.SupportsCopyIn || !capabilities.SupportsCopyOut || !capabilities.SupportsInteractiveIO {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}

	logOutput := waitForLogContent(t, fake.logPath, "run -i --name opentendril-terrarium-")
	for _, needle := range []string{
		"--network none",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges:true",
		"--cpus 1.5",
		"--memory 1024m",
		"--pids-limit 128",
		"--read-only",
		"--user 1000:1000",
		"-v /tmp/workspace:/app",
		"-v /tmp/cache:/cache:ro",
		"--env-file " + envFile,
		"-e FIRST",
		"-e SECOND",
		"-w /app",
		"opentendril-go:latest",
	} {
		if !strings.Contains(logOutput, needle) {
			t.Fatalf("docker run args missing %q in log %q", needle, logOutput)
		}
	}

	// The values must never appear among the arguments. Process arguments are
	// world readable through /proc, and these arguments are echoed to stderr on
	// every terrarium start — a spec carrying a token would leak it to any
	// local user and into terminal scrollback. Docker reads a valueless -e from
	// the client environment instead.
	for _, forbidden := range []string{"FIRST=1", "SECOND=2"} {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("environment VALUE %q reached the docker command line: %q", forbidden, logOutput)
		}
	}
}

// The values still have to arrive, or the split above would be secure and
// useless: the fake docker reports what it actually received in its own
// environment.
func TestDockerProviderPassesEnvironmentValuesOutOfBandOfArguments(t *testing.T) {
	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_ENV_LOG", filepath.Join(t.TempDir(), "docker.env.log"))

	provider := NewDockerProvider()
	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		Image:       "opentendril-go:latest",
		WorkingDir:  "/app",
		Environment: map[string]string{"SECRET_TOKEN": "s3cr3t-value"},
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() { _ = terrariumInstance.Stop(context.Background()) })

	envLog := waitForLogContent(t, os.Getenv("FAKE_DOCKER_ENV_LOG"), "SECRET_TOKEN=")
	if !strings.Contains(envLog, "SECRET_TOKEN=s3cr3t-value") {
		t.Fatalf("docker client did not receive the value in its environment: %q", envLog)
	}
}

func TestDockerProviderCreateDefaultsToPidsLimit(t *testing.T) {
	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	terrariumInstance := newTestTerrarium(t)
	t.Cleanup(func() {
		_ = terrariumInstance.Stop(context.Background())
	})

	logOutput := waitForLogContent(t, fake.logPath, "run -i --name opentendril-terrarium-")
	if !strings.Contains(logOutput, "--pids-limit 512") {
		t.Fatalf("docker run args missing default pids limit in log %q", logOutput)
	}
}

func TestDockerTerrariumRunReturnsMetrics(t *testing.T) {
	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_RESPONSE", `{"status":"success","output":{"tools":[]}}`)
	t.Setenv("FAKE_DOCKER_EXEC_STDOUT", "exec-stdout")
	t.Setenv("FAKE_DOCKER_EXEC_STDERR", "exec-stderr")
	t.Setenv("FAKE_DOCKER_EXEC_EXIT_CODE", "7")

	terrariumInstance := newTestTerrarium(t)
	t.Cleanup(func() {
		_ = terrariumInstance.Stop(context.Background())
	})

	interactiveResult, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Stdin: []byte(`{"tool":"listAvailableTools","arguments":{}}`),
	})
	if err != nil {
		t.Fatalf("interactive Run returned error: %v", err)
	}
	if interactiveResult.Stdout != `{"status":"success","output":{"tools":[]}}` {
		t.Fatalf("interactive stdout = %q", interactiveResult.Stdout)
	}
	if interactiveResult.ExitCode != 0 {
		t.Fatalf("interactive exit code = %d, want 0", interactiveResult.ExitCode)
	}
	assertCommandMetrics(t, interactiveResult)

	execResult, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Command: []string{"sh", "-lc", "echo hi"},
	})
	if err != nil {
		t.Fatalf("exec Run returned error: %v", err)
	}
	if execResult.Stdout != "exec-stdout" {
		t.Fatalf("exec stdout = %q, want exec-stdout", execResult.Stdout)
	}
	if execResult.Stderr != "exec-stderr" {
		t.Fatalf("exec stderr = %q, want exec-stderr", execResult.Stderr)
	}
	if execResult.ExitCode != 7 {
		t.Fatalf("exec exit code = %d, want 7", execResult.ExitCode)
	}
	assertCommandMetrics(t, execResult)

	stdinLog, err := os.ReadFile(fake.stdinPath)
	if err != nil {
		t.Fatalf("read stdin log: %v", err)
	}
	if !strings.Contains(string(stdinLog), `{"tool":"listAvailableTools","arguments":{}}`) {
		t.Fatalf("stdin log missing tool payload: %s", string(stdinLog))
	}

	logs, err := terrariumInstance.SnapshotLogs(context.Background())
	if err != nil {
		t.Fatalf("SnapshotLogs returned error: %v", err)
	}
	if !strings.Contains(logs.Stdout, `{"status":"success","output":{"tools":[]}}`) {
		t.Fatalf("stdout logs missing interactive response: %q", logs.Stdout)
	}
	if !strings.Contains(logs.Stderr, "terrarium stderr ready") {
		t.Fatalf("stderr logs missing terrarium boot output: %q", logs.Stderr)
	}
}

func TestDockerTerrariumRunTimeoutStopsTerrarium(t *testing.T) {
	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_EXEC_SLEEP", "1")

	terrariumInstance := newTestTerrarium(t)

	result, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Command: []string{"sh", "-lc", "sleep 60"},
		Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("TimedOut = false, want true")
	}
	if result.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", result.ExitCode)
	}

	logOutput := waitForLogContent(t, fake.logPath, "rm opentendril-terrarium-")
	if !strings.Contains(logOutput, "stop opentendril-terrarium-") {
		t.Fatalf("timeout did not stop terrarium, log %q", logOutput)
	}
}

func TestDockerTerrariumWatchdogStopsExpiredTerrarium(t *testing.T) {
	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	provider := NewDockerProvider()
	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		Image:     "opentendril-go:latest",
		Timeout:   50 * time.Millisecond,
		PidsLimit: 64,
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = terrariumInstance.Stop(context.Background())
	})

	logOutput := waitForLogContent(t, fake.logPath, "rm opentendril-terrarium-")
	if !strings.Contains(logOutput, "stop opentendril-terrarium-") {
		t.Fatalf("watchdog did not stop terrarium, log %q", logOutput)
	}
}

func TestDockerTerrariumCopyInCopyOutAndStop(t *testing.T) {
	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_COPYOUT_CONTENT", "artifact-data")

	terrariumInstance := newTestTerrarium(t)

	if err := terrariumInstance.CopyIn(context.Background(), []FilePayload{
		{
			Path:    "nested/result.txt",
			Content: []byte("hello terrarium"),
			Mode:    0o640,
		},
	}); err != nil {
		t.Fatalf("CopyIn returned error: %v", err)
	}

	artifacts, err := terrariumInstance.CopyOut(context.Background(), []string{"nested/result.txt"})
	if err != nil {
		t.Fatalf("CopyOut returned error: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("CopyOut returned %d artifacts, want 1", len(artifacts))
	}
	if artifacts[0].Path != "nested/result.txt" {
		t.Fatalf("artifact path = %q, want nested/result.txt", artifacts[0].Path)
	}
	if string(artifacts[0].Content) != "artifact-data" {
		t.Fatalf("artifact content = %q, want artifact-data", string(artifacts[0].Content))
	}

	if err := terrariumInstance.Stop(context.Background()); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	logOutput := waitForLogContent(t, fake.logPath, "rm opentendril-terrarium-")
	for _, needle := range []string{
		"exec opentendril-terrarium-",
		"mkdir -p /app/nested",
		"cp ",
		"nested/result.txt",
		"stop opentendril-terrarium-",
		"rm opentendril-terrarium-",
	} {
		if !strings.Contains(logOutput, needle) {
			t.Fatalf("docker lifecycle log missing %q in %q", needle, logOutput)
		}
	}
}

type fakeDockerPaths struct {
	binDir    string
	logPath   string
	stdinPath string
}

func installFakeDocker(t *testing.T) fakeDockerPaths {
	t.Helper()

	root := t.TempDir()
	logPath := filepath.Join(root, "docker.log")
	stdinPath := filepath.Join(root, "docker.stdin.log")
	scriptPath := filepath.Join(root, "docker")

	script := `#!/bin/sh
set -eu

printf '%s\n' "$*" >> "${FAKE_DOCKER_LOG}"

# Records what the real docker client would resolve a valueless -e from, so a
# test can prove the values arrive without ever touching the argument list.
if [ -n "${FAKE_DOCKER_ENV_LOG:-}" ]; then
  env >> "${FAKE_DOCKER_ENV_LOG}"
fi

cmd="${1:-}"
if [ "$#" -gt 0 ]; then
  shift
fi

case "$cmd" in
  run)
    printf 'terrarium stderr ready\n' >&2
    while IFS= read -r line; do
      printf '%s\n' "$line" >> "${FAKE_DOCKER_STDIN_LOG}"
      sleep 0.01
      response="${FAKE_DOCKER_RESPONSE:-}"
      if [ -z "$response" ]; then
        response='{"status":"success","output":{}}'
      fi
      printf '%s\n' "$response"
    done
    ;;
  info)
    if [ -n "${FAKE_DOCKER_RUNSC_READY:-}" ]; then
      printf 'runsc\n'
    else
      printf '<no value>\n'
    fi
    ;;
  exec)
    terrarium_id="${1:-}"
    if [ "$#" -gt 0 ]; then
      shift
    fi
    if [ "${1:-}" = "mkdir" ] && [ "${2:-}" = "-p" ]; then
      exit 0
    fi
    if [ -n "${FAKE_DOCKER_EXEC_SLEEP:-}" ]; then
      sleep "${FAKE_DOCKER_EXEC_SLEEP}"
    fi
    if [ -n "${FAKE_DOCKER_EXEC_STDOUT:-}" ]; then
      printf '%s' "${FAKE_DOCKER_EXEC_STDOUT}"
    fi
    if [ -n "${FAKE_DOCKER_EXEC_STDERR:-}" ]; then
      printf '%s' "${FAKE_DOCKER_EXEC_STDERR}" >&2
    fi
    exit "${FAKE_DOCKER_EXEC_EXIT_CODE:-0}"
    ;;
  cp)
    src="${1:-}"
    dst="${2:-}"
    if printf '%s' "$src" | grep -q ':'; then
      base=$(basename "${src#*:}")
      mkdir -p "$dst"
      printf '%s' "${FAKE_DOCKER_COPYOUT_CONTENT:-artifact-data}" > "$dst/$base"
    else
      cat "$src" >/dev/null
    fi
    ;;
  stop|rm)
    ;;
esac
`

	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker script: %v", err)
	}

	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("FAKE_DOCKER_STDIN_LOG", stdinPath)

	return fakeDockerPaths{
		binDir:    root,
		logPath:   logPath,
		stdinPath: stdinPath,
	}
}

func newTestTerrarium(t *testing.T) Terrarium {
	t.Helper()

	provider := NewDockerProvider()
	return newTestTerrariumWithProvider(t, provider)
}

func newTestTerrariumWithProvider(t *testing.T, provider TerrariumProvider) Terrarium {
	t.Helper()

	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		Image:       "opentendril-go:latest",
		WorkingDir:  "/app",
		NetworkMode: NetworkModeBridge,
		Mounts: []MountSpec{
			{Source: t.TempDir(), Target: "/app"},
		},
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}

	return terrariumInstance
}

func waitForLogContent(t *testing.T, path string, needle string) string {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(content), needle) {
			return string(content)
		}
		time.Sleep(25 * time.Millisecond)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file %s: %v", path, err)
	}
	return string(content)
}

func assertCommandMetrics(t *testing.T, result CommandResult) {
	t.Helper()

	if result.StartedAt.IsZero() {
		t.Fatalf("StartedAt was zero")
	}
	if result.CompletedAt.IsZero() {
		t.Fatalf("CompletedAt was zero")
	}
	if result.CompletedAt.Before(result.StartedAt) {
		t.Fatalf("CompletedAt %s before StartedAt %s", result.CompletedAt, result.StartedAt)
	}
	if result.Duration <= 0 {
		t.Fatalf("Duration = %s, want > 0", result.Duration)
	}
}

func TestGVisorProviderCreateHonorsTerrariumSpec(t *testing.T) {
	testProviderCreateHonorsTerrariumSpec(t, NewGVisorProvider(), ProviderGVisor, "--runtime=runsc")
}

func TestGVisorProviderCreateDefaultsToPidsLimit(t *testing.T) {
	testProviderCreateDefaultsToPidsLimit(t, NewGVisorProvider())
}

func TestGVisorTerrariumRunReturnsMetrics(t *testing.T) {
	testProviderRunReturnsMetrics(t, NewGVisorProvider())
}

func TestGVisorTerrariumRunTimeoutStopsTerrarium(t *testing.T) {
	testProviderRunTimeoutStopsTerrarium(t, NewGVisorProvider())
}

func TestGVisorTerrariumWatchdogStopsExpiredTerrarium(t *testing.T) {
	testProviderWatchdogStopsExpiredTerrarium(t, NewGVisorProvider())
}

func TestGVisorTerrariumCopyInCopyOutAndStop(t *testing.T) {
	testProviderCopyInCopyOutAndStop(t, NewGVisorProvider())
}

func TestCheckGVisorReadiness(t *testing.T) {
	t.Run("missing runtime", func(t *testing.T) {
		fake := installFakeDocker(t)
		t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

		err := CheckGVisorReadiness(context.Background())
		if err == nil {
			t.Fatal("CheckGVisorReadiness returned nil, want error")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "runsc") {
			t.Fatalf("CheckGVisorReadiness error = %q, want runsc mention", err)
		}
	})

	t.Run("available", func(t *testing.T) {
		fake := installFakeDocker(t)
		t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("FAKE_DOCKER_RUNSC_READY", "1")

		if err := CheckGVisorReadiness(context.Background()); err != nil {
			t.Fatalf("CheckGVisorReadiness returned error: %v", err)
		}
	})
}

func TestNewProviderResolvesTerrariumProviders(t *testing.T) {
	t.Run("defaults to docker", func(t *testing.T) {
		provider, err := NewProvider(context.Background(), "")
		if err != nil {
			t.Fatalf("NewProvider returned error: %v", err)
		}
		if provider == nil {
			t.Fatal("NewProvider returned nil provider")
		}
		if provider.Name() != ProviderDocker {
			t.Fatalf("provider.Name() = %q, want %q", provider.Name(), ProviderDocker)
		}
	})

	t.Run("selects gvisor when ready", func(t *testing.T) {
		fake := installFakeDocker(t)
		t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("FAKE_DOCKER_RUNSC_READY", "1")

		provider, err := NewProvider(context.Background(), ProviderGVisor)
		if err != nil {
			t.Fatalf("NewProvider returned error: %v", err)
		}
		if provider == nil {
			t.Fatal("NewProvider returned nil provider")
		}
		if provider.Name() != ProviderGVisor {
			t.Fatalf("provider.Name() = %q, want %q", provider.Name(), ProviderGVisor)
		}
	})

	t.Run("rejects unknown provider", func(t *testing.T) {
		if _, err := NewProvider(context.Background(), "unknown"); err == nil {
			t.Fatal("NewProvider returned nil error for unknown provider")
		}
	})

	t.Run("selects firecracker when ready", func(t *testing.T) {
		if err := CheckFirecrackerReadiness(context.Background()); err != nil {
			t.Skipf("firecracker not available: %v", err)
		}

		provider, err := NewProvider(context.Background(), ProviderFirecracker)
		if err != nil {
			t.Fatalf("NewProvider returned error: %v", err)
		}
		if provider == nil {
			t.Fatal("NewProvider returned nil provider")
		}
		if provider.Name() != ProviderFirecracker {
			t.Fatalf("provider.Name() = %q, want %q", provider.Name(), ProviderFirecracker)
		}
	})
}

func TestCheckFirecrackerReadiness(t *testing.T) {
	t.Run("missing binary", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv("TENDRIL_FC_KERNEL_PATH", "/tmp/vmlinux")
		t.Setenv("TENDRIL_FC_ROOTFS_PATH", "/tmp/rootfs.ext4")

		err := CheckFirecrackerReadiness(context.Background())
		if err == nil {
			t.Fatal("CheckFirecrackerReadiness returned nil, want error")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "firecracker") {
			t.Fatalf("CheckFirecrackerReadiness error = %q, want firecracker mention", err)
		}
	})

	t.Run("missing kernel path", func(t *testing.T) {
		fakeBin := installFakeFirecrackerBinary(t)
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("TENDRIL_FC_KERNEL_PATH", "")
		t.Setenv("TENDRIL_FC_ROOTFS_PATH", "/tmp/rootfs.ext4")

		err := CheckFirecrackerReadiness(context.Background())
		if err == nil {
			t.Fatal("CheckFirecrackerReadiness returned nil, want error")
		}
		if !strings.Contains(err.Error(), "TENDRIL_FC_KERNEL_PATH") {
			t.Fatalf("error = %q, want TENDRIL_FC_KERNEL_PATH mention", err)
		}
	})

	t.Run("missing rootfs path", func(t *testing.T) {
		fakeBin := installFakeFirecrackerBinary(t)
		t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("TENDRIL_FC_KERNEL_PATH", "/tmp/vmlinux")
		t.Setenv("TENDRIL_FC_ROOTFS_PATH", "")

		err := CheckFirecrackerReadiness(context.Background())
		if err == nil {
			t.Fatal("CheckFirecrackerReadiness returned nil, want error")
		}
		if !strings.Contains(err.Error(), "TENDRIL_FC_ROOTFS_PATH") {
			t.Fatalf("error = %q, want TENDRIL_FC_ROOTFS_PATH mention", err)
		}
	})
}

// TestFirecrackerProvider* are integration tests that require KVM, the
// firecracker binary, and TENDRIL_FC_KERNEL_PATH/TENDRIL_FC_ROOTFS_PATH.
// They are automatically skipped when the environment does not support them.

func TestFirecrackerProviderRunExec(t *testing.T) {
	skipIfFirecrackerUnavailable(t)

	provider := NewFirecrackerProvider()
	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		WorkingDir: "/root",
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() { _ = terrariumInstance.Stop(context.Background()) })

	result, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Command: []string{"echo", "hello-firecracker"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if strings.TrimSpace(result.Stdout) != "hello-firecracker" {
		t.Fatalf("stdout = %q, want hello-firecracker", result.Stdout)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	assertCommandMetrics(t, result)
}

func TestFirecrackerProviderRunExitCode(t *testing.T) {
	skipIfFirecrackerUnavailable(t)

	provider := NewFirecrackerProvider()
	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() { _ = terrariumInstance.Stop(context.Background()) })

	result, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Command: []string{"sh", "-c", "exit 42"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.ExitCode != 42 {
		t.Fatalf("exit code = %d, want 42", result.ExitCode)
	}
}

func TestFirecrackerProviderRunTimeoutStopsTerrarium(t *testing.T) {
	skipIfFirecrackerUnavailable(t)

	provider := NewFirecrackerProvider()
	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}

	result, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Command: []string{"sleep", "60"},
		Timeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("TimedOut = false, want true")
	}
	if result.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", result.ExitCode)
	}
}

func TestFirecrackerProviderWatchdogStopsExpiredTerrarium(t *testing.T) {
	skipIfFirecrackerUnavailable(t)

	provider := NewFirecrackerProvider()
	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		Timeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() { _ = terrariumInstance.Stop(context.Background()) })

	// The watchdog should kill the VM; a subsequent Run should fail.
	time.Sleep(300 * time.Millisecond)
	_, err = terrariumInstance.Run(context.Background(), CommandSpec{
		Command: []string{"echo", "hi"},
	})
	if err == nil {
		t.Fatal("expected Run to fail after watchdog killed terrarium, got nil error")
	}
}

func TestFirecrackerProviderCopyInCopyOut(t *testing.T) {
	skipIfFirecrackerUnavailable(t)

	provider := NewFirecrackerProvider()
	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		WorkingDir: "/root",
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() { _ = terrariumInstance.Stop(context.Background()) })

	if err := terrariumInstance.CopyIn(context.Background(), []FilePayload{
		{Path: "result.txt", Content: []byte("fc-artifact"), Mode: 0o640},
	}); err != nil {
		t.Fatalf("CopyIn returned error: %v", err)
	}

	artifacts, err := terrariumInstance.CopyOut(context.Background(), []string{"result.txt"})
	if err != nil {
		t.Fatalf("CopyOut returned error: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("CopyOut returned %d artifacts, want 1", len(artifacts))
	}
	if string(artifacts[0].Content) != "fc-artifact" {
		t.Fatalf("artifact content = %q, want fc-artifact", string(artifacts[0].Content))
	}
}

func TestFirecrackerProviderCapabilities(t *testing.T) {
	provider := NewFirecrackerProvider()
	if provider.Name() != ProviderFirecracker {
		t.Fatalf("Name() = %q, want %q", provider.Name(), ProviderFirecracker)
	}
	caps := provider.Capabilities()
	if !caps.SupportsCopyIn {
		t.Fatal("expected SupportsCopyIn = true")
	}
	if !caps.SupportsCopyOut {
		t.Fatal("expected SupportsCopyOut = true")
	}
	if caps.SupportsInteractiveIO {
		t.Fatal("expected SupportsInteractiveIO = false for firecracker provider")
	}
}

func skipIfFirecrackerUnavailable(t *testing.T) {
	t.Helper()
	if err := CheckFirecrackerReadiness(context.Background()); err != nil {
		t.Skipf("firecracker not available: %v", err)
	}
}

func installFakeFirecrackerBinary(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	fcPath := filepath.Join(dir, "firecracker")
	if err := os.WriteFile(fcPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake firecracker binary: %v", err)
	}
	return dir
}

func testProviderCreateHonorsTerrariumSpec(t *testing.T, provider TerrariumProvider, expectedName string, extraNeedles ...string) {
	t.Helper()

	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	envFile := filepath.Join(t.TempDir(), "terrarium.env")
	if err := os.WriteFile(envFile, []byte("EXAMPLE=value\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("TENDRIL_ENV_FILE", envFile)

	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		Image:          "opentendril-go:latest",
		WorkingDir:     "/app",
		NetworkMode:    NetworkModeBridge,
		RunAsUser:      "1000:1000",
		CPUQuota:       "1.5",
		MemoryLimitMB:  1024,
		ReadOnlyRootFS: true,
		PidsLimit:      128,
		Mounts: []MountSpec{
			{Source: "/tmp/workspace", Target: "/app"},
			{Source: "/tmp/cache", Target: "/cache", ReadOnly: true},
		},
		Environment: map[string]string{
			"FIRST":  "1",
			"SECOND": "2",
		},
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = terrariumInstance.Stop(context.Background())
	})

	if provider.Name() != expectedName {
		t.Fatalf("provider.Name() = %q, want %s", provider.Name(), expectedName)
	}
	capabilities := provider.Capabilities()
	if !capabilities.SupportsMounts || !capabilities.SupportsCopyIn || !capabilities.SupportsCopyOut || !capabilities.SupportsInteractiveIO {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}

	logOutput := waitForLogContent(t, fake.logPath, "run -i")
	for _, needle := range append([]string{
		"--network none",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges:true",
		"--cpus 1.5",
		"--memory 1024m",
		"--pids-limit 128",
		"--read-only",
		"--user 1000:1000",
		"-v /tmp/workspace:/app",
		"-v /tmp/cache:/cache:ro",
		"--env-file " + envFile,
		"-e FIRST",
		"-e SECOND",
		"-w /app",
		"opentendril-go:latest",
	}, extraNeedles...) {
		if !strings.Contains(logOutput, needle) {
			t.Fatalf("docker run args missing %q in log %q", needle, logOutput)
		}
	}

	// Applies to every provider routed through this helper: a value on the
	// command line is readable through /proc for the container's lifetime and
	// is echoed to stderr on start, so a spec carrying a token would leak it.
	for _, forbidden := range []string{"FIRST=1", "SECOND=2"} {
		if strings.Contains(logOutput, forbidden) {
			t.Fatalf("environment VALUE %q reached the docker command line: %q", forbidden, logOutput)
		}
	}
}

func testProviderCreateDefaultsToPidsLimit(t *testing.T, provider TerrariumProvider) {
	t.Helper()

	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	terrariumInstance := newTestTerrariumWithProvider(t, provider)
	t.Cleanup(func() {
		_ = terrariumInstance.Stop(context.Background())
	})

	logOutput := waitForLogContent(t, fake.logPath, "run -i")
	if !strings.Contains(logOutput, "--pids-limit 512") {
		t.Fatalf("docker run args missing default pids limit in log %q", logOutput)
	}
}

func testProviderRunReturnsMetrics(t *testing.T, provider TerrariumProvider) {
	t.Helper()

	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_RESPONSE", `{"status":"success","output":{"tools":[]}}`)
	t.Setenv("FAKE_DOCKER_EXEC_STDOUT", "exec-stdout")
	t.Setenv("FAKE_DOCKER_EXEC_STDERR", "exec-stderr")
	t.Setenv("FAKE_DOCKER_EXEC_EXIT_CODE", "7")

	terrariumInstance := newTestTerrariumWithProvider(t, provider)
	t.Cleanup(func() {
		_ = terrariumInstance.Stop(context.Background())
	})

	interactiveResult, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Stdin: []byte(`{"tool":"listAvailableTools","arguments":{}}`),
	})
	if err != nil {
		t.Fatalf("interactive Run returned error: %v", err)
	}
	if interactiveResult.Stdout != `{"status":"success","output":{"tools":[]}}` {
		t.Fatalf("interactive stdout = %q", interactiveResult.Stdout)
	}
	if interactiveResult.ExitCode != 0 {
		t.Fatalf("interactive exit code = %d, want 0", interactiveResult.ExitCode)
	}
	assertCommandMetrics(t, interactiveResult)

	execResult, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Command: []string{"sh", "-lc", "echo hi"},
	})
	if err != nil {
		t.Fatalf("exec Run returned error: %v", err)
	}
	if execResult.Stdout != "exec-stdout" {
		t.Fatalf("exec stdout = %q, want exec-stdout", execResult.Stdout)
	}
	if execResult.Stderr != "exec-stderr" {
		t.Fatalf("exec stderr = %q, want exec-stderr", execResult.Stderr)
	}
	if execResult.ExitCode != 7 {
		t.Fatalf("exec exit code = %d, want 7", execResult.ExitCode)
	}
	assertCommandMetrics(t, execResult)

	stdinLog, err := os.ReadFile(fake.stdinPath)
	if err != nil {
		t.Fatalf("read stdin log: %v", err)
	}
	if !strings.Contains(string(stdinLog), `{"tool":"listAvailableTools","arguments":{}}`) {
		t.Fatalf("stdin log missing tool payload: %s", string(stdinLog))
	}

	logs, err := terrariumInstance.SnapshotLogs(context.Background())
	if err != nil {
		t.Fatalf("SnapshotLogs returned error: %v", err)
	}
	if !strings.Contains(logs.Stdout, `{"status":"success","output":{"tools":[]}}`) {
		t.Fatalf("stdout logs missing interactive response: %q", logs.Stdout)
	}
	if !strings.Contains(logs.Stderr, "terrarium stderr ready") {
		t.Fatalf("stderr logs missing terrarium boot output: %q", logs.Stderr)
	}
}

func testProviderRunTimeoutStopsTerrarium(t *testing.T, provider TerrariumProvider) {
	t.Helper()

	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_EXEC_SLEEP", "1")

	terrariumInstance := newTestTerrariumWithProvider(t, provider)

	result, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Command: []string{"sh", "-lc", "sleep 60"},
		Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("TimedOut = false, want true")
	}
	if result.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", result.ExitCode)
	}

	logOutput := waitForLogContent(t, fake.logPath, "rm opentendril-terrarium-")
	if !strings.Contains(logOutput, "stop opentendril-terrarium-") {
		t.Fatalf("timeout did not stop terrarium, log %q", logOutput)
	}
}

func testProviderWatchdogStopsExpiredTerrarium(t *testing.T, provider TerrariumProvider) {
	t.Helper()

	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		Image:     "opentendril-go:latest",
		Timeout:   50 * time.Millisecond,
		PidsLimit: 64,
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = terrariumInstance.Stop(context.Background())
	})

	logOutput := waitForLogContent(t, fake.logPath, "rm opentendril-terrarium-")
	if !strings.Contains(logOutput, "stop opentendril-terrarium-") {
		t.Fatalf("watchdog did not stop terrarium, log %q", logOutput)
	}
}

func testProviderCopyInCopyOutAndStop(t *testing.T, provider TerrariumProvider) {
	t.Helper()

	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DOCKER_COPYOUT_CONTENT", "artifact-data")

	terrariumInstance := newTestTerrariumWithProvider(t, provider)

	if err := terrariumInstance.CopyIn(context.Background(), []FilePayload{
		{
			Path:    "nested/result.txt",
			Content: []byte("hello terrarium"),
			Mode:    0o640,
		},
	}); err != nil {
		t.Fatalf("CopyIn returned error: %v", err)
	}

	artifacts, err := terrariumInstance.CopyOut(context.Background(), []string{"nested/result.txt"})
	if err != nil {
		t.Fatalf("CopyOut returned error: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("CopyOut returned %d artifacts, want 1", len(artifacts))
	}
	if artifacts[0].Path != "nested/result.txt" {
		t.Fatalf("artifact path = %q, want nested/result.txt", artifacts[0].Path)
	}
	if string(artifacts[0].Content) != "artifact-data" {
		t.Fatalf("artifact content = %q, want artifact-data", string(artifacts[0].Content))
	}

	if err := terrariumInstance.Stop(context.Background()); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}

	logOutput := waitForLogContent(t, fake.logPath, "rm opentendril-terrarium-")
	for _, needle := range []string{
		"exec opentendril-terrarium-",
		"mkdir -p /app/nested",
		"cp ",
		"nested/result.txt",
		"stop opentendril-terrarium-",
		"rm opentendril-terrarium-",
	} {
		if !strings.Contains(logOutput, needle) {
			t.Fatalf("docker lifecycle log missing %q in %q", needle, logOutput)
		}
	}
}

// A Run in flight (or issued) after the instance watchdog has killed the
// container must report the timeout, not an opaque pipe error: the sprout path
// classifies its outcome from CommandResult.TimedOut, and before this guard a
// watchdog-killed sprout could still report success.
func TestDockerTerrariumInteractiveRunAfterWatchdogReportsTimedOut(t *testing.T) {
	fake := installFakeDocker(t)
	t.Setenv("PATH", fake.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	provider := NewDockerProvider()
	terrariumInstance, err := provider.Create(context.Background(), TerrariumSpec{
		Image:     "opentendril-go:latest",
		Timeout:   50 * time.Millisecond,
		PidsLimit: 64,
	})
	if err != nil {
		t.Fatalf("provider.Create returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = terrariumInstance.Stop(context.Background())
	})

	waitForLogContent(t, fake.logPath, "rm opentendril-terrarium-")

	result, err := terrariumInstance.Run(context.Background(), CommandSpec{
		Stdin: []byte(`{"tool":"listAvailableTools"}`),
	})
	if err != nil {
		t.Fatalf("Run after watchdog kill returned error: %v (want timed-out result)", err)
	}
	if !result.TimedOut {
		t.Fatalf("TimedOut = false, want true after watchdog kill")
	}
	if result.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", result.ExitCode)
	}
}

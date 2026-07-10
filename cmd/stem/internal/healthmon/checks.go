package healthmon

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/opentendril/core/roots/llm"
)

const (
	bytesInGB       = uint64(1024 * 1024 * 1024)
	bytesInMB       = uint64(1024 * 1024)
	diskCriticalMin = uint64(100) * bytesInMB
	diskWarningMin  = bytesInGB
	memWarningMin   = uint64(500) * 1024
)

func DefaultChecks() []HealthCheck {
	return []HealthCheck{
		DockerDaemonCheck{},
		APIKeyCheck{},
		DiskSpaceCheck{},
		MemoryCheck{},
		WorkspaceCheck{},
	}
}

type DockerDaemonCheck struct{}

func (DockerDaemonCheck) Name() string {
	return "docker-daemon"
}

func (DockerDaemonCheck) Check(ctx context.Context) CheckResult {
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		return CheckResult{
			Healthy: false,
			Message: fmt.Sprintf("docker info failed: %v", err),
			Data:    map[string]interface{}{"severity": "critical"},
		}
	}
	return CheckResult{Healthy: true, Message: "Docker daemon is available", Data: map[string]interface{}{"severity": "info"}}
}

type APIKeyCheck struct{}

func (APIKeyCheck) Name() string {
	return "api-key"
}

func (APIKeyCheck) Check(ctx context.Context) CheckResult {
	_ = ctx
	providers := llm.AvailableProviders()
	hasLocal := false
	hasRemote := false
	for _, provider := range providers {
		switch strings.ToLower(strings.TrimSpace(provider)) {
		case "local":
			hasLocal = true
		case "":
		default:
			hasRemote = true
		}
	}
	if hasRemote || hasLocal {
		return CheckResult{
			Healthy: true,
			Message: "At least one LLM provider is available",
			Data: map[string]interface{}{
				"providers": providers,
				"severity":  "info",
			},
		}
	}
	return CheckResult{
		Healthy: false,
		Message: "No LLM providers are available",
		Data: map[string]interface{}{
			"providers": providers,
			"severity":  "critical",
		},
	}
}

type DiskSpaceCheck struct{}

func (DiskSpaceCheck) Name() string {
	return "disk-space"
}

func (DiskSpaceCheck) Check(ctx context.Context) CheckResult {
	_ = ctx
	cwd, err := os.Getwd()
	if err != nil {
		return CheckResult{Healthy: false, Message: fmt.Sprintf("get working directory: %v", err), Data: map[string]interface{}{"severity": "critical"}}
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(cwd, &stat); err != nil {
		return CheckResult{Healthy: false, Message: fmt.Sprintf("stat filesystem: %v", err), Data: map[string]interface{}{"severity": "critical"}}
	}

	available := stat.Bavail * uint64(stat.Bsize)
	data := map[string]interface{}{"availableBytes": available}
	switch {
	case available < diskCriticalMin:
		data["severity"] = "critical"
		return CheckResult{Healthy: false, Message: "Available disk space is below 100MB", Data: data}
	case available < diskWarningMin:
		data["severity"] = "warning"
		return CheckResult{Healthy: true, Message: "Available disk space is below 1GB", Data: data}
	default:
		data["severity"] = "info"
		return CheckResult{Healthy: true, Message: "Disk space is sufficient", Data: data}
	}
}

type MemoryCheck struct{}

func (MemoryCheck) Name() string {
	return "memory"
}

func (MemoryCheck) Check(ctx context.Context) CheckResult {
	_ = ctx
	availableKB, err := readMemAvailableKB("/proc/meminfo")
	if err != nil {
		return CheckResult{Healthy: false, Message: fmt.Sprintf("read memory info: %v", err), Data: map[string]interface{}{"severity": "critical"}}
	}

	data := map[string]interface{}{"availableKB": availableKB}
	if availableKB < memWarningMin {
		data["severity"] = "warning"
		return CheckResult{Healthy: true, Message: "Available memory is below 500MB", Data: data}
	}

	data["severity"] = "info"
	return CheckResult{Healthy: true, Message: "Memory is sufficient", Data: data}
}

func readMemAvailableKB(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed MemAvailable line")
		}
		return strconv.ParseUint(fields[1], 10, 64)
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemAvailable not found")
}

type WorkspaceCheck struct{}

func (WorkspaceCheck) Name() string {
	return "workspace"
}

func (WorkspaceCheck) Check(ctx context.Context) CheckResult {
	_ = ctx
	dir := ".tendril"
	info, err := os.Stat(dir)
	if err != nil {
		return CheckResult{Healthy: false, Message: ".tendril directory is not available", Data: map[string]interface{}{"severity": "critical"}}
	}
	if !info.IsDir() {
		return CheckResult{Healthy: false, Message: ".tendril exists but is not a directory", Data: map[string]interface{}{"severity": "critical"}}
	}

	file, err := os.CreateTemp(dir, "health-*")
	if err != nil {
		return CheckResult{Healthy: false, Message: fmt.Sprintf(".tendril is not writable: %v", err), Data: map[string]interface{}{"severity": "critical"}}
	}
	name := file.Name()
	closeErr := file.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return CheckResult{Healthy: false, Message: fmt.Sprintf("close workspace temp file: %v", closeErr), Data: map[string]interface{}{"severity": "critical"}}
	}
	if removeErr != nil && !os.IsNotExist(removeErr) {
		return CheckResult{Healthy: false, Message: fmt.Sprintf("remove workspace temp file: %v", removeErr), Data: map[string]interface{}{"severity": "critical"}}
	}

	return CheckResult{
		Healthy: true,
		Message: ".tendril workspace is writable",
		Data: map[string]interface{}{
			"path":     filepath.Clean(dir),
			"severity": "info",
		},
	}
}

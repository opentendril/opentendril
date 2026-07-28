package healthmon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/opentendril/opentendril/roots/llm"
)

const (
	bytesInGB = uint64(1024 * 1024 * 1024)
	bytesInMB = uint64(1024 * 1024)

	EnvHealthDiskCriticalMB = "TENDRIL_HEALTH_DISK_CRITICAL_MB"
	EnvHealthDiskWarningMB  = "TENDRIL_HEALTH_DISK_WARNING_MB"
	EnvHealthMemWarningMB   = "TENDRIL_HEALTH_MEM_WARNING_MB"
)

func healthDiskCriticalMBFromEnv() uint64 {
	raw := strings.TrimSpace(os.Getenv(EnvHealthDiskCriticalMB))
	if raw == "" {
		return 100
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed == 0 {
		log.Printf("⚠️ invalid %s=%q (want a positive integer); using default 100: %v", EnvHealthDiskCriticalMB, raw, err)
		return 100
	}
	return parsed
}

func healthDiskWarningMBFromEnv() uint64 {
	raw := strings.TrimSpace(os.Getenv(EnvHealthDiskWarningMB))
	if raw == "" {
		return 1024
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed == 0 {
		log.Printf("⚠️ invalid %s=%q (want a positive integer); using default 1024: %v", EnvHealthDiskWarningMB, raw, err)
		return 1024
	}
	return parsed
}

func healthMemWarningMBFromEnv() uint64 {
	raw := strings.TrimSpace(os.Getenv(EnvHealthMemWarningMB))
	if raw == "" {
		return 500
	}
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed == 0 {
		log.Printf("⚠️ invalid %s=%q (want a positive integer); using default 500: %v", EnvHealthMemWarningMB, raw, err)
		return 500
	}
	return parsed
}

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

	diskCriticalMin := healthDiskCriticalMBFromEnv() * bytesInMB
	diskWarningMin := healthDiskWarningMBFromEnv() * bytesInMB

	switch {
	case available < diskCriticalMin:
		data["severity"] = "critical"
		return CheckResult{Healthy: false, Message: fmt.Sprintf("Available disk space is below %dMB", diskCriticalMin/bytesInMB), Data: data}
	case available < diskWarningMin:
		data["severity"] = "warning"
		return CheckResult{Healthy: true, Message: fmt.Sprintf("Available disk space is below %dMB", diskWarningMin/bytesInMB), Data: data}
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

	memWarningMin := healthMemWarningMBFromEnv() * 1024

	if availableKB < memWarningMin {
		data["severity"] = "warning"
		return CheckResult{Healthy: true, Message: fmt.Sprintf("Available memory is below %dMB", memWarningMin/1024), Data: data}
	}

	data["severity"] = "info"
	return CheckResult{Healthy: true, Message: "Memory is sufficient", Data: data}
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

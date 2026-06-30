package sandbox

import (
	"context"
	"os"
	"time"
)

// SandboxProvider creates provider-specific sandbox instances.
type SandboxProvider interface {
	Name() string
	Capabilities() SandboxCapabilities
	Create(ctx context.Context, spec SandboxSpec) (Sandbox, error)
}

// Sandbox abstracts a live execution environment.
type Sandbox interface {
	ID() string
	Provider() SandboxProvider
	CopyIn(ctx context.Context, payloads []FilePayload) error
	Run(ctx context.Context, spec CommandSpec) (CommandResult, error)
	CopyOut(ctx context.Context, paths []string) ([]Artifact, error)
	SnapshotLogs(ctx context.Context) (SandboxLogs, error)
	Stop(ctx context.Context) error
}

// SandboxCapabilities describes the optional features a provider supports.
type SandboxCapabilities struct {
	SupportsMounts        bool
	SupportsCopyIn        bool
	SupportsCopyOut       bool
	SupportsInteractiveIO bool
	SupportedNetworkModes []NetworkMode
}

// NetworkMode configures connectivity for a sandbox instance.
type NetworkMode string

const (
	NetworkModeBridge NetworkMode = "bridge"
	NetworkModeHost   NetworkMode = "host"
	NetworkModeNone   NetworkMode = "none"
)

// SandboxSpec defines the initial configuration for a sandbox.
type SandboxSpec struct {
	Image          string
	Command        []string
	WorkingDir     string
	Environment    map[string]string
	NetworkMode    NetworkMode
	Mounts         []MountSpec
	Files          []FilePayload
	RunAsUser      string
	CPUQuota       string
	MemoryLimitMB  int
	ReadOnlyRootFS bool
	PidsLimit      int
	Timeout        time.Duration
}

// CommandSpec describes a command or interactive payload to execute.
type CommandSpec struct {
	Command     []string
	WorkingDir  string
	Environment map[string]string
	Stdin       []byte
	Timeout     time.Duration
}

// CommandResult captures execution outputs and timing metrics.
type CommandResult struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	TimedOut    bool
	StartedAt   time.Time
	CompletedAt time.Time
	Duration    time.Duration
}

// MountSpec maps a host path into the sandbox.
type MountSpec struct {
	Source   string
	Target   string
	ReadOnly bool
}

// FilePayload writes a file into the sandbox filesystem.
type FilePayload struct {
	Path    string
	Content []byte
	Mode    os.FileMode
}

// Artifact represents a file copied back out of the sandbox.
type Artifact struct {
	Path    string
	Content []byte
}

// SandboxLogs captures the latest stdout and stderr emitted by the sandbox.
type SandboxLogs struct {
	Stdout string
	Stderr string
}

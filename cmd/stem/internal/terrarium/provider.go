package terrarium

import (
	"context"
	"os"
	"time"
)

// TerrariumProvider creates provider-specific terrarium instances.
type TerrariumProvider interface {
	Name() string
	Capabilities() TerrariumCapabilities
	Create(ctx context.Context, spec TerrariumSpec) (Terrarium, error)
}

// Terrarium abstracts a live execution environment.
type Terrarium interface {
	ID() string
	Provider() TerrariumProvider
	CopyIn(ctx context.Context, payloads []FilePayload) error
	Run(ctx context.Context, spec CommandSpec) (CommandResult, error)
	CopyOut(ctx context.Context, paths []string) ([]Artifact, error)
	SnapshotLogs(ctx context.Context) (TerrariumLogs, error)
	Stop(ctx context.Context) error
}

// TerrariumCapabilities describes the optional features a provider supports.
type TerrariumCapabilities struct {
	SupportsMounts        bool
	SupportsCopyIn        bool
	SupportsCopyOut       bool
	SupportsInteractiveIO bool
	SupportedNetworkModes []NetworkMode
}

// NetworkMode configures connectivity for a terrarium instance.
type NetworkMode string

const (
	NetworkModeBridge NetworkMode = "bridge"
	NetworkModeHost   NetworkMode = "host"
	NetworkModeNone   NetworkMode = "none"
)

// TerrariumSpec defines the initial configuration for a terrarium.
type TerrariumSpec struct {
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

// MountSpec maps a host path into the terrarium.
type MountSpec struct {
	Source   string
	Target   string
	ReadOnly bool
}

// FilePayload writes a file into the terrarium filesystem.
type FilePayload struct {
	Path    string
	Content []byte
	Mode    os.FileMode
}

// Artifact represents a file copied back out of the terrarium.
type Artifact struct {
	Path    string
	Content []byte
}

// TerrariumLogs captures the latest stdout and stderr emitted by the terrarium.
type TerrariumLogs struct {
	Stdout string
	Stderr string
}

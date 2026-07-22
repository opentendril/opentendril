package conductor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

// Passthrough execution: one bounded command inside a network-sealed Terrarium,
// with external reach mediated by the Stem under a grant's egress allow-list.
//
// Two properties, both load-bearing:
//
//   - The Terrarium is sealed by the container runtime, not by a policy check.
//     The docker provider pins --network none, --cap-drop=ALL and
//     no-new-privileges for every container regardless of spec, so the executed
//     command has no network reach at all.
//   - The grant's egress allow-list governs only the Stem's own fetches, made on
//     the host before the sealed container runs. Every fetch URL must name a host
//     on the EgressPolicy; an empty policy denies every fetch. Payloads are
//     delivered read-only under /tmp/egress and the command executes offline.

// passthroughEgressDirectory is where Stem-mediated fetch payloads land
// inside the Terrarium. It lives under /tmp so delivery never touches the
// mounted workspace: mediated egress produces container-local inputs, not
// host files.
const passthroughEgressDirectory = "/tmp/egress"

// passthroughFetchResponseLimit caps one mediated fetch payload (32 MiB) so a
// delegated execution cannot turn the Stem into an unbounded downloader.
const passthroughFetchResponseLimit = 32 << 20

// passthroughFetchTimeout bounds one Stem-mediated fetch.
const passthroughFetchTimeout = 30 * time.Second

// passthroughHTTPClient performs Stem-mediated fetches; a package variable so
// tests can observe or replace transport behavior.
var passthroughHTTPClient = &http.Client{Timeout: passthroughFetchTimeout}

// PassthroughFetch is one Stem-mediated egress retrieval.
type PassthroughFetch struct {
	// URL is the http(s) resource to retrieve.
	URL string
	// Path is the destination relative to the Terrarium egress directory.
	Path string
}

// PassthroughExecution is a fully resolved passthrough request: a workspace
// on disk, one command, and the (possibly empty) egress allow-list from the
// authorizing delegation grant.
type PassthroughExecution struct {
	// Workspace is the resolved local workspace directory mounted at /app.
	Workspace string
	// Command is the argv vector executed inside the Terrarium.
	Command []string
	// Fetches are the Stem-mediated retrievals delivered before the command
	// runs; each is gated by Egress.
	Fetches []PassthroughFetch
	// Egress is the delegation grant's host allow-list; empty means deny-all.
	Egress []string
	// Timeout bounds the command's execution.
	Timeout time.Duration
}

// PassthroughResult reports the executed command's outcome.
type PassthroughResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
	Duration time.Duration
}

// EgressPolicy is the deny-all-by-default host allow-list a delegation grant
// opens. The zero value (and a nil/empty host list) denies every URL — the
// secure default requires no configuration.
type EgressPolicy struct {
	hosts []string
}

// NewEgressPolicy builds a policy over the grant's egress hosts. Entries are
// exact hosts ("proxy.golang.org") or host:port pairs ("127.0.0.1:8080") —
// no wildcards, matching the grant model's exact-match discipline.
func NewEgressPolicy(hosts []string) EgressPolicy {
	trimmed := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if h := strings.ToLower(strings.TrimSpace(host)); h != "" {
			trimmed = append(trimmed, h)
		}
	}
	return EgressPolicy{hosts: trimmed}
}

// Authorize returns nil when the URL's host is on the allow-list. Only http
// and https URLs are ever eligible; everything else — and every host when the
// list is empty — is denied.
func (p EgressPolicy) Authorize(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("egress denied: %q is not a valid URL: %w", rawURL, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("egress denied: scheme %q is not mediated (only http and https are)", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("egress denied: %q names no host", rawURL)
	}
	if len(p.hosts) == 0 {
		return fmt.Errorf("egress denied: host %q — the egress allow-list is empty (deny-all default)", parsed.Host)
	}

	hostname := strings.ToLower(parsed.Hostname())
	hostWithPort := strings.ToLower(parsed.Host)
	for _, allowed := range p.hosts {
		if allowed == hostname || allowed == hostWithPort {
			return nil
		}
	}
	return fmt.Errorf("egress denied: host %q is not on the delegation grant's egress allow-list", parsed.Host)
}

// fetchEgressPayloads performs the Stem-mediated retrievals for one
// passthrough execution: every URL is authorized against the policy, fetched
// on the Stem with size and time bounds, and returned as read-only file
// payloads addressed under the Terrarium egress directory. Any denial or
// failure aborts the whole execution before a container exists.
func fetchEgressPayloads(ctx context.Context, policy EgressPolicy, fetches []PassthroughFetch) ([]terrarium.FilePayload, error) {
	if len(fetches) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	payloads := make([]terrarium.FilePayload, 0, len(fetches))
	for _, fetch := range fetches {
		if err := policy.Authorize(fetch.URL); err != nil {
			return nil, err
		}
		destination, err := passthroughEgressPath(fetch.Path)
		if err != nil {
			return nil, err
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(fetch.URL), nil)
		if err != nil {
			return nil, fmt.Errorf("mediated fetch %q: %w", fetch.URL, err)
		}
		response, err := passthroughHTTPClient.Do(request)
		if err != nil {
			return nil, fmt.Errorf("mediated fetch %q: %w", fetch.URL, err)
		}
		content, err := io.ReadAll(io.LimitReader(response.Body, passthroughFetchResponseLimit+1))
		_ = response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("mediated fetch %q: read response: %w", fetch.URL, err)
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return nil, fmt.Errorf("mediated fetch %q: status %d", fetch.URL, response.StatusCode)
		}
		if len(content) > passthroughFetchResponseLimit {
			return nil, fmt.Errorf("mediated fetch %q: response exceeds the %d-byte bound", fetch.URL, passthroughFetchResponseLimit)
		}

		payloads = append(payloads, terrarium.FilePayload{
			Path:    destination,
			Content: content,
			Mode:    0o444,
		})
	}
	return payloads, nil
}

// passthroughEgressPath validates one fetch destination and anchors it under
// the Terrarium egress directory. Destinations must be relative and must not
// traverse upward — a fetch can never address the workspace mount or any
// other container path.
func passthroughEgressPath(relative string) (string, error) {
	trimmed := strings.TrimSpace(relative)
	if trimmed == "" {
		return "", fmt.Errorf("mediated fetch destination path is required")
	}
	if pathpkg.IsAbs(trimmed) {
		return "", fmt.Errorf("mediated fetch destination %q must be relative (it is anchored under %s)", relative, passthroughEgressDirectory)
	}
	clean := pathpkg.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("mediated fetch destination %q escapes the egress directory", relative)
	}
	return pathpkg.Join(passthroughEgressDirectory, clean), nil
}

// runPassthroughCommandFn is the Terrarium seam, injectable for tests that
// exercise the mediation path without a container runtime.
var runPassthroughCommandFn = runPassthroughCommand

// RunPassthrough executes one bounded command inside a sealed Terrarium.
// Mediation order is deliberate: egress is authorized and fetched first, so a
// denied execution aborts before any container (or any other side effect)
// exists.
func RunPassthrough(ctx context.Context, execution PassthroughExecution) (PassthroughResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" {
		return PassthroughResult{}, fmt.Errorf("passthrough workspace is required")
	}
	if len(execution.Command) == 0 {
		return PassthroughResult{}, fmt.Errorf("passthrough command is required")
	}
	timeout := execution.Timeout
	if timeout <= 0 {
		timeout = verifierContainerTimeout
	}

	payloads, err := fetchEgressPayloads(ctx, NewEgressPolicy(execution.Egress), execution.Fetches)
	if err != nil {
		return PassthroughResult{}, err
	}

	return runPassthroughCommandFn(ctx, execution, payloads, timeout)
}

// runPassthroughCommand owns the Terrarium lifecycle for one passthrough
// execution. It mirrors the deterministic verifier runner: the same
// toolchain-bearing image and the same sealed-container spec — differing only
// in mounting the workspace read-write (a passthrough exists to replace
// host-side formatters/test runs, so its workspace edits are the point) and
// in delivering the mediated egress payloads before the command runs.
func runPassthroughCommand(ctx context.Context, execution PassthroughExecution, payloads []terrarium.FilePayload, timeout time.Duration) (PassthroughResult, error) {
	if err := ensureSproutImageFn(ctx, verifierImage); err != nil {
		return PassthroughResult{}, fmt.Errorf("build passthrough image: %w", err)
	}

	provider, err := terrarium.NewProvider(ctx, resolveTerrariumProviderName(nil))
	if err != nil {
		return PassthroughResult{}, fmt.Errorf("resolve terrarium provider for passthrough: %w", err)
	}

	spec := terrarium.TerrariumSpec{
		Image:         verifierImage,
		WorkingDir:    "/app",
		NetworkMode:   terrarium.NetworkModeNone,
		CPUQuota:      "1.0",
		MemoryLimitMB: 2048,
		PidsLimit:     512,
		Timeout:       timeout + time.Minute,
		// Run as the host uid:gid so files the command writes into the
		// read-write workspace bind mount are owned by the Stem's user, not
		// root. A passthrough exists to replace host-side formatters/test runs,
		// so its edits land back in the operator's checkout; a root-owned tree
		// would need sudo to clean up. The container user then also matches the
		// bind mount owner.
		RunAsUser: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		Mounts: []terrarium.MountSpec{
			{Source: execution.Workspace, Target: "/app"},
		},
		Files: payloads,
	}
	if modCache, ok := hostGoModCache(); ok {
		spec.Mounts = append(spec.Mounts, terrarium.MountSpec{
			Source: modCache, Target: "/go/pkg/mod", ReadOnly: true,
		})
	}

	instance, err := provider.Create(ctx, spec)
	if err != nil {
		return PassthroughResult{}, fmt.Errorf("start passthrough terrarium: %w", err)
	}
	defer func() { _ = instance.Stop(context.Background()) }()

	result, runErr := instance.Run(ctx, terrarium.CommandSpec{
		Command:    execution.Command,
		WorkingDir: "/app",
		Environment: map[string]string{
			"GOPATH":     "/go",
			"GOMODCACHE": "/go/pkg/mod",
			"GOCACHE":    "/tmp/gocache",
			// The container is network-sealed, so module fetches can never
			// succeed anyway; GOPROXY=off makes that failure immediate and
			// explicit. -buildvcs=false matches the verifier runner: VCS
			// stamping shells out to git against a bind mount owned by a
			// different uid than the container user.
			"GOFLAGS": "-buildvcs=false",
			"GOPROXY": "off",
		},
		Timeout: timeout,
	})
	if runErr != nil {
		return PassthroughResult{}, fmt.Errorf("run passthrough command %q: %w", strings.Join(execution.Command, " "), runErr)
	}

	return PassthroughResult{
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		TimedOut: result.TimedOut,
		Duration: result.Duration,
	}, nil
}

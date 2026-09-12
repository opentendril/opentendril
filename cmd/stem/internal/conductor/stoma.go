package conductor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

// Stoma execution: one bounded command inside a network-sealed Terrarium,
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

// stomaEgressDirectory is where Stem-mediated fetch payloads land
// inside the Terrarium. It lives under /tmp so delivery never touches the
// mounted workspace: mediated egress produces container-local inputs, not
// host files.
const stomaEgressDirectory = "/tmp/egress"

// stomaFetchResponseLimit caps one mediated fetch payload (32 MiB) so a
// delegated execution cannot turn the Stem into an unbounded downloader.
const stomaFetchResponseLimit = 32 << 20

// stomaFetchTimeout bounds one Stem-mediated fetch.
const stomaFetchTimeout = 30 * time.Second

// stomaHTTPClient performs Stem-mediated fetches; a package variable so
// tests can observe or replace transport behavior. Redirect authorization is
// attached per request from the active EgressPolicy — a shared CheckRedirect
// cannot see the grant for the current fetch.
var stomaHTTPClient = &http.Client{Timeout: stomaFetchTimeout}

// consultHostGoModCache is the Stoma-only host cache lookup. Seed Go
// verification never calls it. Macrophage and Sequence verifiers keep their
// own call sites unchanged.
var consultHostGoModCache = hostGoModCache

// stomaMaxRedirects bounds Stem-mediated redirect following. Each hop is
// independently authorized; this cap only prevents a grant-internal loop.
const stomaMaxRedirects = 10

// StomaFetch is one Stem-mediated egress retrieval.
type StomaFetch struct {
	// URL is the http(s) resource to retrieve.
	URL string
	// Path is the destination relative to the Terrarium egress directory.
	Path string
}

// StomaExecution is a fully resolved stoma request: a workspace
// on disk, one command, and the (possibly empty) egress allow-list from the
// authorizing delegation grant.
type StomaExecution struct {
	// Workspace is the resolved local workspace directory mounted at /app.
	Workspace string
	// Command is the argv vector executed inside the Terrarium.
	Command []string
	// Fetches are the Stem-mediated retrievals delivered before the command
	// runs; each is gated by Egress.
	Fetches []StomaFetch
	// Egress is the delegation grant's host allow-list; empty means deny-all.
	Egress []string
	// Timeout bounds the command's execution.
	Timeout time.Duration
	// SkipHostModuleCache, when true, never bind-mounts or consults the host
	// GOMODCACHE. Seed Go verification uses this so the verdict cannot depend
	// on incidental host toolchain state.
	SkipHostModuleCache bool
	// ReadOnlyWorkspace bind-mounts the candidate at /app read-only. Seed Go
	// verification uses this so preparation and the predicate share the same
	// immutable candidate; container-local scratch (/go/pkg/mod, GOCACHE)
	// stays writable. Ordinary non-Go Stoma workspaces remain read-write.
	ReadOnlyWorkspace bool
	// GoVendorMode runs the command against the candidate vendor tree with
	// GOPROXY=off and no Stem-mediated module fetches.
	GoVendorMode bool
	// PopulateGoModuleCache runs `go mod download` against the mediated
	// file:// GOPROXY before the configured command, populating a
	// container-local module cache. The configured predicate then runs with
	// GOPROXY=off.
	PopulateGoModuleCache bool
}

// StomaResult reports the executed command's outcome.
type StomaResult struct {
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
// stoma execution: every URL is authorized against the policy, fetched
// on the Stem with size and time bounds, and returned as read-only file
// payloads addressed under the Terrarium egress directory. Any denial or
// failure aborts the whole execution before a container exists.
func fetchEgressPayloads(ctx context.Context, policy EgressPolicy, fetches []StomaFetch) ([]terrarium.FilePayload, error) {
	return fetchEgressPayloadsBounded(ctx, policy, fetches, 0)
}

// fetchEgressPayloadsBounded is fetchEgressPayloads with an optional
// aggregate byte cap. A non-positive aggregateLimit means "no aggregate cap";
// each object is still bounded by stomaFetchResponseLimit.
func fetchEgressPayloadsBounded(ctx context.Context, policy EgressPolicy, fetches []StomaFetch, aggregateLimit int) ([]terrarium.FilePayload, error) {
	if len(fetches) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	client := mediatedHTTPClient(policy)
	payloads := make([]terrarium.FilePayload, 0, len(fetches))
	total := 0
	for _, fetch := range fetches {
		if err := policy.Authorize(fetch.URL); err != nil {
			return nil, err
		}
		destination, err := stomaEgressPath(fetch.Path)
		if err != nil {
			return nil, err
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(fetch.URL), nil)
		if err != nil {
			return nil, fmt.Errorf("mediated fetch %q: %w", fetch.URL, err)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("mediated fetch %q: %w", fetch.URL, err)
		}
		content, err := io.ReadAll(io.LimitReader(response.Body, stomaFetchResponseLimit+1))
		_ = response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("mediated fetch %q: read response: %w", fetch.URL, err)
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return nil, fmt.Errorf("mediated fetch %q: status %d", fetch.URL, response.StatusCode)
		}
		if len(content) > stomaFetchResponseLimit {
			return nil, fmt.Errorf("mediated fetch %q: response exceeds the %d-byte bound", fetch.URL, stomaFetchResponseLimit)
		}
		total += len(content)
		if aggregateLimit > 0 && total > aggregateLimit {
			return nil, fmt.Errorf("mediated fetch %q: aggregate response exceeds the %d-byte bound", fetch.URL, aggregateLimit)
		}

		payloads = append(payloads, terrarium.FilePayload{
			Path:    destination,
			Content: content,
			Mode:    0o444,
		})
	}
	return payloads, nil
}

// mediatedHTTPClient copies the shared transport/timeout and authorizes every
// redirect destination independently through the same EgressPolicy. A redirect
// is not trusted because the original URL was granted.
func mediatedHTTPClient(policy EgressPolicy) *http.Client {
	return &http.Client{
		Timeout:   stomaHTTPClient.Timeout,
		Transport: stomaHTTPClient.Transport,
		Jar:       stomaHTTPClient.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= stomaMaxRedirects {
				return fmt.Errorf("mediated fetch: stopped after %d redirects", stomaMaxRedirects)
			}
			if req == nil || req.URL == nil {
				return fmt.Errorf("mediated fetch: redirect named no URL")
			}
			return policy.Authorize(req.URL.String())
		},
	}
}

// stomaEgressPath validates one fetch destination and anchors it under
// the Terrarium egress directory. Destinations must be relative and must not
// traverse upward — a fetch can never address the workspace mount or any
// other container path.
func stomaEgressPath(relative string) (string, error) {
	trimmed := strings.TrimSpace(relative)
	if trimmed == "" {
		return "", fmt.Errorf("mediated fetch destination path is required")
	}
	if pathpkg.IsAbs(trimmed) {
		return "", fmt.Errorf("mediated fetch destination %q must be relative (it is anchored under %s)", relative, stomaEgressDirectory)
	}
	clean := pathpkg.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("mediated fetch destination %q escapes the egress directory", relative)
	}
	return pathpkg.Join(stomaEgressDirectory, clean), nil
}

// runStomaCommandFn is the Terrarium seam, injectable for tests that
// exercise the mediation path without a container runtime.
var runStomaCommandFn = runStomaCommand

// RunStoma executes one bounded command inside a sealed Terrarium.
// Mediation order is deliberate: egress is authorized and fetched first, so a
// denied execution aborts before any container (or any other side effect)
// exists.
func RunStoma(ctx context.Context, execution StomaExecution) (StomaResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" {
		return StomaResult{}, fmt.Errorf("stoma workspace is required")
	}
	if len(execution.Command) == 0 {
		return StomaResult{}, fmt.Errorf("stoma command is required")
	}
	timeout := execution.Timeout
	if timeout <= 0 {
		timeout = verifierContainerTimeout
	}

	aggregateLimit := 0
	if execution.PopulateGoModuleCache {
		aggregateLimit = seedGoModuleAggregateLimit
	}
	payloads, err := fetchEgressPayloadsBounded(ctx, NewEgressPolicy(execution.Egress), execution.Fetches, aggregateLimit)
	if err != nil {
		return StomaResult{}, err
	}

	return runStomaCommandFn(ctx, execution, payloads, timeout)
}

// runStomaCommand owns the Terrarium lifecycle for one stoma
// execution. It mirrors the deterministic verifier runner: the same
// toolchain-bearing image and the same sealed-container spec — differing only
// in mounting the workspace read-write (a stoma exists to replace
// host-side formatters/test runs, so its workspace edits are the point) and
// in delivering the mediated egress payloads before the command runs.
func runStomaCommand(ctx context.Context, execution StomaExecution, payloads []terrarium.FilePayload, timeout time.Duration) (StomaResult, error) {
	if err := ensureSproutImageFn(ctx, verifierImage); err != nil {
		return StomaResult{}, fmt.Errorf("build stoma image: %w", err)
	}

	provider, err := terrariumNewProviderFn(ctx, resolveTerrariumProviderName(ctx, nil))
	if err != nil {
		return StomaResult{}, fmt.Errorf("resolve terrarium provider for stoma: %w", err)
	}

	spec := terrarium.TerrariumSpec{
		Image:         verifierImage,
		WorkingDir:    "/app",
		NetworkMode:   terrarium.NetworkModeNone,
		CPUQuota:      "1.0",
		MemoryLimitMB: 2048,
		PidsLimit:     512,
		Timeout:       timeout + time.Minute,
		// Same bind-mount identity as normal Sprout execution: rootless Docker
		// uses 0:0 so the mount is readable; otherwise the Stem host UID:GID
		// so workspace writes remain owned by the operator.
		RunAsUser: terrariumBindMountRunAsUser(),
		Mounts:    stomaBindMounts(execution),
		Files:     payloads,
	}

	instance, err := provider.Create(ctx, spec)
	if err != nil {
		return StomaResult{}, fmt.Errorf("start stoma terrarium: %w", err)
	}
	defer func() { _ = instance.Stop(context.Background()) }()

	if execution.PopulateGoModuleCache {
		if err := populateStomaGoModuleCache(ctx, instance, timeout); err != nil {
			return StomaResult{}, err
		}
	}

	result, runErr := instance.Run(ctx, terrarium.CommandSpec{
		Command:     execution.Command,
		WorkingDir:  "/app",
		Environment: stomaCommandEnvironment(execution, false),
		Timeout:     timeout,
	})
	if runErr != nil {
		return StomaResult{}, fmt.Errorf("run stoma command %q: %w", strings.Join(execution.Command, " "), runErr)
	}

	return StomaResult{
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		TimedOut: result.TimedOut,
		Duration: result.Duration,
	}, nil
}

func stomaBindMounts(execution StomaExecution) []terrarium.MountSpec {
	mounts := []terrarium.MountSpec{
		{Source: execution.Workspace, Target: "/app", ReadOnly: execution.ReadOnlyWorkspace},
	}
	if execution.SkipHostModuleCache {
		return mounts
	}
	if modCache, ok := consultHostGoModCache(); ok {
		mounts = append(mounts, terrarium.MountSpec{
			Source: modCache, Target: "/go/pkg/mod", ReadOnly: true,
		})
	}
	return mounts
}

func stomaCommandEnvironment(execution StomaExecution, populateCache bool) map[string]string {
	flags := "-buildvcs=false"
	switch {
	case execution.GoVendorMode:
		flags += " -mod=vendor"
	case execution.SkipHostModuleCache:
		flags += " -mod=readonly"
	}
	env := map[string]string{
		"GOPATH":     "/go",
		"GOMODCACHE": "/go/pkg/mod",
		"GOCACHE":    "/tmp/gocache",
		"GOFLAGS":    flags,
		"GOPROXY":    "off",
	}
	if populateCache {
		env["GOPROXY"] = seedGoProxyFileURL()
		env["GOSUMDB"] = "off"
		env["GOTOOLCHAIN"] = "local"
		env["GO111MODULE"] = "on"
		return env
	}
	if execution.SkipHostModuleCache || execution.GoVendorMode {
		env["GOTOOLCHAIN"] = "local"
		env["GOSUMDB"] = "off"
	}
	return env
}

func populateStomaGoModuleCache(ctx context.Context, instance terrarium.Terrarium, timeout time.Duration) error {
	result, err := instance.Run(ctx, terrarium.CommandSpec{
		Command:     []string{"go", "mod", "download"},
		WorkingDir:  "/app",
		Environment: stomaCommandEnvironment(StomaExecution{SkipHostModuleCache: true}, true),
		Timeout:     timeout,
	})
	if err != nil {
		return fmt.Errorf("populate Seed Go module cache: %w", err)
	}
	if result.TimedOut {
		return fmt.Errorf("populate Seed Go module cache: command timed out")
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("populate Seed Go module cache: command exited %d", result.ExitCode)
	}
	return nil
}

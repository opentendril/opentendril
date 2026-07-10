package conductor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/opentendril/core/cmd/stem/internal/terrarium"
)

// macrophageFuzzImage is the dedicated, Go-toolchain-enabled terrarium image
// the Macrophage genotype's deterministic fuzz check runs in — never the
// stripped opentendril-go:latest image every other Go sprout uses (issue
// #154). See sprouts/go-fuzz/Dockerfile for why this can't be the same
// image.
const macrophageFuzzImage = "opentendril-go-fuzz:latest"

// macrophageFuzzTime is how long `go test -fuzz` is allowed to search for a
// crashing input per invocation, mirroring the issue's own example
// (`-fuzztime=10s`).
const macrophageFuzzTime = 10 * time.Second

// macrophageContainerTimeout bounds the whole exec — comfortably longer than
// macrophageFuzzTime to leave room for `go build`/module resolution before
// fuzzing starts (the build cache is container-local, so every run compiles
// cold), short enough that a genuinely hung fuzz target still fails the step
// rather than hanging the sequence indefinitely.
const macrophageContainerTimeout = 5 * time.Minute

var runMacrophageFuzzCheckFn = runMacrophageFuzzCheck

// macrophageFuzzError is returned when the Macrophage's fuzz check finds a
// crash, a compile error, or can't complete one — a hard, structural Go
// error, not an LLM-interpreted judgment call, so it reliably drives the
// same recursive-debugger retry loop a Verifier failure does today
// (shouldBudRecursiveDebugger). It implements commandResultCarrier so the
// sequence runner's existing OOM/timeout telemetry (publishStepFailure)
// picks it up for free, exactly like any other terrarium command failure.
type macrophageFuzzError struct {
	summary string
	result  terrarium.CommandResult
}

func (e *macrophageFuzzError) Error() string {
	return "macrophage fuzz verification failed: " + e.summary
}

func (e *macrophageFuzzError) CommandResult() terrarium.CommandResult {
	return e.result
}

// runMacrophageFuzzCheck is the deterministic half of the Macrophage role:
// after the agent turn (running the macrophage genotype) writes a Go native
// fuzz test into the mounted worktree, this executes `go test -fuzz` inside
// a dedicated, network-isolated terrarium and converts a crash, compile
// error, or infrastructure timeout into a hard error — no LLM judgment call
// in the loop. mountPath is the same host worktree path the agent's own
// terrarium session already mounted, so this sees exactly the files the
// agent wrote.
func runMacrophageFuzzCheck(ctx context.Context, providerName, mountPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ensureSproutImage(ctx, macrophageFuzzImage); err != nil {
		return fmt.Errorf("build macrophage fuzz image: %w", err)
	}

	provider, err := terrarium.NewProvider(ctx, providerName)
	if err != nil {
		return fmt.Errorf("resolve terrarium provider for macrophage fuzz check: %w", err)
	}

	spec := terrarium.TerrariumSpec{
		Image:         macrophageFuzzImage,
		WorkingDir:    "/app",
		NetworkMode:   terrarium.NetworkModeNone,
		CPUQuota:      "1.0",
		MemoryLimitMB: 2048,
		PidsLimit:     512,
		Timeout:       macrophageContainerTimeout,
		Mounts: []terrarium.MountSpec{
			{Source: mountPath, Target: "/app"},
		},
		// Deliberately no RunAsUser: this is a build-tool container (like a
		// CI runner), not a code-execution one — running as root here avoids
		// UID-mismatch permission failures reading the host's read-only
		// GOMODCACHE bind mount below. Containment still comes from
		// --network none, --cap-drop=ALL, --security-opt=no-new-privileges,
		// and the CPU/memory/pids caps above (createDockerTerrarium applies
		// these unconditionally, regardless of RunAsUser).
	}
	if modCache, ok := hostGoModCache(); ok {
		spec.Mounts = append(spec.Mounts, terrarium.MountSpec{
			Source: modCache, Target: "/go/pkg/mod", ReadOnly: true,
		})
	}

	instance, err := provider.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("start macrophage fuzz terrarium: %w", err)
	}
	defer func() { _ = instance.Stop(context.Background()) }()

	target := "./..."
	if pkg, ok := findFuzzPackage(mountPath); ok {
		target = pkg
	}

	result, runErr := instance.Run(ctx, terrarium.CommandSpec{
		Command:    []string{"go", "test", "-run=^$", "-fuzz=Fuzz", "-fuzztime=" + macrophageFuzzTime.String(), target},
		WorkingDir: "/app",
		Environment: map[string]string{
			"GOPATH":     "/go",
			"GOMODCACHE": "/go/pkg/mod",
			// Container-local and therefore cold on every run (no host
			// mount): simple and permission-safe, at the cost of a from-
			// scratch build each time. Worth revisiting as a follow-up if
			// Macrophage's build time becomes a bottleneck in practice.
			"GOCACHE": "/tmp/gocache",
			"GOFLAGS": "-mod=mod",
		},
		Timeout: macrophageContainerTimeout,
	})
	if runErr != nil {
		return fmt.Errorf("run macrophage fuzz check: %w", runErr)
	}

	if reason, failed := macrophageFuzzFailed(result); failed {
		return &macrophageFuzzError{summary: reason, result: result}
	}

	return nil
}

// fuzzFuncPattern matches a native Go fuzz target declaration
// (`func FuzzXxx(f *testing.F)`), per the genotype's own instructions.
var fuzzFuncPattern = regexp.MustCompile(`(?m)^func\s+Fuzz\w*\(\s*\w+\s+\*testing\.F\s*\)`)

// findFuzzPackage locates the package directory (relative to mountPath, as a
// `go test` target like "./internal/foo") containing the fuzz test the
// Macrophage genotype just wrote. Scoping the run to that one package
// instead of "./..." avoids "-fuzz matches more than one fuzz test" against
// any pre-existing fuzz tests elsewhere in the repo, and keeps the (always-
// cold) build small. Returns ok=false if no _test.go file with a FuzzXxx
// declaration is found — the caller then falls back to "./...", which will
// itself fail clearly ("no fuzz tests found") rather than hang.
func findFuzzPackage(mountPath string) (target string, ok bool) {
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true, ".tendril": true,
	}

	_ = filepath.WalkDir(mountPath, func(path string, entry os.DirEntry, walkErr error) error {
		if ok || walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if skipDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil || !fuzzFuncPattern.Match(content) {
			return nil
		}
		rel, relErr := filepath.Rel(mountPath, filepath.Dir(path))
		if relErr != nil {
			return nil
		}
		if rel == "." {
			target = "."
		} else {
			target = "./" + filepath.ToSlash(rel)
		}
		ok = true
		return nil
	})
	return target, ok
}

// macrophageFuzzFailed inspects a completed `go test -fuzz` run and decides,
// structurally rather than by LLM interpretation, whether it found a crash.
// A separate function so the decision logic is unit-testable against
// fixture CommandResults with no Docker involved.
func macrophageFuzzFailed(result terrarium.CommandResult) (reason string, failed bool) {
	if result.TimedOut {
		return "fuzz run exceeded its terrarium timeout (possible infinite loop or hang in the fuzz target)", true
	}
	if result.ExitCode != 0 {
		combined := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
		switch {
		case strings.Contains(combined, "panic:"):
			return "fuzzer triggered a panic:\n" + truncateForSummary(combined), true
		case strings.Contains(combined, "--- FAIL"):
			return "fuzzer found a failing input:\n" + truncateForSummary(combined), true
		default:
			return fmt.Sprintf("go test exited %d:\n%s", result.ExitCode, truncateForSummary(combined)), true
		}
	}
	return "", false
}

const macrophageSummaryMaxLen = 4000

func truncateForSummary(output string) string {
	if len(output) <= macrophageSummaryMaxLen {
		return output
	}
	return output[:macrophageSummaryMaxLen] + "\n... (truncated)"
}

// hostGoModCache resolves the host's module cache (`go env GOMODCACHE`) so it
// can be bind-mounted read-only into the fuzz terrarium. This repo doesn't
// vendor its Go dependencies and the terrarium has no network
// (terrarium.NetworkModeNone, same isolation every other sprout gets), so
// without this, `go test` would fail to resolve modules. Returns ok=false
// (not an error) when the host has no Go toolchain or cache yet — the fuzz
// run then fails fast with a clear module-resolution error instead of a
// confusing silent hang.
func hostGoModCache() (string, bool) {
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		return "", false
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", false
	}
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		return "", false
	}
	return dir, true
}

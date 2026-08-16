package conductor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/roots/llm"
)

// TestMain isolates the whole package's tests from the operator's real home
// directory.
//
// This exists because the owned-reference registry lives under ~/.tendril, and
// the first run of the test suite after it was introduced wrote entries into a
// real person's home — recording branches in temporary directories that no
// longer existed. The obvious fix is to remember to set HOME in the tests that
// touch it. That is a guard: it works until someone writes the next test.
//
// Setting it once, here, makes the leak structurally impossible instead. Any
// state a test causes to be written under the home directory lands in a
// temporary directory that the operating system reclaims, whether or not the
// test's author knew such state existed.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "opentendril-test-home-")
	if err != nil {
		panic("create isolated test home: " + err.Error())
	}

	// Some tests invoke the real go toolchain. Its build and module caches
	// default to paths under the home directory, so moving HOME without
	// pinning them first would empty those caches and change what those tests
	// actually exercise. Resolve them while the real home is still in effect
	// and set them explicitly, so isolation costs nothing but the isolation.
	pinGoEnvironment()

	// Both variables matter: os.UserHomeDir reads HOME on Unix and USERPROFILE
	// on Windows, and a test helper may consult either.
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)

	// And from the operator's real provider credentials, for the same reason.
	// A run's post-mortem asks a model to transcribe what the run learned, and
	// resolution reads the ambient environment — so on a developer machine with
	// keys exported, the suite reached a live provider, on the developer's
	// account, for tests that assert nothing about it. The cost is real and so
	// is the runtime: measured here, one such test sat on a hosted request
	// until the suite's own timeout killed it. A test that wants a particular
	// provider declares one with t.Setenv, which still works over these.
	clearProviderEnvironment()

	// Stub CheckGVisorReadinessFn to prevent tests that don't specify a Substrate
	// from accidentally spawning a real `docker info` subprocess just to resolve
	// the default terrarium provider name. This satisfies the implicit assumption
	// of tests written before the gVisor preference was added (that the default
	// resolves purely via strings to "docker"). Tests that actually want to
	// exercise the gVisor logic save and restore this seam themselves.
	originalGVisorReadyFn := CheckGVisorReadinessFn
	CheckGVisorReadinessFn = func(context.Context) error { return errors.New("stubbed unready") }
	// Existing RunSprout tests exercise emerge / Terrarium seams, not a
	// live provider. The real probe is restored by tests that assert it.
	originalProbe := probeProviderAuthFn
	probeProviderAuthFn = func(context.Context, *llm.Client) error { return nil }
	exitCode := m.Run()

	// Restore so the process leaves global state as it found it.
	probeProviderAuthFn = originalProbe
	CheckGVisorReadinessFn = originalGVisorReadyFn
	os.RemoveAll(home)
	os.Exit(exitCode)
}

// clearProviderEnvironment removes every signal that would let model
// resolution reach a real provider: the credentials, the provider and model
// choices, and the local inference endpoint. It names them explicitly rather
// than pattern-matching the environment, so a variable added later is a
// deliberate addition here and not an accident of a regular expression.
func clearProviderEnvironment() {
	for _, key := range []string{
		"DEFAULT_LLM_PROVIDER", "COORDINATOR_LLM_PROVIDER",
		"DEFAULT_MODEL_NAME", "COORDINATOR_MODEL_NAME",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GOOGLE_API_KEY",
		"GROK_API_KEY", "OPENROUTER_API_KEY", "NVIDIA_API_KEY",
		"LOCAL_INFERENCE_URL", "LOCAL_MODEL_NAME", "COORDINATOR_LOCAL_INFERENCE_URL",
	} {
		os.Unsetenv(key)
	}
	for _, provider := range []string{"ANTHROPIC", "OPENAI", "GOOGLE", "GROK", "OPENROUTER", "NVIDIA", "LOCAL"} {
		for _, suffix := range []string{"_MODEL_NAME", "_PREMIUM_MODEL", "_STANDARD_MODEL", "_CHEAPEST_MODEL", "_BASE_URL"} {
			os.Unsetenv(provider + suffix)
		}
	}

	// One inference endpoint is then declared, deliberately pointing nowhere.
	// A sprout run refuses to start when nothing resolves, so the suite has to
	// name a provider for the runs it exercises — and naming a dead local port
	// gives it a deterministic answer that reaches no network: a call nothing
	// stubbed fails at connect in microseconds instead of arriving somewhere
	// real.
	os.Setenv("LOCAL_INFERENCE_URL", "http://127.0.0.1:1/v1")
	os.Setenv("LOCAL_MODEL_NAME", "test-only-model")
}

// pinGoEnvironment resolves the go toolchain's home-relative locations and
// sets them explicitly, so they survive the home directory being moved. A
// missing toolchain is not an error here: the tests that need one skip
// themselves.
func pinGoEnvironment() {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		return
	}
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			continue
		}
		out, err := exec.Command(goBinary, "env", name).Output()
		if err != nil {
			continue
		}
		if value := strings.TrimSpace(string(out)); value != "" {
			os.Setenv(name, value)
		}
	}
}

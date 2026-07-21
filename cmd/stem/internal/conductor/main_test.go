package conductor

import (
	"os"
	"os/exec"
	"strings"
	"testing"
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

	code := m.Run()

	os.RemoveAll(home)
	os.Exit(code)
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

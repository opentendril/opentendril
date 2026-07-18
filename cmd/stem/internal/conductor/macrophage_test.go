package conductor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/terrarium"
)

func TestMacrophageFuzzFailedPassesOnCleanExit(t *testing.T) {
	reason, failed := macrophageFuzzFailed(terrarium.CommandResult{
		ExitCode: 0,
		Stdout:   "PASS\nok  \texample.com/pkg\t10.234s\n",
	})
	if failed {
		t.Fatalf("expected a clean exit to pass, got failure reason %q", reason)
	}
}

func TestMacrophageFuzzFailedDetectsPanic(t *testing.T) {
	reason, failed := macrophageFuzzFailed(terrarium.CommandResult{
		ExitCode: 2,
		Stdout: "--- FAIL: FuzzParseHeader (0.12s)\n" +
			"    --- FAIL: FuzzParseHeader/3a8f9c (0.00s)\n" +
			"panic: runtime error: index out of range [3] with length 2\n" +
			"\ngoroutine 7 [running]:\nexample.com/pkg.ParseHeader(...)\n",
	})
	if !failed {
		t.Fatal("expected a panicking fuzz run to fail")
	}
	if !strings.Contains(reason, "panic:") {
		t.Fatalf("reason = %q, want it to surface the panic", reason)
	}
	if !strings.Contains(reason, "index out of range") {
		t.Fatalf("reason = %q, want it to include the crash detail", reason)
	}
}

func TestMacrophageFuzzFailedDetectsPlainTestFailure(t *testing.T) {
	reason, failed := macrophageFuzzFailed(terrarium.CommandResult{
		ExitCode: 1,
		Stdout:   "--- FAIL: FuzzRoundtrip (0.05s)\n    roundtrip mismatch: got \"\\x00\", want \"a\"\nFAIL\n",
	})
	if !failed {
		t.Fatal("expected a failing fuzz input to fail")
	}
	if !strings.Contains(reason, "--- FAIL") {
		t.Fatalf("reason = %q, want it to include the failing-test marker", reason)
	}
}

func TestMacrophageFuzzFailedDetectsCompileError(t *testing.T) {
	reason, failed := macrophageFuzzFailed(terrarium.CommandResult{
		ExitCode: 2,
		Stderr:   "# example.com/pkg\n./fuzz_test.go:12:2: undefined: Frobnicate\nFAIL\texample.com/pkg [build failed]\n",
	})
	if !failed {
		t.Fatal("expected a build failure to fail")
	}
	if !strings.Contains(reason, "go test exited 2") {
		t.Fatalf("reason = %q, want the exit code surfaced for an otherwise-unrecognized failure", reason)
	}
}

func TestMacrophageFuzzFailedDetectsTimeout(t *testing.T) {
	reason, failed := macrophageFuzzFailed(terrarium.CommandResult{
		TimedOut: true,
		ExitCode: -1,
	})
	if !failed {
		t.Fatal("expected a terrarium timeout to fail")
	}
	if !strings.Contains(reason, "timeout") {
		t.Fatalf("reason = %q, want it to mention the timeout", reason)
	}
}

func TestMacrophageFuzzFailedTruncatesLongOutput(t *testing.T) {
	huge := strings.Repeat("x", macrophageSummaryMaxLen+500)
	reason, failed := macrophageFuzzFailed(terrarium.CommandResult{
		ExitCode: 1,
		Stdout:   "--- FAIL: FuzzBig\n" + huge,
	})
	if !failed {
		t.Fatal("expected failure")
	}
	if len(reason) > macrophageSummaryMaxLen+200 {
		t.Fatalf("summary length = %d, want it truncated near %d", len(reason), macrophageSummaryMaxLen)
	}
	if !strings.Contains(reason, "truncated") {
		t.Fatalf("reason = %q, want a truncation marker", reason)
	}
}

func TestFindFuzzPackageLocatesTheFuzzTest(t *testing.T) {
	root := t.TempDir()

	// A decoy _test.go with no fuzz target should be ignored.
	writeMacrophageFixture(t, filepath.Join(root, "unrelated", "unrelated_test.go"), `package unrelated

import "testing"

func TestSomething(t *testing.T) {}
`)

	pkgDir := filepath.Join(root, "internal", "parser")
	writeMacrophageFixture(t, filepath.Join(pkgDir, "parser_fuzz_test.go"), `package parser

import "testing"

func FuzzParse(f *testing.F) {
	f.Add([]byte("seed"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = Parse(data)
	})
}
`)

	target, ok := findFuzzPackage(root)
	if !ok {
		t.Fatal("expected findFuzzPackage to locate the fuzz test")
	}
	want := "./internal/parser"
	if target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
}

func TestFindFuzzPackageReturnsFalseWithNoFuzzTest(t *testing.T) {
	root := t.TempDir()
	writeMacrophageFixture(t, filepath.Join(root, "main_test.go"), `package main

import "testing"

func TestMain(t *testing.T) {}
`)

	if _, ok := findFuzzPackage(root); ok {
		t.Fatal("expected findFuzzPackage to find nothing when no FuzzXxx function exists")
	}
}

func writeMacrophageFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

package main

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// TestDotenvWarningStaysSilentWhenAbsent pins the ordinary unconfigured case.
// Most installations have no .env, and a warning on every start would train
// operators to ignore the channel that carries the real one.
//
// Mutation target: drop the fs.ErrNotExist arm so every error warns → this
// test fails.
func TestDotenvWarningStaysSilentWhenAbsent(t *testing.T) {
	missing := &fs.PathError{Op: "open", Path: ".env", Err: fs.ErrNotExist}
	if got := dotenvWarning(missing, "/home/tendril"); got != "" {
		t.Fatalf("dotenvWarning(not-exist) = %q, want no warning", got)
	}
	if got := dotenvWarning(nil, "/home/tendril"); got != "" {
		t.Fatalf("dotenvWarning(nil) = %q, want no warning", got)
	}
}

// TestDotenvWarningReportsUnreadableFile is the defect this change exists for:
// a .env that is present and cannot be read left every value in it silently
// absent, so a pinned model was ignored and four measured runs were served by
// a model nobody selected.
//
// Mutation target: restore `_ = godotenv.Load()` semantics by returning "" for
// a non-not-exist error → this test fails.
func TestDotenvWarningReportsUnreadableFile(t *testing.T) {
	denied := &fs.PathError{Op: "open", Path: ".env", Err: fs.ErrPermission}

	got := dotenvWarning(denied, "/home/tendril")
	if got == "" {
		t.Fatal("dotenvWarning(permission-denied) returned no warning; an unreadable .env must not be silent")
	}
	// The directory matters: godotenv resolves .env against the working
	// directory, which for a service is set by the unit and is rarely where
	// the reader of the message is standing.
	if !strings.Contains(got, "/home/tendril/.env") {
		t.Errorf("warning does not name the resolved path; got %q", got)
	}
	// The underlying cause must survive into the message, or the operator
	// learns only that something is wrong.
	if !strings.Contains(got, "permission denied") {
		t.Errorf("warning does not carry the underlying error; got %q", got)
	}
	// Saying the file failed to load is not the same as saying the settings
	// are inactive, which is the consequence an operator acts on.
	if !strings.Contains(got, "in effect") {
		t.Errorf("warning does not state that the settings are not in effect; got %q", got)
	}
}

// TestDotenvWarningSurvivesUnknownWorkingDirectory pins that losing the
// directory degrades the message rather than suppressing it. The path is
// context; the warning is the point.
//
// Mutation target: return "" when dir is empty → this test fails.
func TestDotenvWarningSurvivesUnknownWorkingDirectory(t *testing.T) {
	denied := &fs.PathError{Op: "open", Path: ".env", Err: fs.ErrPermission}

	got := dotenvWarning(denied, "")
	if got == "" {
		t.Fatal("dotenvWarning with an unknown directory returned no warning")
	}
	if !strings.Contains(got, ".env") {
		t.Errorf("warning does not mention .env; got %q", got)
	}
}

// TestDotenvWarningTreatsWrappedNotExistAsAbsent guards the classification
// against error wrapping: godotenv returns a *fs.PathError rather than the
// sentinel, and a == comparison would warn on every installation that simply
// has no .env.
//
// Mutation target: replace errors.Is with `err == fs.ErrNotExist` → this test
// fails.
func TestDotenvWarningTreatsWrappedNotExistAsAbsent(t *testing.T) {
	wrapped := os.NewSyscallError("open", errors.Join(fs.ErrNotExist))
	if got := dotenvWarning(wrapped, "/tmp"); got != "" {
		t.Fatalf("dotenvWarning(wrapped not-exist) = %q, want no warning", got)
	}
}

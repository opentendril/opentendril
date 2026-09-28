package gitpatch

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCheckAndApplyUseFixedGitArgumentsAndHideOutput(t *testing.T) {
	original := runCommand
	t.Cleanup(func() { runCommand = original })
	var calls [][]string
	failure := errors.New("raw patch and absolute path output")
	runCommand = func(_ context.Context, workspace string, patch []byte, args ...string) error {
		if workspace != "/trusted/workspace" || string(patch) != "patch-bytes" {
			t.Fatalf("runner received workspace=%q patch=%q", workspace, patch)
		}
		calls = append(calls, append([]string(nil), args...))
		return failure
	}

	if err := Check(context.Background(), "/trusted/workspace", []byte("patch-bytes")); err != ErrPreflightFailed {
		t.Fatalf("Check error = %v, want stable sentinel", err)
	}
	if err := Apply(context.Background(), "/trusted/workspace", []byte("patch-bytes")); err != ErrApplyFailed {
		t.Fatalf("Apply error = %v, want stable sentinel", err)
	}
	want := [][]string{
		{"apply", "--check", "--binary", "--whitespace=nowarn", "-"},
		{"apply", "--binary", "--whitespace=nowarn", "-"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("Git argument calls = %v, want %v", calls, want)
	}
	for _, err := range []error{ErrPreflightFailed, ErrApplyFailed} {
		if err == failure || err.Error() == failure.Error() {
			t.Fatalf("raw Git output leaked as error: %v", err)
		}
	}
}

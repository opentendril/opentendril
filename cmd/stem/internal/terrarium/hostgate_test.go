package terrarium

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestHostProviderDefaultDeny(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "")

	_, err := NewProvider(context.Background(), ProviderHost)
	if err == nil {
		t.Fatal("expected error when TENDRIL_ALLOW_HOST_EXECUTION is unset, got nil")
	}
}

func TestHostProviderDeniedWhenFalse(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "false")

	_, err := NewProvider(context.Background(), ProviderHost)
	if err == nil {
		t.Fatal("expected error when TENDRIL_ALLOW_HOST_EXECUTION=false, got nil")
	}
}

func TestHostProviderAllowedWhenTrue(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "true")

	provider, err := NewProvider(context.Background(), ProviderHost)
	if err != nil {
		t.Fatalf("expected no error when TENDRIL_ALLOW_HOST_EXECUTION=true, got: %v", err)
	}
	if provider == nil {
		t.Fatal("expected a valid provider, got nil")
	}
	if provider.Name() != ProviderHost {
		t.Fatalf("expected provider name %q, got %q", ProviderHost, provider.Name())
	}
}

func TestHostProviderAllowedCaseInsensitive(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "TRUE")

	_, err := NewProvider(context.Background(), ProviderHost)
	if err != nil {
		t.Fatalf("expected case-insensitive match on TRUE, got: %v", err)
	}
}

func TestHostProviderActivationWarningOnStderr(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "true")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	_, callErr := NewProvider(context.Background(), ProviderHost)
	w.Close()

	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	r.Close()

	if callErr != nil {
		t.Fatalf("expected no error, got: %v", callErr)
	}

	got := buf.String()
	if !strings.Contains(got, EnvAllowHostExecution) {
		t.Errorf("stderr warning missing env var name %q; got: %q", EnvAllowHostExecution, got)
	}
	for _, phrase := range []string{"mount sealing", "network sealing", "host-user permissions"} {
		if !strings.Contains(got, phrase) {
			t.Errorf("stderr warning missing %q; got: %q", phrase, got)
		}
	}
}

func TestHostProviderDenyPathSilentOnStderr(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	origStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	_, callErr := NewProvider(context.Background(), ProviderHost)
	w.Close()

	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	r.Close()

	if callErr == nil {
		t.Fatal("expected error on deny path, got nil")
	}
	if got := buf.String(); got != "" {
		t.Errorf("expected no stderr output on deny path, got: %q", got)
	}
}

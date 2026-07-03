package terrarium

import (
	"context"
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

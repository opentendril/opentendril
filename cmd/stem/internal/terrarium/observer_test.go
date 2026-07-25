package terrarium

import (
	"context"
	"testing"
)

// TestObserverFiredOnHostActivation verifies that an ActivationObserver passed
// to NewProvider is called exactly once when the host provider activates
// successfully. This is the correctness assertion for the audit-event contract:
// one activation → one callback, no more, no less.
func TestObserverFiredOnHostActivation(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "true")

	var calls []string
	obs := ActivationObserver(func(name string) {
		calls = append(calls, name)
	})

	_, err := NewProvider(context.Background(), ProviderHost, obs)
	if err != nil {
		t.Fatalf("expected no error when %s=true, got: %v", EnvAllowHostExecution, err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected observer called exactly once, got %d calls", len(calls))
	}
	if calls[0] != ProviderHost {
		t.Errorf("expected observer called with %q, got %q", ProviderHost, calls[0])
	}
}

// TestObserverNotFiredOnDeny verifies that the observer is never invoked when
// the host provider is denied (env var unset). A denied activation must not
// emit an audit event — only a successful one warrants a record.
func TestObserverNotFiredOnDeny(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "")

	var calls int
	obs := ActivationObserver(func(_ string) {
		calls++
	})

	_, err := NewProvider(context.Background(), ProviderHost, obs)
	if err == nil {
		t.Fatal("expected error when env var is unset, got nil")
	}
	if calls != 0 {
		t.Errorf("expected observer not called on deny path, got %d calls", calls)
	}
}

// TestObserverNotFiredForNonHostProvider verifies that the observer is never
// invoked when a non-host provider (Docker) is requested. Only host-provider
// activation is a security-relevant event today.
func TestObserverNotFiredForNonHostProvider(t *testing.T) {
	var calls int
	obs := ActivationObserver(func(_ string) {
		calls++
	})

	_, err := NewProvider(context.Background(), ProviderDocker, obs)
	if err != nil {
		t.Fatalf("expected no error for docker provider, got: %v", err)
	}
	if calls != 0 {
		t.Errorf("expected observer not called for non-host provider, got %d calls", calls)
	}
}

// TestMultipleObserversAllFired verifies that when several observers are passed
// they are all called exactly once on a successful host activation.
func TestMultipleObserversAllFired(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "true")

	fired := make([]bool, 3)
	observers := []ActivationObserver{
		func(_ string) { fired[0] = true },
		func(_ string) { fired[1] = true },
		func(_ string) { fired[2] = true },
	}

	_, err := NewProvider(context.Background(), ProviderHost, observers...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, f := range fired {
		if !f {
			t.Errorf("observer %d was not called", i)
		}
	}
}

// TestNilObserverSafe verifies that a nil observer element does not panic —
// callers that conditionally build the observer slice may leave nils.
func TestNilObserverSafe(t *testing.T) {
	t.Setenv(EnvAllowHostExecution, "true")

	_, err := NewProvider(context.Background(), ProviderHost, nil)
	if err != nil {
		t.Fatalf("nil observer must not cause an error: %v", err)
	}
}

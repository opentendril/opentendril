package mcpclient

import (
	"testing"
)

func TestResolveStemAddress(t *testing.T) {
	t.Run("defaults to loopback 8080", func(t *testing.T) {
		t.Setenv("TERROIR_HOST", "")
		t.Setenv("PORT", "")
		if got := ResolveStemAddress(""); got != "127.0.0.1:8080" {
			t.Fatalf("got %q, want 127.0.0.1:8080", got)
		}
	})

	t.Run("uses fallback host when TERROIR_HOST is unset", func(t *testing.T) {
		t.Setenv("TERROIR_HOST", "")
		t.Setenv("PORT", "9090")
		if got := ResolveStemAddress("10.0.0.2"); got != "10.0.0.2:9090" {
			t.Fatalf("got %q, want 10.0.0.2:9090", got)
		}
	})

	t.Run("TERROIR_HOST wins over fallback", func(t *testing.T) {
		t.Setenv("TERROIR_HOST", "192.0.2.10")
		t.Setenv("PORT", "8181")
		if got := ResolveStemAddress("10.0.0.2"); got != "192.0.2.10:8181" {
			t.Fatalf("got %q, want 192.0.2.10:8181", got)
		}
	})

	t.Run("strips a port already present on TERROIR_HOST", func(t *testing.T) {
		t.Setenv("TERROIR_HOST", "192.0.2.10:7777")
		t.Setenv("PORT", "8181")
		if got := ResolveStemAddress(""); got != "192.0.2.10:8181" {
			t.Fatalf("got %q, want 192.0.2.10:8181", got)
		}
	})
}

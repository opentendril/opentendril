package healthmon

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

func TestHealthDiskCriticalMBFromEnv(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(EnvHealthDiskCriticalMB, "")
		if got := healthDiskCriticalMBFromEnv(); got != 100 {
			t.Fatalf("got %v, want default 100", got)
		}
	})
	t.Run("valid duration respected", func(t *testing.T) {
		t.Setenv(EnvHealthDiskCriticalMB, "200")
		if got := healthDiskCriticalMBFromEnv(); got != 200 {
			t.Fatalf("got %v, want 200", got)
		}
	})
	t.Run("invalid falls back with warning", func(t *testing.T) {
		t.Setenv(EnvHealthDiskCriticalMB, "not-a-number")
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(prev) })

		if got := healthDiskCriticalMBFromEnv(); got != 100 {
			t.Fatalf("got %v, want default 100", got)
		}
		if !strings.Contains(buf.String(), EnvHealthDiskCriticalMB) {
			t.Fatalf("expected warning mentioning %s, log=%q", EnvHealthDiskCriticalMB, buf.String())
		}
	})
	t.Run("non-positive falls back with warning", func(t *testing.T) {
		t.Setenv(EnvHealthDiskCriticalMB, "0")
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(prev) })

		if got := healthDiskCriticalMBFromEnv(); got != 100 {
			t.Fatalf("got %v, want default 100", got)
		}
		if !strings.Contains(buf.String(), "want a positive integer") {
			t.Fatalf("expected non-positive warning, log=%q", buf.String())
		}
	})
}

func TestHealthDiskWarningMBFromEnv(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(EnvHealthDiskWarningMB, "")
		if got := healthDiskWarningMBFromEnv(); got != 1024 {
			t.Fatalf("got %v, want default 1024", got)
		}
	})
	t.Run("valid duration respected", func(t *testing.T) {
		t.Setenv(EnvHealthDiskWarningMB, "2048")
		if got := healthDiskWarningMBFromEnv(); got != 2048 {
			t.Fatalf("got %v, want 2048", got)
		}
	})
	t.Run("invalid falls back with warning", func(t *testing.T) {
		t.Setenv(EnvHealthDiskWarningMB, "not-a-number")
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(prev) })

		if got := healthDiskWarningMBFromEnv(); got != 1024 {
			t.Fatalf("got %v, want default 1024", got)
		}
		if !strings.Contains(buf.String(), EnvHealthDiskWarningMB) {
			t.Fatalf("expected warning mentioning %s, log=%q", EnvHealthDiskWarningMB, buf.String())
		}
	})
}

func TestHealthMemWarningMBFromEnv(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		t.Setenv(EnvHealthMemWarningMB, "")
		if got := healthMemWarningMBFromEnv(); got != 500 {
			t.Fatalf("got %v, want default 500", got)
		}
	})
	t.Run("valid duration respected", func(t *testing.T) {
		t.Setenv(EnvHealthMemWarningMB, "1000")
		if got := healthMemWarningMBFromEnv(); got != 1000 {
			t.Fatalf("got %v, want 1000", got)
		}
	})
	t.Run("invalid falls back with warning", func(t *testing.T) {
		t.Setenv(EnvHealthMemWarningMB, "not-a-number")
		var buf bytes.Buffer
		prev := log.Writer()
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(prev) })

		if got := healthMemWarningMBFromEnv(); got != 500 {
			t.Fatalf("got %v, want default 500", got)
		}
		if !strings.Contains(buf.String(), EnvHealthMemWarningMB) {
			t.Fatalf("expected warning mentioning %s, log=%q", EnvHealthMemWarningMB, buf.String())
		}
	})
}

func TestDiskSpaceCheckUsesThreshold(t *testing.T) {
	// Set the threshold absurdly high so a real disk fails.
	t.Setenv(EnvHealthDiskCriticalMB, "999999999") // Almost 1 PiB
	check := DiskSpaceCheck{}
	res := check.Check(context.Background())
	if res.Healthy {
		t.Fatal("expected disk space check to fail with absurdly high threshold")
	}
	if res.Data["severity"] != "critical" {
		t.Fatalf("expected severity critical, got %v", res.Data["severity"])
	}
}

func TestMemoryCheckUsesThreshold(t *testing.T) {
	// Set the threshold absurdly high so memory check gives a warning.
	t.Setenv(EnvHealthMemWarningMB, "999999999") // Almost 1 PiB
	check := MemoryCheck{}
	res := check.Check(context.Background())
	// Skip if the environment itself can't read memory info (e.g. /proc/meminfo
	// missing in a stripped container) rather than failing on an unrelated gap.
	if strings.Contains(res.Message, "read memory info") {
		t.Skipf("skipping due to env limitation: %v", res.Message)
	}
	if !res.Healthy {
		t.Fatalf("expected healthy, got false: %v", res.Message)
	}
	if res.Data["severity"] != "warning" {
		t.Fatalf("expected warning severity with high threshold, got %v", res.Data["severity"])
	}
}

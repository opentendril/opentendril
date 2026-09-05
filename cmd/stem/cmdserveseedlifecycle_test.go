package main

import (
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestFormatSeedLifecycleReportEmitsOnlySafeIdentity(t *testing.T) {
	got := formatSeedLifecycleReport(core.SeedLifecycleReport{
		Kind:       core.SeedLifecycleAccountingIncomplete,
		PhytomerID: "tendril-1",
		Handle:     "seed-1",
	})
	if !strings.Contains(got, "kind="+core.SeedLifecycleAccountingIncomplete) {
		t.Fatalf("missing kind: %q", got)
	}
	if !strings.Contains(got, "phytomer=tendril-1") {
		t.Fatalf("missing phytomer: %q", got)
	}
	if !strings.Contains(got, "handle=seed-1") {
		t.Fatalf("missing handle: %q", got)
	}

	unsafe := []string{
		"SECRET-CONTINUATION-INTENT",
		"keep going with this prompt",
		"sk-secret-credential",
		"Bearer botanist-secret",
		"api.example.test",
		"grant",
		"egress",
		"reasoning",
		"diff --git",
		"model output",
	}
	for _, token := range unsafe {
		if strings.Contains(got, token) {
			t.Errorf("lifecycle report leaked %q: %q", token, got)
		}
	}
}

func TestServeSeedLifecycleReporterUsesSafeFormatter(t *testing.T) {
	// The logger is operational presentation over formatSeedLifecycleReport.
	// Invoking it must not panic and must not require extra fields.
	serveSeedLifecycleReporter(core.SeedLifecycleReport{
		Kind:       core.SeedLifecycleAccountingIncomplete,
		PhytomerID: "tendril-1",
		Handle:     "seed-1",
	})
}

package main

import (
	"testing"

	"github.com/opentendril/opentendril/internal/buildinfo"
)

func TestHandleVersionOnlyAcceptsStandaloneFlag(t *testing.T) {
	if !handleVersion([]string{"--version"}) {
		t.Fatal("handleVersion did not accept --version")
	}
	if handleVersion([]string{"--version", "extra"}) {
		t.Fatal("handleVersion accepted extra arguments")
	}
}

func TestBuildInfoDefaultIsDevelopmentIdentity(t *testing.T) {
	if buildinfo.Version == "" || buildinfo.Version == "0.3.13" {
		t.Fatalf("uninjected build identity = %q, want non-release identity", buildinfo.Version)
	}
}

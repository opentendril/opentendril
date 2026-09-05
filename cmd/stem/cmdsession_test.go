package main

import (
	"reflect"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestParseSessionArgsContinue(t *testing.T) {
	got, err := parseSessionArgs(core.CapContinuePhytomer, []string{
		"tendril-1", "--intent", "keep going", "--idempotency-key", "retry-1",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]any{
		"sessionId":      "tendril-1",
		"intent":         "keep going",
		"idempotencyKey": "retry-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseSessionArgsContinueJSON(t *testing.T) {
	got, err := parseSessionArgs(core.CapContinuePhytomer, []string{
		"--json", `{"sessionId":"tendril-1","intent":"keep going","idempotencyKey":"retry-1"}`,
	})
	if err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if got["sessionId"] != "tendril-1" || got["intent"] != "keep going" || got["idempotencyKey"] != "retry-1" {
		t.Fatalf("json input = %#v", got)
	}
}

func TestParseSessionArgsContinueHasNoSubstrateFlag(t *testing.T) {
	got, err := parseSessionArgs(core.CapContinuePhytomer, []string{
		"tendril-1", "--intent", "keep going", "--idempotency-key", "k1", "--substrate", "other",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := got["substrate"]; ok {
		t.Fatalf("top-level substrate leaked into continue input: %#v", got)
	}
}

func TestSessionCommandsIncludeContinue(t *testing.T) {
	command, ok := lookupSessionCommand("continue")
	if !ok || command.capability != core.CapContinuePhytomer {
		t.Fatalf("lookup continue = %+v ok=%v", command, ok)
	}
}

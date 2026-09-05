package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/historydb"
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

func TestSubmitPhytomerContinuePostsIntentBodyToDaemon(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotQuery  string
		gotBody   map[string]any
		gotAuth   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"continuationId":"continuation-1","sessionId":"tendril-1","sequence":1,"deliveryState":"pending","idempotencyKey":"retry-1"}`))
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server: %v", err)
	}
	t.Setenv("PORT", u.Port())
	t.Setenv(EnvBotanistKey, "botanist-secret")

	result, err := submitPhytomerContinue(context.Background(), map[string]any{
		"sessionId":      "tendril-1",
		"intent":         "keep going",
		"idempotencyKey": "retry-1",
		"substrate":      "other",
		"pollen":         "attacker",
		"handle":         "seed-forged",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/phytomers/tendril-1/continue" || gotQuery != "" {
		t.Fatalf("request %s %s?%s", gotMethod, gotPath, gotQuery)
	}
	if gotAuth != "Bearer botanist-secret" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if len(gotBody) != 2 || gotBody["intent"] != "keep going" || gotBody["idempotencyKey"] != "retry-1" {
		t.Fatalf("body = %#v", gotBody)
	}
	if _, ok := gotBody["substrate"]; ok {
		t.Fatal("body included substrate")
	}
	if _, ok := gotBody["pollen"]; ok {
		t.Fatal("body included pollen")
	}
	if _, ok := gotBody["handle"]; ok {
		t.Fatal("body included handle")
	}
	if result.ContinuationID != "continuation-1" || result.PhytomerID != "tendril-1" || result.Sequence != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSubmitPhytomerContinueDaemonUnavailableDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	store, err := historydb.Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("PORT", "65533")

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = submitPhytomerContinue(ctx, map[string]any{
		"sessionId":      "tendril-1",
		"intent":         "keep going",
		"idempotencyKey": "retry-1",
	})
	if err == nil {
		t.Fatal("unreachable daemon succeeded")
	}
	if !strings.Contains(err.Error(), "unreachable") && !strings.Contains(err.Error(), "connection refused") && !strings.Contains(err.Error(), "connect") {
		t.Fatalf("error = %v, want daemon unreachable", err)
	}
	recs, listErr := store.ListContinuationsByPhytomer(ctx, "tendril-1")
	if listErr != nil {
		t.Fatalf("list continuations: %v", listErr)
	}
	if len(recs) != 0 {
		t.Fatalf("continuation persisted without daemon: %+v", recs)
	}
}

func TestSubmitPhytomerContinueDaemonRejects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "phytomer is not continuation-eligible", http.StatusConflict)
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server: %v", err)
	}
	t.Setenv("PORT", u.Port())

	_, err = submitPhytomerContinue(context.Background(), map[string]any{
		"sessionId":      "tendril-1",
		"intent":         "keep going",
		"idempotencyKey": "retry-1",
	})
	if err == nil {
		t.Fatal("rejected continuation succeeded")
	}
	if !strings.Contains(err.Error(), "409") && !strings.Contains(err.Error(), "not continuation-eligible") {
		t.Fatalf("error = %v", err)
	}
}

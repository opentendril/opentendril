package receptors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/session"
)

// TestSyncGrowFlushesPhytomerAfterDispatch is the transport half of dispatch-
// time ownership: once the execution port says the opening row is durable,
// the REST adapter exposes sessionId on X-Phytomer and flushes so a concurrent
// watcher can attach while grow still blocks.
func TestSyncGrowFlushesPhytomerAfterDispatch(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	coreSvc := core.NewService(manager).WithSprout(core.SproutOperations{
		Run: func(ctx context.Context, spec core.SproutSpec) (core.SproutRunReport, error) {
			core.NotifySproutDispatch(ctx, core.SproutDispatch{
				SessionID: spec.SessionID,
				StepID:    spec.StepID,
			})
			close(entered)
			<-release
			return core.SproutRunReport{Output: "grown", Outcome: "complete"}, nil
		},
	})

	mux := http.NewServeMux()
	NewSproutHandler(coreSvc, nil, nil).Register(mux, nil)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	type result struct {
		resp *http.Response
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Post(server.URL+"/v1/sprouts/grow", "application/json",
			strings.NewReader(`{"transcript":"grow","substrate":"myrepo"}`))
		got <- result{resp: resp, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("execution port did not reach dispatch")
	}

	select {
	case outcome := <-got:
		if outcome.err != nil {
			t.Fatalf("POST grow: %v", outcome.err)
		}
		defer outcome.resp.Body.Close()
		if outcome.resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(outcome.resp.Body)
			t.Fatalf("status = %d, want 200 after dispatch flush: %s", outcome.resp.StatusCode, body)
		}
		sessionID := outcome.resp.Header.Get("X-Phytomer")
		if sessionID == "" {
			t.Fatal("X-Phytomer was empty after dispatch")
		}
		close(release)
		var payload struct {
			SessionID string `json:"sessionId"`
			Status    string `json:"status"`
		}
		if err := json.NewDecoder(outcome.resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload.SessionID != sessionID {
			t.Fatalf("body sessionId = %q, want header %q", payload.SessionID, sessionID)
		}
		if payload.Status != "matured" {
			t.Fatalf("status = %q, want matured", payload.Status)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("client did not observe flushed dispatch headers while grow was still running")
	}
}

func TestSyncGrowDoesNotFlushPhytomerWhenDispatchNeverCommits(t *testing.T) {
	manager, err := session.NewManager(context.Background(), nil)
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	coreSvc := core.NewService(manager).WithSprout(core.SproutOperations{
		Run: func(context.Context, core.SproutSpec) (core.SproutRunReport, error) {
			return core.SproutRunReport{}, context.DeadlineExceeded
		},
	})

	mux := http.NewServeMux()
	NewSproutHandler(coreSvc, nil, nil).Register(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sprouts/grow",
		strings.NewReader(`{"transcript":"grow","substrate":"myrepo"}`)))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when dispatch never committed", rec.Code)
	}
	if rec.Header().Get("X-Phytomer") != "" {
		t.Fatal("X-Phytomer leaked without a dispatch commit")
	}
}

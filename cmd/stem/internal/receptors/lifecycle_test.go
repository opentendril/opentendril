package receptors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

type lifecycleObserverStub struct {
	inventory core.LifecycleInventory
	err       error
	calls     int
}

func (s *lifecycleObserverStub) ObserveLifecycleInventory(context.Context) (core.LifecycleInventory, error) {
	s.calls++
	return s.inventory, s.err
}

func TestLifecycleHandlerReturnsCoreInventoryUnchanged(t *testing.T) {
	want := core.LifecycleInventory{
		Sources: []core.LifecycleSourceStatus{
			{Source: core.LifecycleSourceFruit, State: core.LifecycleSourceUnavailable, Reason: "history database unavailable"},
			{Source: core.LifecycleSourceRunWorkspace, State: core.LifecycleSourceAvailable, ItemCount: 1},
			{Source: core.LifecycleSourceDelegatedWorkspace, State: core.LifecycleSourceAvailable, ItemCount: 0},
		},
		RunWorkspaces: []core.RunWorkspaceReport{{
			AllocationRunID: "allocation-1",
			Repository:      "/repo/sample",
			Branch:          "sprout/task",
			Reason:          "RunWorkspace cleanliness is unknown; it is retained",
		}},
		DelegatedWorkspaces: []core.DelegatedWorkspaceReport{},
	}
	observer := &lifecycleObserverStub{inventory: want}
	rec := httptest.NewRecorder()
	NewLifecycleHandler(observer).Handle(rec, httptest.NewRequest(http.MethodGet, "/v1/lifecycle", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if observer.calls != 1 {
		t.Fatalf("calls = %d, want 1", observer.calls)
	}
	var got core.LifecycleInventory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %+v, want %+v", got, want)
	}
	if !strings.Contains(rec.Body.String(), `"fruit":null`) || !strings.Contains(rec.Body.String(), `"delegatedWorkspaces":[]`) {
		t.Fatalf("body = %s, want null unavailable Fruit and an empty delegated list", rec.Body.String())
	}
}

func TestLifecycleHandlerReportsUnavailableObservation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "not wired", err: errors.New("lifecycle inventory is not wired"), status: http.StatusServiceUnavailable},
		{name: "botanist only", err: core.ErrLifecycleInventoryBotanistOnly, status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewLifecycleHandler(&lifecycleObserverStub{err: tc.err}).Handle(rec, httptest.NewRequest(http.MethodGet, "/v1/lifecycle", nil))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "lifecycle inventory unavailable") {
				t.Fatalf("body = %q", rec.Body.String())
			}
		})
	}
}

func TestLifecycleHandlerRejectsNonGet(t *testing.T) {
	observer := &lifecycleObserverStub{}
	rec := httptest.NewRecorder()
	NewLifecycleHandler(observer).Handle(rec, httptest.NewRequest(http.MethodPost, "/v1/lifecycle", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if observer.calls != 0 {
		t.Fatalf("POST invoked Core %d times", observer.calls)
	}
}

func TestLifecycleHandlerNilObserverIsUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	NewLifecycleHandler(nil).Handle(rec, httptest.NewRequest(http.MethodGet, "/v1/lifecycle", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

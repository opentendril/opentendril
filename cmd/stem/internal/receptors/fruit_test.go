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

type fruitInventoryObserverStub struct {
	inventory core.FruitInventory
	err       error
	calls     int
}

func (s *fruitInventoryObserverStub) ObserveFruitInventory(context.Context) (core.FruitInventory, error) {
	s.calls++
	return s.inventory, s.err
}

func TestFruitHandlerReturnsCoreInventoryUnchanged(t *testing.T) {
	want := core.FruitInventory{
		Items: []core.FruitInventoryItem{{
			ProducerKind:     "Seed",
			ProducerIdentity: "seed-1",
			Repository:       "owner/repo",
			Branch:           "feature/one",
			Commit:           "abc1234",
			PublicationState: "published",
			ReviewState:      "unknown",
			UnknownReason:    "review-evidence-ambiguous",
			PullRequest:      19,
		}},
		// The adapter must preserve Core-provided counts rather than derive
		// them from the item list.
		Counts: core.FruitReviewPressure{Outstanding: 3, Unknown: 4, ClosedUnmerged: 5, Merged: 6, Total: 18},
	}
	observer := &fruitInventoryObserverStub{inventory: want}
	handler := NewFruitHandler(observer)
	req := httptest.NewRequest(http.MethodGet, "/v1/fruit", nil)
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if observer.calls != 1 {
		t.Fatalf("ObserveFruitInventory calls = %d, want 1", observer.calls)
	}
	var got core.FruitInventory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response = %+v, want %+v", got, want)
	}
}

func TestFruitHandlerReportsUnavailableInventory(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "not wired", err: core.ErrFruitInventoryNotWired},
		{name: "evidence source failure", err: errors.New("history database unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observer := &fruitInventoryObserverStub{err: tc.err}
			rec := httptest.NewRecorder()
			NewFruitHandler(observer).Handle(rec, httptest.NewRequest(http.MethodGet, "/v1/fruit", nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "Fruit inventory unavailable") {
				t.Fatalf("body = %q, want explicit unavailable message", rec.Body.String())
			}
		})
	}
}

func TestFruitHandlerRejectsNonGet(t *testing.T) {
	observer := &fruitInventoryObserverStub{}
	rec := httptest.NewRecorder()
	NewFruitHandler(observer).Handle(rec, httptest.NewRequest(http.MethodPost, "/v1/fruit", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if observer.calls != 0 {
		t.Fatalf("POST invoked Core %d times, want 0", observer.calls)
	}
}

func TestFruitHandlerNilObserverIsUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	NewFruitHandler(nil).Handle(rec, httptest.NewRequest(http.MethodGet, "/v1/fruit", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

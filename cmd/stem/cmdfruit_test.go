package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

type fruitObserverStub struct {
	inventory core.FruitInventory
	err       error
	calls     int
}

func (s *fruitObserverStub) ObserveFruitInventory(context.Context) (core.FruitInventory, error) {
	s.calls++
	return s.inventory, s.err
}

func TestExecuteFruitListTextCallsCoreOnceAndPreservesCoreValues(t *testing.T) {
	observer := &fruitObserverStub{inventory: core.FruitInventory{
		Items: []core.FruitInventoryItem{
			{
				ProducerKind:     "Sprout",
				ProducerIdentity: "run-first",
				PhytomerID:       "phytomer-first",
				Substrate:        "repo-one",
				Repository:       "owner/one",
				Branch:           "feature/first",
				Commit:           "1111111",
				PublicationState: "published",
				ReviewState:      "outstanding",
			},
			{
				ProducerKind:     "Seed",
				ProducerIdentity: "seed-second",
				Repository:       "owner/two",
				Branch:           "feature/second",
				Commit:           "2222222",
				PublicationState: "local-only",
				ReviewState:      "unknown",
				UnknownReason:    "forge-unavailable",
				PullRequest:      42,
			},
		},
		// Deliberately do not recompute these in the adapter: the values are
		// owned by Core even when a fixture makes them unusual.
		Counts: core.FruitReviewPressure{Outstanding: 7, Unknown: 8, ClosedUnmerged: 9, Merged: 10, Total: 34},
	}}

	var out bytes.Buffer
	if err := executeFruitList(context.Background(), fruitListOptions{}, observer, &out); err != nil {
		t.Fatalf("executeFruitList: %v", err)
	}
	if observer.calls != 1 {
		t.Fatalf("ObserveFruitInventory calls = %d, want 1", observer.calls)
	}
	text := out.String()
	for _, want := range []string{
		"Fruit review pressure:",
		"outstanding: 7",
		"unknown: 8",
		"closed-unmerged: 9",
		"merged: 10",
		"total: 34",
		"producer kind: Sprout",
		"producer identity: run-first",
		"phytomer identity: phytomer-first",
		"substrate: repo-one",
		"repository: owner/one",
		"branch: feature/first",
		"commit: 1111111",
		"publication state: published",
		"review state: outstanding",
		"unknown reason: forge-unavailable",
		"pull request: #42",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "producer identity: run-first") > strings.Index(text, "producer identity: seed-second") {
		t.Fatal("text output reordered Core inventory items")
	}
}

func TestExecuteFruitListJSONSerializesCoreContractDirectly(t *testing.T) {
	want := core.FruitInventory{
		Items: []core.FruitInventoryItem{{
			ProducerKind:     "Sprout",
			ProducerIdentity: "run-1",
			Repository:       "owner/repo",
			Branch:           "feature/one",
			Commit:           "deadbeef",
			PublicationState: "published",
			ReviewState:      "merged",
			PullRequest:      17,
		}},
		Counts: core.FruitReviewPressure{Merged: 1, Total: 1},
	}
	observer := &fruitObserverStub{inventory: want}
	var out bytes.Buffer
	if err := executeFruitList(context.Background(), fruitListOptions{jsonOutput: true}, observer, &out); err != nil {
		t.Fatalf("executeFruitList: %v", err)
	}
	if observer.calls != 1 {
		t.Fatalf("ObserveFruitInventory calls = %d, want 1", observer.calls)
	}

	var got core.FruitInventory
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON inventory = %+v, want %+v", got, want)
	}
}

func TestExecuteFruitListDoesNotMaskUnavailableInventory(t *testing.T) {
	observer := &fruitObserverStub{err: core.ErrFruitInventoryNotWired}
	var out bytes.Buffer
	err := executeFruitList(context.Background(), fruitListOptions{jsonOutput: true}, observer, &out)
	if !errors.Is(err, core.ErrFruitInventoryNotWired) {
		t.Fatalf("error = %v, want ErrFruitInventoryNotWired", err)
	}
	if out.Len() != 0 {
		t.Fatalf("unavailable inventory produced success output: %q", out.String())
	}
	if observer.calls != 1 {
		t.Fatalf("ObserveFruitInventory calls = %d, want 1", observer.calls)
	}
}

func TestParseFruitListArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want fruitListOptions
		err  bool
	}{
		{"list", []string{"list"}, fruitListOptions{}, false},
		{"json", []string{"list", "--json"}, fruitListOptions{jsonOutput: true}, false},
		{"help", []string{"list", "--help"}, fruitListOptions{help: true}, false},
		{"missing subcommand", nil, fruitListOptions{}, true},
		{"unknown subcommand", []string{"show"}, fruitListOptions{}, true},
		{"unknown flag", []string{"list", "--wat"}, fruitListOptions{}, true},
		{"help with extra flag", []string{"list", "--help", "--json"}, fruitListOptions{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFruitListArgs(tc.args)
			if (err != nil) != tc.err {
				t.Fatalf("parseFruitListArgs error = %v, want error=%t", err, tc.err)
			}
			if err == nil && got != tc.want {
				t.Fatalf("options = %+v, want %+v", got, tc.want)
			}
		})
	}
}

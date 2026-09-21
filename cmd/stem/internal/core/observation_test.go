package core_test

import (
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestClassifyFailureProviderAuthWinsOverFailedOutcome(t *testing.T) {
	got := core.ClassifyFailure(core.ObservationFacts{
		Outcome:                  "failed",
		RunFailed:                true,
		ProviderRequestAttempted: true,
		ProviderStatusCode:       401,
	})
	if got != core.FailureCategoryProviderAuthRejected {
		t.Fatalf("ClassifyFailure() = %q, want %q", got, core.FailureCategoryProviderAuthRejected)
	}
}

func TestClassifyFailureDoesNotReadErrorText(t *testing.T) {
	// A 401-shaped free-text error without a typed status must not become
	// provider-auth-rejected. Classification is not string matching.
	got := core.ClassifyFailure(core.ObservationFacts{
		Outcome:                  "failed",
		RunFailed:                true,
		ProviderRequestAttempted: true,
		ProviderStatusCode:       0,
	})
	if got != core.FailureCategoryExecutionFailed {
		t.Fatalf("ClassifyFailure() = %q, want %q (no typed status)", got, core.FailureCategoryExecutionFailed)
	}
}

func TestClassifyFailureCategories(t *testing.T) {
	cases := []struct {
		name  string
		facts core.ObservationFacts
		want  core.FailureCategory
	}{
		{
			name: "401 is auth rejected",
			facts: core.ObservationFacts{
				Outcome: "failed", RunFailed: true,
				ProviderRequestAttempted: true, ProviderStatusCode: 401,
			},
			want: core.FailureCategoryProviderAuthRejected,
		},
		{
			name: "403 is auth rejected",
			facts: core.ObservationFacts{
				Outcome: "failed", RunFailed: true,
				ProviderRequestAttempted: true, ProviderStatusCode: 403,
			},
			want: core.FailureCategoryProviderAuthRejected,
		},
		{
			name: "404 is request rejected",
			facts: core.ObservationFacts{
				Outcome: "failed", RunFailed: true,
				ProviderRequestAttempted: true, ProviderStatusCode: 404,
			},
			want: core.FailureCategoryProviderRequestRejected,
		},
		{
			name: "400 is request rejected",
			facts: core.ObservationFacts{
				Outcome: "failed", RunFailed: true,
				ProviderRequestAttempted: true, ProviderStatusCode: 400,
			},
			want: core.FailureCategoryProviderRequestRejected,
		},
		{
			name: "429 is request rejected",
			facts: core.ObservationFacts{
				Outcome: "failed", RunFailed: true,
				ProviderRequestAttempted: true, ProviderStatusCode: 429,
			},
			want: core.FailureCategoryProviderRequestRejected,
		},
		{
			name: "no-engagement outcome",
			facts: core.ObservationFacts{
				Outcome: "no-engagement", ProviderRequestAttempted: true,
			},
			want: core.FailureCategoryNoEngagement,
		},
		{
			name: "timed-out is terrarium runtime",
			facts: core.ObservationFacts{
				Outcome: "timed-out", RunFailed: true,
			},
			want: core.FailureCategoryTerrariumRuntime,
		},
		{
			name: "reaped is terrarium runtime",
			facts: core.ObservationFacts{
				Outcome: "reaped", RunFailed: true,
			},
			want: core.FailureCategoryTerrariumRuntime,
		},
		{
			name: "failed plus OOM is terrarium runtime",
			facts: core.ObservationFacts{
				Outcome: "failed", RunFailed: true, TerrariumOOM: true,
			},
			want: core.FailureCategoryTerrariumRuntime,
		},
		{
			name: "plain failed is execution failed",
			facts: core.ObservationFacts{
				Outcome: "failed", RunFailed: true,
			},
			want: core.FailureCategoryExecutionFailed,
		},
		{
			name:  "complete is matured",
			facts: core.ObservationFacts{Outcome: "complete"},
			want:  core.FailureCategoryMatured,
		},
		{
			name:  "no-changes is matured",
			facts: core.ObservationFacts{Outcome: "no-changes"},
			want:  core.FailureCategoryMatured,
		},
		{
			name:  "reported is matured",
			facts: core.ObservationFacts{Outcome: "reported"},
			want:  core.FailureCategoryMatured,
		},
		{
			name:  "skipped is matured",
			facts: core.ObservationFacts{Outcome: "skipped"},
			want:  core.FailureCategoryMatured,
		},
		{
			name: "5xx without outcome is execution failed",
			facts: core.ObservationFacts{
				RunFailed: true, ProviderRequestAttempted: true, ProviderStatusCode: 503,
			},
			want: core.FailureCategoryExecutionFailed,
		},
		{
			name:  "empty facts are matured",
			facts: core.ObservationFacts{},
			want:  core.FailureCategoryMatured,
		},
		{
			name: "401 wins over no-engagement",
			facts: core.ObservationFacts{
				Outcome: "no-engagement", ProviderRequestAttempted: true, ProviderStatusCode: 401,
			},
			want: core.FailureCategoryProviderAuthRejected,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := core.ClassifyFailure(testCase.facts)
			if got != testCase.want {
				t.Fatalf("ClassifyFailure(%+v) = %q, want %q", testCase.facts, got, testCase.want)
			}
		})
	}
}

func TestClassifyLifecycleStatus(t *testing.T) {
	cases := []struct {
		name     string
		category core.FailureCategory
		want     string
	}{
		{name: "matured", category: core.FailureCategoryMatured, want: "matured"},
		{name: "provider auth rejected", category: core.FailureCategoryProviderAuthRejected, want: "withered"},
		{name: "provider request rejected", category: core.FailureCategoryProviderRequestRejected, want: "withered"},
		{name: "no engagement", category: core.FailureCategoryNoEngagement, want: "withered"},
		{name: "terrarium runtime", category: core.FailureCategoryTerrariumRuntime, want: "withered"},
		{name: "execution failed", category: core.FailureCategoryExecutionFailed, want: "withered"},
		{name: "unknown category", category: core.FailureCategory("future-category"), want: "withered"},
		{name: "empty category", category: "", want: "withered"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := core.ClassifyLifecycleStatus(testCase.category); got != testCase.want {
				t.Fatalf("ClassifyLifecycleStatus(%q) = %q, want %q", testCase.category, got, testCase.want)
			}
		})
	}
}

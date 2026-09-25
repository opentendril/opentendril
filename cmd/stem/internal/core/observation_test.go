package core_test

import (
	"reflect"
	"testing"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

func TestFailureStageVocabularyIsClosedAndExact(t *testing.T) {
	want := []core.FailureStage{
		"substrate-resolution",
		"workspace-preparation",
		"task-context-preparation",
		"provider-resolution",
		"provider-preflight",
		"terrarium-preparation",
		"sprout-execution",
		"post-run",
		"fruit-publication",
		"unknown",
	}
	got := []core.FailureStage{
		core.FailureStageSubstrateResolution,
		core.FailureStageWorkspacePreparation,
		core.FailureStageTaskContextPreparation,
		core.FailureStageProviderResolution,
		core.FailureStageProviderPreflight,
		core.FailureStageTerrariumPreparation,
		core.FailureStageSproutExecution,
		core.FailureStagePostRun,
		core.FailureStageFruitPublication,
		core.FailureStageUnknown,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FailureStage vocabulary = %v, want %v", got, want)
	}
	for _, stage := range want {
		if normalized := core.NormalizeFailureStage(stage, "withered"); normalized != stage {
			t.Errorf("NormalizeFailureStage(%q) = %q", stage, normalized)
		}
	}
	if got := core.NormalizeFailureStage("invented-stage", "withered"); got != core.FailureStageUnknown {
		t.Fatalf("invalid stage normalized to %q, want unknown", got)
	}
}

func TestPersistedFailureStageValidationDoesNotNormalizeHistoricalValues(t *testing.T) {
	if got := core.ValidatePersistedFailureStage(""); got != "" {
		t.Fatalf("empty persisted stage = %q, want empty", got)
	}
	if got := core.ValidatePersistedFailureStage(core.FailureStageSubstrateResolution); got != core.FailureStageSubstrateResolution {
		t.Fatalf("valid persisted stage = %q", got)
	}
	if got := core.ValidatePersistedFailureStage("host-path-/private"); got != "" {
		t.Fatalf("invalid persisted stage = %q, want omitted rather than unknown", got)
	}
}

func TestDiagnosticCodeVocabularyIsClosedAndExact(t *testing.T) {
	want := []core.DiagnosticCode{
		"substrate-not-found",
		"substrate-access-denied",
		"substrate-invalid",
		"workspace-preparation-failed",
		"task-context-unavailable",
		"provider-unresolved",
		"provider-preflight-rejected",
		"terrarium-preparation-failed",
		"terrarium-start-failed",
		"terrarium-oom",
		"sprout-execution-failed",
		"post-run-failed",
		"fruit-publication-failed",
	}
	got := []core.DiagnosticCode{
		core.DiagnosticCodeSubstrateNotFound,
		core.DiagnosticCodeSubstrateAccessDenied,
		core.DiagnosticCodeSubstrateInvalid,
		core.DiagnosticCodeWorkspacePreparationFailed,
		core.DiagnosticCodeTaskContextUnavailable,
		core.DiagnosticCodeProviderUnresolved,
		core.DiagnosticCodeProviderPreflightRejected,
		core.DiagnosticCodeTerrariumPreparationFailed,
		core.DiagnosticCodeTerrariumStartFailed,
		core.DiagnosticCodeTerrariumOOM,
		core.DiagnosticCodeSproutExecutionFailed,
		core.DiagnosticCodePostRunFailed,
		core.DiagnosticCodeFruitPublicationFailed,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiagnosticCode vocabulary = %v, want %v", got, want)
	}
	for _, code := range want {
		if normalized := core.NormalizeDiagnosticCode(code, "withered"); normalized != code {
			t.Errorf("NormalizeDiagnosticCode(%q) = %q", code, normalized)
		}
	}
	if got := core.NormalizeDiagnosticCode("invented-code", "withered"); got != "" {
		t.Fatalf("invalid diagnostic code normalized to %q, want omitted", got)
	}
}

func TestPersistedDiagnosticCodeValidationOmitsInvalidValues(t *testing.T) {
	if got := core.ValidatePersistedDiagnosticCode(""); got != "" {
		t.Fatalf("empty persisted diagnostic = %q, want empty", got)
	}
	if got := core.ValidatePersistedDiagnosticCode(core.DiagnosticCodeSubstrateAccessDenied); got != core.DiagnosticCodeSubstrateAccessDenied {
		t.Fatalf("valid persisted diagnostic = %q", got)
	}
	if got := core.ValidatePersistedDiagnosticCode("permission denied: /private"); got != "" {
		t.Fatalf("invalid persisted diagnostic = %q, want omitted", got)
	}
}

func TestMaturedLifecycleHasNoFailureProvenance(t *testing.T) {
	if got := core.NormalizeFailureStage(core.FailureStageSubstrateResolution, "matured"); got != "" {
		t.Fatalf("matured stage = %q, want empty", got)
	}
	if got := core.NormalizeDiagnosticCode(core.DiagnosticCodeSubstrateAccessDenied, "matured"); got != "" {
		t.Fatalf("matured diagnostic code = %q, want empty", got)
	}
}

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

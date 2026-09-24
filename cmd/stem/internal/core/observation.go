package core

// FailureCategory is the Botanist-facing observation class for one Sprout run.
// It is a closed domain enum owned by the Core. Adapters persist and render
// the value; they do not invent it, and they do not derive it by reading
// free-text errors.
//
// Wire values are kebab-case domain enums.
type FailureCategory string

// FailureStage is the closed Core-owned lifecycle location vocabulary for a
// withered Sprout. Values describe deterministic execution boundaries; they
// are never derived from error text.
type FailureStage string

const (
	FailureStageSubstrateResolution    FailureStage = "substrate-resolution"
	FailureStageWorkspacePreparation   FailureStage = "workspace-preparation"
	FailureStageTaskContextPreparation FailureStage = "task-context-preparation"
	FailureStageProviderResolution     FailureStage = "provider-resolution"
	FailureStageProviderPreflight      FailureStage = "provider-preflight"
	FailureStageTerrariumPreparation   FailureStage = "terrarium-preparation"
	FailureStageSproutExecution        FailureStage = "sprout-execution"
	FailureStagePostRun                FailureStage = "post-run"
	FailureStageFruitPublication       FailureStage = "fruit-publication"
	FailureStageUnknown                FailureStage = "unknown"
)

// DiagnosticCode is the closed, optional vocabulary for bounded typed detail
// about a failed Sprout. It must not carry paths, error messages, or payloads.
type DiagnosticCode string

const (
	DiagnosticCodeSubstrateNotFound          DiagnosticCode = "substrate-not-found"
	DiagnosticCodeSubstrateAccessDenied      DiagnosticCode = "substrate-access-denied"
	DiagnosticCodeSubstrateInvalid           DiagnosticCode = "substrate-invalid"
	DiagnosticCodeWorkspacePreparationFailed DiagnosticCode = "workspace-preparation-failed"
	DiagnosticCodeTaskContextUnavailable     DiagnosticCode = "task-context-unavailable"
	DiagnosticCodeProviderUnresolved         DiagnosticCode = "provider-unresolved"
	DiagnosticCodeProviderPreflightRejected  DiagnosticCode = "provider-preflight-rejected"
	DiagnosticCodeTerrariumPreparationFailed DiagnosticCode = "terrarium-preparation-failed"
	DiagnosticCodeTerrariumStartFailed       DiagnosticCode = "terrarium-start-failed"
	DiagnosticCodeTerrariumOOM               DiagnosticCode = "terrarium-oom"
	DiagnosticCodeSproutExecutionFailed      DiagnosticCode = "sprout-execution-failed"
	DiagnosticCodePostRunFailed              DiagnosticCode = "post-run-failed"
	DiagnosticCodeFruitPublicationFailed     DiagnosticCode = "fruit-publication-failed"
)

// NormalizeFailureStage returns only a Core-approved lifecycle stage. A
// matured run has no failure stage; a withered run with absent or invalid
// evidence fails honestly to unknown.
func NormalizeFailureStage(stage FailureStage, lifecycleStatus string) FailureStage {
	if lifecycleStatus == "matured" {
		return ""
	}
	if validated := ValidatePersistedFailureStage(stage); validated != "" {
		return validated
	}
	return FailureStageUnknown
}

// ValidatePersistedFailureStage preserves an approved durable value and omits
// empty or invalid historical data. Unlike NormalizeFailureStage, it never
// manufactures unknown for an absent or invalid persisted value.
func ValidatePersistedFailureStage(stage FailureStage) FailureStage {
	switch stage {
	case FailureStageSubstrateResolution,
		FailureStageWorkspacePreparation,
		FailureStageTaskContextPreparation,
		FailureStageProviderResolution,
		FailureStageProviderPreflight,
		FailureStageTerrariumPreparation,
		FailureStageSproutExecution,
		FailureStagePostRun,
		FailureStageFruitPublication,
		FailureStageUnknown:
		return stage
	default:
		return ""
	}
}

// NormalizeDiagnosticCode returns a code only when it belongs to the closed
// Core vocabulary and the run withered. It never examines error text.
func NormalizeDiagnosticCode(code DiagnosticCode, lifecycleStatus string) DiagnosticCode {
	if lifecycleStatus == "matured" {
		return ""
	}
	return ValidatePersistedDiagnosticCode(code)
}

// ValidatePersistedDiagnosticCode preserves an approved durable value and
// omits empty or invalid historical data.
func ValidatePersistedDiagnosticCode(code DiagnosticCode) DiagnosticCode {
	switch code {
	case DiagnosticCodeSubstrateNotFound,
		DiagnosticCodeSubstrateAccessDenied,
		DiagnosticCodeSubstrateInvalid,
		DiagnosticCodeWorkspacePreparationFailed,
		DiagnosticCodeTaskContextUnavailable,
		DiagnosticCodeProviderUnresolved,
		DiagnosticCodeProviderPreflightRejected,
		DiagnosticCodeTerrariumPreparationFailed,
		DiagnosticCodeTerrariumStartFailed,
		DiagnosticCodeTerrariumOOM,
		DiagnosticCodeSproutExecutionFailed,
		DiagnosticCodePostRunFailed,
		DiagnosticCodeFruitPublicationFailed:
		return code
	default:
		return ""
	}
}

const (
	// FailureCategoryProviderAuthRejected: the provider refused the principal
	// (HTTP 401 / 403 / 407) after a Mycorrhizal request was issued.
	FailureCategoryProviderAuthRejected FailureCategory = "provider-auth-rejected"
	// FailureCategoryProviderRequestRejected: the provider refused the request
	// itself (other 4xx) after a Mycorrhizal request was issued.
	FailureCategoryProviderRequestRejected FailureCategory = "provider-request-rejected"
	// FailureCategoryNoEngagement: the run finished without error but never
	// answered or acted.
	FailureCategoryNoEngagement FailureCategory = "no-engagement"
	// FailureCategoryTerrariumRuntime: a Terrarium clock or crash ended the
	// run (timeout, reap, OOM).
	FailureCategoryTerrariumRuntime FailureCategory = "terrarium-runtime"
	// FailureCategoryExecutionFailed: the run errored for a reason that is not
	// a more specific category above.
	FailureCategoryExecutionFailed FailureCategory = "execution-failed"
	// FailureCategoryMatured: the run finished as a successful maturation
	// (complete, no-changes, reported, or skipped).
	FailureCategoryMatured FailureCategory = "matured"
)

// ProviderDiagnostic is a safe, credential-free explanation of a provider
// response. Message must never carry secrets, bearer tokens, or API keys.
type ProviderDiagnostic struct {
	StatusCode int    `json:"statusCode,omitempty"`
	Message    string `json:"message,omitempty"`
	Provider   string `json:"provider,omitempty"`
}

// ObservationFacts are the typed inputs ClassifyFailure may consult. They are
// facts already established by Roots or the Conductor — never raw error text.
type ObservationFacts struct {
	// Outcome is the Conductor's existing SproutOutcome* verdict.
	Outcome string
	// RunFailed is true when the execution port returned an error.
	RunFailed bool
	// TerrariumOOM is true when the Terrarium reported an OOM (exit 137).
	TerrariumOOM bool
	// ProviderRequestAttempted is true when at least one Mycorrhizal request
	// was issued.
	ProviderRequestAttempted bool
	// ProviderStatusCode is the HTTP status of a typed provider response, or
	// zero when no typed provider response exists.
	ProviderStatusCode int
}

// ClassifyFailure maps typed observation facts onto the closed FailureCategory
// vocabulary. Provider status wins over outcome because a 401 that also
// "failed" is an authentication rejection, not a generic execution failure.
// The function never inspects error strings.
func ClassifyFailure(facts ObservationFacts) FailureCategory {
	if facts.ProviderStatusCode == 401 || facts.ProviderStatusCode == 403 || facts.ProviderStatusCode == 407 {
		return FailureCategoryProviderAuthRejected
	}
	if facts.ProviderStatusCode >= 400 && facts.ProviderStatusCode < 500 {
		return FailureCategoryProviderRequestRejected
	}
	switch facts.Outcome {
	case "no-engagement":
		return FailureCategoryNoEngagement
	case "timed-out", "reaped":
		return FailureCategoryTerrariumRuntime
	case "complete", "no-changes", "reported", "skipped":
		return FailureCategoryMatured
	case "failed":
		if facts.TerrariumOOM {
			return FailureCategoryTerrariumRuntime
		}
		return FailureCategoryExecutionFailed
	}
	if facts.TerrariumOOM {
		return FailureCategoryTerrariumRuntime
	}
	if facts.RunFailed {
		return FailureCategoryExecutionFailed
	}
	return FailureCategoryMatured
}

// ClassifyLifecycleStatus maps a Core-owned FailureCategory onto the terminal
// Sprout lifecycle. Unknown categories fail closed as withered.
func ClassifyLifecycleStatus(category FailureCategory) string {
	if category == FailureCategoryMatured {
		return "matured"
	}
	return "withered"
}

package core

// FailureCategory is the Botanist-facing observation class for one Sprout run.
// It is a closed domain enum owned by the Core. Adapters persist and render
// the value; they do not invent it, and they do not derive it by reading
// free-text errors.
//
// Wire values are kebab-case domain enums.
type FailureCategory string

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

package conductor

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/roots/llm"
)

// providerAuthPreflightBudget bounds the host-side Roots authentication
// probe. It is a discovery clock, not a growth budget: a hung provider
// must not hold Terrarium creation open indefinitely.
const providerAuthPreflightBudget = 15 * time.Second

var probeProviderAuthFn = probeProviderAuthentication

func probeProviderAuthentication(ctx context.Context, mind *llm.Client) error {
	if mind == nil {
		return nil
	}
	// Local inference has no provider credential to reject. Reachability
	// is already the existing preflight (local endpoint).
	if strings.EqualFold(strings.TrimSpace(mind.Provider()), "local") {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, providerAuthPreflightBudget)
	defer cancel()
	return mind.ProbeAuthentication(probeCtx)
}

func isProviderAuthRejection(err error) bool {
	var reqErr *llm.RequestError
	if !errors.As(err, &reqErr) || reqErr == nil {
		return false
	}
	return core.ClassifyFailure(core.ObservationFacts{
		ProviderStatusCode: reqErr.StatusCode,
	}) == core.FailureCategoryProviderAuthRejected
}

// applyProviderAuthPreflight asks Roots to prove the selected provider
// will accept the run's credential. An authentication rejection is
// terminal and must happen before sprout-emerged / Terrarium creation.
// Any other probe error is not this gate: the run continues and later
// classification paths still apply.
func applyProviderAuthPreflight(ctx context.Context, mind *llm.Client, report *SproutRunReport) error {
	err := probeProviderAuthFn(ctx, mind)
	if err == nil {
		return nil
	}
	if !isProviderAuthRejection(err) {
		return nil
	}
	if report != nil {
		report.RequestsMade = true
	}
	return err
}

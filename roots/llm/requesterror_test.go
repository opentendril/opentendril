package llm

import (
	"errors"
	"strings"
	"testing"
)

func TestSafeProviderMessageExtractsJSONError(t *testing.T) {
	got := safeProviderMessage(`{"error":{"message":"User not found","code":401}}`)
	if got != "User not found" {
		t.Fatalf("safeProviderMessage() = %q, want User not found", got)
	}
}

func TestSafeProviderMessageRedactsBearerAndTruncates(t *testing.T) {
	got := safeProviderMessage("denied Bearer sk-super-secret-value-that-must-not-leak")
	if strings.Contains(got, "sk-super-secret") || strings.Contains(strings.ToLower(got), "bearer sk-") {
		t.Fatalf("safeProviderMessage leaked a secret: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("safeProviderMessage() = %q, want a redacted marker", got)
	}

	long := strings.Repeat("x", maxSafeProviderMessage+50)
	got = safeProviderMessage(long)
	if len(got) <= maxSafeProviderMessage {
		t.Fatalf("truncated length = %d, want marker past the cap", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("safeProviderMessage() = %q, want a truncation marker", got)
	}
}

func TestRequestErrorPreservesHistoricalShapeAndUnwrap(t *testing.T) {
	err := newRequestError(401, `{"error":{"message":"User not found"}}`, ProviderSpec{
		Provider:      "openrouter",
		Model:         "anthropic/claude-sonnet-4.6",
		Tier:          TierPremium,
		OutputLimit:   8192,
		CeilingSource: "compiled fallback",
	}, nil)
	if !strings.Contains(err.Error(), "llm returned 401") {
		t.Fatalf("Error() = %q, want llm returned 401", err.Error())
	}
	if !strings.Contains(err.Error(), "User not found") {
		t.Fatalf("Error() = %q, want the provider explanation", err.Error())
	}
	if err.SafeMessage() != "User not found" {
		t.Fatalf("SafeMessage() = %q, want User not found", err.SafeMessage())
	}

	wrapped := newRequestError(400, "bad tools", ProviderSpec{Provider: "openai", Model: "gpt"}, ErrRejectedWithTools)
	if !errors.Is(wrapped, ErrRejectedWithTools) {
		t.Fatal("RequestError with tools refusal must unwrap to ErrRejectedWithTools")
	}
	var got *RequestError
	if !errors.As(wrapped, &got) || got.StatusCode != 400 {
		t.Fatalf("errors.As() = %+v, want StatusCode 400", got)
	}
}

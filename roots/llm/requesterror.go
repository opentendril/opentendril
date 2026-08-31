package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const maxSafeProviderMessage = 400

var requestErrorSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]+`),
	regexp.MustCompile(`(sk-|ghp_|github_pat_|xox[baprs]-)[A-Za-z0-9._\-]+`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`),
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|secret|password)(\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;}]+)`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;}]+)`),
}

// RequestError is a typed provider HTTP rejection. StatusCode is the fact
// Stem core classifies from; Body is a credential-free excerpt of the
// provider's own explanation. The Error() string keeps the historical
// "llm returned N: …" shape so existing readers stay valid.
type RequestError struct {
	StatusCode int
	Provider   string
	Model      string
	Tier       ModelTier
	Ceiling    int
	Source     string
	// Body is the sanitized provider explanation. It never carries the
	// request's API key or a bearer token.
	Body               string
	EndpointResolution LocalEndpointResolution
	AttemptedEndpoint  string
	Failure            ReachabilityFailureClass
	wrapped            error
}

func (e *RequestError) Error() string {
	if e == nil {
		return "llm request error"
	}
	base := fmt.Sprintf("llm returned %d: %s (provider=%s model=%s tier=%s ceiling=%d source=%q)",
		e.StatusCode, e.Body, e.Provider, e.Model, e.Tier, e.Ceiling, e.Source)
	if e.EndpointResolution.EffectiveEndpoint != "" || e.AttemptedEndpoint != "" {
		base += " (" + safeEndpointDescription(e.EndpointResolution, e.AttemptedEndpoint) + ")"
	}
	if e.wrapped != nil {
		return e.wrapped.Error() + ": " + base
	}
	return base
}

func (e *RequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.wrapped
}

// SafeMessage returns the credential-free provider explanation.
func (e *RequestError) SafeMessage() string {
	if e == nil {
		return ""
	}
	return e.Body
}

func (e *RequestError) FailureClass() ReachabilityFailureClass {
	if e == nil {
		return ""
	}
	if e.Failure != "" {
		return e.Failure
	}
	return ReachabilityFailureProviderHTTP
}

// NewRequestError builds a typed provider rejection whose Body has already
// been stripped of bearer tokens and API-key material.
func NewRequestError(status int, body string, spec ProviderSpec) *RequestError {
	return newRequestError(status, body, spec, nil)
}

func newRequestError(status int, body string, spec ProviderSpec, wrapped error) *RequestError {
	return newRequestErrorAt(status, body, spec, wrapped, "")
}

func newRequestErrorAt(status int, body string, spec ProviderSpec, wrapped error, attempted string) *RequestError {
	return &RequestError{
		StatusCode:         status,
		Provider:           spec.Provider,
		Model:              spec.Model,
		Tier:               spec.Tier,
		Ceiling:            spec.OutputLimit,
		Source:             spec.CeilingSource,
		Body:               safeProviderMessage(body),
		EndpointResolution: endpointResolutionForAttempt(spec, attempted),
		AttemptedEndpoint:  attempted,
		Failure:            ReachabilityFailureProviderHTTP,
		wrapped:            wrapped,
	}
}

func safeProviderMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if msg := extractJSONErrorMessage(body); msg != "" {
		body = msg
	}
	body = redactEndpointURLs(body)
	body = redactProviderSecrets(body)
	if len(body) > maxSafeProviderMessage {
		return body[:maxSafeProviderMessage] + "…"
	}
	return body
}

func extractJSONErrorMessage(body string) string {
	var payload any
	if json.Unmarshal([]byte(body), &payload) != nil {
		return ""
	}
	return findJSONErrorMessage(payload)
}

func findJSONErrorMessage(value any) string {
	object, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if errValue, exists := object["error"]; exists {
		switch typed := errValue.(type) {
		case string:
			if msg := strings.TrimSpace(typed); msg != "" {
				return msg
			}
		case map[string]any:
			if msg, _ := typed["message"].(string); strings.TrimSpace(msg) != "" {
				return strings.TrimSpace(msg)
			}
		}
	}
	if msg, _ := object["message"].(string); strings.TrimSpace(msg) != "" {
		return strings.TrimSpace(msg)
	}
	return ""
}

func redactProviderSecrets(text string) string {
	redacted := text
	for _, pattern := range requestErrorSecretPatterns {
		redacted = pattern.ReplaceAllString(redacted, "[REDACTED]")
	}
	return redacted
}

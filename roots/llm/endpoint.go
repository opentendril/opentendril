package llm

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// EndpointSource records where a local-provider endpoint came from. An
// explicit source is authoritative; only the built-in/default source may
// synthesize a caller-specific endpoint.
type EndpointSource string

const (
	EndpointSourceEnvironment           EndpointSource = "environment"
	EndpointSourceProviderConfig        EndpointSource = "provider-config"
	EndpointSourceOrchestrationOverride EndpointSource = "orchestration-override"
	EndpointSourceBuiltInDefault        EndpointSource = "built-in/default"
)

// EndpointCaller identifies the process network namespace that will issue the
// Mycorrhizal request.
type EndpointCaller string

const (
	EndpointCallerHost      EndpointCaller = "host"
	EndpointCallerContainer EndpointCaller = "container"
)

// LocalEndpointContext is the caller-side network fact used when a local
// endpoint has to be synthesized. HostAliasApplicable must be established by
// the caller; this package does not manufacture a Docker alias merely because
// its hostname is familiar.
type LocalEndpointContext struct {
	Caller              EndpointCaller
	HostAlias           string
	HostAliasApplicable bool
}

// HostLocalEndpointContext describes a request issued by the host Stem.
func HostLocalEndpointContext() LocalEndpointContext {
	return LocalEndpointContext{Caller: EndpointCallerHost}
}

// ContainerLocalEndpointContext describes a request issued from a container.
// The caller supplies the established applicability of its host alias.
func ContainerLocalEndpointContext(hostAlias string, applicable bool) LocalEndpointContext {
	return LocalEndpointContext{
		Caller:              EndpointCallerContainer,
		HostAlias:           strings.TrimSpace(hostAlias),
		HostAliasApplicable: applicable,
	}
}

// localEndpointHostAliasAvailableFn is intentionally a narrow applicability
// probe, not a DNS or networking manager. It is used only by the convenience
// caller-context resolver; callers that already established their container
// topology should use ContainerLocalEndpointContext directly.
var localEndpointHostAliasAvailableFn = func(alias string) bool {
	_, err := net.LookupHost(alias)
	return err == nil
}

var endpointInErrorPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// LocalEndpointResolution is the canonical local-provider endpoint state
// shared by request execution, model discovery, and Conductor preflight.
type LocalEndpointResolution struct {
	ConfiguredEndpoint string
	EffectiveEndpoint  string
	Source             EndpointSource
	CallerContext      LocalEndpointContext
	Synthesized        bool
	SynthesisReason    string
	Candidates         []string
}

// ReachabilityFailureClass is the safe, deterministic failure category for a
// provider readiness or request attempt.
type ReachabilityFailureClass string

const (
	ReachabilityFailureDNS              ReachabilityFailureClass = "dns/resolution"
	ReachabilityFailureConnection       ReachabilityFailureClass = "connection refused/unreachable"
	ReachabilityFailureProxy            ReachabilityFailureClass = "proxy-mediated"
	ReachabilityFailureTLS              ReachabilityFailureClass = "tls"
	ReachabilityFailureProviderHTTP     ReachabilityFailureClass = "provider-http"
	ReachabilityFailureModelUnavailable ReachabilityFailureClass = "model-unavailable"
)

// ProviderReachabilityError carries safe provider endpoint diagnostics. It
// intentionally reports endpoint facts through redacted rendering rather than
// including the raw URL or transport error in its public message.
type ProviderReachabilityError struct {
	Class             ReachabilityFailureClass
	Provider          string
	Model             string
	Resolution        LocalEndpointResolution
	AttemptedEndpoint string
	StatusCode        int
	Message           string
	Cause             error
}

func (e *ProviderReachabilityError) Error() string {
	if e == nil {
		return "provider reachability error"
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = string(e.Class)
	}
	status := ""
	if e.StatusCode != 0 {
		status = fmt.Sprintf(" status=%d", e.StatusCode)
	}
	return fmt.Sprintf("provider %s failure%s: %s (provider=%q model=%q %s)", e.Class, status, message, e.Provider, e.Model, safeEndpointDescription(e.Resolution, e.AttemptedEndpoint))
}

func (e *ProviderReachabilityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ProviderReachabilityError) FailureClass() ReachabilityFailureClass {
	if e == nil {
		return ""
	}
	return e.Class
}

func (e *ProviderReachabilityError) SafeMessage() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newProviderReachabilityError(spec ProviderSpec, attempted string, class ReachabilityFailureClass, cause error) *ProviderReachabilityError {
	resolution := endpointResolutionForAttempt(spec, attempted)
	return &ProviderReachabilityError{
		Class:             class,
		Provider:          spec.Provider,
		Model:             spec.Model,
		Resolution:        resolution,
		AttemptedEndpoint: attempted,
		Message:           safeTransportMessage(cause),
		Cause:             cause,
	}
}

// NewModelUnavailableError reports that the resolved provider answered its
// models request but did not advertise the model selected for the run.
func NewModelUnavailableError(client *Client) *ProviderReachabilityError {
	if client == nil {
		return &ProviderReachabilityError{
			Class:   ReachabilityFailureModelUnavailable,
			Message: "selected model is unavailable",
		}
	}
	resolution := client.EndpointResolution()
	return &ProviderReachabilityError{
		Class:             ReachabilityFailureModelUnavailable,
		Provider:          client.Provider(),
		Model:             client.Model(),
		Resolution:        resolution,
		AttemptedEndpoint: resolution.EffectiveEndpoint,
		Message:           fmt.Sprintf("selected model %q is not advertised by the provider", client.Model()),
	}
}

func classifyTransportFailure(err error) ReachabilityFailureClass {
	if err == nil {
		return ReachabilityFailureConnection
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "proxy") || strings.Contains(message, "proxyconnect"):
		return ReachabilityFailureProxy
	case strings.Contains(message, "tls") || strings.Contains(message, "certificate") || strings.Contains(message, "x509"):
		return ReachabilityFailureTLS
	case strings.Contains(message, "no such host") || strings.Contains(message, "server misbehaving") || strings.Contains(message, "nodename nor servname") || strings.Contains(message, "name or service not known") || strings.Contains(message, "lookup"):
		return ReachabilityFailureDNS
	default:
		return ReachabilityFailureConnection
	}
}

func safeTransportMessage(err error) string {
	if err == nil {
		return "provider transport failed"
	}
	return safeProviderMessage(err.Error())
}

func redactEndpointURLs(message string) string {
	return endpointInErrorPattern.ReplaceAllStringFunc(message, safeEndpointURL)
}

func (r LocalEndpointResolution) candidateURLs() []string {
	if len(r.Candidates) == 0 {
		if strings.TrimSpace(r.EffectiveEndpoint) == "" {
			return nil
		}
		return []string{r.EffectiveEndpoint}
	}
	return append([]string(nil), r.Candidates...)
}

func normalizeLocalEndpointContext(context LocalEndpointContext) LocalEndpointContext {
	if context.Caller == "" {
		context.Caller = EndpointCallerHost
	}
	if context.Caller == EndpointCallerContainer && context.HostAlias == "" {
		context.HostAlias = "host.docker.internal"
	}
	return context
}

// ResolveLocalEndpoint resolves the local provider endpoint from the current
// environment/configuration using the supplied caller context.
func ResolveLocalEndpoint(context LocalEndpointContext) LocalEndpointResolution {
	providerConfig := configuredProvider("local")
	return resolveLocalEndpoint(context, "", os.Getenv("LOCAL_INFERENCE_URL"), providerConfig.BaseURL)
}

// ResolveLocalEndpointForCaller is a convenience resolver for callers that do
// not need to pass an already-established alias fact. A container caller gets
// a host alias only after this narrow lookup establishes that it is usable.
func ResolveLocalEndpointForCaller(caller EndpointCaller) LocalEndpointResolution {
	return ResolveLocalEndpoint(localEndpointContextForCaller(caller))
}

func localEndpointContextForCaller(caller EndpointCaller) LocalEndpointContext {
	context := LocalEndpointContext{Caller: caller}
	if caller == EndpointCallerContainer && !localEndpointHasExplicitConfiguration() {
		context.HostAlias = "host.docker.internal"
		context.HostAliasApplicable = localEndpointHostAliasAvailableFn(context.HostAlias)
	}
	return context
}

func localEndpointHasExplicitConfiguration() bool {
	if strings.TrimSpace(os.Getenv("LOCAL_INFERENCE_URL")) != "" {
		return true
	}
	return strings.TrimSpace(configuredProvider("local").BaseURL) != ""
}

func resolveLocalEndpoint(context LocalEndpointContext, orchestrationOverride, environmentURL, providerConfigURL string) LocalEndpointResolution {
	context = normalizeLocalEndpointContext(context)
	for _, explicit := range []struct {
		value  string
		source EndpointSource
	}{
		{orchestrationOverride, EndpointSourceOrchestrationOverride},
		{environmentURL, EndpointSourceEnvironment},
		{providerConfigURL, EndpointSourceProviderConfig},
	} {
		if endpoint := strings.TrimSpace(explicit.value); endpoint != "" {
			return LocalEndpointResolution{
				ConfiguredEndpoint: endpoint,
				EffectiveEndpoint:  endpoint,
				Source:             explicit.source,
				CallerContext:      context,
				Candidates:         []string{endpoint},
			}
		}
	}

	resolution := LocalEndpointResolution{
		Source:          EndpointSourceBuiltInDefault,
		CallerContext:   context,
		Synthesized:     true,
		SynthesisReason: "no explicit local-provider endpoint was configured",
	}
	if context.Caller == EndpointCallerContainer {
		if context.HostAliasApplicable && strings.TrimSpace(context.HostAlias) != "" {
			resolution.EffectiveEndpoint = "http://" + strings.TrimSpace(context.HostAlias) + ":11434/v1"
			resolution.Candidates = []string{resolution.EffectiveEndpoint}
			resolution.SynthesisReason = "container caller has an established host alias"
			return resolution
		}
		resolution.SynthesisReason = "container caller has no established host alias or bridge address"
		return resolution
	}

	resolution.EffectiveEndpoint = "http://localhost:11434/v1"
	resolution.Candidates = []string{resolution.EffectiveEndpoint}
	resolution.SynthesisReason = "host caller uses host-local loopback for the built-in local-provider default"
	return resolution
}

func resolutionForExplicitSpec(spec ProviderSpec) LocalEndpointResolution {
	endpoint := strings.TrimSpace(spec.BaseURL)
	if endpoint == "" && len(spec.BaseURLs) > 0 {
		endpoint = strings.TrimSpace(spec.BaseURLs[0])
	}
	resolution := spec.EndpointResolution
	if resolution.CallerContext.Caller == "" {
		resolution.CallerContext = HostLocalEndpointContext()
	}
	if resolution.EffectiveEndpoint == "" {
		resolution.EffectiveEndpoint = endpoint
	}
	if resolution.ConfiguredEndpoint == "" {
		resolution.ConfiguredEndpoint = endpoint
	}
	if resolution.Source == "" {
		resolution.Source = EndpointSourceOrchestrationOverride
	}
	if len(resolution.Candidates) == 0 {
		if len(spec.BaseURLs) > 0 {
			resolution.Candidates = append([]string(nil), spec.BaseURLs...)
		} else if endpoint != "" {
			resolution.Candidates = []string{endpoint}
		}
	}
	return resolution
}

// ApplyExplicitBaseURLOverride applies an orchestration/CLI endpoint override
// to an already resolved spec. The operation is kept in Roots so adapters do
// not reconstruct provider endpoint semantics themselves.
func ApplyExplicitBaseURLOverride(spec *ProviderSpec, baseURL string) {
	if spec == nil {
		return
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return
	}
	spec.BaseURL = baseURL
	spec.BaseURLs = []string{baseURL}
	if strings.EqualFold(strings.TrimSpace(spec.Provider), "local") {
		caller := spec.EndpointResolution.CallerContext
		if caller.Caller == "" {
			caller = HostLocalEndpointContext()
		}
		spec.EndpointResolution = LocalEndpointResolution{
			ConfiguredEndpoint: baseURL,
			EffectiveEndpoint:  baseURL,
			Source:             EndpointSourceOrchestrationOverride,
			CallerContext:      caller,
			Candidates:         []string{baseURL},
		}
	}
}

func endpointResolutionForAttempt(spec ProviderSpec, attempt string) LocalEndpointResolution {
	resolution := spec.EndpointResolution
	if resolution.EffectiveEndpoint == "" {
		resolution = resolutionForExplicitSpec(spec)
	}
	if resolution.CallerContext.Caller == "" {
		resolution.CallerContext = HostLocalEndpointContext()
	}
	return resolution
}

func safeEndpointURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid endpoint]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func safeEndpointDescription(resolution LocalEndpointResolution, attempted string) string {
	effective := safeEndpointURL(resolution.EffectiveEndpoint)
	configured := safeEndpointURL(resolution.ConfiguredEndpoint)
	attempted = safeEndpointURL(attempted)
	return fmt.Sprintf("configured=%q effective=%q attempted=%q provenance=%q caller=%q alias=%q aliasApplicable=%t synthesized=%t synthesisReason=%q", configured, effective, attempted, resolution.Source, resolution.CallerContext.Caller, resolution.CallerContext.HostAlias, resolution.CallerContext.HostAliasApplicable, resolution.Synthesized, resolution.SynthesisReason)
}

func (c *Client) endpointCandidates() []string {
	if c == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(c.spec.Provider), "local") {
		if candidates := c.spec.EndpointResolution.candidateURLs(); len(candidates) > 0 {
			return candidates
		}
	}
	if len(c.spec.BaseURLs) > 0 {
		return append([]string(nil), c.spec.BaseURLs...)
	}
	if strings.TrimSpace(c.spec.BaseURL) == "" {
		return nil
	}
	return []string{c.spec.BaseURL}
}

func isTransportFailure(err error) bool {
	var reachabilityErr *ProviderReachabilityError
	return errors.As(err, &reachabilityErr)
}

// preferAttemptError preserves the first meaningful application/provider
// failure when a later synthesized candidate only contributes transport noise.
// A later provider response can replace an earlier transport failure because it
// tells the operator more about the endpoint that did answer.
func preferAttemptError(current, candidate error) error {
	if current == nil {
		return candidate
	}
	if candidate == nil {
		return current
	}
	if isTransportFailure(current) && !isTransportFailure(candidate) {
		return candidate
	}
	return current
}

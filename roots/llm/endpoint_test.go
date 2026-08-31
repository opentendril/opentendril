package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestLocalEndpointResolutionPreservesExplicitEnvironmentEndpoint(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_INFERENCE_URL", "http://localhost:11434/v1")
	t.Setenv("LOCAL_MODEL_NAME", "present-model")

	spec := ResolveProviderSpec()
	assertExplicitLocalEndpoint(t, spec, "http://localhost:11434/v1", EndpointSourceEnvironment)
}

func TestLocalEndpointResolutionPreservesExplicitLoopbackAndRemoteEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"http://127.0.0.1:11434/v1",
		"https://remote.example/v1",
		"http://host.docker.internal:11434/v1",
	} {
		t.Run(endpoint, func(t *testing.T) {
			clearProviderKeys(t)
			t.Setenv("DEFAULT_LLM_PROVIDER", "local")
			t.Setenv("LOCAL_INFERENCE_URL", endpoint)
			t.Setenv("LOCAL_MODEL_NAME", "present-model")

			assertExplicitLocalEndpoint(t, ResolveProviderSpec(), endpoint, EndpointSourceEnvironment)
		})
	}
}

func TestLocalEndpointResolutionUsesHostProviderConfigExactly(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "")
	t.Setenv("LOCAL_INFERENCE_URL", "")
	chdirWithTendrilConfig(t, "llm:\n  default-provider: local\n  providers:\n    local:\n      base-url: http://localhost:11434/v1\n      model: llama3.2\n      endpoint: /chat/completions\n      temperature: 0.1\n")

	spec := ResolveLocalProviderSpecForContext(HostLocalEndpointContext())
	assertExplicitLocalEndpoint(t, spec, "http://localhost:11434/v1", EndpointSourceProviderConfig)
	if spec.Model != "llama3.2" {
		t.Fatalf("model = %q, want repository local model", spec.Model)
	}
	if spec.EndpointResolution.CallerContext.Caller != EndpointCallerHost {
		t.Fatalf("caller = %q, want host", spec.EndpointResolution.CallerContext.Caller)
	}
}

func TestModelIDsMatchPreservesExactAndLocalDefaultTagIdentities(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		selected   string
		advertised string
		want       bool
	}{
		{name: "exact local identity", provider: "local", selected: "llama3.2", advertised: "llama3.2", want: true},
		{name: "selected local default tag", provider: "local", selected: "llama3.2", advertised: "llama3.2:latest", want: true},
		{name: "advertised local default tag", provider: "local", selected: "llama3.2:latest", advertised: "llama3.2", want: true},
		{name: "different local tag", provider: "local", selected: "llama3.2:7b", advertised: "llama3.2:latest", want: false},
		{name: "malformed double default tag", provider: "local", selected: "llama3.2:latest:latest", advertised: "llama3.2:latest", want: false},
		{name: "non-local default tag", provider: "openrouter", selected: "llama3.2", advertised: "llama3.2:latest", want: false},
		{name: "missing local model", provider: "local", selected: "qwen2.5", advertised: "llama3.2:latest", want: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ModelIDsMatch(testCase.provider, testCase.selected, testCase.advertised); got != testCase.want {
				t.Fatalf("ModelIDsMatch(%q, %q, %q) = %t, want %t", testCase.provider, testCase.selected, testCase.advertised, got, testCase.want)
			}
		})
	}
}

func TestLocalEndpointResolutionPreservesExplicitOrchestrationOverride(t *testing.T) {
	spec := ProviderSpec{Provider: "local", BaseURL: "http://localhost:11434/v1", Mode: ModeOpenAIish}
	ApplyExplicitBaseURLOverride(&spec, "https://remote.example/v1")

	assertExplicitLocalEndpoint(t, spec, "https://remote.example/v1", EndpointSourceOrchestrationOverride)
	if got := spec.EndpointResolution.Candidates; len(got) != 1 || got[0] != "https://remote.example/v1" {
		t.Fatalf("candidates = %#v, want only the explicit endpoint", got)
	}
}

func TestLocalEndpointResolutionUsesCallerContextForSynthesizedDefaults(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_INFERENCE_URL", "")
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	host := ResolveLocalProviderSpecForContext(HostLocalEndpointContext())
	if got := host.EndpointResolution.EffectiveEndpoint; got != "http://localhost:11434/v1" {
		t.Fatalf("host effective endpoint = %q, want localhost default", got)
	}
	if host.EndpointResolution.Source != EndpointSourceBuiltInDefault {
		t.Fatalf("host endpoint source = %q, want built-in/default", host.EndpointResolution.Source)
	}
	if host.EndpointResolution.SynthesisReason == "" {
		t.Fatal("host synthesized endpoint has no synthesis reason")
	}
	if !host.EndpointResolution.Synthesized {
		t.Fatal("host default endpoint is not marked synthesized")
	}

	container := ResolveLocalProviderSpecForContext(ContainerLocalEndpointContext("host.docker.internal", true))
	if got := container.EndpointResolution.EffectiveEndpoint; got != "http://host.docker.internal:11434/v1" {
		t.Fatalf("container effective endpoint = %q, want established host alias", got)
	}
	if container.EndpointResolution.CallerContext.Caller != EndpointCallerContainer {
		t.Fatalf("container caller context = %q, want %q", container.EndpointResolution.CallerContext.Caller, EndpointCallerContainer)
	}
	if !container.EndpointResolution.CallerContext.HostAliasApplicable {
		t.Fatal("container caller context lost the established host-alias fact")
	}

	withoutAlias := ResolveLocalProviderSpecForContext(ContainerLocalEndpointContext("host.docker.internal", false))
	if strings.Contains(withoutAlias.EndpointResolution.EffectiveEndpoint, "host.docker.internal") {
		t.Fatalf("unavailable alias was manufactured: %+v", withoutAlias.EndpointResolution)
	}
	if withoutAlias.ResolutionErr == nil {
		t.Fatal("unavailable container alias produced no resolution error")
	}
	var reachabilityErr *ProviderReachabilityError
	if !errors.As(withoutAlias.ResolutionErr, &reachabilityErr) {
		t.Fatalf("unavailable container alias error = %v, want typed reachability error", withoutAlias.ResolutionErr)
	}
	if reachabilityErr.FailureClass() != ReachabilityFailureConnection {
		t.Fatalf("unavailable container alias failure class = %q, want connection failure", reachabilityErr.FailureClass())
	}
}

func TestLocalEndpointResolutionDoesNotRetryExplicitEndpoint(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_INFERENCE_URL", "http://localhost:11434/v1")
	t.Setenv("LOCAL_MODEL_NAME", "present-model")

	spec := ResolveProviderSpec()
	if got := spec.EndpointResolution.Candidates; len(got) != 1 || got[0] != "http://localhost:11434/v1" {
		t.Fatalf("explicit candidates = %#v, want exactly one endpoint", got)
	}
}

func TestExplicitEndpointSkipsContainerAliasProbe(t *testing.T) {
	clearProviderKeys(t)
	t.Setenv("DEFAULT_LLM_PROVIDER", "local")
	t.Setenv("LOCAL_INFERENCE_URL", "http://localhost:11434/v1")

	originalProbe := localEndpointHostAliasAvailableFn
	t.Cleanup(func() { localEndpointHostAliasAvailableFn = originalProbe })
	probed := false
	localEndpointHostAliasAvailableFn = func(string) bool {
		probed = true
		return false
	}

	resolution := ResolveLocalEndpointForCaller(EndpointCallerContainer)
	if resolution.EffectiveEndpoint != "http://localhost:11434/v1" {
		t.Fatalf("effective endpoint = %q, want exact explicit endpoint", resolution.EffectiveEndpoint)
	}
	if probed {
		t.Fatal("explicit endpoint triggered an unnecessary container alias probe")
	}
}

func TestLocalChatAndModelDiscoveryShareResolvedEndpoint(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"present-model"}]}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	endpoint := server.URL + "/v1"
	client := NewClient(ProviderSpec{
		Provider: "local",
		BaseURL:  endpoint,
		Model:    "present-model",
		Endpoint: "/chat/completions",
		Mode:     ModeOpenAIish,
	})
	if _, err := client.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if _, err := client.Call(context.Background(), []Message{{Role: "user", Content: "hello"}}); err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if got := client.EndpointResolution().Candidates; len(got) != 1 || got[0] != endpoint {
		t.Fatalf("resolved candidates = %#v, want only %q", got, endpoint)
	}
	if got, want := fmt.Sprint(paths), "[/v1/models /v1/chat/completions]"; got != want {
		t.Fatalf("request paths = %s, want %s", got, want)
	}
}

type failingRoundTripper func(*http.Request) error

func (f failingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, f(req)
}

func TestProviderReachabilityDiagnosticsRemainClassifiedAndCredentialSafe(t *testing.T) {
	knownFailures := []struct {
		name  string
		err   error
		class ReachabilityFailureClass
	}{
		{
			name:  "dns",
			err:   &url.Error{Op: "Get", URL: "http://provider.invalid/v1/models", Err: &net.DNSError{Err: "no such host", Name: "provider.invalid"}},
			class: ReachabilityFailureDNS,
		},
		{name: "connection", err: errors.New("dial tcp 127.0.0.1:11434: connect: connection refused"), class: ReachabilityFailureConnection},
		{name: "proxy", err: errors.New("proxyconnect tcp: dial tcp 10.0.0.1:8080: connect: connection refused"), class: ReachabilityFailureProxy},
		{name: "tls", err: errors.New("tls: failed to verify certificate: x509: certificate signed by unknown authority"), class: ReachabilityFailureTLS},
	}

	for _, tc := range knownFailures {
		t.Run(tc.name, func(t *testing.T) {
			client := NewClient(ProviderSpec{Provider: "local", BaseURL: "http://provider.invalid/v1", Model: "present-model", Mode: ModeOpenAIish})
			client.httpClient = &http.Client{Transport: failingRoundTripper(func(*http.Request) error { return tc.err })}

			_, err := client.ListModels(context.Background())
			if err == nil {
				t.Fatal("ListModels() error = nil")
			}
			var reachabilityErr *ProviderReachabilityError
			if !errors.As(err, &reachabilityErr) {
				t.Fatalf("error = %v, want ProviderReachabilityError", err)
			}
			if got := reachabilityErr.FailureClass(); got != tc.class {
				t.Fatalf("failure class = %q, want %q", got, tc.class)
			}
		})
	}

	client := NewClient(ProviderSpec{
		Provider: "local",
		BaseURL:  "http://operator:password@provider.invalid/v1?token=top-secret",
		Model:    "present-model",
		Mode:     ModeOpenAIish,
	})
	client.httpClient = &http.Client{Transport: failingRoundTripper(func(*http.Request) error {
		return errors.New(`Get "http://operator:password@provider.invalid/v1/models?token=top-secret": proxyconnect tcp: connection refused`)
	})}
	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Fatal("credential-bearing request returned nil error")
	}
	for _, secret := range []string{"operator", "password", "top-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("reachability error exposed %q: %v", secret, err)
		}
	}
}

func TestProviderHTTPErrorPrecedesLaterTransportFailure(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "selected model is not available", status)
			}))
			defer first.Close()

			spec := ProviderSpec{
				Provider: "local",
				BaseURL:  first.URL,
				BaseURLs: []string{first.URL, "http://does-not-exist.invalid/v1"},
				Endpoint: "/chat/completions",
				Mode:     ModeOpenAIish,
				Model:    "present-model",
			}
			_, err := NewClient(spec).Call(context.Background(), []Message{{Role: "user", Content: "hello"}})
			if err == nil {
				t.Fatal("Call returned nil error")
			}
			var requestErr *RequestError
			if !errors.As(err, &requestErr) {
				t.Fatalf("error = %v, want RequestError", err)
			}
			if requestErr.StatusCode != status {
				t.Fatalf("status = %d, want %d", requestErr.StatusCode, status)
			}
			if requestErr.FailureClass() != ReachabilityFailureProviderHTTP {
				t.Fatalf("failure class = %q, want provider-http", requestErr.FailureClass())
			}
			if strings.Contains(err.Error(), "does-not-exist.invalid") {
				t.Fatalf("later transport failure masked provider error: %v", err)
			}
		})
	}
}

func TestProviderHTTP407IsClassifiedAsProxyFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{
		Provider: "local",
		BaseURL:  server.URL + "/v1",
		Model:    "present-model",
		Mode:     ModeOpenAIish,
	})
	_, err := client.ListModels(context.Background())
	if err == nil {
		t.Fatal("ListModels() error = nil, want HTTP 407 failure")
	}

	var requestErr *RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("error = %v, want RequestError", err)
	}
	if requestErr.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("status = %d, want %d", requestErr.StatusCode, http.StatusProxyAuthRequired)
	}
	if got := requestErr.FailureClass(); got != ReachabilityFailureProxy {
		t.Fatalf("failure class = %q, want proxy-mediated", got)
	}
}

func assertExplicitLocalEndpoint(t *testing.T, spec ProviderSpec, want string, source EndpointSource) {
	t.Helper()
	if spec.EndpointResolution.ConfiguredEndpoint != want {
		t.Fatalf("configured endpoint = %q, want %q", spec.EndpointResolution.ConfiguredEndpoint, want)
	}
	if spec.EndpointResolution.EffectiveEndpoint != want {
		t.Fatalf("effective endpoint = %q, want %q", spec.EndpointResolution.EffectiveEndpoint, want)
	}
	if spec.EndpointResolution.Source != source {
		t.Fatalf("endpoint source = %q, want %q", spec.EndpointResolution.Source, source)
	}
	if spec.EndpointResolution.Synthesized {
		t.Fatal("explicit endpoint is marked synthesized")
	}
	if len(spec.EndpointResolution.Candidates) != 1 || spec.EndpointResolution.Candidates[0] != want {
		t.Fatalf("endpoint candidates = %#v, want only %q", spec.EndpointResolution.Candidates, want)
	}
}

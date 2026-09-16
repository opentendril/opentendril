package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
)

func TestDefaultPublicIngressLimits(t *testing.T) {
	limits := defaultPublicIngressLimits()
	if limits.maxTCPConnections != 128 {
		t.Errorf("maxTCPConnections = %d, want 128", limits.maxTCPConnections)
	}
	if limits.readHeaderTimeout != 5*time.Second {
		t.Errorf("readHeaderTimeout = %s, want 5s", limits.readHeaderTimeout)
	}
	if limits.idleTimeout != 60*time.Second {
		t.Errorf("idleTimeout = %s, want 60s", limits.idleTimeout)
	}
	if limits.maxHeaderBytes != 32<<10 {
		t.Errorf("maxHeaderBytes = %d, want 32 KiB", limits.maxHeaderBytes)
	}
	if limits.ordinaryBodyBytes != 4<<20 {
		t.Errorf("ordinaryBodyBytes = %d, want 4 MiB", limits.ordinaryBodyBytes)
	}
	if limits.mintBodyBytes != 16<<10 {
		t.Errorf("mintBodyBytes = %d, want 16 KiB", limits.mintBodyBytes)
	}
	if limits.maxRequests != 64 {
		t.Errorf("maxRequests = %d, want 64", limits.maxRequests)
	}
	if limits.maxAuthenticatedAdmissions != 32 {
		t.Errorf("maxAuthenticatedAdmissions = %d, want 32", limits.maxAuthenticatedAdmissions)
	}
	if limits.maxObservations != 16 {
		t.Errorf("maxObservations = %d, want 16", limits.maxObservations)
	}
	if limits.maxMintRequests != 8 {
		t.Errorf("maxMintRequests = %d, want 8", limits.maxMintRequests)
	}
	if limits.mintRatePerSecond != 4 {
		t.Errorf("mintRatePerSecond = %v, want 4", limits.mintRatePerSecond)
	}
	if limits.mintBurst != 8 {
		t.Errorf("mintBurst = %d, want 8", limits.mintBurst)
	}
}

func TestPublicRequestConcurrencyRejectsPromptlyAndReleases(t *testing.T) {
	limits := defaultPublicIngressLimits()
	limits.maxRequests = 2
	ingress := newPublicIngress(limits, nil)
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	finished := make(chan struct{}, 3)
	handler := ingress.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
		finished <- struct{}{}
	}))
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	start := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		go handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
		return recorder
	}
	first := start()
	second := start()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not enter the bounded handler")
		}
	}

	third := httptest.NewRecorder()
	thirdDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(third, httptest.NewRequest(http.MethodGet, "/health", nil))
		close(thirdDone)
	}()
	select {
	case <-thirdDone:
	case <-time.After(time.Second):
		t.Fatal("exhausted public request slot waited instead of failing promptly")
	}
	if third.Code != http.StatusServiceUnavailable {
		t.Fatalf("exhausted request status = %d, want 503", third.Code)
	}

	close(release)
	for range 2 {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("active request did not finish after release")
		}
	}
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code != http.StatusNoContent {
			t.Errorf("released request status = %d, want 204", response.Code)
		}
	}

	fourth := httptest.NewRecorder()
	handler.ServeHTTP(fourth, httptest.NewRequest(http.MethodGet, "/health", nil))
	if fourth.Code != http.StatusNoContent {
		t.Fatalf("request after capacity release status = %d, want 204", fourth.Code)
	}
}

func TestPublicAuthenticatedAdmissionFollowsAuthenticationAndPrecedesBodyRead(t *testing.T) {
	signer, token := newPublicIngressTestAccessToken(t)
	limits := defaultPublicIngressLimits()
	limits.maxRequests = 4
	limits.maxAuthenticatedAdmissions = 1
	ingress := newPublicIngress(limits, nil)
	var accessTokenVerified atomic.Bool
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var coreCalls atomic.Int32
	mux := http.NewServeMux()
	verifier := publicIngressObservedVerifier{AccessTokenVerifier: signer, verified: &accessTokenVerified}
	mux.HandleFunc("POST /v1", ingress.authenticate(verifier, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		coreCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	handler := ingress.wrap(mux)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	firstBody := newPublicIngressTestBody("first body")
	firstBody.authenticated = &accessTokenVerified
	first := httptest.NewRecorder()
	firstRequest := httptest.NewRequest(http.MethodPost, "/v1", firstBody)
	firstRequest.Header.Set("Authorization", "Bearer "+token)
	firstDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(first, firstRequest)
		close(firstDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("valid access token did not enter the governed handler")
	}

	invalidBody := newPublicIngressTestBody("invalid token body")
	invalidRequest := httptest.NewRequest(http.MethodPost, "/v1", invalidBody)
	invalidRequest.Header.Set("Authorization", "Bearer forged-access-token")
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusUnauthorized {
		t.Fatalf("failed authentication status = %d, want 401", invalid.Code)
	}
	if got := invalidBody.reads.Load(); got != 0 {
		t.Fatalf("failed authentication consumed the body: %d reads", got)
	}

	fullBody := newPublicIngressTestBody("valid but capacity is full")
	fullRequest := httptest.NewRequest(http.MethodPost, "/v1", fullBody)
	fullRequest.Header.Set("Authorization", "Bearer "+token)
	full := httptest.NewRecorder()
	handler.ServeHTTP(full, fullRequest)
	if full.Code != http.StatusServiceUnavailable {
		t.Fatalf("full governed admission status = %d, want 503", full.Code)
	}
	if got := fullBody.reads.Load(); got != 0 {
		t.Fatalf("capacity rejection consumed the body: %d reads", got)
	}
	if got := coreCalls.Load(); got != 0 {
		t.Fatalf("Core-equivalent handler calls before active request release = %d, want 0", got)
	}

	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("valid request did not finish after release")
	}
	if first.Code != http.StatusNoContent || coreCalls.Load() != 1 {
		t.Fatalf("released valid request status/calls = %d/%d, want 204/1", first.Code, coreCalls.Load())
	}
	if firstBody.reads.Load() == 0 {
		t.Fatal("valid authenticated request never reached body decoding")
	}
	if firstBody.readBeforeAuthentication.Load() {
		t.Fatal("valid authenticated request body was read before access-token authentication succeeded")
	}
}

func TestPublicObservationConcurrencyIsAdditionalAndReleases(t *testing.T) {
	signer, token := newPublicIngressTestAccessToken(t)
	limits := defaultPublicIngressLimits()
	limits.maxRequests = 4
	limits.maxAuthenticatedAdmissions = 2
	limits.maxObservations = 1
	ingress := newPublicIngress(limits, nil)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var observations atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/phytomers/{sessionId}/watch", ingress.authenticate(signer, func(w http.ResponseWriter, _ *http.Request) {
		observations.Add(1)
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	handler := ingress.wrap(mux)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	start := func(sessionID string) (*httptest.ResponseRecorder, <-chan struct{}) {
		request := httptest.NewRequest(http.MethodGet, "/v1/phytomers/"+sessionID+"/watch", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(recorder, request)
			close(done)
		}()
		return recorder, done
	}
	first, firstDone := start("session-1")
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first authenticated observation did not enter")
	}

	second, secondDone := start("session-2")
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("observation cap rejection did not return promptly")
	}
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("observation cap status = %d, want 503", second.Code)
	}
	if observations.Load() != 1 {
		t.Fatalf("observation handler entries = %d, want 1", observations.Load())
	}

	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodGet, "/v1/phytomers/session-2/watch", nil)
	invalidRequest.Header.Set("Authorization", "Bearer forged-access-token")
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusUnauthorized || observations.Load() != 1 {
		t.Fatalf("invalid observation reached watch handler: status/entries = %d/%d", invalid.Code, observations.Load())
	}

	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first observation did not finish after release")
	}
	if first.Code != http.StatusNoContent {
		t.Fatalf("first observation status = %d, want 204", first.Code)
	}
	third, thirdDone := start("session-3")
	select {
	case <-thirdDone:
	case <-time.After(time.Second):
		t.Fatal("observation after capacity release did not finish")
	}
	if third.Code != http.StatusNoContent || observations.Load() != 2 {
		t.Fatalf("observation after release status/entries = %d/%d, want 204/2", third.Code, observations.Load())
	}
}

func TestPublicMintConcurrencyAndRateAreBounded(t *testing.T) {
	limits := defaultPublicIngressLimits()
	limits.maxRequests = 4
	limits.maxMintRequests = 1
	limits.mintBurst = 20
	limits.mintRatePerSecond = 20
	ingress := newPublicIngress(limits, nil)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pollinator/token", func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	handler := ingress.wrap(mux)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	start := func() (*httptest.ResponseRecorder, <-chan struct{}) {
		recorder := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/pollinator/token", nil))
			close(done)
		}()
		return recorder, done
	}
	first, firstDone := start()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first mint request did not enter")
	}
	second, secondDone := start()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("exhausted mint concurrency waited instead of failing")
	}
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("exhausted mint concurrency status = %d, want 503", second.Code)
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("mint handler did not finish after release")
	}
	if first.Code != http.StatusNoContent {
		t.Fatalf("first mint status = %d, want 204", first.Code)
	}

	current := time.Unix(100, 0)
	rateLimits := defaultPublicIngressLimits()
	rateLimits.maxRequests = 4
	rateLimits.maxMintRequests = 2
	rateLimits.mintRatePerSecond = 1
	rateLimits.mintBurst = 1
	rateIngress := newPublicIngress(rateLimits, func() time.Time { return current })
	rateMux := http.NewServeMux()
	rateCalls := atomic.Int32{}
	rateMux.HandleFunc("POST /v1/pollinator/token", func(w http.ResponseWriter, _ *http.Request) {
		rateCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	rateHandler := rateIngress.wrap(rateMux)
	request := func(headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/pollinator/token", nil)
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		rec := httptest.NewRecorder()
		rateHandler.ServeHTTP(rec, req)
		return rec
	}
	if got := request(map[string]string{"X-Forwarded-For": "192.0.2.1"}).Code; got != http.StatusNoContent {
		t.Fatalf("initial burst request status = %d, want 204", got)
	}
	if got := request(map[string]string{
		"Forwarded":         "for=127.0.0.1;host=localhost;proto=http",
		"X-Forwarded-For":   "127.0.0.1",
		"X-Forwarded-Host":  "localhost",
		"X-Forwarded-Proto": "http",
	}).Code; got != http.StatusTooManyRequests {
		t.Fatalf("forwarded-header mint pressure status = %d, want 429", got)
	}
	if rateCalls.Load() != 1 {
		t.Fatalf("rate-limited handler calls = %d, want 1", rateCalls.Load())
	}
	current = current.Add(time.Second)
	if got := request(map[string]string{"X-Forwarded-For": "203.0.113.77"}).Code; got != http.StatusNoContent {
		t.Fatalf("refilled global rate request status = %d, want 204", got)
	}
	if rateCalls.Load() != 2 {
		t.Fatalf("refilled mint handler calls = %d, want 2", rateCalls.Load())
	}
}

func TestPublicMintRateBurstAndRefillAreDeterministic(t *testing.T) {
	limits := defaultPublicIngressLimits()
	current := time.Unix(200, 0)
	ingress := newPublicIngress(limits, func() time.Time { return current })
	for i := 0; i < 8; i++ {
		if !ingress.mintRate.allow() {
			t.Fatalf("initial burst request %d was refused", i+1)
		}
	}
	if ingress.mintRate.allow() {
		t.Fatal("request beyond the configured initial burst was allowed")
	}
	current = current.Add(250 * time.Millisecond)
	if !ingress.mintRate.allow() {
		t.Fatal("one request did not refill after 250ms at 4 requests/second")
	}
	if ingress.mintRate.allow() {
		t.Fatal("rate limiter allowed more than one refilled request")
	}
}

func TestPublicBodyLimitsRejectOversizeAndAllowConfiguredBoundary(t *testing.T) {
	limits := defaultPublicIngressLimits()
	limits.ordinaryBodyBytes = 4
	limits.mintBodyBytes = 3
	ingress := newPublicIngress(limits, nil)
	var ordinaryCalls, mintCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1", func(w http.ResponseWriter, r *http.Request) {
		ordinaryCalls.Add(1)
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "bounded body", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v1/pollinator/token", func(w http.ResponseWriter, r *http.Request) {
		mintCalls.Add(1)
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "bounded mint body", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := ingress.wrap(mux)
	request := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if got := request("/v1", "1234").Code; got != http.StatusNoContent {
		t.Fatalf("ordinary body at limit status = %d, want 204", got)
	}
	if got := request("/v1", "12345").Code; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized ordinary body status = %d, want 413", got)
	}
	if got := ordinaryCalls.Load(); got != 1 {
		t.Fatalf("ordinary handler calls = %d, want 1", got)
	}
	if got := request("/v1/pollinator/token", "123").Code; got != http.StatusNoContent {
		t.Fatalf("mint body at limit status = %d, want 204", got)
	}
	if got := request("/v1/pollinator/token", "1234").Code; got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized mint body status = %d, want 413", got)
	}
	if got := mintCalls.Load(); got != 1 {
		t.Fatalf("mint handler calls = %d, want 1", got)
	}
}

func TestPublicChunkedBodyLimitUsesBoundedReader(t *testing.T) {
	limits := defaultPublicIngressLimits()
	limits.ordinaryBodyBytes = 4
	ingress := newPublicIngress(limits, nil)
	var coreCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			if _, ok := err.(*http.MaxBytesError); ok {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		coreCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	handler := ingress.wrap(mux)
	request := httptest.NewRequest(http.MethodPost, "/v1", strings.NewReader("12345"))
	request.ContentLength = -1
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversized body status = %d, want 413", response.Code)
	}
	if coreCalls.Load() != 0 {
		t.Fatalf("Core-equivalent handler calls = %d, want 0", coreCalls.Load())
	}
}

func TestRemoteIngressResourceRejectionsDoNotChangeMintAuthentication(t *testing.T) {
	fixture := newRemoteMuxTestFixture(t, "", nil)
	limits := defaultPublicIngressLimits()
	limits.maxRequests = 8
	limits.maxMintRequests = 8
	limits.mintBurst = 2
	limits.mintRatePerSecond = 1
	current := time.Unix(300, 0)
	handler := buildRemoteServeHandlerWithLimits(fixture.deps, limits, func() time.Time { return current })
	request := func(bearer string, forwarded string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/pollinator/token", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
			req.Header.Set("Forwarded", "for="+forwarded)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if got := request("botanist-key", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("Botanist mint status = %d, want 401", got)
	}
	if got := request(fixture.root, "").Code; got != http.StatusOK {
		t.Fatalf("durable-root mint status = %d, want 200", got)
	}
	if got := request(fixture.root, "127.0.0.1").Code; got != http.StatusTooManyRequests {
		t.Fatalf("forwarded root request after burst status = %d, want 429", got)
	}
	current = current.Add(time.Second)
	if got := request(fixture.root, "203.0.113.4").Code; got != http.StatusOK {
		t.Fatalf("durable-root mint after rate refill status = %d, want 200", got)
	}
}

func newPublicIngressTestAccessToken(t *testing.T) (*core.StemSigner, string) {
	t.Helper()
	signer, err := core.LoadOrCreateStemSigner(t.TempDir())
	if err != nil {
		t.Fatalf("create Stem signer: %v", err)
	}
	token, err := signer.MintAccessToken("claude", time.Minute, core.AccessTokenScope{})
	if err != nil {
		t.Fatalf("mint public test access token: %v", err)
	}
	return signer, token
}

type publicIngressTestBody struct {
	reader                   io.Reader
	reads                    atomic.Int32
	authenticated            *atomic.Bool
	readBeforeAuthentication atomic.Bool
}

func newPublicIngressTestBody(body string) *publicIngressTestBody {
	return &publicIngressTestBody{reader: strings.NewReader(body)}
}

func (b *publicIngressTestBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	if b.authenticated != nil && !b.authenticated.Load() {
		b.readBeforeAuthentication.Store(true)
	}
	return b.reader.Read(p)
}

func (b *publicIngressTestBody) Close() error { return nil }

type publicIngressObservedVerifier struct {
	receptors.AccessTokenVerifier
	verified *atomic.Bool
}

func (v publicIngressObservedVerifier) VerifyAccessToken(token string) (core.AccessTokenClaims, bool) {
	claims, ok := v.AccessTokenVerifier.VerifyAccessToken(token)
	if ok {
		v.verified.Store(true)
	}
	return claims, ok
}

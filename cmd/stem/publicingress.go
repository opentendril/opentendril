package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/receptors"
)

const (
	publicPollinatorTokenPath  = "/v1/pollinator/token"
	publicPhytomerWatchPattern = "GET /v1/phytomers/{sessionId}/watch"
)

// publicIngressLimits keeps resource governance at the public transport
// boundary. It does not carry Pollinator, grant, or capability authority.
type publicIngressLimits struct {
	maxTCPConnections          int
	readHeaderTimeout          time.Duration
	idleTimeout                time.Duration
	maxHeaderBytes             int
	ordinaryBodyBytes          int64
	mintBodyBytes              int64
	maxRequests                int
	maxAuthenticatedAdmissions int
	maxObservations            int
	maxMintRequests            int
	mintRatePerSecond          float64
	mintBurst                  int
}

func defaultPublicIngressLimits() publicIngressLimits {
	return publicIngressLimits{
		maxTCPConnections:          128,
		readHeaderTimeout:          5 * time.Second,
		idleTimeout:                60 * time.Second,
		maxHeaderBytes:             32 << 10,
		ordinaryBodyBytes:          4 << 20,
		mintBodyBytes:              16 << 10,
		maxRequests:                64,
		maxAuthenticatedAdmissions: 32,
		maxObservations:            16,
		maxMintRequests:            8,
		mintRatePerSecond:          4,
		mintBurst:                  8,
	}
}

type publicIngress struct {
	limits       publicIngressLimits
	requests     ingressSlots
	admissions   ingressSlots
	observations ingressSlots
	mints        ingressSlots
	mintRate     *publicMintRateLimiter
}

func newPublicIngress(limits publicIngressLimits, now func() time.Time) *publicIngress {
	return &publicIngress{
		limits:       limits,
		requests:     newIngressSlots(limits.maxRequests),
		admissions:   newIngressSlots(limits.maxAuthenticatedAdmissions),
		observations: newIngressSlots(limits.maxObservations),
		mints:        newIngressSlots(limits.maxMintRequests),
		mintRate:     newPublicMintRateLimiter(limits.mintRatePerSecond, limits.mintBurst, now),
	}
}

// wrap bounds all public requests and body sizes. MaxBytesReader is installed
// without consuming the request, leaving the route's authentication middleware
// to run before any decoder reads an ordinary governed body.
func (p *publicIngress) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		releaseRequest, ok := p.requests.tryAcquire()
		if !ok {
			http.Error(w, "public request capacity exhausted", http.StatusServiceUnavailable)
			return
		}
		defer releaseRequest()

		isMint := r.Method == http.MethodPost && r.URL.Path == publicPollinatorTokenPath
		if isMint {
			releaseMint, ok := p.mints.tryAcquire()
			if !ok {
				http.Error(w, "public token mint capacity exhausted", http.StatusServiceUnavailable)
				return
			}
			defer releaseMint()
			if !p.mintRate.allow() {
				http.Error(w, "public token mint rate exceeded", http.StatusTooManyRequests)
				return
			}
		}

		maxBodyBytes := p.limits.ordinaryBodyBytes
		if isMint {
			maxBodyBytes = p.limits.mintBodyBytes
		}
		if r.Body != nil {
			if r.ContentLength > maxBodyBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		if isMint {
			cleanupBody, status := spoolUnknownLengthPublicBody(r)
			if status != 0 {
				writePublicBodyReadError(w, status)
				return
			}
			if cleanupBody != nil {
				defer cleanupBody()
			}
		}

		next.ServeHTTP(w, r)
	})
}

// authenticate acquires governed admission only after the existing short-lived
// access-token authenticator has proven a Pollen. A REST watch then takes its
// additional observation slot before entering WatchAuthority or Core.
func (p *publicIngress) authenticate(verifier receptors.AccessTokenVerifier, next http.HandlerFunc) http.HandlerFunc {
	return withPollinatorAccessTokenAuth(verifier, func(w http.ResponseWriter, r *http.Request) {
		releaseAdmission, ok := p.admissions.tryAcquire()
		if !ok {
			http.Error(w, "public governed admission capacity exhausted", http.StatusServiceUnavailable)
			return
		}
		defer releaseAdmission()

		if r.Pattern == publicPhytomerWatchPattern {
			releaseObservation, ok := p.observations.tryAcquire()
			if !ok {
				http.Error(w, "public observation capacity exhausted", http.StatusServiceUnavailable)
				return
			}
			defer releaseObservation()
		}
		cleanupBody, status := spoolUnknownLengthPublicBody(r)
		if status != 0 {
			writePublicBodyReadError(w, status)
			return
		}
		if cleanupBody != nil {
			defer cleanupBody()
		}

		next(w, r)
	})
}

// spoolUnknownLengthPublicBody consumes unknown-length bodies through the
// already-installed MaxBytesReader before JSON decoders or governed handlers
// can accept a valid prefix and leave an oversized chunked tail unread. The
// bounded body is staged to a private temporary file rather than retained in
// memory. Known-length bodies are already rejected by their declared size or
// bounded by MaxBytesReader while read.
func spoolUnknownLengthPublicBody(r *http.Request) (cleanup func(), status int) {
	if r == nil || r.Body == nil || r.ContentLength >= 0 {
		return nil, 0
	}

	body := r.Body
	file, err := os.CreateTemp("", "opentendril-public-body-*")
	if err != nil {
		_ = body.Close()
		return nil, http.StatusServiceUnavailable
	}
	removePartial := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
		_ = body.Close()
	}

	buffer := make([]byte, 32<<10)
	noProgress := 0
	for {
		read, readErr := body.Read(buffer)
		if read > 0 {
			noProgress = 0
			if _, err := file.Write(buffer[:read]); err != nil {
				removePartial()
				return nil, http.StatusServiceUnavailable
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				removePartial()
				var maxBytesError *http.MaxBytesError
				if errors.As(readErr, &maxBytesError) {
					return nil, http.StatusRequestEntityTooLarge
				}
				return nil, http.StatusBadRequest
			}
			break
		}
		if read == 0 {
			noProgress++
			if noProgress >= 100 {
				removePartial()
				return nil, http.StatusBadRequest
			}
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		removePartial()
		return nil, http.StatusServiceUnavailable
	}
	_ = body.Close()
	temporaryBody := &publicTemporaryRequestBody{file: file, path: file.Name()}
	r.Body = temporaryBody
	return func() { _ = temporaryBody.Close() }, 0
}

func writePublicBodyReadError(w http.ResponseWriter, status int) {
	switch status {
	case http.StatusRequestEntityTooLarge:
		http.Error(w, "request body too large", status)
	case http.StatusServiceUnavailable:
		http.Error(w, "public request body could not be staged", status)
	default:
		http.Error(w, "invalid request body", http.StatusBadRequest)
	}
}

type publicTemporaryRequestBody struct {
	file      *os.File
	path      string
	closeOnce sync.Once
	closeErr  error
}

func (b *publicTemporaryRequestBody) Read(p []byte) (int, error) {
	return b.file.Read(p)
}

func (b *publicTemporaryRequestBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.file.Close()
		if err := os.Remove(b.path); err != nil && !os.IsNotExist(err) && b.closeErr == nil {
			b.closeErr = err
		}
	})
	return b.closeErr
}

type ingressSlots struct {
	active chan struct{}
}

func newIngressSlots(limit int) ingressSlots {
	if limit < 0 {
		limit = 0
	}
	return ingressSlots{active: make(chan struct{}, limit)}
}

func (s ingressSlots) tryAcquire() (func(), bool) {
	select {
	case s.active <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-s.active })
		}, true
	default:
		return nil, false
	}
}

type publicMintRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	tokens  float64
	last    time.Time
	nowFunc func() time.Time
}

func newPublicMintRateLimiter(ratePerSecond float64, burst int, now func() time.Time) *publicMintRateLimiter {
	if now == nil {
		now = time.Now
	}
	currentTime := now()
	return &publicMintRateLimiter{
		rate:    ratePerSecond,
		burst:   float64(burst),
		tokens:  float64(burst),
		last:    currentTime,
		nowFunc: now,
	}
}

func (l *publicMintRateLimiter) allow() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.nowFunc()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

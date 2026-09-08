package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// stemHTTPError is a typed error produced when the local Stem daemon responds
// with a non-2xx HTTP status. It carries the exact status code and safe
// response body text so callers can distinguish an HTTP rejection from a
// transport/unreachable failure without substring matching.
type stemHTTPError struct {
	StatusCode int
	Body       string // trimmed response body; may be the status text if the body was empty
}

func (e *stemHTTPError) Error() string {
	return fmt.Sprintf("Stem daemon rejected the request (status %d): %s", e.StatusCode, e.Body)
}

// newStemHTTPError reads body text from raw, trims whitespace, and falls back
// to the HTTP status text when the body is empty.
func newStemHTTPError(statusCode int, raw []byte, statusText string) *stemHTTPError {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		text = statusText
	}
	return &stemHTTPError{StatusCode: statusCode, Body: text}
}

// localStemClient is a typed, authenticated HTTP client scoped to the local
// Stem daemon. It is the single place that knows how to reach the daemon,
// resolve the bearer, and decode the governed response contracts.
//
// All daemon-backed CLI operations (detached Seed dispatch, Seed collection,
// Phytomer continuation, Phytomer watch) must route through this client so
// endpoint resolution and credential handling cannot diverge across commands.
type localStemClient struct {
	port   string // resolved once, never changes during a CLI invocation
	bearer string // resolved once; empty means no credential is available
}

// newLocalStemClient resolves the target port and bearer key for the local
// Stem daemon and returns a ready-to-use client.
//
// Port resolution: PORT environment variable wins; default is 8080.
// Bearer resolution: BOTANIST_KEY wins; then ./.tendril/api-key; never
// fabricated. An empty bearer means no credential could be resolved; the
// client still sends requests (an unauthenticated Stem is an operator's
// misconfiguration, not a reason to refuse the request here).
func newLocalStemClient() *localStemClient {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	return &localStemClient{
		port:   port,
		bearer: resolveLocalBearer(),
	}
}

// resolveLocalBearer resolves the bearer credential for local Stem requests.
// BOTANIST_KEY takes precedence over the persisted ./.tendril/api-key file.
// It never fabricates a credential; an absent credential returns "".
func resolveLocalBearer() string {
	if key := strings.TrimSpace(os.Getenv(EnvBotanistKey)); key != "" {
		return key
	}
	return readPersistedAPIKey("./.tendril")
}

// baseURL returns the full http://localhost:<port> base URL for the daemon.
func (c *localStemClient) baseURL() string {
	return fmt.Sprintf("http://localhost:%s", c.port)
}

// do issues an authenticated HTTP request to the local Stem daemon.
// body may be nil for GET requests. It returns the raw response for callers
// to decode; callers are responsible for closing resp.Body.
func (c *localStemClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	return http.DefaultClient.Do(req)
}

// SeedDispatchResult is the decoded response from a detached Seed dispatch.
// It carries exactly the fields the canonical /v1/seeds/grow response
// returns for a detached=true request.
type SeedDispatchResult struct {
	Handle     string `json:"handle"`
	PhytomerID string `json:"phytomerId"`
	Status     string `json:"status"`
}

// SeedCollectResult is the decoded Fruit from a /v1/seeds/runs/{handle}
// response. It maps the public Fruit fields returned by tendril seed collect.
type SeedCollectResult struct {
	Status     string `json:"status"`
	Iterations int    `json:"iterations"`
	PhytomerID string `json:"phytomerId"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	Diff       string `json:"diff"`
	Logs       string `json:"logs"`
}

// DispatchSeed posts a detached canonical Seed-grow request to
// POST /v1/seeds/grow with detached:true set in the payload. The supplied
// input map provides the Seed fields (substrate, goal, verify, etc.); this
// method adds the detached flag. It does not accept nor inject raw intent,
// reasoning, credentials, or other non-Seed fields.
//
// A non-202 status is returned as an error with the status code included.
func (c *localStemClient) DispatchSeed(ctx context.Context, input map[string]any) (SeedDispatchResult, error) {
	body := map[string]any{}
	for _, key := range []string{"substrate", "goal", "verify", "maxIterations", "timeoutSeconds", "origin"} {
		if v, ok := input[key]; ok {
			body[key] = v
		}
	}
	body["detached"] = true

	payload, err := json.Marshal(body)
	if err != nil {
		return SeedDispatchResult{}, fmt.Errorf("encode seed dispatch request: %w", err)
	}

	resp, err := c.do(ctx, http.MethodPost, "/v1/seeds/grow", payload)
	if err != nil {
		return SeedDispatchResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		return SeedDispatchResult{}, newStemHTTPError(resp.StatusCode, raw, resp.Status)
	}

	var result SeedDispatchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return SeedDispatchResult{}, fmt.Errorf("decode seed dispatch response: %w", err)
	}
	return result, nil
}

// CollectSeed fetches the reviewable Fruit for a dispatched Seed by handle
// via GET /v1/seeds/runs/{handle}. The decoded SeedCollectResult carries
// only the public Fruit contract fields.
//
// Every non-2xx HTTP response — including 404 — is returned as *stemHTTPError
// carrying the exact status code and safe response body. Callers classify by
// typed error and StatusCode; no substring matching is required.
func (c *localStemClient) CollectSeed(ctx context.Context, handle string) (SeedCollectResult, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/seeds/runs/"+url.PathEscape(handle), nil)
	if err != nil {
		return SeedCollectResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SeedCollectResult{}, newStemHTTPError(resp.StatusCode, raw, resp.Status)
	}

	var result SeedCollectResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return SeedCollectResult{}, fmt.Errorf("decode collect response: %w", err)
	}
	return result, nil
}

// ContinuePhytomer posts continued intent to the running Stem daemon via
// POST /v1/phytomers/{id}/continue. The caller supplies the exact
// idempotency key; this method preserves it without modification.
//
// A non-2xx response is returned as an error that includes the status code
// and the body text so the caller can surface a meaningful error.
func (c *localStemClient) ContinuePhytomer(ctx context.Context, phytomerID, intent, idempotencyKey string) (core.ContinuationResult, error) {
	payload, err := json.Marshal(map[string]any{
		"intent":         intent,
		"idempotencyKey": idempotencyKey,
	})
	if err != nil {
		return core.ContinuationResult{}, err
	}

	path := "/v1/phytomers/" + url.PathEscape(phytomerID) + "/continue"
	resp, err := c.do(ctx, http.MethodPost, path, payload)
	if err != nil {
		return core.ContinuationResult{}, fmt.Errorf("Stem daemon is unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return core.ContinuationResult{}, newStemHTTPError(resp.StatusCode, raw, resp.Status)
	}

	var result core.ContinuationResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return core.ContinuationResult{}, fmt.Errorf("decode continuation result: %w", err)
	}
	return result, nil
}

// WatchPhytomer opens a Server-Sent Events stream for
// GET /v1/phytomers/{id}/watch and decodes each "observation" event into a
// core.PhytomerObservation, passing it to the supplied callback. The stream
// is consumed until the context is cancelled, the server closes the
// connection, or the callback returns a non-nil error.
//
// Only "observation"-typed SSE events are decoded; "error"-typed events and
// any other event types cause WatchPhytomer to return with a descriptive
// error. Raw transcript, raw continued intent, reasoning, credentials, and
// other non-observation fields are not part of the decoded contract; Core
// owns that projection at the source.
func (c *localStemClient) WatchPhytomer(ctx context.Context, phytomerID string, onObservation func(core.PhytomerObservation) error) error {
	path := "/v1/phytomers/" + url.PathEscape(phytomerID) + "/watch"
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("Stem daemon is unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		text := strings.TrimSpace(string(raw))
		if text == "" {
			text = resp.Status
		}
		return fmt.Errorf("watch request rejected (status %d): %s", resp.StatusCode, text)
	}

	return decodeSSEObservations(resp.Body, onObservation)
}

// decodeSSEObservations reads raw SSE frames from r and calls onObservation
// for each "observation"-typed event. An "error"-typed event terminates the
// stream with a descriptive error. Any other event type is skipped silently.
// A malformed JSON data payload returns an error. If onObservation itself
// returns an error the stream is terminated with that error.
func decodeSSEObservations(r io.Reader, onObservation func(core.PhytomerObservation) error) error {
	scanner := bufio.NewScanner(r)
	var currentEvent string
	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "event:"):
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			// Blank line: dispatch the accumulated event, then reset.
			if len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				if err := handleSSEEvent(currentEvent, data, onObservation); err != nil {
					return err
				}
			}
			currentEvent = ""
			dataLines = dataLines[:0]
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading SSE stream: %w", err)
	}
	return nil
}

// handleSSEEvent dispatches one complete SSE event. It returns a non-nil
// error for "error"-typed server events, malformed JSON, and onObservation
// errors. Unknown event types are silently skipped.
func handleSSEEvent(event, data string, onObservation func(core.PhytomerObservation) error) error {
	switch event {
	case "observation":
		var obs core.PhytomerObservation
		if err := json.Unmarshal([]byte(data), &obs); err != nil {
			return fmt.Errorf("decode SSE observation: %w", err)
		}
		return onObservation(obs)
	case "error":
		return fmt.Errorf("watch stream closed by server: %s", data)
	}
	// Unknown event type: skip.
	return nil
}

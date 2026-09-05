package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// stemDaemonRequest issues an authenticated request to the local Stem daemon —
// the same bearer-authenticated client path detached seed dispatch, seed
// collect, and phytomer continue use. Those commands must run on the long-lived
// serve process that owns active executors, not in a one-shot CLI Core.
func stemDaemonRequest(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://localhost:%s%s", port, path), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key := strings.TrimSpace(os.Getenv(EnvBotanistKey)); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return http.DefaultClient.Do(req)
}

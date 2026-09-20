package conductor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObserveFruitReviewRetainsExactCommitAndHeadEvidence(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer fruit-test-token" {
			t.Fatalf("authorization = %q, want bearer token", r.Header.Get("Authorization"))
		}
		switch {
		case strings.Contains(r.URL.Path, "/commits/merged-commit/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 101, "state": "closed", "merged_at": "2026-09-20T00:00:00Z", "head": map[string]any{"sha": "branch-tip"}}})
		case strings.HasSuffix(r.URL.Path, "/pulls") && r.URL.Query().Get("head") != "":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case strings.Contains(r.URL.Path, "/commits/closed-commit/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case strings.Contains(r.URL.Path, "/commits/reused-commit/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	defer RedirectGitHubAPIBaseURL(server.URL)()

	credential := ResolvedCredential{Method: CredentialPAT, TokenValue: "fruit-test-token"}
	merged, err := ObserveFruitReview(context.Background(), "github.com/org/repo", "review/merged", "merged-commit", credential)
	if err != nil {
		t.Fatalf("merged review: %v", err)
	}
	if len(merged.Records) != 1 || !merged.Records[0].CommitLookup || !merged.Records[0].Merged {
		t.Fatalf("merged evidence = %+v, want exact merged record", merged.Records)
	}

	closed, err := ObserveFruitReview(context.Background(), "github.com/org/repo", "review/closed", "closed-commit", credential)
	if err != nil {
		t.Fatalf("closed review: %v", err)
	}
	// This server returns no branch PR for the closed case; the next test below
	// exercises the branch-head path with an exact SHA.
	if len(closed.Records) != 0 {
		t.Fatalf("closed evidence = %+v, want no convenient commit association", closed.Records)
	}
	if len(requests) < 4 {
		t.Fatalf("review requests = %v, want exact-commit and head lookups", requests)
	}
}

func TestObserveFruitReviewRetainsExactFactsWhenHeadLookupFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits/merged-commit/pulls") {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 202, "state": "closed", "merged_at": "2026-09-20T00:00:00Z", "head": map[string]any{"sha": "branch-tip"}}})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	defer RedirectGitHubAPIBaseURL(server.URL)()

	evidence, err := ObserveFruitReview(context.Background(), "github.com/org/repo", "review/merged", "merged-commit", ResolvedCredential{Method: CredentialPAT, TokenValue: "token"})
	if err == nil {
		t.Fatal("ObserveFruitReview error = nil, want head lookup failure")
	}
	if len(evidence.Records) != 1 || !evidence.Records[0].CommitLookup || !evidence.Records[0].Merged {
		t.Fatalf("partial evidence = %+v, want exact merged facts", evidence)
	}
}

func TestObserveFruitReviewClosedBranchRetainsHeadSHA(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/commits/closed-commit/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/pulls") && r.URL.Query().Get("head") != "":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 303, "state": "closed", "merged_at": nil, "head": map[string]any{"sha": "closed-commit"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	defer RedirectGitHubAPIBaseURL(server.URL)()

	evidence, err := ObserveFruitReview(context.Background(), "github.com/org/repo", "review/closed", "closed-commit", ResolvedCredential{Method: CredentialPAT, TokenValue: "token"})
	if err != nil {
		t.Fatalf("closed review: %v", err)
	}
	if len(evidence.Records) != 1 || evidence.Records[0].Number != 303 || evidence.Records[0].HeadCommit != "closed-commit" || evidence.Records[0].CommitLookup {
		t.Fatalf("closed evidence = %+v, want exact branch-head evidence", evidence.Records)
	}
}

func TestObserveFruitReviewReusedBranchNameRetainsNonMatchingHead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/commits/new-commit/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/pulls") && r.URL.Query().Get("head") != "":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 404, "state": "closed", "merged_at": nil, "head": map[string]any{"sha": "old-commit"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	defer RedirectGitHubAPIBaseURL(server.URL)()

	evidence, err := ObserveFruitReview(context.Background(), "github.com/org/repo", "review/reused", "new-commit", ResolvedCredential{Method: CredentialPAT, TokenValue: "token"})
	if err != nil {
		t.Fatalf("reused branch review: %v", err)
	}
	if len(evidence.Records) != 1 || evidence.Records[0].HeadCommit != "old-commit" {
		t.Fatalf("reused branch evidence = %+v, want non-matching head retained", evidence.Records)
	}
}

func TestObserveFruitReviewMarksConflictingDuplicatePRStateAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/commits/conflict-commit/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 505, "state": "closed", "merged_at": "2026-09-20T00:00:00Z", "head": map[string]any{"sha": "branch-tip"}}})
		case strings.HasSuffix(r.URL.Path, "/pulls") && r.URL.Query().Get("head") != "":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 505, "state": "open", "merged_at": nil, "head": map[string]any{"sha": "branch-tip"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	defer RedirectGitHubAPIBaseURL(server.URL)()

	evidence, err := ObserveFruitReview(context.Background(), "github.com/org/repo", "review/conflict", "conflict-commit", ResolvedCredential{Method: CredentialPAT, TokenValue: "token"})
	if err != nil {
		t.Fatalf("conflicting review: %v", err)
	}
	if !evidence.Ambiguous {
		t.Fatalf("evidence = %+v, want ambiguous duplicate PR state", evidence)
	}
}

func TestObserveFruitReviewPaginatesHistoricalHeads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/commits/paged-commit/pulls") {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/pulls") && r.URL.Query().Get("head") != "" {
			if r.URL.Query().Get("page") == "1" {
				pulls := make([]map[string]any, 100)
				for index := range pulls {
					pulls[index] = map[string]any{"number": index + 1, "state": "closed", "head": map[string]any{"sha": "old-commit"}}
				}
				_ = json.NewEncoder(w).Encode(pulls)
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 606, "state": "closed", "head": map[string]any{"sha": "paged-commit"}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	defer RedirectGitHubAPIBaseURL(server.URL)()

	evidence, err := ObserveFruitReview(context.Background(), "github.com/org/repo", "review/paged", "paged-commit", ResolvedCredential{Method: CredentialPAT, TokenValue: "token"})
	if err != nil {
		t.Fatalf("paged review: %v", err)
	}
	if len(evidence.Records) != 101 {
		t.Fatalf("records = %d, want 101 pages of review evidence", len(evidence.Records))
	}
	if evidence.Records[len(evidence.Records)-1].Number != 606 || evidence.Records[len(evidence.Records)-1].HeadCommit != "paged-commit" {
		t.Fatalf("last record = %+v, want exact paged head", evidence.Records[len(evidence.Records)-1])
	}
}

func TestObserveFruitRemoteRefDistinguishesExactAndMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/org/repo" {
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "org/repo"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/git/ref/heads/review/exact") {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": "exact-commit"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	defer RedirectGitHubAPIBaseURL(server.URL)()

	credential := ResolvedCredential{Method: CredentialPAT, TokenValue: "token"}
	exact, err := ObserveFruitRemoteRef(context.Background(), "github.com/org/repo", "review/exact", credential)
	if err != nil || !exact.Exists || exact.Commit != "exact-commit" {
		t.Fatalf("exact ref = %+v, err=%v", exact, err)
	}
	missing, err := ObserveFruitRemoteRef(context.Background(), "github.com/org/repo", "review/missing", credential)
	if err != nil || missing.Exists || missing.Commit != "" {
		t.Fatalf("missing ref = %+v, err=%v", missing, err)
	}
}

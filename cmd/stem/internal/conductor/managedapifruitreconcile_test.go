package conductor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	reconcileBaseOID    = "base-oid"
	reconcileFruitOID   = "fruit-oid"
	reconcileMainOID    = "main-oid"
	reconcileDefaultRef = "main"
	reconcileBranch     = "tendril/reconcile"
	reconcileRequestID  = "1C73:26D4:E2E500:1EF78F4:62EC2479"
)

type reconcileMutation struct {
	response string
	state    string
	oid      string
}

type reconcileFruitFake struct {
	createRefStatus int
	createRefState  string
	refState        string
	mutations       []reconcileMutation
	badFruitContent bool
	extraFruitPath  bool
	readFailure     string
	requestID       string
	defaultBranch   string

	createRefCalls       int
	graphQLCalls         int
	readCalls            int
	defaultOID           string
	defaultBranchTouched bool
	createRefBranches    []string
	mutationBranches     []string
	expectedHeads        []string
}

func (f *reconcileFruitFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "/access_tokens") && r.Method == http.MethodPost {
		respondJSON(w, map[string]any{
			"token":      "ghs_reconcile_token",
			"expires_at": "2099-01-01T00:00:00Z",
		})
		return
	}

	if strings.HasSuffix(r.URL.Path, "/git/refs") && r.Method == http.MethodPost {
		f.createRefCalls++
		var request struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		}
		createRefTargetsDefault := false
		if err := json.NewDecoder(r.Body).Decode(&request); err == nil {
			branch := strings.TrimPrefix(request.Ref, "refs/heads/")
			f.createRefBranches = append(f.createRefBranches, branch)
			if branch == f.defaultBranch {
				f.defaultBranchTouched = true
				createRefTargetsDefault = true
			}
		}
		if f.createRefState != "" {
			f.refState = f.createRefState
		}
		status := f.createRefStatus
		if status == 0 {
			status = http.StatusCreated
		}
		if createRefTargetsDefault && status >= 200 && status < 300 {
			f.defaultOID = request.SHA
		}
		if status < 200 || status >= 300 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "Authorization: Bearer upstream-secret-content")
			return
		}
		w.WriteHeader(status)
		return
	}

	if r.URL.Path == "/graphql" && r.Method == http.MethodPost {
		f.graphQLCalls++
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Variables struct {
				Input struct {
					ExpectedHeadOID string `json:"expectedHeadOid"`
					Branch          struct {
						BranchName string `json:"branchName"`
					} `json:"branch"`
				} `json:"input"`
			} `json:"variables"`
		}
		if err := json.Unmarshal(body, &request); err == nil {
			f.expectedHeads = append(f.expectedHeads, request.Variables.Input.ExpectedHeadOID)
			branch := request.Variables.Input.Branch.BranchName
			f.mutationBranches = append(f.mutationBranches, branch)
			if branch == f.defaultBranch {
				f.defaultBranchTouched = true
				f.defaultOID = "default-branch-mutated"
			}
		}

		mutation := reconcileMutation{response: "success", state: "exact", oid: reconcileFruitOID}
		if index := f.graphQLCalls - 1; index < len(f.mutations) {
			mutation = f.mutations[index]
		}
		if mutation.state != "" {
			f.refState = mutation.state
		}
		if mutation.oid == "" {
			mutation.oid = reconcileFruitOID
		}
		switch mutation.response {
		case "lost":
			if hijacker, ok := w.(http.Hijacker); ok {
				connection, _, err := hijacker.Hijack()
				if err == nil {
					_ = connection.Close()
					return
				}
			}
			http.Error(w, "connection could not be closed", http.StatusInternalServerError)
		case "error":
			requestID := f.requestID
			if requestID == "" {
				requestID = fmt.Sprintf("req-%d", f.graphQLCalls)
			}
			w.Header().Set("X-GitHub-Request-Id", requestID)
			respondJSON(w, map[string]any{
				"errors": []map[string]any{{"message": "upstream-secret-content"}},
			})
		case "partial":
			respondJSON(w, map[string]any{"data": map[string]any{}})
		default:
			respondJSON(w, map[string]any{
				"data": map[string]any{
					"createCommitOnBranch": map[string]any{
						"commit": map[string]any{"oid": mutation.oid},
					},
				},
			})
		}
		return
	}

	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/repos/owner/repo/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.readCalls++
	if f.readFailure != "" {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, f.readFailure)
		return
	}

	switch {
	case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/ref/heads/"):
		branch := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/git/ref/heads/")
		if branch == f.defaultBranch {
			respondJSON(w, map[string]any{
				"object": map[string]any{"sha": f.defaultOID, "type": "commit"},
			})
			return
		}
		if f.refState == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		respondJSON(w, map[string]any{
			"object": map[string]any{"sha": f.stateOID(), "type": "commit"},
		})
	case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/commits/"):
		oid := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/git/commits/")
		switch oid {
		case reconcileBaseOID:
			respondJSON(w, map[string]any{
				"sha": oid,
				"commit": map[string]any{
					"message": "base",
					"tree":    map[string]any{"sha": "base-tree"},
				},
			})
		case reconcileFruitOID:
			respondJSON(w, map[string]any{
				"sha": oid,
				"commit": map[string]any{
					"message": "make fruit",
					"tree":    map[string]any{"sha": "fruit-tree"},
				},
				"parents": []map[string]string{{"sha": reconcileBaseOID}},
			})
		default:
			respondJSON(w, map[string]any{
				"sha": oid,
				"commit": map[string]any{
					"message": "unexpected",
					"tree":    map[string]any{"sha": "unexpected-tree"},
				},
				"parents": []map[string]string{{"sha": reconcileBaseOID}},
			})
		}
	case strings.HasSuffix(r.URL.Path, "/git/trees/base-tree"):
		respondJSON(w, map[string]any{
			"tree": []map[string]string{
				{"path": "keep.txt", "mode": "100644", "type": "blob", "sha": "base-keep"},
				{"path": "modified.txt", "mode": "100644", "type": "blob", "sha": "base-modified"},
				{"path": "delete.txt", "mode": "100644", "type": "blob", "sha": "base-delete"},
			},
			"truncated": false,
		})
	case strings.HasSuffix(r.URL.Path, "/git/trees/fruit-tree"):
		entries := []map[string]string{
			{"path": "keep.txt", "mode": "100644", "type": "blob", "sha": "base-keep"},
			{"path": "modified.txt", "mode": "100644", "type": "blob", "sha": "fruit-modified"},
			{"path": "new.txt", "mode": "100644", "type": "blob", "sha": "fruit-new"},
		}
		if f.extraFruitPath {
			entries = append(entries, map[string]string{"path": "unexpected.txt", "mode": "100644", "type": "blob", "sha": "unexpected-blob"})
		}
		respondJSON(w, map[string]any{
			"tree":      entries,
			"truncated": false,
		})
	case strings.HasSuffix(r.URL.Path, "/git/blobs/fruit-modified"):
		contents := base64.StdEncoding.EncodeToString([]byte("new-modified\n"))
		if f.badFruitContent {
			contents = base64.StdEncoding.EncodeToString([]byte("wrong\n"))
		}
		respondJSON(w, map[string]any{"encoding": "base64", "content": contents})
	case strings.HasSuffix(r.URL.Path, "/git/blobs/fruit-new"):
		respondJSON(w, map[string]any{
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString([]byte("new\n")),
		})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *reconcileFruitFake) stateOID() string {
	switch f.refState {
	case "exact":
		return reconcileFruitOID
	case "unexpected":
		return "unexpected-oid"
	default:
		return reconcileBaseOID
	}
}

func respondJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func setupReconcileFruitTest(t *testing.T, fake *reconcileFruitFake) (string, AppCredential) {
	t.Helper()
	_, keyPath := genTestKeyPEM(t)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	t.Cleanup(RedirectGitHubAPIBaseURL(srv.URL))
	t.Cleanup(redirectGitHubGraphQLURL(srv.URL + "/graphql"))
	resetGitHubAppTokenCache()
	t.Cleanup(resetGitHubAppTokenCache)

	repo := t.TempDir()
	if _, err := runGitCommand(context.Background(), repo, "init", "-q"); err != nil {
		t.Fatalf("init repository: %v", err)
	}
	if _, err := runGitCommand(context.Background(), repo, "remote", "add", "origin", "https://github.com/owner/repo.git"); err != nil {
		t.Fatalf("add origin: %v", err)
	}
	return repo, AppCredential{AppID: "1", InstallationID: 42, PrivateKeyPath: keyPath}
}

func reconcileFruitChanges() ([]apiCommitFileAddition, []apiCommitFileDeletion) {
	return []apiCommitFileAddition{
		{Path: "new.txt", Contents: base64.StdEncoding.EncodeToString([]byte("new\n"))},
		{Path: "modified.txt", Contents: base64.StdEncoding.EncodeToString([]byte("new-modified\n"))},
	}, []apiCommitFileDeletion{{Path: "delete.txt"}}
}

func TestPublishAPIFruitReconcilesManagedMutationOutcomes(t *testing.T) {
	additions, deletions := reconcileFruitChanges()
	cases := []struct {
		name              string
		createRefStatus   int
		createRefState    string
		initialState      string
		mutations         []reconcileMutation
		badFruitContent   bool
		extraFruitPath    bool
		readFailure       string
		wantOID           string
		wantErr           string
		wantOutcome       string
		requestID         string
		wantRequestID     string
		wantMutations     int
		wantReads         bool
		wantDefaultBranch string
	}{
		{
			name:              "ordinary mutation success",
			mutations:         []reconcileMutation{{response: "success", state: "exact"}},
			wantOID:           reconcileFruitOID,
			wantMutations:     1,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name:              "mutation response lost after exact Fruit was created",
			mutations:         []reconcileMutation{{response: "lost", state: "exact"}},
			wantOID:           reconcileFruitOID,
			wantMutations:     1,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name: "base remains, permit one identical evidence-gated retry",
			mutations: []reconcileMutation{
				{response: "error", state: "base"},
				{response: "success", state: "exact"},
			},
			wantOID:           reconcileFruitOID,
			wantMutations:     2,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name:              "unexpected branch state fails closed",
			mutations:         []reconcileMutation{{response: "error", state: "unexpected"}},
			wantErr:           "unexpected state",
			wantOutcome:       apiFruitOutcomeUnexpectedState,
			requestID:         reconcileRequestID,
			wantRequestID:     reconcileRequestID,
			wantMutations:     1,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name:              "existing exact Fruit prevents duplicate mutation",
			createRefStatus:   http.StatusUnprocessableEntity,
			initialState:      "exact",
			wantOID:           reconcileFruitOID,
			wantMutations:     0,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name:              "target ref conflict does not authorize a commit",
			createRefStatus:   http.StatusUnprocessableEntity,
			initialState:      "unexpected",
			wantErr:           "already exists",
			wantOutcome:       apiFruitOutcomeTargetRefConflict,
			wantMutations:     0,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name:              "ambiguous target ref creation is read only",
			createRefStatus:   http.StatusInternalServerError,
			createRefState:    "base",
			wantErr:           "already exists",
			wantOutcome:       apiFruitOutcomeTargetRefConflict,
			wantMutations:     0,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name: "second ambiguous mutation is reconciled without a third mutation",
			mutations: []reconcileMutation{
				{response: "error", state: "base"},
				{response: "lost", state: "exact"},
			},
			wantOID:           reconcileFruitOID,
			wantMutations:     2,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name: "second ambiguous mutation remains unproven and stops",
			mutations: []reconcileMutation{
				{response: "error", state: "base"},
				{response: "error", state: "base"},
			},
			wantErr:           "final mutation",
			wantOutcome:       apiFruitOutcomeRetryExhausted,
			wantRequestID:     "req-2",
			wantMutations:     2,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name:              "exact identity mismatch is rejected",
			mutations:         []reconcileMutation{{response: "error", state: "exact"}},
			badFruitContent:   true,
			wantErr:           "unexpected state",
			wantOutcome:       apiFruitOutcomeUnexpectedState,
			wantRequestID:     "req-1",
			wantMutations:     1,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name:              "exact identity with an unexpected changed path is rejected",
			mutations:         []reconcileMutation{{response: "error", state: "exact"}},
			extraFruitPath:    true,
			wantErr:           "unexpected state",
			wantOutcome:       apiFruitOutcomeUnexpectedState,
			wantMutations:     1,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name:              "reconciliation read failure fails closed",
			mutations:         []reconcileMutation{{response: "error", state: "base"}},
			readFailure:       "Authorization: Bearer upstream-secret-content",
			wantErr:           "reconciliation",
			wantOutcome:       apiFruitOutcomeReconciliationFailure,
			wantRequestID:     "req-1",
			wantMutations:     1,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
		{
			name:              "ambiguous target ref creation sees absent ref",
			createRefStatus:   http.StatusInternalServerError,
			wantErr:           apiFruitOutcomeTargetRefAbsent,
			wantOutcome:       apiFruitOutcomeTargetRefAbsent,
			wantMutations:     0,
			wantReads:         true,
			wantDefaultBranch: reconcileMainOID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &reconcileFruitFake{
				createRefStatus: tc.createRefStatus,
				createRefState:  tc.createRefState,
				refState:        tc.initialState,
				mutations:       tc.mutations,
				badFruitContent: tc.badFruitContent,
				extraFruitPath:  tc.extraFruitPath,
				readFailure:     tc.readFailure,
				requestID:       tc.requestID,
				defaultOID:      reconcileMainOID,
				defaultBranch:   reconcileDefaultRef,
			}
			repo, credential := setupReconcileFruitTest(t, fake)
			gotOID, err := publishAPIFruit(context.Background(), repo, reconcileBranch, reconcileBaseOID, credential, additions, deletions, "make fruit")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("publishAPIFruit: %v", err)
				}
				if gotOID != tc.wantOID {
					t.Fatalf("OID = %q, want %q", gotOID, tc.wantOID)
				}
			} else {
				if err == nil {
					t.Fatal("publishAPIFruit succeeded despite untrusted outcome")
				}
				if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantErr)) {
					t.Fatalf("error = %q, want %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "upstream-secret-content") {
					t.Fatalf("unsafe upstream content leaked in error: %q", err)
				}
				if tc.wantRequestID != "" && !strings.Contains(err.Error(), tc.wantRequestID) {
					t.Fatalf("error = %q, want captured request ID %q", err, tc.wantRequestID)
				}
				if tc.wantOutcome != "" {
					var failure *apiFruitPublicationFailure
					if !errors.As(err, &failure) {
						t.Fatalf("error = %T %v, want classified publication failure", err, err)
					}
					if failure.Outcome != tc.wantOutcome {
						t.Fatalf("publication outcome = %q, want %q", failure.Outcome, tc.wantOutcome)
					}
					if tc.wantRequestID != "" && failure.RequestID != tc.wantRequestID {
						t.Fatalf("publication request ID = %q, want %q", failure.RequestID, tc.wantRequestID)
					}
				}
			}
			if fake.createRefCalls != 1 {
				t.Fatalf("create-ref calls = %d, want 1", fake.createRefCalls)
			}
			if fake.graphQLCalls != tc.wantMutations {
				t.Fatalf("GraphQL mutations = %d, want %d", fake.graphQLCalls, tc.wantMutations)
			}
			if len(fake.expectedHeads) != fake.graphQLCalls {
				t.Fatalf("captured expected heads = %v for %d mutations", fake.expectedHeads, fake.graphQLCalls)
			}
			for _, head := range fake.expectedHeads {
				if head != reconcileBaseOID {
					t.Fatalf("mutation ExpectedHeadOid = %q, want %q", head, reconcileBaseOID)
				}
			}
			if len(fake.createRefBranches) != fake.createRefCalls {
				t.Fatalf("captured create-ref branches = %v for %d calls", fake.createRefBranches, fake.createRefCalls)
			}
			for _, branch := range fake.createRefBranches {
				if branch != reconcileBranch {
					t.Fatalf("create-ref branch = %q, want %q", branch, reconcileBranch)
				}
			}
			if len(fake.mutationBranches) != fake.graphQLCalls {
				t.Fatalf("captured mutation branches = %v for %d calls", fake.mutationBranches, fake.graphQLCalls)
			}
			for _, branch := range fake.mutationBranches {
				if branch != reconcileBranch {
					t.Fatalf("createCommitOnBranch branch = %q, want %q", branch, reconcileBranch)
				}
			}
			if fake.defaultBranchTouched {
				t.Fatalf("publication targeted configured default branch %q", fake.defaultBranch)
			}
			if tc.wantReads && fake.readCalls == 0 {
				t.Fatal("expected read-only reconciliation calls")
			}
			if fake.defaultOID != tc.wantDefaultBranch {
				t.Fatalf("default branch OID = %q, want %q", fake.defaultOID, tc.wantDefaultBranch)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGithubGraphQLPostClassifiesDefinitePreMutationFailure(t *testing.T) {
	original := githubAppHTTPClient
	t.Cleanup(func() { githubAppHTTPClient = original })
	githubAppHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("upstream-secret-content")
	})}

	metadata, err := githubGraphQLPost(context.Background(), "token", "query", nil, nil)
	if err == nil {
		t.Fatal("expected GraphQL transport error")
	}
	if metadata.RequestWritten {
		t.Fatal("pre-write transport failure was classified as written")
	}
	var mutationErr *githubMutationError
	if !errors.As(err, &mutationErr) || mutationErr.Kind != githubMutationBeforeWrite {
		t.Fatalf("error = %T %v, want typed before-write mutation error", err, err)
	}
	if strings.Contains(err.Error(), "upstream-secret-content") {
		t.Fatalf("unsafe transport error leaked: %q", err)
	}
}

func TestPublishAPIFruitTargetRefAbsentAfterAmbiguousCreationFailsClosed(t *testing.T) {
	fake := &reconcileFruitFake{
		createRefStatus: http.StatusInternalServerError,
		defaultOID:      reconcileMainOID,
		defaultBranch:   reconcileDefaultRef,
	}
	repo, credential := setupReconcileFruitTest(t, fake)
	additions, deletions := reconcileFruitChanges()
	_, err := publishAPIFruit(context.Background(), repo, reconcileBranch, reconcileBaseOID, credential, additions, deletions, "make fruit")
	var failure *apiFruitPublicationFailure
	if err == nil || !errors.As(err, &failure) {
		t.Fatalf("error = %v, want classified target-ref-absent failure", err)
	}
	if failure.Outcome != apiFruitOutcomeTargetRefAbsent {
		t.Fatalf("publication outcome = %q, want %q", failure.Outcome, apiFruitOutcomeTargetRefAbsent)
	}
	if strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, must not claim target ref already exists", err)
	}
	if fake.graphQLCalls != 0 {
		t.Fatalf("GraphQL mutations = %d, want none", fake.graphQLCalls)
	}
}

func TestPublishAPIFruitDoesNotExposeIntentContentsInDiagnostics(t *testing.T) {
	const secretContent = "PRIVATE_PROMPT_CONTENT"
	fake := &reconcileFruitFake{
		mutations:  []reconcileMutation{{response: "error", state: "unexpected"}},
		defaultOID: reconcileMainOID,
	}
	repo, credential := setupReconcileFruitTest(t, fake)
	additions := []apiCommitFileAddition{{
		Path:     "secret.txt",
		Contents: base64.StdEncoding.EncodeToString([]byte(secretContent)),
	}}
	_, err := publishAPIFruit(context.Background(), repo, reconcileBranch, reconcileBaseOID, credential, additions, nil, "make fruit")
	if err == nil {
		t.Fatal("publishAPIFruit succeeded despite unexpected state")
	}
	if strings.Contains(err.Error(), secretContent) {
		t.Fatalf("intent content leaked in diagnostic: %q", err)
	}
}

func TestGithubRequestIDIsBoundedAndSafe(t *testing.T) {
	if got := safeGitHubRequestID("req-123_ABC.def"); got != "req-123_ABC.def" {
		t.Fatalf("safe request ID = %q", got)
	}
	if got := safeGitHubRequestID(reconcileRequestID); got != reconcileRequestID {
		t.Fatalf("colon-separated request ID = %q, want %q", got, reconcileRequestID)
	}
	if got := safeGitHubRequestID("Bearer secret"); got != "" {
		t.Fatalf("unsafe request ID was retained: %q", got)
	}
	if got := safeGitHubRequestID("Authorization: Bearer secret"); got != "" {
		t.Fatalf("authorization-like request ID was retained: %q", got)
	}
	if got := safeGitHubRequestID("request-id\nnext-line"); got != "" {
		t.Fatalf("control-character request ID was retained: %q", got)
	}
	if got := safeGitHubRequestID(strings.Repeat("a", 129)); got != "" {
		t.Fatalf("oversized request ID was retained: %q", got)
	}
}

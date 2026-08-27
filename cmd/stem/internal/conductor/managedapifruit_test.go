package conductor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startAPIFruitFake starts a fake GitHub API+GraphQL server that records calls
// and returns scripted responses. It redirects both the REST and GraphQL URLs
// used by the conductor and restores them on test cleanup.
func startAPIFruitFake(t *testing.T, createRefStatus int, graphQLOID string) *apifruitFake {
	t.Helper()
	fake := &apifruitFake{
		createRefStatus: createRefStatus,
		graphQLOID:      graphQLOID,
	}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	restoreREST := RedirectGitHubAPIBaseURL(srv.URL)
	t.Cleanup(restoreREST)
	restoreGraphQL := redirectGitHubGraphQLURL(srv.URL + "/graphql")
	t.Cleanup(restoreGraphQL)
	resetGitHubAppTokenCache()
	t.Cleanup(resetGitHubAppTokenCache)

	fake.installTokenURL = srv.URL + "/repos/owner/repo/git/refs"
	return fake
}

type apifruitFake struct {
	createRefStatus int    // HTTP status to return for POST /repos/.../git/refs
	graphQLOID      string // OID to embed in the GraphQL createCommitOnBranch response

	// Captured requests
	installCalled   int
	tokenCalled     int
	createRefCalled int
	graphQLCalled   int
	createRefBody   string // raw JSON body of the first create-ref POST
	graphQLBody     string // raw JSON body of the GraphQL POST
	installTokenURL string // for reference

	// Set to non-empty to return an error from GraphQL.
	graphQLError string
}

func (f *apifruitFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/installation") && r.Method == http.MethodGet:
		f.installCalled++
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1001})

	case strings.Contains(r.URL.Path, "/access_tokens") && r.Method == http.MethodPost:
		f.tokenCalled++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_test_install_token",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})

	case strings.Contains(r.URL.Path, "/git/refs") && r.Method == http.MethodPost:
		f.createRefCalled++
		body, _ := readBody(r)
		if f.createRefBody == "" {
			f.createRefBody = body
		}
		if f.createRefStatus != 0 && f.createRefStatus != http.StatusCreated {
			w.WriteHeader(f.createRefStatus)
			return
		}
		w.WriteHeader(http.StatusCreated)

	case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
		f.graphQLCalled++
		body, _ := readBody(r)
		if f.graphQLBody == "" {
			f.graphQLBody = body
		}
		if f.graphQLError != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"errors": []map[string]any{{"message": f.graphQLError}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"createCommitOnBranch": map[string]any{
					"commit": map[string]any{
						"oid": f.graphQLOID,
					},
				},
			},
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func readBody(r *http.Request) (string, error) {
	buf := make([]byte, 4096)
	n, _ := r.Body.Read(buf)
	return string(buf[:n]), nil
}

// ----------------------------------------------------------------------------
// githubCreateRef unit tests
// ----------------------------------------------------------------------------

// TestGithubCreateRefSuccess verifies that githubCreateRef POSTs the correct
// JSON body and succeeds when the server returns 201 Created.
func TestGithubCreateRefSuccess(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	fake := startAPIFruitFake(t, http.StatusCreated, "")

	if err := githubCreateRef(context.Background(), "owner", "repo", "sprout/task-x", "abc123", "ghs_tok"); err != nil {
		t.Fatalf("githubCreateRef failed: %v", err)
	}
	if fake.createRefCalled != 1 {
		t.Fatalf("create-ref endpoint called %d times, want 1", fake.createRefCalled)
	}

	var body map[string]string
	if err := json.Unmarshal([]byte(fake.createRefBody), &body); err != nil {
		t.Fatalf("decode create-ref body: %v", err)
	}
	if body["ref"] != "refs/heads/sprout/task-x" {
		t.Errorf("create-ref body[ref] = %q, want refs/heads/sprout/task-x", body["ref"])
	}
	if body["sha"] != "abc123" {
		t.Errorf("create-ref body[sha] = %q, want abc123", body["sha"])
	}
	_ = keyPath
}

// TestGithubCreateRefAlreadyExists verifies that a 422 Unprocessable Entity
// from GitHub is surfaced as an explicit "already exists" error.
func TestGithubCreateRefAlreadyExists(t *testing.T) {
	fake := startAPIFruitFake(t, http.StatusUnprocessableEntity, "")

	err := githubCreateRef(context.Background(), "owner", "repo", "sprout/task-exists", "deadbeef", "ghs_tok")
	if err == nil {
		t.Fatal("expected error for 422 response, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %q, want 'already exists' message", err.Error())
	}
	_ = fake
}

// TestGithubCreateRefServerError verifies that a 500 Internal Server Error
// is propagated as an error rather than silently ignored.
func TestGithubCreateRefServerError(t *testing.T) {
	fake := startAPIFruitFake(t, http.StatusInternalServerError, "")

	err := githubCreateRef(context.Background(), "owner", "repo", "sprout/task-fail", "deadbeef", "ghs_tok")
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	_ = fake
}

// ----------------------------------------------------------------------------
// prepareReconcileRepo builds a local repo with a "remote" (bare) origin,
// a committed base, a Fruit commit pushed to origin, and a linked worktree
// whose RunWorkspace ownership is properly registered. Returns the full
// RunWorkspace and the expected Fruit OID.
// ----------------------------------------------------------------------------
func prepareReconcileRepo(t *testing.T, stepID string) (RunWorkspace, string) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create the "remote" (origin) bare repository.
	origin := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main"},
	} {
		if _, err := runGitCommand(ctx, origin, args...); err != nil {
			t.Fatalf("init origin: %v", err)
		}
	}

	// Create a local repository as the managed checkout.
	local := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
		{"remote", "add", "origin", origin},
	} {
		if _, err := runGitCommand(ctx, local, args...); err != nil {
			t.Fatalf("setup local repo: %v", err)
		}
	}

	// Commit the base file and push to origin.
	if err := os.WriteFile(filepath.Join(local, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	for _, args := range [][]string{
		{"add", "base.txt"},
		{"commit", "-q", "-m", "base"},
		{"push", "-q", "origin", "main"},
	} {
		if _, err := runGitCommand(ctx, local, args...); err != nil {
			t.Fatalf("base commit/push: %v", err)
		}
	}
	baseOID, err := runGitCommand(ctx, local, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse base: %v", err)
	}
	baseOID = strings.TrimSpace(baseOID)

	// Allocate a proper run workspace (creates the branch + linked worktree +
	// registers ownership).
	rw, err := CreateRunWorkspace(ctx, local, stepID, baseOID)
	if err != nil {
		t.Fatalf("CreateRunWorkspace: %v", err)
	}

	// Simulate GitHub API having committed a Fruit file to the branch on origin.
	// We do this by committing locally on the run branch and pushing to origin.
	if err := os.WriteFile(filepath.Join(rw.Path, "fruit.txt"), []byte("api fruit\n"), 0o644); err != nil {
		t.Fatalf("write fruit file: %v", err)
	}
	for _, args := range [][]string{
		{"add", "fruit.txt"},
		{"commit", "-q", "-m", "api-published fruit"},
		{"push", "-q", "origin", rw.Branch},
	} {
		if _, err := runGitCommand(ctx, rw.Path, args...); err != nil {
			t.Fatalf("fruit commit/push: %v", err)
		}
	}
	fruitOID, err := runGitCommand(ctx, rw.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse fruit: %v", err)
	}

	// Reset local worktree back to baseOID to simulate state before reconciliation
	// (as if GitHub committed and we haven't fetched yet).
	if _, err := runGitCommand(ctx, rw.Path, "reset", "--hard", baseOID); err != nil {
		t.Fatalf("reset worktree to base: %v", err)
	}

	return rw, strings.TrimSpace(fruitOID)
}

// ----------------------------------------------------------------------------
// ReconcilePublishedFruit tests
// ----------------------------------------------------------------------------

// TestReconcilePublishedFruitFetchesAndResets verifies the happy path:
// ownership is valid, Path is the registered worktree, the fetched OID matches
// what GitHub returned, and git reset --hard moves the linked worktree.
func TestReconcilePublishedFruitFetchesAndResets(t *testing.T) {
	ctx := context.Background()
	rw, fruitOID := prepareReconcileRepo(t, "reconcile-happy")

	// Before reconciliation the worktree is at baseOID.
	priorOID, err := runGitCommand(ctx, rw.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("pre-reconcile rev-parse: %v", err)
	}
	if strings.TrimSpace(priorOID) == fruitOID {
		t.Fatal("pre-reconcile: worktree already at fruit OID before reconciliation")
	}

	if err := rw.ReconcilePublishedFruit(ctx, fruitOID); err != nil {
		t.Fatalf("ReconcilePublishedFruit: %v", err)
	}

	afterOID, err := runGitCommand(ctx, rw.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("post-reconcile rev-parse: %v", err)
	}
	if strings.TrimSpace(afterOID) != fruitOID {
		t.Fatalf("post-reconcile HEAD = %q, want %q", strings.TrimSpace(afterOID), fruitOID)
	}

	// The Fruit file should now be present in the worktree.
	if _, err := os.Stat(filepath.Join(rw.Path, "fruit.txt")); err != nil {
		t.Fatalf("fruit.txt missing after reconciliation: %v", err)
	}

	// Cleanup: reset file back to base so Cleanup sees a clean worktree.
	if _, err := runGitCommand(ctx, rw.Path, "reset", "--hard", rw.BaseCommit); err != nil {
		t.Fatalf("restore for cleanup: %v", err)
	}
	if err := rw.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("cleanup after reconcile test: %v", err)
	}
}

// TestReconcilePublishedFruitRejectsIncompleteIdentity verifies that an
// incomplete RunWorkspace (any empty field) is rejected before any git
// operations are attempted.
func TestReconcilePublishedFruitRejectsIncompleteIdentity(t *testing.T) {
	ctx := context.Background()
	rw, fruitOID := prepareReconcileRepo(t, "reconcile-incomplete")
	t.Cleanup(func() {
		if _, err := runGitCommand(ctx, rw.Path, "reset", "--hard", rw.BaseCommit); err == nil {
			_ = rw.Cleanup(ctx, ResolvedCredential{})
		}
	})

	cases := []struct {
		name string
		rw   RunWorkspace
	}{
		{"missing Repository", RunWorkspace{Path: rw.Path, Branch: rw.Branch, BaseCommit: rw.BaseCommit, RunID: rw.RunID}},
		{"missing Path", RunWorkspace{Repository: rw.Repository, Branch: rw.Branch, BaseCommit: rw.BaseCommit, RunID: rw.RunID}},
		{"missing Branch", RunWorkspace{Repository: rw.Repository, Path: rw.Path, BaseCommit: rw.BaseCommit, RunID: rw.RunID}},
		{"missing BaseCommit", RunWorkspace{Repository: rw.Repository, Path: rw.Path, Branch: rw.Branch, RunID: rw.RunID}},
		{"missing RunID", RunWorkspace{Repository: rw.Repository, Path: rw.Path, Branch: rw.Branch, BaseCommit: rw.BaseCommit}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.rw.ReconcilePublishedFruit(ctx, fruitOID)
			if err == nil {
				t.Fatalf("%s: expected error for incomplete identity, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("%s: error = %q, want 'incomplete' message", tc.name, err.Error())
			}
		})
	}
}

// TestReconcilePublishedFruitRejectsEmptyOID verifies that a zero published OID
// is rejected before any git operations are attempted.
func TestReconcilePublishedFruitRejectsEmptyOID(t *testing.T) {
	ctx := context.Background()
	rw, _ := prepareReconcileRepo(t, "reconcile-emptyoid")
	t.Cleanup(func() {
		if _, err := runGitCommand(ctx, rw.Path, "reset", "--hard", rw.BaseCommit); err == nil {
			_ = rw.Cleanup(ctx, ResolvedCredential{})
		}
	})

	err := rw.ReconcilePublishedFruit(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty published OID, got nil")
	}
	if !strings.Contains(err.Error(), "OID is required") {
		t.Fatalf("error = %q, want 'OID is required' message", err.Error())
	}
}

// TestReconcilePublishedFruitRejectsUnownedBranch verifies that a RunWorkspace
// whose Branch is not in the owned-reference registry is rejected before any
// destructive operation.
func TestReconcilePublishedFruitRejectsUnownedBranch(t *testing.T) {
	ctx := context.Background()
	rw, fruitOID := prepareReconcileRepo(t, "reconcile-unowned")
	t.Cleanup(func() {
		if _, err := runGitCommand(ctx, rw.Path, "reset", "--hard", rw.BaseCommit); err == nil {
			_ = rw.Cleanup(ctx, ResolvedCredential{})
		}
	})

	// Present a RunWorkspace that looks correct but has a RunID that doesn't
	// match the registry entry (simulating a stale handle or wrong run).
	stale := rw
	stale.RunID = "not-the-registered-run-id"

	err := stale.ReconcilePublishedFruit(ctx, fruitOID)
	if err == nil {
		t.Fatal("expected error for mismatched RunID, got nil")
	}
	// Worktree must be unchanged.
	headOID, revErr := runGitCommand(ctx, rw.Path, "rev-parse", "HEAD")
	if revErr != nil {
		t.Fatalf("rev-parse after rejection: %v", revErr)
	}
	if strings.TrimSpace(headOID) != rw.BaseCommit {
		t.Fatalf("worktree HEAD changed to %q after rejection (want %q)", strings.TrimSpace(headOID), rw.BaseCommit)
	}
}

// TestReconcilePublishedFruitRejectsWrongPurpose verifies that a branch whose
// registered purpose is NOT PurposeSproutIsolation is rejected.
func TestReconcilePublishedFruitRejectsWrongPurpose(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Build a minimal local repo with a branch registered as DelegatedWorkspace.
	repo, baseOID := prepareRunWorkspaceTest(t)
	wrongBranch := "sprout/task-wrong-purpose"
	if _, err := runGitCommand(ctx, repo, "branch", wrongBranch, baseOID); err != nil {
		t.Fatalf("create wrong-purpose branch: %v", err)
	}
	if err := RegisterOwnedRef(OwnedRef{
		Repository: repo,
		Branch:     wrongBranch,
		Purpose:    PurposeDelegatedWorkspace,
		Base:       baseOID,
		RunID:      "some-run",
	}); err != nil {
		t.Fatalf("register delegated-workspace ref: %v", err)
	}

	// Create a temp worktree path (doesn't need to really be a worktree since
	// the purpose check fires first).
	rw := RunWorkspace{
		Repository: repo,
		Path:       t.TempDir(),
		Branch:     wrongBranch,
		BaseCommit: baseOID,
		RunID:      "some-run",
	}

	err := rw.ReconcilePublishedFruit(ctx, baseOID)
	if err == nil {
		t.Fatal("expected error for delegated-workspace purpose, got nil")
	}
	if !strings.Contains(err.Error(), "not a recorded Tendril-owned Sprout isolation branch") {
		t.Fatalf("error = %q, want 'not a recorded' message", err.Error())
	}
}

// TestReconcilePublishedFruitRejectsRemoteRefMismatch verifies that when the
// fetched tip of the Fruit branch differs from the GitHub-returned OID, the
// reset is not performed and an error is returned.
func TestReconcilePublishedFruitRejectsRemoteRefMismatch(t *testing.T) {
	ctx := context.Background()
	rw, fruitOID := prepareReconcileRepo(t, "reconcile-mismatch")
	t.Cleanup(func() {
		if _, err := runGitCommand(ctx, rw.Path, "reset", "--hard", rw.BaseCommit); err == nil {
			_ = rw.Cleanup(ctx, ResolvedCredential{})
		}
	})

	// Fetch the real branch first so origin/branch is reachable, then pass a
	// deliberately wrong OID to simulate a GitHub mutation returning a different
	// commit than the one origin actually serves.
	if _, err := runGitCommand(ctx, rw.Repository, "fetch", "origin", rw.Branch); err != nil {
		t.Fatalf("pre-fetch: %v", err)
	}
	wrongOID := strings.Repeat("a", 40) // plausible SHA-1 hex, guaranteed wrong

	err := rw.ReconcilePublishedFruit(ctx, wrongOID)
	if err == nil {
		t.Fatal("expected error for remote-ref mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "workspace left untouched") {
		t.Fatalf("error = %q, want 'workspace left untouched' message", err.Error())
	}

	// Worktree must be unchanged (still at base commit).
	headOID, revErr := runGitCommand(ctx, rw.Path, "rev-parse", "HEAD")
	if revErr != nil {
		t.Fatalf("rev-parse after mismatch rejection: %v", revErr)
	}
	if strings.TrimSpace(headOID) != rw.BaseCommit {
		t.Fatalf("worktree HEAD changed to %q after mismatch rejection (want %q)", strings.TrimSpace(headOID), rw.BaseCommit)
	}
	_ = fruitOID
}

// ----------------------------------------------------------------------------
// publishManagedAPIFruit integration test
// ----------------------------------------------------------------------------

// TestPublishManagedAPIFruitEndToEnd verifies the full publishManagedAPIFruit
// path using the existing fake REST/GraphQL seams. It asserts:
//   - The create-ref branch name and SHA match the RunWorkspace.
//   - BaseCommit is used as expectedHeadOid in the GraphQL body.
//   - The measured file additions and deletions appear in the GraphQL body.
//   - The returned OID comes from the GraphQL response.
//   - ReconcilePublishedFruit succeeds and Cleanup completes cleanly.
func TestPublishManagedAPIFruitEndToEnd(t *testing.T) {
	ctx := context.Background()
	_, keyPath := genTestKeyPEM(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create the "remote" (origin) bare repository.
	originDir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "--bare", "-b", "main"}} {
		if _, err := runGitCommand(ctx, originDir, args...); err != nil {
			t.Fatalf("init origin: %v", err)
		}
	}

	local := t.TempDir()
	// Use a fake GitHub-style URL for origin so parseOwnerRepo succeeds.
	// The url.insteadOf config redirects actual git operations to originDir.
	fakeGitHubURL := "https://github.com/opentendril/opentendril.git"
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
		{"remote", "add", "origin", fakeGitHubURL},
		// Redirect the exact origin URL to originDir so git push/fetch use the local bare dir.
		{"config", "url." + originDir + ".insteadOf", "https://github.com/opentendril/opentendril.git"},
	} {
		if _, err := runGitCommand(ctx, local, args...); err != nil {
			t.Fatalf("setup local: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(local, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	for _, args := range [][]string{
		{"add", "base.txt"},
		{"commit", "-q", "-m", "base"},
		{"push", "-q", "origin", "main"},
	} {
		if _, err := runGitCommand(ctx, local, args...); err != nil {
			t.Fatalf("base commit/push: %v", err)
		}
	}
	baseOID, err := runGitCommand(ctx, local, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse base: %v", err)
	}
	baseOID = strings.TrimSpace(baseOID)

	// Create a run workspace on the local managed checkout.
	rw, err := CreateRunWorkspace(ctx, local, "e2e-api-fruit", baseOID)
	if err != nil {
		t.Fatalf("CreateRunWorkspace: %v", err)
	}

	// The run worktree also needs the insteadOf so it can push to originDir.
	if _, err := runGitCommand(ctx, rw.Path, "config", "url."+originDir+".insteadOf", "https://github.com/opentendril/opentendril.git"); err != nil {
		t.Fatalf("set insteadOf on worktree: %v", err)
	}

	// Write two files into the worktree as the "Sprout's work".
	if err := os.WriteFile(filepath.Join(rw.Path, "feature.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write feature.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rw.Path, "notes.txt"), []byte("notes\n"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}

	// Build the fake REST+GraphQL server. The GraphQL handler performs the
	// "GitHub API commit" by committing locally and pushing to originDir,
	// then returns the OID so ReconcilePublishedFruit can verify it.
	fake := &apifruitFake{createRefStatus: http.StatusCreated}
	reconcileOID := ""
	rwBranch := rw.Branch
	rwPath := rw.Path

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation") && r.Method == http.MethodGet:
			fake.installCalled++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1001})

		case strings.Contains(r.URL.Path, "/access_tokens") && r.Method == http.MethodPost:
			fake.tokenCalled++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_test_install_token",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})

		case strings.Contains(r.URL.Path, "/git/refs") && r.Method == http.MethodPost:
			fake.createRefCalled++
			body, _ := readBody(r)
			if fake.createRefBody == "" {
				fake.createRefBody = body
			}
			w.WriteHeader(http.StatusCreated)

		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			fake.graphQLCalled++
			body, _ := readBody(r)
			if fake.graphQLBody == "" {
				fake.graphQLBody = body
			}

			// Simulate the GitHub API committing the Fruit files. We commit
			// directly in the worktree and push to the bare originDir by path
			// (not via the 'origin' remote) to avoid HTTPS redirect confusion.
			gctx := context.Background()
			for _, args := range [][]string{
				{"add", "feature.txt", "notes.txt"},
				{"commit", "-q", "-m", "api-published fruit"},
				{"push", "-q", originDir, rwBranch},
			} {
				if _, err := runGitCommand(gctx, rwPath, args...); err != nil {
					http.Error(w, "git error: "+err.Error(), http.StatusInternalServerError)
					return
				}
			}
			oid, _ := runGitCommand(gctx, rwPath, "rev-parse", "HEAD")
			reconcileOID = strings.TrimSpace(oid)

			// Reset the worktree to baseOID to simulate pre-reconciliation state.
			if _, err := runGitCommand(gctx, rwPath, "reset", "--hard", baseOID); err != nil {
				http.Error(w, "reset error: "+err.Error(), http.StatusInternalServerError)
				return
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"createCommitOnBranch": map[string]any{
						"commit": map[string]any{"oid": reconcileOID},
					},
				},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(RedirectGitHubAPIBaseURL(srv.URL))
	t.Cleanup(redirectGitHubGraphQLURL(srv.URL + "/graphql"))
	resetGitHubAppTokenCache()
	t.Cleanup(resetGitHubAppTokenCache)

	plan := &substrateExecutionPlan{
		credential: ResolvedCredential{
			Method:     CredentialApp,
			CommitMode: CommitModeAPI,
			App: AppCredential{
				AppID:          "1234",
				InstallationID: 1001,
				PrivateKeyPath: keyPath,
			},
		},
	}
	executionStatus := sproutExecutionStatus{
		StepID:        "e2e-api-fruit",
		Status:        "success",
		FilesModified: []string{"feature.txt", "notes.txt"},
	}

	// ---- Call publishManagedAPIFruit ----
	gotOID, pubErr := publishManagedAPIFruit(ctx, rw.Path, executionStatus, "e2e test task", plan, rw)
	if pubErr != nil {
		t.Fatalf("publishManagedAPIFruit: %v", pubErr)
	}
	if gotOID != reconcileOID {
		t.Errorf("publishManagedAPIFruit returned OID %q, want %q", gotOID, reconcileOID)
	}

	// Assert create-ref received the correct branch and SHA.
	var refBody map[string]string
	if err := json.Unmarshal([]byte(fake.createRefBody), &refBody); err != nil {
		t.Fatalf("decode create-ref body: %v", err)
	}
	wantRef := "refs/heads/" + rw.Branch
	if refBody["ref"] != wantRef {
		t.Errorf("create-ref body[ref] = %q, want %q", refBody["ref"], wantRef)
	}
	if refBody["sha"] != rw.BaseCommit {
		t.Errorf("create-ref body[sha] = %q, want %q (BaseCommit)", refBody["sha"], rw.BaseCommit)
	}

	// Assert GraphQL body contains expectedHeadOid = BaseCommit.
	if !strings.Contains(fake.graphQLBody, rw.BaseCommit) {
		t.Errorf("graphQL body does not contain expectedHeadOid %q: %s", rw.BaseCommit, fake.graphQLBody)
	}

	// Assert GraphQL body contains the file additions (base64-encoded content).
	featureB64 := base64.StdEncoding.EncodeToString([]byte("hello\n"))
	notesB64 := base64.StdEncoding.EncodeToString([]byte("notes\n"))
	if !strings.Contains(fake.graphQLBody, featureB64) {
		t.Errorf("graphQL body does not contain base64 of feature.txt")
	}
	if !strings.Contains(fake.graphQLBody, notesB64) {
		t.Errorf("graphQL body does not contain base64 of notes.txt")
	}

	// Assert fake was called the right number of times.
	if fake.createRefCalled != 1 {
		t.Errorf("create-ref called %d times, want 1", fake.createRefCalled)
	}
	if fake.graphQLCalled != 1 {
		t.Errorf("graphQL called %d times, want 1", fake.graphQLCalled)
	}

	// Assert ReconcilePublishedFruit with the returned OID succeeds and moves
	// the linked worktree to the published commit.
	if err := rw.ReconcilePublishedFruit(ctx, gotOID); err != nil {
		t.Fatalf("ReconcilePublishedFruit after publishManagedAPIFruit: %v", err)
	}
	afterOID, err := runGitCommand(ctx, rw.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse after reconcile: %v", err)
	}
	if strings.TrimSpace(afterOID) != gotOID {
		t.Errorf("worktree HEAD = %q after reconcile, want %q", strings.TrimSpace(afterOID), gotOID)
	}
	if _, err := os.Stat(filepath.Join(rw.Path, "feature.txt")); err != nil {
		t.Fatalf("feature.txt missing after reconciliation: %v", err)
	}

	// After reconciliation the worktree must be clean: the published commit
	// contains exactly the files that were committed, so git status should
	// report nothing. This is the state that Cleanup must be able to handle
	// directly — no manual reset is required or permitted.
	statusOut, err := runGitCommandRawOutput(ctx, rw.Path, "status", "--porcelain", "-uall", "-z")
	if err != nil {
		t.Fatalf("git status after reconcile: %v", err)
	}
	if statusOut != "" {
		t.Errorf("workspace is not clean after ReconcilePublishedFruit (git status: %q)", statusOut)
	}

	// Cleanup the worktree in the real post-publication state (reconciled to the
	// Fruit commit). The branch carries committed Fruit so Cleanup must retain it
	// rather than reclaiming it.
	if err := rw.Cleanup(ctx, ResolvedCredential{}); err != nil {
		t.Fatalf("Cleanup in post-publication state: %v", err)
	}

	// The disposable worktree directory must be gone.
	if _, err := os.Lstat(rw.Path); !os.IsNotExist(err) {
		t.Errorf("worktree path %q still exists after Cleanup", rw.Path)
	}

	// The contract: published remote Fruit OID == retained local Fruit branch OID.
	// Cleanup must NOT delete or move the branch because it carries committed Fruit.
	retainedOID, err := runGitCommand(ctx, rw.Repository, "rev-parse", rw.Branch)
	if err != nil {
		t.Fatalf("rev-parse retained Fruit branch after Cleanup: %v", err)
	}
	if strings.TrimSpace(retainedOID) != gotOID {
		t.Errorf("retained local Fruit branch = %q, want published OID %q", strings.TrimSpace(retainedOID), gotOID)
	}
}

// ----------------------------------------------------------------------------
// RunSprout routing tests
// ----------------------------------------------------------------------------

// TestPublishManagedAPIFruitRoutesThroughFn verifies that the managed remote

// run path calls publishManagedAPIFruitFn (not commitTerrariumExecutionFn) when
// CommitMode is api and the run is both managed and remoteClone.
func TestPublishManagedAPIFruitRoutesThroughFn(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)

	repository := prepareManagedRunRepository(t)

	// Write a substrates YAML pointing at the local repo with commit: api and
	// auth: app.
	writeSubstratesYAML(t, filepath.Join(mustGetwd(), "substrates.yaml"),
		"substrates:\n  apifruit:\n    url: "+repository+"\n    branch: main\n    checkout:\n      mode: managed\n    commit: api\n    auth:\n      method: app\n      appId: \"1234\"\n      privateKeyPath: "+keyPath+"\n")

	// Redirect GitHub REST/GraphQL to a test server that hands out tokens and
	// no-ops GraphQL calls. This prevents real network calls during App auth.
	fake := startAPIFruitFake(t, http.StatusCreated, "")
	_ = fake

	// Stub materializeManagedCheckoutFn so the actual clone uses the local
	// repo URL without needing HTTPS credentials.
	origMaterialize := materializeManagedCheckoutFn
	t.Cleanup(func() { materializeManagedCheckoutFn = origMaterialize })
	materializeManagedCheckoutFn = func(name, dest, url, branch string, _ ResolvedCredential, _ []string) error {
		ctx := context.Background()
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if _, err := runGitCommand(ctx, filepath.Dir(dest), "clone", "-q", repository, dest); err != nil {
			return err
		}
		return nil
	}

	stepID := "api-fruit-route"
	runner := newManagedWritingRunner("fruit.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	// Stub publishManagedAPIFruitFn and commitTerrariumExecutionFn.
	var apiPublishCalled bool
	var commitCalled bool
	origPublish := publishManagedAPIFruitFn
	origCommit := commitTerrariumExecutionFn
	t.Cleanup(func() {
		publishManagedAPIFruitFn = origPublish
		commitTerrariumExecutionFn = origCommit
	})
	publishManagedAPIFruitFn = func(_ context.Context, _ string, _ sproutExecutionStatus, _ string, _ *substrateExecutionPlan, _ RunWorkspace) (string, error) {
		apiPublishCalled = true
		return "api-oid-deadbeef", nil
	}
	commitTerrariumExecutionFn = func(_ context.Context, _, _, _ string, _ sproutExecutionStatus, _ string, _ ResolvedCredential) (string, error) {
		commitCalled = true
		return "local-oid", nil
	}

	// Stub pushTerrariumCommitFn — must not be called on the api path.
	var pushCalled bool
	origPush := pushTerrariumCommitFn
	t.Cleanup(func() { pushTerrariumCommitFn = origPush })
	pushTerrariumCommitFn = func(_ context.Context, _, _ string, _ ResolvedCredential, _ bool, _ string) error {
		pushCalled = true
		return nil
	}

	// ReconcilePublishedFruit will fail (no real remote Fruit branch) but
	// the routing assertions are what matter.
	_, _ = (&DockerOrchestrator{
		Substrate:        "apifruit",
		StepID:           stepID,
		DisableMergeBack: false,
	}).RunSprout(context.Background(), "api fruit route test")

	if !apiPublishCalled {
		t.Errorf("publishManagedAPIFruitFn was not called for commit:api managed remote run")
	}
	if commitCalled {
		t.Errorf("commitTerrariumExecutionFn was called; want only publishManagedAPIFruitFn for commit:api path")
	}
	if pushCalled {
		t.Errorf("pushTerrariumCommitFn was called; want reconciliation only for commit:api path")
	}
}

// TestPublishManagedAPIFruitFallsBackToLocalForNonAPIMode verifies that when
// CommitMode is NOT api, the existing commitTerrariumExecutionFn path is taken
// (the new api branch is not entered).
func TestPublishManagedAPIFruitFallsBackToLocalForNonAPIMode(t *testing.T) {
	repository := prepareManagedRunRepository(t)
	stepID := "local-commit-route"
	runner := newManagedWritingRunner("local.txt")
	runner.releaseRun()
	capture := newManagedRunCapture()
	installManagedRunSeams(t, capture, map[string]sproutRunner{stepID: runner})

	var apiPublishCalled bool
	origPublish := publishManagedAPIFruitFn
	t.Cleanup(func() { publishManagedAPIFruitFn = origPublish })
	publishManagedAPIFruitFn = func(_ context.Context, _ string, _ sproutExecutionStatus, _ string, _ *substrateExecutionPlan, _ RunWorkspace) (string, error) {
		apiPublishCalled = true
		return "api-oid", nil
	}

	// Substrate with NO commit:api — local commit mode is the default.
	_, err := (&DockerOrchestrator{
		Substrate:        repository,
		StepID:           stepID,
		DisableMergeBack: true, // avoid merge-back path to keep test simple.
	}).RunSprout(context.Background(), "local commit path")
	if err != nil {
		t.Fatalf("RunSprout: %v", err)
	}

	if apiPublishCalled {
		t.Errorf("publishManagedAPIFruitFn was called for non-api commit mode; want commitTerrariumExecutionFn")
	}
}

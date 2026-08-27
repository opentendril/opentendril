package conductor

import (
	"context"
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
	installTokenURL string // unused directly; for reference

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
		f.createRefBody = body
		if f.createRefStatus != 0 && f.createRefStatus != http.StatusCreated {
			w.WriteHeader(f.createRefStatus)
			return
		}
		w.WriteHeader(http.StatusCreated)

	case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
		f.graphQLCalled++
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

// TestGithubCreateRefSuccess verifies that githubCreateRef POSTs the correct
// JSON body and succeeds when the server returns 201 Created.
func TestGithubCreateRefSuccess(t *testing.T) {
	_, keyPath := genTestKeyPEM(t)
	fake := startAPIFruitFake(t, http.StatusCreated, "")

	// We call githubCreateRef directly; it only needs the token, not the App.
	if err := githubCreateRef(context.Background(), "owner", "repo", "sprout/task-x", "abc123", "ghs_tok"); err != nil {
		t.Fatalf("githubCreateRef succeeded: %v", err)
	}
	if fake.createRefCalled != 1 {
		t.Fatalf("create-ref endpoint called %d times, want 1", fake.createRefCalled)
	}

	// Verify the request body contains the expected ref and sha.
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

	// Silence unused variable warning.
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

// TestReconcilePublishedFruitFetchesAndResets verifies that ReconcilePublishedFruit
// fetches the Fruit branch from origin and hard-resets the worktree to the OID.
func TestReconcilePublishedFruitFetchesAndResets(t *testing.T) {
	ctx := context.Background()

	// Create the "remote" (origin) repository.
	origin := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "--bare", "-b", "main"},
	} {
		if _, err := runGitCommand(ctx, origin, args...); err != nil {
			t.Fatalf("init origin: %v", err)
		}
	}

	// Create a local repository that cloned from origin.
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

	// Commit the seed file and push to origin.
	if err := os.WriteFile(filepath.Join(local, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	for _, args := range [][]string{
		{"add", "seed.txt"},
		{"commit", "-q", "-m", "seed"},
		{"push", "-q", "origin", "main"},
	} {
		if _, err := runGitCommand(ctx, local, args...); err != nil {
			t.Fatalf("seed commit/push: %v", err)
		}
	}
	baseOID, err := runGitCommand(ctx, local, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}

	// Simulate the GitHub API having created a Fruit branch on origin (off the
	// base commit) and then committed the Sprout's changes to it.
	fruitBranch := "sprout/task-reconcile-test"
	if _, err := runGitCommand(ctx, local, "checkout", "-b", fruitBranch); err != nil {
		t.Fatalf("create fruit branch locally: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "sprout.txt"), []byte("sprout work\n"), 0o644); err != nil {
		t.Fatalf("write sprout file: %v", err)
	}
	for _, args := range [][]string{
		{"add", "sprout.txt"},
		{"commit", "-q", "-m", "api-published fruit"},
		{"push", "-q", "origin", fruitBranch},
	} {
		if _, err := runGitCommand(ctx, local, args...); err != nil {
			t.Fatalf("push fruit branch: %v", err)
		}
	}
	fruitOID, err := runGitCommand(ctx, local, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse fruit: %v", err)
	}

	// Create a linked worktree from the base commit to simulate the run workspace.
	worktreePath := t.TempDir()
	if _, err := runGitCommand(ctx, local, "worktree", "add", worktreePath, baseOID); err != nil {
		t.Fatalf("create run worktree: %v", err)
	}
	t.Cleanup(func() {
		_, _ = runGitCommand(ctx, local, "worktree", "remove", "--force", worktreePath)
	})

	rw := &RunWorkspace{
		Repository: local,
		Path:       worktreePath,
		Branch:     fruitBranch,
	}

	// Before reconciliation, the worktree is at baseOID.
	priorOID, err := runGitCommand(ctx, worktreePath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("pre-reconcile rev-parse: %v", err)
	}
	if strings.TrimSpace(priorOID) != strings.TrimSpace(baseOID) {
		t.Fatalf("pre-reconcile HEAD = %q, want base %q", priorOID, baseOID)
	}

	// Reconcile: should fetch the branch and reset to fruitOID.
	if err := rw.ReconcilePublishedFruit(ctx, fruitOID); err != nil {
		t.Fatalf("ReconcilePublishedFruit: %v", err)
	}

	afterOID, err := runGitCommand(ctx, worktreePath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("post-reconcile rev-parse: %v", err)
	}
	if strings.TrimSpace(afterOID) != strings.TrimSpace(fruitOID) {
		t.Fatalf("post-reconcile HEAD = %q, want %q", afterOID, fruitOID)
	}

	// The Sprout file should now be present in the worktree.
	if _, err := os.Stat(filepath.Join(worktreePath, "sprout.txt")); err != nil {
		t.Fatalf("sprout.txt missing after reconciliation: %v", err)
	}
}

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

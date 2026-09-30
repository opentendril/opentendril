package conductor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type delegatedAPIGitFixture struct {
	t          *testing.T
	repository string
	origin     string
	workspace  DelegatedWorkspace
	credential ResolvedCredential
	baseOID    string
	mainOID    string
	fake       *delegatedAPIGitForge
}

type delegatedAPIGitForge struct {
	t                 *testing.T
	repository        string
	origin            string
	refs              map[string]string
	createRefCalls    int
	graphQLCalls      int
	commitCount       int
	ambiguousRef      bool
	ambiguousGraphQL  bool
	ambiguousRefSpent bool
	ambiguousGQLSpent bool
}

func newDelegatedAPIGitFixture(t *testing.T, remoteFeature string) *delegatedAPIGitFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	origin := filepath.Join(root, "origin.git")
	if _, err := runGitCommand(ctx, "", "init", "--quiet", "--bare", "--initial-branch=main", origin); err != nil {
		t.Fatalf("initialize bare origin: %v", err)
	}
	if _, err := runGitCommand(ctx, "", "init", "--quiet", "--initial-branch=main", repository); err != nil {
		t.Fatalf("initialize Substrate: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.name", "Fixture Botanist"},
		{"config", "user.email", "botanist@example.invalid"},
		{"remote", "add", "origin", (&url.URL{Scheme: "file", Path: origin}).String()},
	} {
		if _, err := runGitCommand(ctx, repository, args...); err != nil {
			t.Fatalf("configure Substrate: git %s: %v", strings.Join(args, " "), err)
		}
	}
	for name, content := range map[string]string{"keep.txt": "original\n", "remove.txt": "remove\n"} {
		if err := os.WriteFile(filepath.Join(repository, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write base file %s: %v", name, err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "--quiet", "-m", "base"}, {"push", "--quiet", "-u", "origin", "main"}} {
		if _, err := runGitCommand(ctx, repository, args...); err != nil {
			t.Fatalf("prepare Substrate: git %s: %v", strings.Join(args, " "), err)
		}
	}
	baseOID, err := runGitCommand(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read base OID: %v", err)
	}
	baseOID = strings.TrimSpace(baseOID)

	workspace, err := ResolveDelegatedWorkspace(ctx, "demo", repository, "codex", ResolvedCredential{})
	if err != nil {
		t.Fatalf("resolve delegated workspace: %v", err)
	}
	if _, err := RunGitBranch(ctx, GitBranchExecution{Workspace: workspace.Path, Branch: "feat/api", ConfiguredBranch: "main"}); err != nil {
		t.Fatalf("create local feature branch: %v", err)
	}
	for name, content := range map[string]string{"keep.txt": "changed\n", "new.txt": "added\n"} {
		if err := os.WriteFile(filepath.Join(workspace.Path, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write workspace file %s: %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(workspace.Path, "remove.txt")); err != nil {
		t.Fatalf("delete workspace file: %v", err)
	}

	forge := &delegatedAPIGitForge{t: t, repository: repository, origin: origin, refs: map[string]string{"main": baseOID}}
	if remoteFeature == "base" {
		forge.seedRemoteRef(t, "feat/api", baseOID)
	}
	if remoteFeature == "conflict" {
		conflictOID := forge.commitTree(baseOID, "remote: conflicting work", nil, nil)
		forge.seedRemoteRef(t, "feat/api", conflictOID)
	}
	_, keyPath := genTestKeyPEM(t)
	fixture := &delegatedAPIGitFixture{
		t:          t,
		repository: repository,
		origin:     origin,
		workspace:  workspace,
		baseOID:    baseOID,
		mainOID:    baseOID,
		credential: ResolvedCredential{Method: CredentialApp, CommitMode: CommitModeAPI, App: AppCredential{AppID: "4276558", InstallationID: 99001, PrivateKeyPath: keyPath}},
		fake:       forge,
	}
	fixture.startForge()
	return fixture
}

func (fixture *delegatedAPIGitFixture) startForge() {
	fixture.t.Helper()
	server := httptest.NewServer(fixture.fake)
	fixture.t.Cleanup(server.Close)
	fixture.t.Cleanup(RedirectGitHubAPIBaseURL(server.URL))
	fixture.t.Cleanup(redirectGitHubGraphQLURL(server.URL + "/graphql"))
	resetGitHubAppTokenCache()
	fixture.t.Cleanup(resetGitHubAppTokenCache)
}

func (fixture *delegatedAPIGitFixture) execution(message string) GitCommitExecution {
	return GitCommitExecution{
		Workspace:        fixture.workspace.Path,
		Repository:       fixture.repository,
		Substrate:        "demo",
		Pollen:           fixture.workspace.Pollen,
		Message:          message,
		Credential:       fixture.credential,
		ConfiguredBranch: "main",
	}
}

func (fixture *delegatedAPIGitFixture) startOID(branch string) string {
	fixture.t.Helper()
	out, err := runGitCommand(context.Background(), fixture.origin, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		fixture.t.Fatalf("read remote %s OID: %v", branch, err)
	}
	return strings.TrimSpace(out)
}

func (fixture *delegatedAPIGitFixture) resetWorkspaceChanges(t *testing.T) {
	t.Helper()
	for _, args := range [][]string{{"reset", "--hard", "HEAD"}, {"clean", "-fd"}} {
		if _, err := runGitCommand(context.Background(), fixture.workspace.Path, args...); err != nil {
			t.Fatalf("reset fixture workspace: git %s: %v", strings.Join(args, " "), err)
		}
	}
}

func (f *delegatedAPIGitForge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.Contains(r.URL.Path, "/access_tokens") && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_delegated_test", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
	case strings.Contains(r.URL.Path, "/git/refs") && r.Method == http.MethodPost:
		f.createRef(w, r)
	case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
		f.createCommit(w, r)
	case strings.Contains(r.URL.Path, "/git/ref/heads/") && r.Method == http.MethodGet:
		branch := strings.TrimPrefix(r.URL.Path, strings.SplitN(r.URL.Path, "/git/ref/heads/", 2)[0]+"/git/ref/heads/")
		if oid := f.refs[branch]; oid != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": oid, "type": "commit"}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.Contains(r.URL.Path, "/git/commits/") && r.Method == http.MethodGet:
		f.writeCommit(w, strings.TrimPrefix(r.URL.Path, strings.SplitN(r.URL.Path, "/git/commits/", 2)[0]+"/git/commits/"))
	case strings.Contains(r.URL.Path, "/git/trees/") && r.Method == http.MethodGet:
		f.writeTree(w, strings.TrimPrefix(r.URL.Path, strings.SplitN(r.URL.Path, "/git/trees/", 2)[0]+"/git/trees/"))
	case strings.Contains(r.URL.Path, "/git/blobs/") && r.Method == http.MethodGet:
		f.writeBlob(w, strings.TrimPrefix(r.URL.Path, strings.SplitN(r.URL.Path, "/git/blobs/", 2)[0]+"/git/blobs/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *delegatedAPIGitForge) createRef(w http.ResponseWriter, r *http.Request) {
	f.createRefCalls++
	var body struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("decode ref creation: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	branch := strings.TrimPrefix(body.Ref, "refs/heads/")
	if f.refs[branch] != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	f.seedRemoteRef(f.t, branch, body.SHA)
	if f.ambiguousRef && !f.ambiguousRefSpent {
		f.ambiguousRefSpent = true
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (f *delegatedAPIGitForge) createCommit(w http.ResponseWriter, r *http.Request) {
	f.graphQLCalls++
	var request struct {
		Variables struct {
			Input createCommitOnBranchInput `json:"input"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		f.t.Errorf("decode commit mutation: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	input := request.Variables.Input
	branch := input.Branch.BranchName
	if f.refs[branch] != input.ExpectedHeadOid {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": "expected head changed"}}})
		return
	}
	oid := f.commitTree(input.ExpectedHeadOid, input.Message.Headline, input.FileChanges.Additions, input.FileChanges.Deletions)
	f.seedRemoteRef(f.t, branch, oid)
	f.commitCount++
	if f.ambiguousGraphQL && !f.ambiguousGQLSpent {
		f.ambiguousGQLSpent = true
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": "response lost after commit"}}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"createCommitOnBranch": map[string]any{"commit": map[string]any{"oid": oid}}}})
}

func (f *delegatedAPIGitForge) seedRemoteRef(t *testing.T, branch, oid string) {
	t.Helper()
	if _, err := runGitCommand(context.Background(), f.repository, "push", "--quiet", "origin", oid+":refs/heads/"+branch); err != nil {
		t.Fatalf("seed fake remote ref %s: %v", branch, err)
	}
	f.refs[branch] = oid
}

func (f *delegatedAPIGitForge) commitTree(base, message string, additions []apiCommitFileAddition, deletions []apiCommitFileDeletion) string {
	f.t.Helper()
	index := filepath.Join(f.t.TempDir(), "index")
	run := func(args []string, input []byte, env ...string) string {
		f.t.Helper()
		cmd := exec.Command("git", append([]string{"-C", f.repository}, args...)...)
		cmd.Env = append(os.Environ(), append([]string{"GIT_INDEX_FILE=" + index}, env...)...)
		if input != nil {
			cmd.Stdin = strings.NewReader(string(input))
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run([]string{"read-tree", base}, nil)
	for _, addition := range additions {
		contents, err := base64.StdEncoding.DecodeString(addition.Contents)
		if err != nil {
			f.t.Fatalf("decode API addition: %v", err)
		}
		blob := run([]string{"hash-object", "-w", "--stdin"}, contents)
		run([]string{"update-index", "--add", "--cacheinfo", "100644," + blob + "," + addition.Path}, nil)
	}
	for _, deletion := range deletions {
		run([]string{"update-index", "--force-remove", "--", deletion.Path}, nil)
	}
	tree := run([]string{"write-tree"}, nil)
	args := []string{"commit-tree", tree, "-p", base, "-m", message}
	return run(args, nil,
		"GIT_AUTHOR_NAME=GitHub",
		"GIT_AUTHOR_EMAIL=noreply@github.com",
		"GIT_COMMITTER_NAME=GitHub",
		"GIT_COMMITTER_EMAIL=noreply@github.com",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	)
}

func (f *delegatedAPIGitForge) writeCommit(w http.ResponseWriter, oid string) {
	message := f.git("show", "-s", "--format=%B", oid)
	tree := f.git("show", "-s", "--format=%T", oid)
	parents := strings.Fields(f.git("show", "-s", "--format=%P", oid))
	parentRows := make([]map[string]string, 0, len(parents))
	for _, parent := range parents {
		parentRows = append(parentRows, map[string]string{"sha": parent})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sha":     oid,
		"commit":  map[string]any{"message": strings.TrimSuffix(message, "\n"), "tree": map[string]string{"sha": tree}},
		"parents": parentRows,
	})
}

func (f *delegatedAPIGitForge) writeTree(w http.ResponseWriter, oid string) {
	raw := f.git("ls-tree", "-r", "-z", oid)
	entries := []map[string]string{}
	for _, item := range strings.Split(raw, "\x00") {
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "\t", 2)
		metadata := strings.Fields(parts[0])
		if len(metadata) != 3 {
			f.t.Errorf("unexpected git ls-tree entry: %q", item)
			continue
		}
		entries = append(entries, map[string]string{"path": parts[1], "mode": metadata[0], "type": metadata[1], "sha": metadata[2]})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"tree": entries, "truncated": false})
}

func (f *delegatedAPIGitForge) writeBlob(w http.ResponseWriter, oid string) {
	cmd := exec.Command("git", "-C", f.repository, "cat-file", "blob", oid)
	data, err := cmd.Output()
	if err != nil {
		f.t.Errorf("read fake forge blob: %v", err)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64"})
}

func (f *delegatedAPIGitForge) git(args ...string) string {
	f.t.Helper()
	out, err := runGitCommand(context.Background(), f.repository, args...)
	if err != nil {
		f.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func TestDelegatedAPICommitCreatesAbsentFeatureBranchAndReconcilesWorkspace(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "absent")
	if _, err := runGitCommand(context.Background(), f.origin, "rev-parse", "refs/heads/feat/api"); err == nil {
		t.Fatal("git.branch created the remote feature ref; it must remain local-only")
	}

	result, err := RunGitCommit(context.Background(), f.execution("feat: delegated API commit"))
	if err != nil {
		t.Fatalf("delegated API commit: %v", err)
	}
	if result.Status != "committed" || result.CommitHash == "" {
		t.Fatalf("result = %+v, want an authoritative commit", result)
	}
	if f.startOID("feat/api") != result.CommitHash {
		t.Fatalf("remote feature tip = %s, want %s", f.startOID("feat/api"), result.CommitHash)
	}
	if got := f.startOID("main"); got != f.mainOID {
		t.Fatalf("default branch moved to %s, want %s", got, f.mainOID)
	}
	status, err := RunGitStatus(context.Background(), GitStatusExecution{Workspace: f.workspace.Path, ConfiguredBranch: "main"})
	if err != nil {
		t.Fatalf("post-commit git.status: %v", err)
	}
	if status.Branch != "feat/api" || status.Head != result.CommitHash || !status.Clean {
		t.Fatalf("post-commit status = %+v, want same feature branch, returned HEAD, and clean workspace", status)
	}
	if f.fake.createRefCalls != 1 || f.fake.graphQLCalls != 1 || f.fake.commitCount != 1 {
		t.Fatalf("mutations = create-ref:%d GraphQL:%d commits:%d, want 1/1/1", f.fake.createRefCalls, f.fake.graphQLCalls, f.fake.commitCount)
	}

	push, err := RunGitPush(context.Background(), GitPushExecution{Workspace: f.workspace.Path, Credential: f.credential})
	if err != nil {
		t.Fatalf("subsequent ordinary up-to-date push: %v", err)
	}
	if push.Status != "pushed" || push.Branch != "feat/api" {
		t.Fatalf("push = %+v, want successful push of the same branch", push)
	}
	if f.startOID("feat/api") != result.CommitHash || f.fake.commitCount != 1 {
		t.Fatal("subsequent git.push changed the already-published commit")
	}
}

func TestDelegatedAPICommitPreservesUnselectedChangesForPathLimitedCommits(t *testing.T) {
	cases := []struct {
		name           string
		selectedPath   string
		unselectedPath string
		prepare        func(*testing.T, *delegatedAPIGitFixture)
		unselectedData []byte
	}{
		{
			name:           "selected modification and unselected modification",
			selectedPath:   "keep.txt",
			unselectedPath: "remove.txt",
			unselectedData: []byte("unselected modification\n"),
			prepare: func(t *testing.T, f *delegatedAPIGitFixture) {
				writeWorkspaceFile(t, f.workspace.Path, "keep.txt", []byte("selected modification\n"))
				writeWorkspaceFile(t, f.workspace.Path, "remove.txt", []byte("unselected modification\n"))
			},
		},
		{
			name:           "selected addition and unselected modification",
			selectedPath:   "new.txt",
			unselectedPath: "keep.txt",
			unselectedData: []byte("unselected modification\n"),
			prepare: func(t *testing.T, f *delegatedAPIGitFixture) {
				writeWorkspaceFile(t, f.workspace.Path, "new.txt", []byte("selected addition\n"))
				writeWorkspaceFile(t, f.workspace.Path, "keep.txt", []byte("unselected modification\n"))
			},
		},
		{
			name:           "selected deletion and unselected modification",
			selectedPath:   "remove.txt",
			unselectedPath: "keep.txt",
			unselectedData: []byte("unselected modification\n"),
			prepare: func(t *testing.T, f *delegatedAPIGitFixture) {
				if err := os.Remove(filepath.Join(f.workspace.Path, "remove.txt")); err != nil {
					t.Fatalf("delete selected path: %v", err)
				}
				writeWorkspaceFile(t, f.workspace.Path, "keep.txt", []byte("unselected modification\n"))
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newDelegatedAPIGitFixture(t, "absent")
			f.resetWorkspaceChanges(t)
			testCase.prepare(t, f)
			execution := f.execution("feat: commit selected paths")
			execution.Paths = []string{testCase.selectedPath}

			result, err := RunGitCommit(context.Background(), execution)
			if err != nil {
				t.Fatalf("path-limited delegated API commit: %v", err)
			}
			if result.Status != "committed" || result.CommitHash == "" {
				t.Fatalf("result = %+v, want authoritative commit", result)
			}
			if got := f.startOID("feat/api"); got != result.CommitHash {
				t.Fatalf("remote tip = %s, want returned commit %s", got, result.CommitHash)
			}
			if got, err := runGitCommand(context.Background(), f.workspace.Path, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(got) != result.CommitHash {
				t.Fatalf("local HEAD = %q, err=%v; want authoritative commit %s", strings.TrimSpace(got), err, result.CommitHash)
			}

			remotePaths, err := runGitCommand(context.Background(), f.origin, "diff", "--name-only", "--no-renames", f.baseOID, result.CommitHash)
			if err != nil {
				t.Fatalf("inspect remote commit paths: %v", err)
			}
			if got := strings.Fields(remotePaths); len(got) != 1 || got[0] != testCase.selectedPath {
				t.Fatalf("remote commit paths = %q, want only %q", got, testCase.selectedPath)
			}
			if got := f.startOID("main"); got != f.mainOID {
				t.Fatalf("default branch moved to %s, want %s", got, f.mainOID)
			}

			unselected, err := os.ReadFile(filepath.Join(f.workspace.Path, testCase.unselectedPath))
			if err != nil || !bytes.Equal(unselected, testCase.unselectedData) {
				t.Fatalf("unselected %s = %q, err=%v; want unchanged bytes %q", testCase.unselectedPath, unselected, err, testCase.unselectedData)
			}
			status, err := RunGitStatus(context.Background(), GitStatusExecution{Workspace: f.workspace.Path, ConfiguredBranch: "main"})
			if err != nil {
				t.Fatalf("post-commit git.status: %v", err)
			}
			if status.Branch != "feat/api" || status.Head != result.CommitHash || status.Clean || len(status.Changes) != 1 || status.Changes[0].Path != testCase.unselectedPath {
				t.Fatalf("post-commit status = %+v, want only unchanged unselected path %s", status, testCase.unselectedPath)
			}
			if f.fake.graphQLCalls != 1 || f.fake.commitCount != 1 {
				t.Fatalf("remote mutations = GraphQL:%d commits:%d, want one selected commit", f.fake.graphQLCalls, f.fake.commitCount)
			}
		})
	}
}

func writeWorkspaceFile(t *testing.T, workspace, name string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, name), contents, 0o644); err != nil {
		t.Fatalf("write workspace file %s: %v", name, err)
	}
}

func TestDelegatedAPICommitAcceptsMatchingRemoteFeatureBranch(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "base")
	result, err := RunGitCommit(context.Background(), f.execution("feat: existing exact branch"))
	if err != nil {
		t.Fatalf("delegated API commit: %v", err)
	}
	if result.Status != "committed" || f.fake.createRefCalls != 0 || f.startOID("feat/api") != result.CommitHash {
		t.Fatalf("result = %+v, create-ref calls = %d, remote = %s", result, f.fake.createRefCalls, f.startOID("feat/api"))
	}
	if got := f.startOID("main"); got != f.mainOID {
		t.Fatalf("default branch moved to %s, want %s", got, f.mainOID)
	}
}

func TestDelegatedAPICommitRejectsConflictingRemoteFeatureBranch(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "conflict")
	conflictOID := f.startOID("feat/api")
	if _, err := RunGitCommit(context.Background(), f.execution("feat: must not move conflict")); err == nil {
		t.Fatal("conflicting remote feature branch was accepted")
	}
	if f.startOID("feat/api") != conflictOID {
		t.Fatal("conflicting remote feature branch was moved")
	}
	if f.fake.createRefCalls != 0 || f.fake.graphQLCalls != 0 {
		t.Fatalf("mutations = create-ref:%d GraphQL:%d, want no remote mutation", f.fake.createRefCalls, f.fake.graphQLCalls)
	}
	if got := f.startOID("main"); got != f.mainOID {
		t.Fatalf("default branch moved to %s, want %s", got, f.mainOID)
	}
}

func TestDelegatedAPICommitReconcilesAmbiguousBranchCreationBeforeCommit(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "absent")
	f.fake.ambiguousRef = true
	result, err := RunGitCommit(context.Background(), f.execution("feat: reconcile created branch"))
	if err != nil {
		t.Fatalf("delegated API commit: %v", err)
	}
	if result.Status != "committed" || f.fake.createRefCalls != 1 || f.fake.graphQLCalls != 1 || f.fake.commitCount != 1 {
		t.Fatalf("result = %+v; create-ref:%d GraphQL:%d commits:%d", result, f.fake.createRefCalls, f.fake.graphQLCalls, f.fake.commitCount)
	}
	if got := f.startOID("main"); got != f.mainOID {
		t.Fatalf("default branch moved to %s, want %s", got, f.mainOID)
	}
}

func TestDelegatedAPICommitReconcilesAmbiguousCommitWithoutDuplicate(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "base")
	f.fake.ambiguousGraphQL = true
	result, err := RunGitCommit(context.Background(), f.execution("feat: ambiguous response"))
	if err != nil {
		t.Fatalf("delegated API commit: %v", err)
	}
	if result.CommitHash == "" || f.fake.graphQLCalls != 1 || f.fake.commitCount != 1 {
		t.Fatalf("result = %+v; GraphQL calls:%d commits:%d, want one exact idempotent recovery", result, f.fake.graphQLCalls, f.fake.commitCount)
	}
	if status, statusErr := RunGitStatus(context.Background(), GitStatusExecution{Workspace: f.workspace.Path, ConfiguredBranch: "main"}); statusErr != nil || status.Head != result.CommitHash || !status.Clean {
		t.Fatalf("post-commit status = %+v, err=%v", status, statusErr)
	}
}

func TestDelegatedAPICommitRecognizesExactExistingIntentIdempotently(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "base")
	additions, deletions, err := apiCommitFileChangesFromWorkspace(context.Background(), f.workspace.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	remoteOID := f.fake.commitTree(f.baseOID, "feat: already committed", additions, deletions)
	f.fake.seedRemoteRef(t, "feat/api", remoteOID)
	result, err := RunGitCommit(context.Background(), f.execution("feat: already committed"))
	if err != nil {
		t.Fatalf("idempotent delegated API commit: %v", err)
	}
	if result.CommitHash != remoteOID || f.fake.graphQLCalls != 0 || f.fake.commitCount != 0 {
		t.Fatalf("result = %+v; GraphQL calls:%d fake mutations:%d", result, f.fake.graphQLCalls, f.fake.commitCount)
	}
	status, err := RunGitStatus(context.Background(), GitStatusExecution{Workspace: f.workspace.Path, ConfiguredBranch: "main"})
	if err != nil || status.Branch != "feat/api" || status.Head != remoteOID || !status.Clean {
		t.Fatalf("status = %+v, err=%v; want exact clean idempotent recovery", status, err)
	}
}

func TestDelegatedAPICommitRecoversAfterLocalReconciliationFailure(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "absent")
	f.resetWorkspaceChanges(t)
	writeWorkspaceFile(t, f.workspace.Path, "keep.txt", []byte("selected change\n"))
	writeWorkspaceFile(t, f.workspace.Path, "remove.txt", []byte("preserved unselected change\n"))
	execution := f.execution("feat: recover local reconciliation")
	execution.Paths = []string{"keep.txt"}
	unselectedBefore, err := os.ReadFile(filepath.Join(f.workspace.Path, "remove.txt"))
	if err != nil {
		t.Fatalf("read unselected work before commit: %v", err)
	}
	original := runGitAPICommitReconcileCommandFn
	failed := false
	runGitAPICommitReconcileCommandFn = func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
		if !failed && len(args) > 0 && args[0] == "fetch" {
			failed = true
			return "", errors.New("injected local reconciliation failure")
		}
		return original(ctx, dir, env, args...)
	}
	t.Cleanup(func() { runGitAPICommitReconcileCommandFn = original })
	if _, err := RunGitCommit(context.Background(), execution); err == nil {
		t.Fatal("commit reported success despite local reconciliation failure")
	}
	if f.fake.commitCount != 1 || f.fake.graphQLCalls != 1 {
		t.Fatalf("first call mutations = GraphQL:%d commits:%d, want exactly one remote commit", f.fake.graphQLCalls, f.fake.commitCount)
	}
	result, err := RunGitCommit(context.Background(), execution)
	if err != nil {
		t.Fatalf("recover delegated API commit: %v", err)
	}
	if result.Status != "committed" || f.fake.graphQLCalls != 1 || f.fake.commitCount != 1 {
		t.Fatalf("recovery = %+v; GraphQL:%d commits:%d, want no second remote mutation", result, f.fake.graphQLCalls, f.fake.commitCount)
	}
	status, err := RunGitStatus(context.Background(), GitStatusExecution{Workspace: f.workspace.Path, ConfiguredBranch: "main"})
	if err != nil || status.Branch != "feat/api" || status.Head != result.CommitHash || status.Clean || len(status.Changes) != 1 || status.Changes[0].Path != "remove.txt" {
		t.Fatalf("recovered status = %+v, err=%v", status, err)
	}
	selectedAdditions, selectedDeletions, err := apiCommitFileChangesFromWorkspace(context.Background(), f.workspace.Path, []string{"keep.txt"})
	if err != nil || len(selectedAdditions)+len(selectedDeletions) != 0 {
		t.Fatalf("selected path remains dirty after recovery: additions=%v deletions=%v err=%v", selectedAdditions, selectedDeletions, err)
	}
	unselectedAfter, err := os.ReadFile(filepath.Join(f.workspace.Path, "remove.txt"))
	if err != nil || !bytes.Equal(unselectedBefore, unselectedAfter) {
		t.Fatalf("unselected work changed during recovery: before=%q after=%q err=%v", unselectedBefore, unselectedAfter, err)
	}
	if got := f.startOID("main"); got != f.mainOID {
		t.Fatalf("default branch moved to %s, want %s", got, f.mainOID)
	}
}

func TestDelegatedAPICommitRetryRecognizesAlreadyReconciledHead(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "absent")
	f.resetWorkspaceChanges(t)
	writeWorkspaceFile(t, f.workspace.Path, "keep.txt", []byte("selected change\n"))
	writeWorkspaceFile(t, f.workspace.Path, "remove.txt", []byte("preserved unselected change\n"))
	execution := f.execution("feat: retry after mixed reconciliation")
	execution.Paths = []string{"keep.txt"}
	unselectedBefore, err := os.ReadFile(filepath.Join(f.workspace.Path, "remove.txt"))
	if err != nil {
		t.Fatalf("read unselected work before commit: %v", err)
	}
	original := runGitAPICommitReconcileCommandFn
	failed := false
	runGitAPICommitReconcileCommandFn = func(ctx context.Context, dir string, env []string, args ...string) (string, error) {
		if !failed && len(args) >= 2 && args[0] == "reset" && args[1] == "--mixed" {
			failed = true
			output, err := original(ctx, dir, env, args...)
			if err != nil {
				return output, err
			}
			return output, errors.New("injected failure after mixed reset")
		}
		return original(ctx, dir, env, args...)
	}
	t.Cleanup(func() { runGitAPICommitReconcileCommandFn = original })
	if _, err := RunGitCommit(context.Background(), execution); err == nil {
		t.Fatal("commit reported success despite a failed local reconciliation command")
	}
	remoteOID := f.startOID("feat/api")
	if head, err := runGitCommand(context.Background(), f.workspace.Path, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(head) != remoteOID {
		t.Fatalf("HEAD after injected failure = %q, err=%v; want remote OID %s", strings.TrimSpace(head), err, remoteOID)
	}
	if f.fake.graphQLCalls != 1 || f.fake.commitCount != 1 {
		t.Fatalf("first call mutations = GraphQL:%d commits:%d, want one commit", f.fake.graphQLCalls, f.fake.commitCount)
	}

	result, err := RunGitCommit(context.Background(), execution)
	if err != nil {
		t.Fatalf("retry after mixed reconciliation: %v", err)
	}
	if result.Status != "committed" || result.CommitHash != remoteOID || f.fake.graphQLCalls != 1 || f.fake.commitCount != 1 {
		t.Fatalf("retry = %+v; GraphQL:%d commits:%d, want exact idempotent recovery", result, f.fake.graphQLCalls, f.fake.commitCount)
	}
	status, err := RunGitStatus(context.Background(), GitStatusExecution{Workspace: f.workspace.Path, ConfiguredBranch: "main"})
	if err != nil || status.Head != remoteOID || status.Clean || len(status.Changes) != 1 || status.Changes[0].Path != "remove.txt" {
		t.Fatalf("post-retry status = %+v, err=%v", status, err)
	}
	unselectedAfter, err := os.ReadFile(filepath.Join(f.workspace.Path, "remove.txt"))
	if err != nil || !bytes.Equal(unselectedBefore, unselectedAfter) {
		t.Fatalf("unselected work changed during retry: before=%q after=%q err=%v", unselectedBefore, unselectedAfter, err)
	}
}

func TestDelegatedAPIPushRefusesUnexpectedRemoteAdvance(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "base")
	result, err := RunGitCommit(context.Background(), f.execution("feat: before external advance"))
	if err != nil {
		t.Fatalf("delegated API commit: %v", err)
	}
	advancedOID := f.fake.commitTree(result.CommitHash, "remote: concurrent advance", nil, nil)
	f.fake.seedRemoteRef(t, "feat/api", advancedOID)
	if _, err := RunGitPush(context.Background(), GitPushExecution{Workspace: f.workspace.Path, Credential: f.credential}); err == nil {
		t.Fatal("ordinary git.push overwrote an unexpectedly advanced remote branch")
	}
	if got := f.startOID("feat/api"); got != advancedOID {
		t.Fatalf("remote feature branch = %s, want preserved concurrent commit %s", got, advancedOID)
	}
	if got := f.startOID("main"); got != f.mainOID {
		t.Fatalf("default branch moved to %s, want %s", got, f.mainOID)
	}
}

func TestDelegatedLocalCommitModeRemainsLocal(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "absent")
	credential := ResolvedCredential{Method: CredentialNone, CommitMode: CommitModeLocal, Identity: ResolvedIdentity{Name: "Local Botanist", Email: "local@example.invalid"}}
	execution := f.execution("feat: unchanged local commit")
	execution.Credential = credential
	result, err := RunGitCommit(context.Background(), execution)
	if err != nil {
		t.Fatalf("local delegated commit: %v", err)
	}
	head, err := runGitCommand(context.Background(), f.workspace.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "committed" || result.CommitHash != strings.TrimSpace(head) || f.fake.createRefCalls != 0 || f.fake.graphQLCalls != 0 {
		t.Fatalf("result = %+v; local HEAD = %s; API ref/GraphQL calls = %d/%d", result, head, f.fake.createRefCalls, f.fake.graphQLCalls)
	}
	if _, err := runGitCommand(context.Background(), f.origin, "rev-parse", "refs/heads/feat/api"); err == nil {
		t.Fatal("local commit mode published the remote feature branch")
	}
	if got := f.startOID("main"); got != f.mainOID {
		t.Fatalf("default branch moved to %s, want %s", got, f.mainOID)
	}
}

func TestDelegatedLocalCommitPathsRemainLimited(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "absent")
	execution := f.execution("feat: local path-limited commit")
	execution.Credential = ResolvedCredential{Method: CredentialNone, CommitMode: CommitModeLocal, Identity: ResolvedIdentity{Name: "Local Botanist", Email: "local@example.invalid"}}
	execution.Paths = []string{"keep.txt"}
	result, err := RunGitCommit(context.Background(), execution)
	if err != nil {
		t.Fatalf("local path-limited commit: %v", err)
	}
	if result.Status != "committed" || result.CommitHash == "" {
		t.Fatalf("result = %+v, want local commit", result)
	}
	committedPaths, err := runGitCommand(context.Background(), f.workspace.Path, "show", "--format=", "--name-only", result.CommitHash)
	if err != nil {
		t.Fatalf("inspect local commit paths: %v", err)
	}
	if got := strings.Fields(committedPaths); len(got) != 1 || got[0] != "keep.txt" {
		t.Fatalf("local commit paths = %q, want only keep.txt", got)
	}
	status, err := RunGitStatus(context.Background(), GitStatusExecution{Workspace: f.workspace.Path, ConfiguredBranch: "main"})
	if err != nil {
		t.Fatalf("status after local path-limited commit: %v", err)
	}
	if status.Head != result.CommitHash || status.Clean || status.ChangeCount != 2 {
		t.Fatalf("local path-limited status = %+v, want HEAD at commit and two untouched unselected changes", status)
	}
	if _, err := os.Stat(filepath.Join(f.workspace.Path, "new.txt")); err != nil {
		t.Fatalf("unselected addition was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.workspace.Path, "remove.txt")); !os.IsNotExist(err) {
		t.Fatalf("unselected deletion was restored or inaccessible: err=%v", err)
	}
	if f.fake.createRefCalls != 0 || f.fake.graphQLCalls != 0 {
		t.Fatalf("local path-limited commit invoked API mutations: ref=%d GraphQL=%d", f.fake.createRefCalls, f.fake.graphQLCalls)
	}
}

func TestGitFetchStillLeavesDelegatedWorkspaceStateUntouched(t *testing.T) {
	f := newDelegatedAPIGitFixture(t, "absent")
	beforeHEAD := f.git(t, "rev-parse", "HEAD")
	beforeStatus := f.git(t, "status", "--porcelain", "-z")
	beforeBranch := f.git(t, "branch", "--show-current")
	url := f.git(t, "remote", "get-url", "origin")
	_, err := runGitFetchWithHooks(context.Background(), GitFetchExecution{
		Repository: f.repository,
		Substrate:  "demo",
		URL:        url,
		Credential: ResolvedCredential{Method: CredentialNone},
	}, gitFetchHooks{allowFileTransport: true})
	if err != nil {
		t.Fatalf("governed git.fetch: %v", err)
	}
	if got := f.git(t, "rev-parse", "HEAD"); got != beforeHEAD {
		t.Fatalf("git.fetch moved workspace HEAD from %s to %s", beforeHEAD, got)
	}
	if got := f.git(t, "status", "--porcelain", "-z"); got != beforeStatus {
		t.Fatalf("git.fetch changed workspace contents: before %q, after %q", beforeStatus, got)
	}
	if got := f.git(t, "branch", "--show-current"); got != beforeBranch {
		t.Fatalf("git.fetch changed workspace branch from %s to %s", beforeBranch, got)
	}
}

func (f *delegatedAPIGitFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runGitCommand(context.Background(), f.workspace.Path, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

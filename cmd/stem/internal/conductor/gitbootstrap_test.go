package conductor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPrepareGitBootstrapUsesConfiguredBranchBeforeDefaultAndInput(t *testing.T) {
	fake := &bootstrapGitHubFake{defaultBranch: "trunk", empty: true}
	startBootstrapGitHubFake(t, fake)

	plan, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec("release"), appReadinessCred(t), "operator-choice")
	if err != nil {
		t.Fatalf("PrepareGitBootstrap: %v", err)
	}
	if plan.Branch != "release" || plan.BranchSource != GitBootstrapBranchFromConfig {
		t.Fatalf("plan = %+v, want configured release branch", plan)
	}
}

func TestPrepareGitBootstrapUsesDefaultBeforeInput(t *testing.T) {
	fake := &bootstrapGitHubFake{defaultBranch: "trunk", empty: true}
	startBootstrapGitHubFake(t, fake)

	plan, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), appReadinessCred(t), "operator-choice")
	if err != nil {
		t.Fatalf("PrepareGitBootstrap: %v", err)
	}
	if plan.Branch != "trunk" || plan.BranchSource != GitBootstrapBranchFromDefault {
		t.Fatalf("plan = %+v, want repository default trunk branch", plan)
	}
}

func TestPrepareGitBootstrapUsesInputWhenNoDefaultExists(t *testing.T) {
	fake := &bootstrapGitHubFake{empty: true, noDefault: true}
	startBootstrapGitHubFake(t, fake)

	plan, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), appReadinessCred(t), "operator-choice")
	if err != nil {
		t.Fatalf("PrepareGitBootstrap: %v", err)
	}
	if plan.Branch != "operator-choice" || plan.BranchSource != GitBootstrapBranchFromInput {
		t.Fatalf("plan = %+v, want explicit operator branch", plan)
	}
}

func TestPrepareGitBootstrapRequiresBranchWhenNoApprovedSourceExists(t *testing.T) {
	fake := &bootstrapGitHubFake{empty: true, noDefault: true}
	startBootstrapGitHubFake(t, fake)

	_, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), appReadinessCred(t), "")
	if err == nil || !strings.Contains(err.Error(), "explicit Botanist branch") {
		t.Fatalf("error = %v, want explicit branch refusal", err)
	}
}

func TestPrepareGitBootstrapRefusesNonEmptyRepository(t *testing.T) {
	fake := &bootstrapGitHubFake{defaultBranch: "main"}
	startBootstrapGitHubFake(t, fake)

	_, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), appReadinessCred(t), "")
	if err == nil || !strings.Contains(err.Error(), "already contains commits") {
		t.Fatalf("error = %v, want non-empty refusal", err)
	}
}

func TestPrepareGitBootstrapRefusesExistingTargetRef(t *testing.T) {
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, targetExists: true}
	startBootstrapGitHubFake(t, fake)

	_, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), appReadinessCred(t), "")
	if err == nil || !strings.Contains(err.Error(), "target branch") {
		t.Fatalf("error = %v, want existing-target refusal", err)
	}
}

func TestPrepareGitBootstrapAuthenticatesBeforeRepositoryInspection(t *testing.T) {
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true}
	startBootstrapGitHubFake(t, fake)

	if _, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), appReadinessCred(t), ""); err != nil {
		t.Fatalf("PrepareGitBootstrap: %v", err)
	}
	firstRepositoryCall := -1
	firstAppCall := -1
	for i, call := range fake.calls {
		if call.Path == "/app" && firstAppCall == -1 {
			firstAppCall = i
		}
		if strings.HasPrefix(call.Path, "/repos/acme/widget") && firstRepositoryCall == -1 {
			firstRepositoryCall = i
		}
	}
	if firstAppCall == -1 || firstRepositoryCall == -1 || firstAppCall > firstRepositoryCall {
		t.Fatalf("calls = %+v, want App authentication before repository inspection", fake.calls)
	}
}

func TestPrepareGitBootstrapRefusesAppAuthenticationBeforeRepositoryInspection(t *testing.T) {
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, appStatus: http.StatusUnauthorized}
	startBootstrapGitHubFake(t, fake)

	_, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), appReadinessCred(t), "")
	if err == nil || !strings.Contains(err.Error(), "rejected the App credentials") {
		t.Fatalf("error = %v, want App authentication refusal", err)
	}
	for _, call := range fake.calls {
		if strings.HasPrefix(call.Path, "/repos/acme/widget") {
			t.Fatalf("App authentication failure inspected repository: %+v", fake.calls)
		}
	}
}

func TestPrepareGitBootstrapRedactsCredentialMaterialFromRemoteErrors(t *testing.T) {
	secret := "ghs_BOOTSTRAP_SECRET"
	fake := &bootstrapGitHubFake{
		defaultBranch: "main",
		empty:         true,
		appStatus:     http.StatusInternalServerError,
		appBody:       `{"message":"diagnostic ` + secret + `"}`,
	}
	startBootstrapGitHubFake(t, fake)

	_, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), appReadinessCred(t), "")
	if err == nil {
		t.Fatal("PrepareGitBootstrap unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed installation token: %v", err)
	}
}

func TestPrepareGitBootstrapRefusesUnsupportedPosturesBeforeRemoteInspection(t *testing.T) {
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true}
	startBootstrapGitHubFake(t, fake)

	cases := map[string]ResolvedCredential{
		"pat": {Method: CredentialPAT, TokenValue: "github_pat_test"},
		"ssh": {Method: CredentialSSH, SSHKeyPath: "/tmp/id"},
	}
	for name, cred := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), cred, "")
			if err == nil || !strings.Contains(err.Error(), "GitHub App") {
				t.Fatalf("error = %v, want unsupported GitHub App posture refusal", err)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("unsupported posture inspected remote: %+v", fake.calls)
			}
		})
	}
}

func TestPrepareGitBootstrapRefusesEmbeddedRemoteCredentialsBeforeRemoteInspection(t *testing.T) {
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true}
	startBootstrapGitHubFake(t, fake)

	spec := managedAPIWidgetSpec("")
	spec.URL = "https://ghs_BOOTSTRAP_SECRET@github.com/acme/widget"
	_, err := PrepareGitBootstrap(context.Background(), spec, appReadinessCred(t), "")
	if err == nil || !strings.Contains(err.Error(), "embedded credentials") {
		t.Fatalf("error = %v, want embedded-credential refusal", err)
	}
	if strings.Contains(err.Error(), "ghs_BOOTSTRAP_SECRET") {
		t.Fatalf("error exposed embedded credential: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("embedded-credential refusal inspected remote: %+v", fake.calls)
	}
}

func TestRunGitBootstrapCreatesOneEmptyRootCommitAndUsesAbsentLease(t *testing.T) {
	remote := newEmptyBootstrapRemote(t)
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, remote: remote}
	startBootstrapGitHubFake(t, fake)

	spec := managedAPIWidgetSpec("")
	spec.URL = fileBootstrapURL(remote)
	cred := appReadinessCred(t)
	cred.CommitMode = CommitModeAPI
	plan, err := PrepareGitBootstrap(context.Background(), spec, cred, "")
	if err != nil {
		t.Fatalf("PrepareGitBootstrap: %v", err)
	}

	oldCommand := runGitBootstrapCommandFn
	var pushArgs []string
	var pushEnv []string
	var workspace string
	var workspaceConfig []byte
	var commandOutput strings.Builder
	runGitBootstrapCommandFn = func(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
		workspace = dir
		if len(args) > 0 && args[0] == "push" {
			workspaceConfig, _ = os.ReadFile(filepath.Join(dir, ".git", "config"))
		}
		out, err := oldCommand(ctx, dir, env, input, args...)
		commandOutput.WriteString(out)
		if len(args) > 0 && args[0] == "push" {
			pushEnv = append([]string(nil), env...)
		}
		if len(args) > 0 && args[0] == "push" {
			pushArgs = append([]string(nil), args...)
		}
		return out, err
	}
	t.Cleanup(func() { runGitBootstrapCommandFn = oldCommand })

	result, err := RunGitBootstrap(context.Background(), plan)
	if err != nil {
		t.Fatalf("RunGitBootstrap: %v", err)
	}
	if result.Branch != "main" || result.CommitOID == "" {
		t.Fatalf("result = %+v, want main and a commit OID", result)
	}
	if !containsBootstrapArg(pushArgs, "--force-with-lease=refs/heads/main:") {
		t.Fatalf("push args = %v, want expected-absent target lease", pushArgs)
	}
	for _, arg := range pushArgs {
		if strings.Contains(arg, bootstrapInstallToken) || strings.Contains(arg, "--force") && !strings.Contains(arg, "--force-with-lease") {
			t.Fatalf("push args expose a secret or unconditional force: %v", pushArgs)
		}
	}
	if !containsBootstrapValue(pushEnv, bootstrapInstallToken) {
		t.Fatalf("push environment did not carry the short-lived installation token")
	}
	if strings.Contains(commandOutput.String(), bootstrapInstallToken) {
		t.Fatalf("git command output exposed installation token: %q", commandOutput.String())
	}
	if strings.Contains(string(workspaceConfig), bootstrapInstallToken) {
		t.Fatalf("temporary Git config exposed installation token: %q", workspaceConfig)
	}
	if workspace == "" {
		t.Fatal("bootstrap did not create a temporary Git workspace")
	}
	if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("temporary workspace still exists after bootstrap: %s (%v)", workspace, statErr)
	}

	tree, err := runGitCommand(context.Background(), remote, "cat-file", "-t", result.CommitOID)
	if err != nil {
		t.Fatalf("inspect bootstrap commit: %v", err)
	}
	if strings.TrimSpace(tree) != "commit" {
		t.Fatalf("object %s = %q, want commit", result.CommitOID, tree)
	}
	entries, err := runGitCommand(context.Background(), remote, "ls-tree", result.CommitOID)
	if err != nil {
		t.Fatalf("inspect bootstrap tree: %v", err)
	}
	if strings.TrimSpace(entries) != "" {
		t.Fatalf("bootstrap tree is not empty: %q", entries)
	}
	if got := fake.ref("main"); got != result.CommitOID {
		t.Fatalf("remote main = %q, want bootstrap OID %q", got, result.CommitOID)
	}
}

func TestRunGitBootstrapTargetRaceFailsClosedWithoutOverwrite(t *testing.T) {
	remote := newEmptyBootstrapRemote(t)
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, remote: remote}
	startBootstrapGitHubFake(t, fake)

	spec := managedAPIWidgetSpec("main")
	spec.URL = fileBootstrapURL(remote)
	cred := appReadinessCred(t)
	cred.CommitMode = CommitModeAPI
	plan := GitBootstrapPlan{Spec: spec, Credential: cred, Owner: "acme", Repo: "widget", Branch: "main", BranchSource: GitBootstrapBranchFromConfig}
	oldCommand := runGitBootstrapCommandFn
	runGitBootstrapCommandFn = func(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "push" {
			raceOID := bootstrapCommitForTest(t, remote, "concurrent target")
			if _, err := runGitCommand(context.Background(), remote, "update-ref", "refs/heads/main", raceOID); err != nil {
				t.Fatalf("create concurrent target ref: %v", err)
			}
		}
		return oldCommand(ctx, dir, env, input, args...)
	}
	t.Cleanup(func() { runGitBootstrapCommandFn = oldCommand })

	_, err := RunGitBootstrap(context.Background(), plan)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "concurrency") {
		t.Fatalf("error = %v, want target concurrency refusal", err)
	}
	if got, err := runGitCommand(context.Background(), remote, "rev-parse", "refs/heads/main"); err != nil || strings.TrimSpace(got) == "" {
		t.Fatalf("concurrent target ref was not preserved: %q, %v", got, err)
	}
}

func TestRunGitBootstrapPreservesUnrelatedConcurrentRefWhenReadinessAgrees(t *testing.T) {
	remote := newEmptyBootstrapRemote(t)
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, remote: remote}
	startBootstrapGitHubFake(t, fake)

	spec := managedAPIWidgetSpec("main")
	spec.URL = fileBootstrapURL(remote)
	cred := appReadinessCred(t)
	cred.CommitMode = CommitModeAPI
	plan := GitBootstrapPlan{Spec: spec, Credential: cred, Owner: "acme", Repo: "widget", Branch: "main", BranchSource: GitBootstrapBranchFromConfig}
	oldCommand := runGitBootstrapCommandFn
	runGitBootstrapCommandFn = func(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "push" {
			unrelatedOID := bootstrapCommitForTest(t, remote, "unrelated ref")
			if _, err := runGitCommand(context.Background(), remote, "update-ref", "refs/heads/unrelated", unrelatedOID); err != nil {
				t.Fatalf("create unrelated ref: %v", err)
			}
			fake.unrelatedOID = unrelatedOID
		}
		return oldCommand(ctx, dir, env, input, args...)
	}
	t.Cleanup(func() { runGitBootstrapCommandFn = oldCommand })

	result, err := RunGitBootstrap(context.Background(), plan)
	if err != nil {
		t.Fatalf("RunGitBootstrap: %v", err)
	}
	if got, err := runGitCommand(context.Background(), remote, "rev-parse", "refs/heads/unrelated"); err != nil || strings.TrimSpace(got) != fake.unrelatedOID {
		t.Fatalf("unrelated ref changed or disappeared: %q, %v", got, err)
	}
	if result.CommitOID != fake.ref("main") {
		t.Fatalf("main = %q, want exact bootstrap OID %q", fake.ref("main"), result.CommitOID)
	}
}

func TestRunGitBootstrapPostPublicationTargetMismatchFailsWithoutRepair(t *testing.T) {
	remote := newEmptyBootstrapRemote(t)
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, remote: remote, targetMismatch: true}
	startBootstrapGitHubFake(t, fake)

	spec := managedAPIWidgetSpec("main")
	spec.URL = fileBootstrapURL(remote)
	cred := appReadinessCred(t)
	plan := GitBootstrapPlan{Spec: spec, Credential: cred, Owner: "acme", Repo: "widget", Branch: "main", BranchSource: GitBootstrapBranchFromConfig}
	result, err := RunGitBootstrap(context.Background(), plan)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "concurrency") {
		t.Fatalf("result = %+v error = %v, want post-publication concurrency failure", result, err)
	}
}

func TestRunGitBootstrapPostPublicationReadinessMismatchFails(t *testing.T) {
	remote := newEmptyBootstrapRemote(t)
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, remote: remote, readinessMismatch: true}
	startBootstrapGitHubFake(t, fake)

	spec := managedAPIWidgetSpec("")
	spec.URL = fileBootstrapURL(remote)
	cred := appReadinessCred(t)
	plan := GitBootstrapPlan{Spec: spec, Credential: cred, Owner: "acme", Repo: "widget", Branch: "main", BranchSource: GitBootstrapBranchFromDefault}
	_, err := RunGitBootstrap(context.Background(), plan)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "concurrency") {
		t.Fatalf("error = %v, want readiness concurrency failure", err)
	}
}

func TestRunGitBootstrapPostPublicationVerificationValueMismatchFails(t *testing.T) {
	remote := newEmptyBootstrapRemote(t)
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, remote: remote}
	startBootstrapGitHubFake(t, fake)

	spec := managedAPIWidgetSpec("main")
	spec.URL = fileBootstrapURL(remote)
	cred := appReadinessCred(t)
	plan := GitBootstrapPlan{Spec: spec, Credential: cred, Owner: "acme", Repo: "widget", Branch: "main", BranchSource: GitBootstrapBranchFromConfig}
	oldVerify := verifyGitBootstrapSetupFn
	verifyGitBootstrapSetupFn = func(context.Context, SubstrateSpec, ResolvedCredential) (SubstrateSetupVerification, error) {
		return SubstrateSetupVerification{Managed: true, GitBase: SubstrateGitReadiness{Branch: "other", Commit: "other"}}, nil
	}
	t.Cleanup(func() { verifyGitBootstrapSetupFn = oldVerify })

	_, err := RunGitBootstrap(context.Background(), plan)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "concurrency") {
		t.Fatalf("error = %v, want readiness-value concurrency failure", err)
	}
}

func TestRunGitBootstrapUnrelatedRefReadinessMismatchPreservesRef(t *testing.T) {
	remote := newEmptyBootstrapRemote(t)
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, remote: remote, readinessMismatch: true}
	startBootstrapGitHubFake(t, fake)

	spec := managedAPIWidgetSpec("main")
	spec.URL = fileBootstrapURL(remote)
	cred := appReadinessCred(t)
	plan := GitBootstrapPlan{Spec: spec, Credential: cred, Owner: "acme", Repo: "widget", Branch: "main", BranchSource: GitBootstrapBranchFromConfig}
	oldCommand := runGitBootstrapCommandFn
	runGitBootstrapCommandFn = func(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "push" {
			unrelatedOID := bootstrapCommitForTest(t, remote, "unrelated ref")
			if _, err := runGitCommand(context.Background(), remote, "update-ref", "refs/heads/unrelated", unrelatedOID); err != nil {
				t.Fatalf("create unrelated ref: %v", err)
			}
			fake.unrelatedOID = unrelatedOID
		}
		return oldCommand(ctx, dir, env, input, args...)
	}
	t.Cleanup(func() { runGitBootstrapCommandFn = oldCommand })

	_, err := RunGitBootstrap(context.Background(), plan)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "concurrency") {
		t.Fatalf("error = %v, want readiness concurrency failure", err)
	}
	if got, refErr := runGitCommand(context.Background(), remote, "rev-parse", "refs/heads/unrelated"); refErr != nil || strings.TrimSpace(got) != fake.unrelatedOID {
		t.Fatalf("unrelated ref changed or disappeared: %q, %v", got, refErr)
	}
}

func TestRunGitBootstrapDoesNotRunForMissingContentsWrite(t *testing.T) {
	fake := &bootstrapGitHubFake{defaultBranch: "main", empty: true, contentsPermission: "read"}
	startBootstrapGitHubFake(t, fake)

	_, err := PrepareGitBootstrap(context.Background(), managedAPIWidgetSpec(""), appReadinessCred(t), "")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "contents write") {
		t.Fatalf("error = %v, want missing contents write refusal", err)
	}
	for _, call := range fake.calls {
		if call.Method != http.MethodGet {
			t.Fatalf("permission failure made mutating request: %+v", call)
		}
	}
}

const bootstrapInstallToken = "ghs_BOOTSTRAP_INSTALL_TOKEN"

type bootstrapGitHubFake struct {
	mu                 sync.Mutex
	calls              []recordedGitHubCall
	defaultBranch      string
	appStatus          int
	appBody            string
	contentsPermission string
	empty              bool
	noDefault          bool
	targetExists       bool
	remote             string
	unrelatedOID       string
	targetMismatch     bool
	readinessMismatch  bool
}

func startBootstrapGitHubFake(t *testing.T, fake *bootstrapGitHubFake) {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	t.Cleanup(RedirectGitHubAPIBaseURL(srv.URL))
}

func (f *bootstrapGitHubFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, recordedGitHubCall{Method: r.Method, Path: r.URL.Path, Authorization: r.Header.Get("Authorization")})
	f.mu.Unlock()

	switch {
	case r.URL.Path == "/app":
		if f.appStatus != 0 && f.appStatus != http.StatusOK {
			w.WriteHeader(f.appStatus)
			if f.appBody != "" {
				_, _ = w.Write([]byte(f.appBody))
			}
			return
		}
		_, _ = w.Write([]byte(`{"id":1}`))
	case strings.Contains(r.URL.Path, "/access_tokens"):
		_, _ = w.Write([]byte(`{"token":"` + bootstrapInstallToken + `"}`))
	case strings.HasSuffix(r.URL.Path, "/installation"):
		_, _ = w.Write([]byte(`{"id":42}`))
	case strings.HasPrefix(r.URL.Path, "/app/installations/"):
		permission := f.contentsPermission
		if permission == "" {
			permission = "write"
		}
		_, _ = fmt.Fprintf(w, `{"permissions":{"contents":%q}}`, permission)
	default:
		f.serveBootstrapRepository(w, r)
	}
}

func (f *bootstrapGitHubFake) serveBootstrapRepository(w http.ResponseWriter, r *http.Request) {
	owner, repo, rest, ok := parseReposAPIPath(r.URL.Path)
	if !ok || owner != "acme" || repo != "widget" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch {
	case rest == "":
		branch := f.defaultBranch
		if f.noDefault {
			branch = ""
		}
		_, _ = fmt.Fprintf(w, `{"default_branch":%q}`, branch)
	case rest == "commits":
		if f.isRemoteEmpty() {
			if f.empty {
				w.WriteHeader(http.StatusConflict)
				return
			}
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`[{"sha":"` + f.remoteRef(f.defaultBranch) + `"}]`))
	case strings.HasPrefix(rest, "commits/"):
		branch := strings.TrimPrefix(rest, "commits/")
		if f.readinessMismatch {
			branch = "unrelated"
		}
		if oid := f.remoteRef(branch); oid != "" {
			_, _ = fmt.Fprintf(w, `{"sha":%q}`, oid)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	case strings.HasPrefix(rest, "git/ref/heads/"):
		branch := strings.TrimPrefix(rest, "git/ref/heads/")
		if f.targetExists && branch == "main" {
			_, _ = fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"sha":%q,"type":"commit"}}`, strings.Repeat("e", 40))
			return
		}
		if oid := f.remoteRef(branch); oid != "" {
			if f.targetMismatch && branch == "main" {
				oid = strings.Repeat("f", 40)
			}
			_, _ = fmt.Fprintf(w, `{"ref":"refs/heads/%s","object":{"sha":%q,"type":"commit"}}`, branch, oid)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *bootstrapGitHubFake) isRemoteEmpty() bool {
	if f.remote == "" {
		return f.empty
	}
	_, err := runGitCommand(context.Background(), f.remote, "show-ref")
	return err != nil
}

func (f *bootstrapGitHubFake) remoteRef(branch string) string {
	if f.remote == "" {
		return ""
	}
	oid, err := runGitCommand(context.Background(), f.remote, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(oid)
}

func (f *bootstrapGitHubFake) ref(branch string) string {
	return f.remoteRef(branch)
}

func newEmptyBootstrapRemote(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "acme", "widget")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatalf("prepare bare remote parent: %v", err)
	}
	if _, err := runGitCommand(context.Background(), filepath.Dir(remote), "init", "--bare", remote); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}
	return remote
}

func fileBootstrapURL(remote string) string {
	return "file://" + remote
}

func bootstrapCommitForTest(t *testing.T, remote, message string) string {
	t.Helper()
	oid, err := runGitCommandWithEnv(context.Background(), remote, []string{
		"GIT_AUTHOR_NAME=Concurrent Test",
		"GIT_AUTHOR_EMAIL=concurrent@example.invalid",
		"GIT_COMMITTER_NAME=Concurrent Test",
		"GIT_COMMITTER_EMAIL=concurrent@example.invalid",
	}, "commit-tree", emptyGitTreeOID, "-m", message)
	if err != nil {
		t.Fatalf("create test commit: %v", err)
	}
	return strings.TrimSpace(oid)
}

func containsBootstrapArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsBootstrapValue(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

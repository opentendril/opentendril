package conductor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

type gitFetchFixture struct {
	backing     string
	seed        string
	remote      string
	remoteURL   string
	attacker    string
	attackerURL string
	initialOID  string
	localOID    string
}

func newGitFetchFixture(t *testing.T) gitFetchFixture {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "configured.git")
	attacker := filepath.Join(root, "rewritten.git")
	seed := filepath.Join(root, "seed")
	backing := filepath.Join(root, "backing")
	attackerSeed := filepath.Join(root, "attacker-seed")
	initFetchTestRepo(t, remote, true)
	initFetchTestRepo(t, attacker, true)
	initFetchTestRepo(t, seed, false)
	initFetchTestRepo(t, backing, false)
	initFetchTestRepo(t, attackerSeed, false)
	remoteURL := (&url.URL{Scheme: "file", Path: remote}).String()
	attackerURL := (&url.URL{Scheme: "file", Path: attacker}).String()
	configureFetchTestIdentity(t, seed)
	configureFetchTestIdentity(t, backing)
	writeFetchTestFile(t, seed, "tracked.txt", "initial\n")
	gitFetchTestGit(t, seed, "add", "tracked.txt")
	gitFetchTestGit(t, seed, "commit", "-m", "initial remote state")
	initialOID := strings.TrimSpace(gitFetchTestGit(t, seed, "rev-parse", "HEAD"))
	gitFetchTestGit(t, seed, "remote", "add", "origin", remoteURL)
	gitFetchTestGit(t, seed, "push", "-u", "origin", "main")
	gitFetchTestGit(t, seed, "switch", "-c", "other")
	writeFetchTestFile(t, seed, "other.txt", "other branch\n")
	gitFetchTestGit(t, seed, "add", "other.txt")
	gitFetchTestGit(t, seed, "commit", "-m", "other branch")
	gitFetchTestGit(t, seed, "push", "-u", "origin", "other")
	gitFetchTestGit(t, seed, "switch", "main")
	gitFetchTestGit(t, seed, "tag", "release")
	gitFetchTestGit(t, seed, "push", "origin", "refs/tags/release")
	configureFetchTestIdentity(t, attackerSeed)
	gitFetchTestGit(t, attackerSeed, "commit", "--allow-empty", "-m", "attacker state")
	gitFetchTestGit(t, attackerSeed, "remote", "add", "origin", attackerURL)
	gitFetchTestGit(t, attackerSeed, "push", "origin", "main")
	gitFetchTestGit(t, attackerSeed, "switch", "-c", "evil")
	gitFetchTestGit(t, attackerSeed, "push", "-u", "origin", "evil")
	gitFetchTestGit(t, backing, "config", "user.name", "Botanist")
	gitFetchTestGit(t, backing, "config", "user.email", "botanist@example.invalid")
	writeFetchTestFile(t, backing, "local.txt", "local base\n")
	gitFetchTestGit(t, backing, "add", "local.txt")
	gitFetchTestGit(t, backing, "commit", "-m", "local base")
	localOID := strings.TrimSpace(gitFetchTestGit(t, backing, "rev-parse", "HEAD"))
	gitFetchTestGit(t, backing, "remote", "add", "origin", remoteURL)
	gitFetchTestGit(t, backing, "fetch", "--no-tags", "origin", "main", "other")
	gitFetchTestGit(t, backing, "update-ref", "refs/remotes/origin/stale", localOID)
	gitFetchTestGit(t, backing, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	gitFetchTestGit(t, backing, "tag", "local-tag", localOID)
	writeFetchTestFile(t, backing, ".git/FETCH_HEAD", "backing FETCH_HEAD sentinel\n")
	return gitFetchFixture{
		backing: backing, seed: seed, remote: remote, remoteURL: remoteURL,
		attacker: attacker, attackerURL: attackerURL, initialOID: initialOID, localOID: localOID,
	}
}

func initFetchTestRepo(t *testing.T, path string, bare bool) {
	t.Helper()
	args := []string{"init", "--initial-branch=main"}
	if bare {
		args = append(args, "--bare")
	}
	args = append(args, path)
	command := exec.Command("git", args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func configureFetchTestIdentity(t *testing.T, repo string) {
	t.Helper()
	gitFetchTestGit(t, repo, "config", "user.name", "Fetcher")
	gitFetchTestGit(t, repo, "config", "user.email", "fetcher@example.invalid")
}

func gitFetchTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitFetchTestGitNoFatal(t, dir, args...)
}

func gitFetchTestGitNoFatal(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", dir}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %q %v: %v\n%s", dir, args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeFetchTestFile(t *testing.T, dir, name, value string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f gitFetchFixture) advanceRemote(t *testing.T) (string, string) {
	t.Helper()
	gitFetchTestGit(t, f.seed, "switch", "--orphan", "replacement-main")
	writeFetchTestFile(t, f.seed, "tracked.txt", "force-moved remote state\n")
	gitFetchTestGit(t, f.seed, "add", "tracked.txt")
	gitFetchTestGit(t, f.seed, "commit", "-m", "force move main")
	updatedOID := strings.TrimSpace(gitFetchTestGit(t, f.seed, "rev-parse", "HEAD"))
	if _, err := exec.Command("git", "-C", f.seed, "merge-base", "--is-ancestor", f.initialOID, updatedOID).CombinedOutput(); err == nil {
		t.Fatal("replacement main tip is still descended from the original tip")
	}
	gitFetchTestGit(t, f.seed, "push", "--force", "origin", "HEAD:refs/heads/main")
	gitFetchTestGit(t, f.seed, "switch", "main")
	gitFetchTestGit(t, f.seed, "branch", "new-branch")
	gitFetchTestGit(t, f.seed, "push", "origin", "new-branch")
	gitFetchTestGit(t, f.seed, "push", "origin", "--delete", "other")
	return updatedOID, strings.TrimSpace(gitFetchTestGit(t, f.seed, "rev-parse", "refs/heads/new-branch"))
}

func runLocalGitFetch(t *testing.T, f gitFetchFixture) (core.GitFetchResult, error) {
	t.Helper()
	return runGitFetchWithHooks(context.Background(), GitFetchExecution{
		Repository: f.backing,
		Substrate:  "demo",
		URL:        f.remoteURL,
		Credential: ResolvedCredential{Method: CredentialNone},
	}, gitFetchHooks{allowFileTransport: true})
}

func TestGitFetchImportsAndAtomicallyReconcilesOnlyOriginBranchRefs(t *testing.T) {
	f := newGitFetchFixture(t)
	updatedOID, createdOID := f.advanceRemote(t)
	if _, err := exec.Command("git", "-C", f.backing, "cat-file", "-e", updatedOID+"^{commit}").CombinedOutput(); err == nil {
		t.Fatal("force-moved remote commit unexpectedly existed in backing repository before fetch")
	}
	gitFetchTestGit(t, f.backing, "config", "--local", "remote.origin.fetch", "+refs/heads/*:refs/remotes/untrusted/*")
	gitFetchTestGit(t, f.backing, "config", "--local", "url."+f.attackerURL+".insteadOf", f.remoteURL)
	configBefore, err := os.ReadFile(filepath.Join(f.backing, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	writeFetchTestFile(t, f.backing, "tracked.txt", "staged work\n")
	gitFetchTestGit(t, f.backing, "add", "tracked.txt")
	writeFetchTestFile(t, f.backing, "tracked.txt", "staged plus unstaged work\n")
	writeFetchTestFile(t, f.backing, "untracked.txt", "untracked work\n")
	localStateBefore := snapshotLocalGitFetchState(t, f.backing)

	result, err := runLocalGitFetch(t, f)
	if err != nil {
		t.Fatalf("governed fetch: %v", err)
	}
	if result.Status != "fetched" || result.Substrate != "demo" || result.Remote != "origin" {
		t.Fatalf("result identity = %#v", result)
	}
	if result.Created != 1 || result.Updated != 1 || result.Pruned != 2 || result.Total != 4 {
		t.Fatalf("result counts = created:%d updated:%d pruned:%d total:%d, want 1/1/2/4", result.Created, result.Updated, result.Pruned, result.Total)
	}
	if len(result.Changes) != 4 || result.DetailsTruncated {
		t.Fatalf("bounded change details = %#v truncated=%v", result.Changes, result.DetailsTruncated)
	}
	refs := make([]string, 0, len(result.Changes))
	for _, detail := range result.Changes {
		refs = append(refs, detail.Ref)
	}
	if !sort.StringsAreSorted(refs) {
		t.Fatalf("ref details are not sorted: %v", refs)
	}
	if strings.Contains(strings.Join(refs, "\n"), "origin/HEAD") || strings.Contains(strings.Join(refs, "\n"), "untrusted") || strings.Contains(strings.Join(refs, "\n"), "evil") {
		t.Fatalf("fetch changed an out-of-bound ref: %v", refs)
	}
	if got := strings.TrimSpace(gitFetchTestGit(t, f.backing, "rev-parse", "refs/remotes/origin/main")); got != updatedOID {
		t.Fatalf("origin/main = %s, want force-moved %s", got, updatedOID)
	}
	if got := strings.TrimSpace(gitFetchTestGit(t, f.backing, "rev-parse", "refs/remotes/origin/new-branch")); got != createdOID {
		t.Fatalf("origin/new-branch = %s, want %s", got, createdOID)
	}
	if _, err := exec.Command("git", "-C", f.backing, "show-ref", "--verify", "--quiet", "refs/remotes/origin/other").CombinedOutput(); err == nil {
		t.Fatal("stale origin/other was not pruned")
	}
	if _, err := exec.Command("git", "-C", f.backing, "show-ref", "--verify", "--quiet", "refs/remotes/origin/stale").CombinedOutput(); err == nil {
		t.Fatal("stale direct branch-tracking ref was not pruned")
	}
	if got := gitFetchTestGit(t, f.backing, "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/main" {
		t.Fatalf("origin/HEAD target changed: %q", got)
	}
	if got := strings.TrimSpace(gitFetchTestGit(t, f.backing, "rev-parse", "HEAD")); got != localStateBefore.head {
		t.Fatalf("local HEAD moved from %s to %s", localStateBefore.head, got)
	}
	localStateAfter := snapshotLocalGitFetchState(t, f.backing)
	if !reflect.DeepEqual(localStateBefore, localStateAfter) {
		t.Fatalf("local state changed\nbefore: %#v\nafter:  %#v", localStateBefore, localStateAfter)
	}
	configAfter, err := os.ReadFile(filepath.Join(f.backing, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(configBefore, configAfter) {
		t.Fatal("backing remote configuration changed")
	}
	fetchHead, err := os.ReadFile(filepath.Join(f.backing, ".git", "FETCH_HEAD"))
	if err != nil || string(fetchHead) != "backing FETCH_HEAD sentinel\n" {
		t.Fatalf("backing FETCH_HEAD changed: %q, %v", fetchHead, err)
	}
	if got := strings.TrimSpace(gitFetchTestGit(t, f.backing, "rev-parse", "refs/tags/local-tag")); got != f.localOID {
		t.Fatalf("local tag moved: %s", got)
	}
	if _, err := exec.Command("git", "-C", f.backing, "show-ref", "--verify", "--quiet", "refs/tags/release").CombinedOutput(); err == nil {
		t.Fatal("remote tag was fetched")
	}
	if _, err := exec.Command("git", "-C", f.backing, "show-ref", "--verify", "--quiet", "refs/opentendril/fetch-stage/main").CombinedOutput(); err == nil {
		t.Fatal("a backing staging ref was created")
	}
	if _, err := exec.Command("git", "-C", f.backing, "cat-file", "-e", updatedOID+"^{commit}").CombinedOutput(); err != nil {
		t.Fatalf("imported commit is not available after staging cleanup: %v", err)
	}
	if _, err := exec.Command("git", "-C", f.backing, "show-ref", "--verify", "--quiet", "refs/remotes/origin/evil").CombinedOutput(); err == nil {
		t.Fatal("local URL rewrite or untrusted remote refspec widened fetch")
	}
}

type localGitFetchState struct {
	head         string
	branchRefs   string
	tagRefs      string
	originHead   []byte
	index        []byte
	status       string
	tracked      []byte
	untracked    []byte
	stagedDiff   string
	unstagedDiff string
}

func snapshotLocalGitFetchState(t *testing.T, repo string) localGitFetchState {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	originHead, err := os.ReadFile(filepath.Join(repo, ".git", "refs", "remotes", "origin", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	tracked, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	untracked, err := os.ReadFile(filepath.Join(repo, "untracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return localGitFetchState{
		head:         strings.TrimSpace(gitFetchTestGit(t, repo, "rev-parse", "HEAD")),
		branchRefs:   gitFetchTestGit(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads"),
		tagRefs:      gitFetchTestGit(t, repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/tags"),
		originHead:   originHead,
		index:        index,
		status:       gitFetchTestGit(t, repo, "status", "--porcelain", "-z"),
		tracked:      tracked,
		untracked:    untracked,
		stagedDiff:   gitFetchTestGit(t, repo, "diff", "--cached", "--binary"),
		unstagedDiff: gitFetchTestGit(t, repo, "diff", "--binary"),
	}
}

func TestGitFetchIdentityMismatchPrecedesCredentialResolutionAndRedactsFailure(t *testing.T) {
	f := newGitFetchFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			select {
			case accepted <- struct{}{}:
			default:
			}
			_ = connection.Close()
		}
	}()
	configuredURL := "https://" + listener.Addr().String() + "/owner/repository.git"
	credentialUsed := false
	_, err = runGitFetchWithHooks(context.Background(), GitFetchExecution{
		Repository: f.backing, Substrate: "demo", URL: configuredURL,
		ResolveCredential: func() (ResolvedCredential, error) {
			credentialUsed = true
			return ResolvedCredential{Method: CredentialPAT, TokenValue: "sensitive-token"}, nil
		},
	}, gitFetchHooks{})
	var fetchErr core.GitFetchError
	if !errors.As(err, &fetchErr) || fetchErr.Category != core.GitFetchFailureRemoteIdentityMismatch {
		t.Fatalf("error = %v, want typed remote identity mismatch", err)
	}
	if credentialUsed {
		t.Fatal("credential was materialized before origin identity matched")
	}
	select {
	case <-accepted:
		t.Fatal("network connection was attempted before origin identity matched")
	default:
	}
	if strings.Contains(err.Error(), "sensitive-token") || strings.Contains(err.Error(), f.backing) || strings.Contains(err.Error(), configuredURL) {
		t.Fatalf("failure was not redacted: %v", err)
	}
}

func TestGitFetchFailedOrUnavailableObjectImportStartsNoRefTransaction(t *testing.T) {
	for _, test := range []struct {
		name        string
		importer    func(string, string, string, []string, map[string]string) (string, error)
		wantMessage string
	}{
		{name: "failed import", importer: func(string, string, string, []string, map[string]string) (string, error) {
			return "", errors.New("private import path /secret token=hidden")
		}, wantMessage: "git-fetch-failure"},
		{name: "objects remain unavailable", importer: func(string, string, string, []string, map[string]string) (string, error) { return "", nil }, wantMessage: "git-fetch-failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newGitFetchFixture(t)
			_, _ = f.advanceRemote(t)
			transactions := 0
			hooks := gitFetchHooks{
				allowFileTransport: true,
				importObjects: func(_ context.Context, root, stage, common string, env []string, refs map[string]string) (string, error) {
					return test.importer(root, stage, common, env, refs)
				},
				updateRefs: func(_ context.Context, _, _ string, _ []string, _ []gitFetchChange) error {
					transactions++
					return nil
				},
			}
			_, err := runGitFetchWithHooks(context.Background(), GitFetchExecution{Repository: f.backing, Substrate: "demo", URL: f.remoteURL, Credential: ResolvedCredential{Method: CredentialNone}}, hooks)
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("error = %v, want %s", err, test.wantMessage)
			}
			if strings.Contains(err.Error(), "/secret") || strings.Contains(err.Error(), "hidden") {
				t.Fatalf("raw import failure escaped: %v", err)
			}
			if transactions != 0 {
				t.Fatalf("ref transaction calls = %d after failed/unavailable import", transactions)
			}
			if _, err := exec.Command("git", "-C", f.backing, "show-ref", "--verify", "--quiet", "refs/remotes/origin/new-branch").CombinedOutput(); err == nil {
				t.Fatal("tracking ref changed despite unavailable object import")
			}
		})
	}
}

func TestGitFetchFailedCASBatchLeavesNoPartialTrackingUpdates(t *testing.T) {
	f := newGitFetchFixture(t)
	updatedOID, _ := f.advanceRemote(t)
	oldMain := strings.TrimSpace(gitFetchTestGit(t, f.backing, "rev-parse", "refs/remotes/origin/main"))
	transactions := 0
	hooks := gitFetchHooks{
		allowFileTransport: true,
		updateRefs: func(ctx context.Context, root, common string, env []string, changes []gitFetchChange) error {
			transactions++
			if _, err := runGitFetchCommand(ctx, root, env, "--git-dir="+common, "update-ref", "refs/remotes/origin/new-branch", f.localOID); err != nil {
				return err
			}
			return updateGitFetchRefs(ctx, root, common, env, changes)
		},
	}
	_, err := runGitFetchWithHooks(context.Background(), GitFetchExecution{Repository: f.backing, Substrate: "demo", URL: f.remoteURL, Credential: ResolvedCredential{Method: CredentialNone}}, hooks)
	if err == nil {
		t.Fatal("CAS-conflicted ref transaction succeeded")
	}
	if transactions != 1 {
		t.Fatalf("update-ref transaction calls = %d, want 1", transactions)
	}
	if got := strings.TrimSpace(gitFetchTestGit(t, f.backing, "rev-parse", "refs/remotes/origin/main")); got != oldMain || got == updatedOID {
		t.Fatalf("failed transaction partially moved origin/main: got %s old %s new %s", got, oldMain, updatedOID)
	}
	if got := strings.TrimSpace(gitFetchTestGit(t, f.backing, "rev-parse", "refs/remotes/origin/new-branch")); got != f.localOID {
		t.Fatalf("concurrent conflicting ref was overwritten: %s", got)
	}
}

func TestGitFetchTransportIsolationAndSSHTrustConfiguration(t *testing.T) {
	remote, err := parseGitFetchRemote("https://github.com/owner/repo.git", false)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "ambient-home"))
	t.Setenv("HTTP_PROXY", "http://proxy.invalid")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid")
	t.Setenv("ALL_PROXY", "http://proxy.invalid")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "!echo ambient")
	t.Setenv("GIT_SSH_COMMAND", "ssh -o ProxyCommand=ambient")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ambient-ssh-socket")
	stage, env, _, err := prepareGitFetchStage(context.Background(), root, remote, ResolvedCredential{Method: CredentialPAT, TokenValue: "held-token"}, false)
	if err != nil {
		t.Fatalf("prepare HTTPS stage: %v", err)
	}
	if strings.Contains(strings.Join(env.local, "\n"), "proxy.invalid") || strings.Contains(strings.Join(env.local, "\n"), "ambient-ssh-socket") || strings.Contains(strings.Join(env.local, "\n"), "ProxyCommand=ambient") {
		t.Fatalf("ambient proxy/SSH values survived environment isolation: %v", env.local)
	}
	if !containsFetchEnv(env.local, "TENDRIL_GIT_TOKEN=held-token") {
		t.Fatal("Stem-held PAT was not passed via the explicit child environment")
	}
	globalConfig := fetchEnvValue(env.local, "GIT_CONFIG_GLOBAL")
	globalBytes, err := os.ReadFile(globalConfig)
	if err != nil || len(globalBytes) != 0 {
		t.Fatalf("controlled global config is not empty: %q, %v", globalBytes, err)
	}
	args := gitFetchTransportArgs(stage, remote)
	joined := strings.Join(args, " ")
	for _, required := range []string{"http.followRedirects=false", "http.proxy=", "https.proxy=", "--no-write-fetch-head", "--no-tags", "--refmap=", "+refs/heads/*:refs/opentendril/fetch-stage/*"} {
		if !strings.Contains(joined, required) {
			t.Errorf("isolated fetch args omit %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"--prune", "refs/tags", "--upload-pack", "--config"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("caller-widening or forbidden fetch option %q present: %s", forbidden, joined)
		}
	}
	if strings.Contains(strings.Join(env.base, "\n"), "HTTP_PROXY") || strings.Contains(strings.Join(env.base, "\n"), "HTTPS_PROXY") {
		t.Fatalf("ambient proxy variables were included: %v", env.base)
	}

	sshHome := filepath.Join(root, "ssh-home")
	t.Setenv("HOME", sshHome)
	if err := os.MkdirAll(filepath.Join(sshHome, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshHome, ".ssh", "config"), []byte("Host *\n ProxyCommand /bin/false\n IdentityFile /tmp/ambient-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	trust := "example.invalid ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey\n"
	if err := os.WriteFile(filepath.Join(sshHome, ".ssh", "known_hosts"), []byte(trust), 0o600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "configured-key")
	if err := os.WriteFile(key, []byte("private key fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	sshRemote, err := parseGitFetchRemote("git@github.com:owner/repo.git", false)
	if err != nil {
		t.Fatal(err)
	}
	sshAuth, err := materializeGitFetchAuth(context.Background(), root, sshRemote, ResolvedCredential{Method: CredentialSSH, SSHKeyPath: key}, false)
	if err != nil {
		t.Fatalf("materialize isolated SSH: %v", err)
	}
	sshConfigPath := shellUnquoteConfigPath(sshAuth.env[0])
	sshConfig, err := os.ReadFile(sshConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	sshConfigText := string(sshConfig)
	for _, required := range []string{"IdentityAgent none", "IdentitiesOnly yes", "StrictHostKeyChecking accept-new", "GlobalKnownHostsFile /etc/ssh/ssh_known_hosts", "UserKnownHostsFile", "ProxyCommand none", "ProxyJump none", "LocalCommand none", "ForwardAgent no", "ClearAllForwardings yes", "ControlMaster no", "ControlPath none", key} {
		if !strings.Contains(sshConfigText, required) {
			t.Errorf("isolated SSH config omits %q: %s", required, sshConfigText)
		}
	}
	if strings.Contains(sshConfigText, "ProxyCommand /bin/false") || strings.Contains(sshConfigText, "ambient-key") || strings.Contains(sshConfigText, "Include ") {
		t.Fatalf("ambient SSH config widened authority: %s", sshConfigText)
	}
	stemKnownHosts := filepath.Join(sshHome, ".tendril", "ssh", "known_hosts")
	knownBytes, err := os.ReadFile(stemKnownHosts)
	if err != nil || string(knownBytes) != trust {
		t.Fatalf("existing host trust was not bootstrapped as data: %q, %v", knownBytes, err)
	}
	for _, envEntry := range sshAuth.env {
		if strings.HasPrefix(envEntry, "SSH_AUTH_SOCK=") || strings.Contains(envEntry, "ambient-ssh-socket") {
			t.Fatalf("ambient SSH_AUTH_SOCK survived: %v", sshAuth.env)
		}
	}
}

func TestGitFetchResultIsSortedAndBounded(t *testing.T) {
	changes := make([]gitFetchChange, 0, 60)
	for i := 59; i >= 0; i-- {
		changes = append(changes, gitFetchChange{ref: fmt.Sprintf("refs/remotes/origin/branch-%02d", i), change: "created", newOID: strings.Repeat("a", 40)})
	}
	result := makeGitFetchResult("demo", changes)
	if result.Total != 60 || len(result.Changes) != core.MaxGitFetchRefDetails || !result.DetailsTruncated {
		t.Fatalf("bounded result = total:%d details:%d truncated:%v", result.Total, len(result.Changes), result.DetailsTruncated)
	}
	refs := make([]string, 0, len(result.Changes))
	for _, detail := range result.Changes {
		refs = append(refs, detail.Ref)
	}
	if !sort.StringsAreSorted(refs) {
		t.Fatalf("bounded details are not the lexical prefix: %v", refs)
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > core.MaxGitFetchResultBytes {
		t.Fatalf("serialized result size = %d, err = %v", len(encoded), err)
	}
}

func TestCommonGitRemoteRefLockSerializesSharedRepositoryOnly(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	other := filepath.Join(root, "other")
	initFetchTestRepo(t, repo, false)
	configureFetchTestIdentity(t, repo)
	writeFetchTestFile(t, repo, "base.txt", "base\n")
	gitFetchTestGit(t, repo, "add", "base.txt")
	gitFetchTestGit(t, repo, "commit", "-m", "base")
	initFetchTestRepo(t, other, false)
	configureFetchTestIdentity(t, other)
	writeFetchTestFile(t, other, "base.txt", "base\n")
	gitFetchTestGit(t, other, "add", "base.txt")
	gitFetchTestGit(t, other, "commit", "-m", "base")
	worktree := filepath.Join(root, "linked")
	gitFetchTestGit(t, repo, "worktree", "add", "-b", "linked", worktree)

	unlock, err := lockCommonGitRemoteRefs(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	otherAcquired := make(chan error, 1)
	go func() {
		unlockOther, lockErr := lockCommonGitRemoteRefs(context.Background(), other)
		if lockErr == nil {
			unlockOther()
		}
		otherAcquired <- lockErr
	}()
	select {
	case err := <-otherAcquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		unlock()
		t.Fatal("unrelated repository was blocked by the remote-ref lock")
	}
	linkedAcquired := make(chan error, 1)
	go func() {
		unlockLinked, lockErr := lockCommonGitRemoteRefs(context.Background(), worktree)
		if lockErr == nil {
			unlockLinked()
		}
		linkedAcquired <- lockErr
	}()
	select {
	case err := <-linkedAcquired:
		unlock()
		t.Fatalf("linked worktree did not share its common-repository lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	// Disjoint local branch refs and content-addressed object writes do not
	// acquire this lock and must proceed while a fetch holds it.
	disjointDone := make(chan error, 1)
	go func() {
		if _, runErr := runGitCommand(context.Background(), repo, "update-ref", "refs/heads/disjoint", "HEAD"); runErr != nil {
			disjointDone <- runErr
			return
		}
		command := exec.Command("git", "-C", repo, "hash-object", "-w", "--stdin")
		command.Stdin = strings.NewReader("concurrent object\n")
		_, runErr := command.CombinedOutput()
		disjointDone <- runErr
	}()
	select {
	case err := <-disjointDone:
		if err != nil {
			unlock()
			t.Fatalf("disjoint Git writer failed: %v", err)
		}
	case <-time.After(time.Second):
		unlock()
		t.Fatal("disjoint local-ref/object write was blocked by the fetch lock")
	}
	unlock()
	if err := <-linkedAcquired; err != nil {
		t.Fatal(err)
	}
}

func TestCommonGitRemoteRefLockOrderAndReversedOrderDeadlockEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace")
	initFetchTestRepo(t, path, false)
	configureFetchTestIdentity(t, path)
	writeFetchTestFile(t, path, "base.txt", "base\n")
	gitFetchTestGit(t, path, "add", "base.txt")
	gitFetchTestGit(t, path, "commit", "-m", "base")
	canonicalDone := make(chan error, 1)
	go func() {
		unlockWorkspace := LockWorkspace(path)
		defer unlockWorkspace()
		unlockRun := lockRunWorkspaceGit(path)
		defer unlockRun()
		unlockRemote, err := lockCommonGitRemoteRefs(context.Background(), path)
		if err == nil {
			unlockRemote()
		}
		canonicalDone <- err
	}()
	select {
	case err := <-canonicalDone:
		if err != nil {
			t.Fatalf("canonical lock order: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canonical LockWorkspace -> run-workspace -> common-ref order hung")
	}

	// Construct both sides of the reversed nested order. The probes block until
	// the test releases their first locks after observing the lock cycle.
	path = filepath.Join(t.TempDir(), "cycle")
	workspaceHeld := make(chan func(), 1)
	runHeld := make(chan func(), 1)
	attempting := make(chan struct{}, 2)
	done := make(chan struct{}, 2)
	start := make(chan struct{})
	go func() {
		unlockFirst := LockWorkspace(path)
		workspaceHeld <- unlockFirst
		<-start
		attempting <- struct{}{}
		unlockSecond := lockRunWorkspaceGit(path)
		unlockSecond()
		done <- struct{}{}
	}()
	go func() {
		unlockFirst := lockRunWorkspaceGit(path)
		runHeld <- unlockFirst
		<-start
		attempting <- struct{}{}
		unlockSecond := LockWorkspace(path)
		unlockSecond()
		done <- struct{}{}
	}()
	unlockWorkspace := <-workspaceHeld
	unlockRun := <-runHeld
	close(start)
	<-attempting
	<-attempting
	select {
	case <-done:
		t.Fatal("reversed nested lock order unexpectedly completed without a cycle")
	case <-time.After(50 * time.Millisecond):
	}
	unlockWorkspace()
	unlockRun()
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("reversed lock probes did not drain after releasing the cycle")
		}
	}
}

func containsFetchEnv(env []string, expected string) bool {
	for _, entry := range env {
		if entry == expected {
			return true
		}
	}
	return false
}

func fetchEnvValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func shellUnquoteConfigPath(command string) string {
	parts := strings.Split(command, " -F ")
	if len(parts) != 2 {
		return ""
	}
	return strings.Trim(parts[1], "'")
}

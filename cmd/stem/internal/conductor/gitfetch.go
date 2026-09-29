package conductor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// GitFetchExecution is the Stem-only input to governed fetch. URL and
// credential authority are resolved from the named Substrate configuration;
// no Pollinator-provided transport or ref arguments reach this type.
type GitFetchExecution struct {
	Repository        string
	Substrate         string
	URL               string
	Credential        ResolvedCredential
	ResolveCredential func() (ResolvedCredential, error)
}

type gitFetchRemote struct {
	transport string
	identity  string
	raw       string
}

type gitFetchRef struct {
	oid    string
	symref string
}

type gitFetchChange struct {
	ref    string
	change string
	oldOID string
	newOID string
}

type gitFetchHooks struct {
	allowFileTransport bool
	importObjects      func(context.Context, string, string, string, []string, map[string]string) (string, error)
	updateRefs         func(context.Context, string, string, []string, []gitFetchChange) error
}

var commonGitRemoteRefLocks sync.Map

// lockCommonGitRemoteRefs serializes only writers of shared remote-tracking
// refs. Linked worktrees resolve to the same canonical common Git directory;
// unrelated repositories retain independent locks.
func lockCommonGitRemoteRefs(ctx context.Context, repository string) (func(), error) {
	commonDir, err := resolveGitCommonDir(ctx, repository)
	if err != nil {
		return nil, err
	}
	value, _ := commonGitRemoteRefLocks.LoadOrStore(commonDir, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock, nil
}

func resolveGitCommonDir(ctx context.Context, repository string) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return "", errors.New("repository is required")
	}
	output, err := runGitFetchCommand(ctx, "", fetchLocalEnv(), "-C", repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	commonDir := strings.TrimSpace(string(output))
	if commonDir == "" {
		return "", errors.New("Git common directory is unavailable")
	}
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(commonDir); resolveErr == nil {
		commonDir = resolved
	}
	return filepath.Clean(commonDir), nil
}

func sameCanonicalPath(left, right string) bool {
	canonical := func(value string) string {
		value, err := filepath.Abs(strings.TrimSpace(value))
		if err != nil {
			return ""
		}
		if resolved, resolveErr := filepath.EvalSymlinks(value); resolveErr == nil {
			value = resolved
		}
		return filepath.Clean(value)
	}
	a, b := canonical(left), canonical(right)
	return a != "" && a == b
}

// RunGitFetch synchronizes configured remote branches into the fixed
// refs/remotes/origin namespace. Staging, object import, object verification,
// and the final CAS transaction all run while holding the common-repository
// remote-ref lock.
func RunGitFetch(ctx context.Context, execution GitFetchExecution) (core.GitFetchResult, error) {
	return runGitFetchWithHooks(ctx, execution, gitFetchHooks{})
}

func runGitFetchWithHooks(ctx context.Context, execution GitFetchExecution, hooks gitFetchHooks) (core.GitFetchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	repository := strings.TrimSpace(execution.Repository)
	substrate := strings.TrimSpace(execution.Substrate)
	if repository == "" || substrate == "" {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureSubstrateUnavailable)
	}
	if _, err := os.Stat(repository); err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureSubstrateUnavailable)
	}
	rootOutput, rootErr := runGitFetchCommand(ctx, "", fetchLocalEnv(), "-C", repository, "rev-parse", "--show-toplevel")
	if rootErr != nil || !sameCanonicalPath(repository, strings.TrimSpace(string(rootOutput))) {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureSubstrateUnavailable)
	}
	configured, err := parseGitFetchRemote(execution.URL, hooks.allowFileTransport)
	if err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureDestinationDenied)
	}
	localURL, err := readLocalOriginURL(ctx, repository)
	if err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureRemoteIdentityMismatch)
	}
	local, err := parseGitFetchRemote(localURL, hooks.allowFileTransport)
	if err != nil || !sameGitFetchIdentity(configured, local) {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureRemoteIdentityMismatch)
	}
	commonDir, err := resolveGitCommonDir(ctx, repository)
	if err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureSubstrateUnavailable)
	}
	unlock, err := lockCommonGitRemoteRefs(ctx, repository)
	if err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureSubstrateUnavailable)
	}
	defer unlock()
	localURL, err = readLocalOriginURL(ctx, repository)
	if err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureRemoteIdentityMismatch)
	}
	local, err = parseGitFetchRemote(localURL, hooks.allowFileTransport)
	if err != nil || !sameGitFetchIdentity(configured, local) {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureRemoteIdentityMismatch)
	}
	credential := execution.Credential
	if execution.ResolveCredential != nil {
		credential, err = execution.ResolveCredential()
		if err != nil {
			return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureAuthentication)
		}
	}

	root, err := os.MkdirTemp("", "opentendril-git-fetch-")
	if err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
	}
	defer os.RemoveAll(root)
	if err := os.Chmod(root, 0o700); err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
	}
	stageDir, env, _, err := prepareGitFetchStage(ctx, root, configured, credential, hooks.allowFileTransport)
	if err != nil {
		return core.GitFetchResult{}, err
	}
	if err := initializeGitFetchStage(ctx, root, stageDir, env); err != nil {
		return core.GitFetchResult{}, classifyGitFetchCommand(err)
	}
	if err := fetchGitFetchBranches(ctx, root, stageDir, env, configured); err != nil {
		return core.GitFetchResult{}, classifyGitFetchCommand(err)
	}

	stagedRefs, err := readGitFetchRefs(ctx, root, stageDir, env.local, "refs/opentendril/fetch-stage", false)
	if err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
	}
	newRefs := make(map[string]string, len(stagedRefs))
	for stageRef, ref := range stagedRefs {
		branch := strings.TrimPrefix(stageRef, "refs/opentendril/fetch-stage/")
		if branch == stageRef || branch == "" || branch == "HEAD" || !validGitFetchOID(ref.oid) {
			return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
		}
		backingRef := "refs/remotes/origin/" + branch
		if _, checkErr := runGitFetchCommand(ctx, root, env.local, "--git-dir="+stageDir, "check-ref-format", backingRef); checkErr != nil {
			return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
		}
		objectType, typeErr := runGitFetchCommand(ctx, root, env.local, "--git-dir="+stageDir, "cat-file", "-t", ref.oid)
		if typeErr != nil || strings.TrimSpace(string(objectType)) != "commit" {
			return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
		}
		newRefs[backingRef] = ref.oid
	}

	oldRefs, err := readGitFetchRefs(ctx, root, commonDir, env.local, "refs/remotes/origin", true)
	if err != nil {
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
	}
	oldHead, hasOriginHead := oldRefs["refs/remotes/origin/HEAD"]
	if _, collision := newRefs["refs/remotes/origin/HEAD"]; collision || (hasOriginHead && strings.TrimSpace(oldHead.symref) == "") {
		// origin/HEAD is reserved and must remain byte-for-byte unchanged.
		return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
	}
	for ref, old := range oldRefs {
		if ref != "refs/remotes/origin/HEAD" && old.symref != "" {
			return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
		}
	}

	importObjects := hooks.importObjects
	if importObjects == nil {
		importObjects = importMissingGitFetchObjects
	}
	keepPath, err := importObjects(ctx, root, stageDir, commonDir, env.local, newRefs)
	if err != nil {
		return core.GitFetchResult{}, classifyGitFetchCommand(err)
	}
	if keepPath != "" {
		defer os.Remove(keepPath)
	}
	for _, oid := range newRefs {
		if _, err := runGitFetchCommand(ctx, root, env.local, "--git-dir="+commonDir, "cat-file", "-e", oid+"^{commit}"); err != nil {
			return core.GitFetchResult{}, gitFetchError(core.GitFetchFailureGit)
		}
	}

	changes := diffGitFetchRefs(oldRefs, newRefs)
	if len(changes) > 0 {
		updateRefs := hooks.updateRefs
		if updateRefs == nil {
			updateRefs = updateGitFetchRefs
		}
		if err := updateRefs(ctx, root, commonDir, env.local, changes); err != nil {
			return core.GitFetchResult{}, classifyGitFetchCommand(err)
		}
	}
	return makeGitFetchResult(substrate, changes), nil
}

type gitFetchEnvironment struct {
	local []string
	base  []string
}

type gitFetchAuth struct {
	transport string
	env       []string
}

func prepareGitFetchStage(ctx context.Context, root string, remote gitFetchRemote, credential ResolvedCredential, allowFileTransport bool) (string, gitFetchEnvironment, gitFetchAuth, error) {
	stageDir := filepath.Join(root, "stage.git")
	templateDir := filepath.Join(root, "template")
	homeDir := filepath.Join(root, "home")
	if err := os.Mkdir(templateDir, 0o700); err != nil {
		return "", gitFetchEnvironment{}, gitFetchAuth{}, gitFetchError(core.GitFetchFailureGit)
	}
	if err := os.Mkdir(homeDir, 0o700); err != nil {
		return "", gitFetchEnvironment{}, gitFetchAuth{}, gitFetchError(core.GitFetchFailureGit)
	}
	globalConfig := filepath.Join(root, "global.gitconfig")
	if err := os.WriteFile(globalConfig, nil, 0o600); err != nil {
		return "", gitFetchEnvironment{}, gitFetchAuth{}, gitFetchError(core.GitFetchFailureGit)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", gitFetchEnvironment{}, gitFetchAuth{}, gitFetchError(core.GitFetchFailureGit)
	}
	pathValue := filepath.Dir(gitPath)
	if pathValue == "" {
		pathValue = "/usr/bin:/bin"
	}
	base := []string{
		"PATH=" + pathValue,
		"HOME=" + homeDir,
		"XDG_CONFIG_HOME=" + filepath.Join(homeDir, ".config"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + globalConfig,
	}
	if err := os.Mkdir(filepath.Join(root, "tmp"), 0o700); err != nil {
		return "", gitFetchEnvironment{}, gitFetchAuth{}, gitFetchError(core.GitFetchFailureGit)
	}
	auth, err := materializeGitFetchAuth(ctx, root, remote, credential, allowFileTransport)
	if err != nil {
		return "", gitFetchEnvironment{}, gitFetchAuth{}, err
	}
	protocols := remote.transport
	if allowFileTransport && remote.transport == "file" {
		protocols = "file"
	}
	base = append(base, "GIT_ALLOW_PROTOCOL="+protocols)
	local := append([]string(nil), base...)
	if len(auth.env) > 0 {
		local = append(local, auth.env...)
	} else {
		local = append(local, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=")
	}
	return stageDir, gitFetchEnvironment{local: local, base: base}, auth, nil
}

func materializeGitFetchAuth(ctx context.Context, root string, remote gitFetchRemote, credential ResolvedCredential, allowFileTransport bool) (gitFetchAuth, error) {
	auth := gitFetchAuth{transport: remote.transport}
	if remote.transport == "file" && allowFileTransport && (credential.Method == CredentialNone || credential.Method == CredentialUnspecified) {
		return auth, nil
	}
	if remote.transport == "ssh" {
		if credential.Method != CredentialSSH && credential.Method != CredentialUnspecified && credential.Method != CredentialNone {
			return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
		}
		key := ""
		if credential.Method == CredentialSSH {
			if credential.SSHKeyPath == "" {
				return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
			}
			var err error
			key, err = filepath.Abs(credential.SSHKeyPath)
			if err != nil || strings.ContainsAny(key, "\x00\r\n") {
				return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
			}
			info, err := os.Stat(key)
			if err != nil || !info.Mode().IsRegular() {
				return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
			}
		}
		knownHosts, err := seedStemKnownHosts()
		if err != nil {
			return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
		}
		sshPath, err := exec.LookPath("ssh")
		if err != nil {
			return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
		}
		configPath := filepath.Join(root, "ssh_config")
		configLines := []string{
			"Host *",
			"  BatchMode yes",
			"  IdentitiesOnly yes",
			"  IdentityAgent none",
			"  StrictHostKeyChecking accept-new",
			"  GlobalKnownHostsFile /etc/ssh/ssh_known_hosts",
			"  UserKnownHostsFile " + sshConfigQuote(knownHosts),
			"  UpdateHostKeys no",
			"  CanonicalizeHostname no",
			"  ProxyCommand none",
			"  ProxyJump none",
			"  LocalCommand none",
			"  PermitLocalCommand no",
			"  ForwardAgent no",
			"  ClearAllForwardings yes",
			"  ControlMaster no",
			"  ControlPath none",
		}
		if key != "" {
			configLines = append(configLines[:4], append([]string{"  IdentityFile " + sshConfigQuote(key)}, configLines[4:]...)...)
		}
		config := strings.Join(append(configLines, ""), "\n")
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
		}
		auth.env = []string{"GIT_SSH_COMMAND=" + shellQuote(sshPath) + " -F " + shellQuote(configPath), "GIT_SSH_VARIANT=ssh"}
		return auth, nil
	}
	if remote.transport != "https" || credential.Method == CredentialSSH {
		return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
	}
	switch credential.Method {
	case CredentialPAT, CredentialApp, CredentialUnspecified, CredentialNone:
		if credential.Method == CredentialPAT && credential.TokenValue == "" {
			return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
		}
		if credential.Method == CredentialApp {
			token, err := githubAppInstallationToken(ctx, credential.App, remote.raw)
			if err != nil || token == "" {
				return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
			}
			auth.env = gitTokenCredentialEnv(token)
		} else if credential.Method == CredentialPAT {
			auth.env = gitTokenCredentialEnv(credential.TokenValue)
		}
	default:
		return gitFetchAuth{}, gitFetchError(core.GitFetchFailureAuthentication)
	}
	return auth, nil
}

func sshConfigQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func seedStemKnownHosts() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("Stem home is unavailable")
	}
	dir := filepath.Join(home, ".tendril", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(dir, "known_hosts")
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("Stem host trust file is not a regular file")
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var source []byte
	sourcePath := filepath.Join(home, ".ssh", "known_hosts")
	if data, readErr := os.ReadFile(sourcePath); readErr == nil {
		source = data
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", readErr
	}
	temp, err := os.CreateTemp(dir, ".known-hosts-*")
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", err
	}
	if _, err := temp.Write(source); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tempName, target); err != nil {
		if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() {
			return target, nil
		}
		return "", err
	}
	return target, nil
}

func initializeGitFetchStage(ctx context.Context, root, stageDir string, env gitFetchEnvironment) error {
	_, err := runGitFetchCommand(ctx, root, env.base, "init", "--bare", "--quiet", "--template="+filepath.Join(root, "template"), stageDir)
	return err
}

func fetchGitFetchBranches(ctx context.Context, root, stageDir string, env gitFetchEnvironment, remote gitFetchRemote) error {
	_, err := runGitFetchCommand(ctx, root, env.local, gitFetchTransportArgs(stageDir, remote)...)
	return err
}

func gitFetchTransportArgs(stageDir string, remote gitFetchRemote) []string {
	return []string{
		"-c", "http.followRedirects=false",
		"-c", "http.proxy=",
		"-c", "https.proxy=",
		"--git-dir=" + stageDir,
		"fetch",
		"--no-write-fetch-head",
		"--no-tags",
		"--no-recurse-submodules",
		"--refmap=",
		"--",
		remoteURLFromParsed(remote),
		"+refs/heads/*:refs/opentendril/fetch-stage/*",
	}
}

func remoteURLFromParsed(remote gitFetchRemote) string {
	return remote.raw
}

func readGitFetchRefs(ctx context.Context, root, gitDir string, env []string, namespace string, includeSymrefs bool) (map[string]gitFetchRef, error) {
	format := "%(refname)%00%(objectname)"
	if includeSymrefs {
		format += "%00%(symref)"
	}
	output, err := runGitFetchCommand(ctx, root, env, "--git-dir="+gitDir, "for-each-ref", "--format="+format, namespace)
	if err != nil {
		return nil, err
	}
	refs := make(map[string]gitFetchRef)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		parts := bytes.Split(scanner.Bytes(), []byte{0})
		wantParts := 2
		if includeSymrefs {
			wantParts = 3
		}
		if len(parts) != wantParts {
			return nil, errors.New("malformed ref snapshot")
		}
		refName := string(parts[0])
		if !strings.HasPrefix(refName, namespace+"/") {
			continue
		}
		if refName == "refs/remotes/origin/HEAD" && !includeSymrefs {
			continue
		}
		record := gitFetchRef{oid: string(parts[1])}
		if includeSymrefs {
			record.symref = string(parts[2])
			if record.symref == "" && !validGitFetchOID(record.oid) {
				return nil, errors.New("invalid backing object ID")
			}
		}
		if !includeSymrefs && !validGitFetchOID(record.oid) {
			return nil, errors.New("invalid staging object ID")
		}
		refs[refName] = record
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return refs, nil
}

func importMissingGitFetchObjects(ctx context.Context, root, stageDir, commonDir string, env []string, refs map[string]string) (string, error) {
	missing := false
	for _, oid := range refs {
		if _, err := runGitFetchCommand(ctx, root, env, "--git-dir="+commonDir, "cat-file", "-e", oid+"^{commit}"); err != nil {
			missing = true
			break
		}
	}
	if !missing {
		return "", nil
	}
	markerBytes := make([]byte, 12)
	if _, err := rand.Read(markerBytes); err != nil {
		return "", err
	}
	marker := "opentendril-fetch-" + hex.EncodeToString(markerBytes)
	oids := make([]string, 0, len(refs))
	for _, oid := range refs {
		oids = append(oids, oid)
	}
	sort.Strings(oids)
	input := []byte(strings.Join(oids, "\n") + "\n")
	producer := exec.CommandContext(ctx, "git", "--git-dir="+stageDir, "pack-objects", "--stdout", "--revs")
	producer.Dir = root
	producer.Env = env
	consumer := exec.CommandContext(ctx, "git", "--git-dir="+commonDir, "index-pack", "--stdin", "--keep="+marker)
	consumer.Dir = root
	consumer.Env = env
	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		return "", err
	}
	producer.Stdout = pipeWrite
	producer.Stdin = bytes.NewReader(input)
	var producerErr, consumerErr boundedFetchOutput
	producer.Stderr = &producerErr
	consumer.Stderr = &consumerErr
	var consumerOut boundedFetchOutput
	consumer.Stdout = &consumerOut
	consumer.Stdin = pipeRead
	if err := consumer.Start(); err != nil {
		_ = pipeRead.Close()
		_ = pipeWrite.Close()
		return "", err
	}
	if err := producer.Start(); err != nil {
		_ = pipeRead.Close()
		_ = pipeWrite.Close()
		_ = consumer.Process.Kill()
		_ = consumer.Wait()
		return "", err
	}
	_ = pipeWrite.Close()
	producerWaitErr := producer.Wait()
	_ = pipeRead.Close()
	consumerWaitErr := consumer.Wait()
	if producerWaitErr != nil {
		return "", fmt.Errorf("pack-objects: %w: %s", producerWaitErr, producerErr.String())
	}
	if consumerWaitErr != nil {
		return "", fmt.Errorf("index-pack: %w: %s", consumerWaitErr, consumerErr.String())
	}
	packOID := strings.TrimSpace(consumerOut.String())
	if fields := strings.SplitN(packOID, "\t", 2); len(fields) == 2 && fields[0] == "keep" {
		packOID = strings.TrimSpace(fields[1])
	}
	if !validGitFetchOID(packOID) {
		return "", errors.New("index-pack returned invalid pack identity")
	}
	packDir := filepath.Join(commonDir, "objects", "pack")
	keepPath := filepath.Join(packDir, "pack-"+packOID+".keep")
	if info, statErr := os.Stat(keepPath); statErr == nil && info.Size() <= int64(len(marker)+64) {
		contents, readErr := os.ReadFile(keepPath)
		if readErr == nil && string(bytes.TrimSpace(contents)) == marker {
			return keepPath, nil
		}
	}
	return "", nil
}

func updateGitFetchRefs(ctx context.Context, root, commonDir string, env []string, changes []gitFetchChange) error {
	var transaction strings.Builder
	transaction.WriteString("start\noption no-deref\n")
	for _, change := range changes {
		switch change.change {
		case "created":
			fmt.Fprintf(&transaction, "create %s %s\n", change.ref, change.newOID)
		case "updated":
			fmt.Fprintf(&transaction, "update %s %s %s\n", change.ref, change.newOID, change.oldOID)
		case "pruned":
			fmt.Fprintf(&transaction, "delete %s %s\n", change.ref, change.oldOID)
		default:
			return errors.New("unknown fetch ref change")
		}
	}
	transaction.WriteString("prepare\ncommit\n")
	_, err := runGitFetchCommandInput(ctx, root, env, []byte(transaction.String()), "--git-dir="+commonDir, "update-ref", "--stdin")
	return err
}

func diffGitFetchRefs(oldRefs map[string]gitFetchRef, newRefs map[string]string) []gitFetchChange {
	var changes []gitFetchChange
	for ref, newOID := range newRefs {
		old, exists := oldRefs[ref]
		if !exists {
			changes = append(changes, gitFetchChange{ref: ref, change: "created", newOID: newOID})
		} else if old.symref == "" && old.oid != newOID {
			changes = append(changes, gitFetchChange{ref: ref, change: "updated", oldOID: old.oid, newOID: newOID})
		}
	}
	for ref, old := range oldRefs {
		if ref == "refs/remotes/origin/HEAD" || old.symref != "" {
			continue
		}
		if _, exists := newRefs[ref]; !exists {
			changes = append(changes, gitFetchChange{ref: ref, change: "pruned", oldOID: old.oid})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ref < changes[j].ref })
	return changes
}

func makeGitFetchResult(substrate string, changes []gitFetchChange) core.GitFetchResult {
	changes = append([]gitFetchChange(nil), changes...)
	sort.Slice(changes, func(i, j int) bool { return changes[i].ref < changes[j].ref })
	result := core.GitFetchResult{
		Status:           "fetched",
		Substrate:        substrate,
		Remote:           "origin",
		Total:            len(changes),
		DetailsTruncated: true,
		Changes:          []core.GitFetchRefChange{},
	}
	all := make([]core.GitFetchRefChange, 0, len(changes))
	for _, change := range changes {
		detail := core.GitFetchRefChange{Ref: change.ref, Change: change.change, OldOID: change.oldOID, NewOID: change.newOID}
		all = append(all, detail)
		switch change.change {
		case "created":
			result.Created++
		case "updated":
			result.Updated++
		case "pruned":
			result.Pruned++
		}
	}
	for _, detail := range all {
		if len(result.Changes) >= core.MaxGitFetchRefDetails {
			result.DetailsTruncated = true
			break
		}
		result.Changes = append(result.Changes, detail)
		encoded, _ := jsonMarshalGitFetchResult(result)
		if len(encoded) > core.MaxGitFetchResultBytes {
			result.Changes = result.Changes[:len(result.Changes)-1]
			result.DetailsTruncated = true
			break
		}
	}
	if len(result.Changes) == len(all) && len(all) <= core.MaxGitFetchRefDetails {
		result.DetailsTruncated = false
	}
	return result
}

func jsonMarshalGitFetchResult(result core.GitFetchResult) ([]byte, error) {
	return json.Marshal(result)
}

func readLocalOriginURL(ctx context.Context, repository string) (string, error) {
	output, err := runGitFetchCommand(ctx, "", fetchLocalEnv(), "-C", repository, "config", "--no-includes", "--local", "--null", "--get-all", "remote.origin.url")
	if err != nil {
		return "", err
	}
	raw := string(output)
	if !strings.HasSuffix(raw, "\x00") {
		return "", errors.New("origin URL is not a single local value")
	}
	values := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return "", errors.New("origin URL is not a single local value")
	}
	return values[0], nil
}

func parseGitFetchRemote(raw string, allowFileTransport bool) (gitFetchRemote, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\r\n\t") {
		return gitFetchRemote{}, errors.New("invalid configured remote")
	}
	var host, pathPart, transport string
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" || parsed.RawPath != "" {
			return gitFetchRemote{}, errors.New("invalid configured remote")
		}
		transport = strings.ToLower(parsed.Scheme)
		if transport == "file" && allowFileTransport {
			if parsed.User != nil || parsed.Host != "" || parsed.Path == "" || !filepath.IsAbs(parsed.Path) || parsed.RawQuery != "" || parsed.Fragment != "" {
				return gitFetchRemote{}, errors.New("invalid file remote")
			}
			return gitFetchRemote{transport: transport, identity: "file/" + filepath.Clean(parsed.Path), raw: raw}, nil
		}
		if transport != "https" && transport != "ssh" {
			return gitFetchRemote{}, errors.New("unsupported transport")
		}
		if parsed.User != nil {
			if transport != "ssh" || parsed.User.Username() == "" {
				return gitFetchRemote{}, errors.New("embedded credentials are not permitted")
			}
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return gitFetchRemote{}, errors.New("embedded credentials are not permitted")
			}
		}
		host = strings.ToLower(parsed.Hostname())
		pathPart = parsed.EscapedPath()
		if parsed.Port() != "" {
			port, portErr := strconv.Atoi(parsed.Port())
			if portErr != nil || port < 1 || port > 65535 {
				return gitFetchRemote{}, errors.New("invalid port")
			}
			host += ":" + parsed.Port()
		}
	} else {
		transport = "ssh"
		colon := strings.IndexByte(raw, ':')
		if colon <= 0 || strings.Contains(raw[:colon], "/") {
			return gitFetchRemote{}, errors.New("unsupported remote syntax")
		}
		authority := raw[:colon]
		pathPart = raw[colon+1:]
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			if at == 0 || authority[:at] == "" {
				return gitFetchRemote{}, errors.New("invalid SSH user")
			}
			if strings.Contains(authority[:at], ":") {
				return gitFetchRemote{}, errors.New("embedded credentials are not permitted")
			}
			authority = authority[at+1:]
		}
		host = strings.ToLower(authority)
	}
	pathPart = strings.TrimPrefix(pathPart, "/")
	if host == "" || strings.ContainsAny(host, " /\\%") || strings.Contains(pathPart, "%") || strings.Contains(pathPart, "\\") || strings.HasPrefix(pathPart, "/") || strings.HasSuffix(pathPart, "/") {
		return gitFetchRemote{}, errors.New("invalid remote authority")
	}
	parts := strings.Split(pathPart, "/")
	if len(parts) < 2 {
		return gitFetchRemote{}, errors.New("remote repository path is incomplete")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return gitFetchRemote{}, errors.New("invalid remote repository path")
		}
	}
	pathPart = strings.TrimSuffix(pathPart, ".git")
	if pathPart == "" {
		return gitFetchRemote{}, errors.New("remote repository path is empty")
	}
	identityPath := pathPart
	if host == "github.com" {
		identityPath = strings.ToLower(identityPath)
	}
	return gitFetchRemote{transport: transport, identity: host + "/" + identityPath, raw: raw}, nil
}

func sameGitFetchIdentity(left, right gitFetchRemote) bool {
	return left.identity == right.identity
}

func validGitFetchOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func runGitFetchCommand(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	return runGitFetchCommandInput(ctx, dir, env, nil, args...)
}

func runGitFetchCommandInput(ctx context.Context, dir string, env []string, input []byte, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append([]string(nil), env...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var output boundedFetchOutput
	output.limit = 16 * 1024 * 1024
	var stderr boundedFetchOutput
	cmd.Stdout = &output
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git command failed: %w: %s", err, stderr.String())
	}
	if output.full {
		return nil, errors.New("git command output exceeded the bounded snapshot limit")
	}
	return output.Bytes(), nil
}

type boundedFetchOutput struct {
	buffer bytes.Buffer
	limit  int
	full   bool
}

func (o *boundedFetchOutput) Write(data []byte) (int, error) {
	if o.limit == 0 {
		o.limit = 16 * 1024
	}
	remaining := o.limit - o.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			_, _ = o.buffer.Write(data[:remaining])
		} else {
			_, _ = o.buffer.Write(data)
		}
	}
	if len(data) > remaining {
		o.full = true
	}
	return len(data), nil
}

func (o *boundedFetchOutput) String() string { return o.buffer.String() }
func (o *boundedFetchOutput) Bytes() []byte  { return append([]byte(nil), o.buffer.Bytes()...) }

func fetchLocalEnv() []string {
	pathValue, _ := os.LookupEnv("PATH")
	if pathValue == "" {
		pathValue = "/usr/bin:/bin"
	}
	home, _ := os.UserHomeDir()
	return []string{"PATH=" + pathValue, "HOME=" + home, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
}

func classifyGitFetchCommand(err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "authentication failed"), strings.Contains(text, "could not read username"), strings.Contains(text, "permission denied (publickey)"), strings.Contains(text, "http basic: access denied"), strings.Contains(text, "terminal prompts disabled"):
		return gitFetchError(core.GitFetchFailureAuthentication)
	case strings.Contains(text, "redirect"), strings.Contains(text, "returned error: 301"), strings.Contains(text, "returned error: 302"), strings.Contains(text, "returned error: 307"), strings.Contains(text, "returned error: 308"):
		return gitFetchError(core.GitFetchFailureDestinationDenied)
	case strings.Contains(text, "could not resolve host"), strings.Contains(text, "could not connect"), strings.Contains(text, "connection timed out"), strings.Contains(text, "connection refused"), strings.Contains(text, "network is unreachable"), strings.Contains(text, "repository not found"), strings.Contains(text, "remote end hung up"):
		return gitFetchError(core.GitFetchFailureNetwork)
	default:
		return gitFetchError(core.GitFetchFailureGit)
	}
}

func gitFetchError(category string) error {
	return core.GitFetchError{Category: category}
}

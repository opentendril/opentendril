package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CredentialMethod enumerates the supported substrate authentication methods.
// Design RFC / implementation plan.
type CredentialMethod string

const (
	// CredentialUnspecified is the legacy default: no explicit method. A PAT is
	// resolved from the referenced env var (or the ambient github.com fallback
	// in the terrarium), preserving pre-RFC behavior.
	CredentialUnspecified CredentialMethod = ""
	CredentialPAT         CredentialMethod = "pat"
	CredentialSSH         CredentialMethod = "ssh"
	CredentialNone        CredentialMethod = "none"
	CredentialApp         CredentialMethod = "app"
)

// ResolvedSigning is the resolved commit-signing configuration.
type ResolvedSigning struct {
	Method string // "ssh" | "gpg" | "" (disabled)
	Key    string
}

// ResolvedIdentity is the resolved commit identity (author/committer name and
// email). Both fields empty means "unset": no identity config is applied and
// the ambient git identity in the terrarium attributes the commit.
type ResolvedIdentity struct {
	Name  string
	Email string
}

// AppCredential is the resolved GitHub App config (method "app"). The Stem mints
// short-lived installation tokens from it; it carries no long-lived secret.
type AppCredential struct {
	AppID          string
	InstallationID int64 // 0 => auto-discover from the substrate's repo
	PrivateKeyPath string
	PrivateKeyEnv  string
}

func (a AppCredential) isSet() bool {
	return strings.TrimSpace(a.AppID) != "" || strings.TrimSpace(a.PrivateKeyPath) != "" || strings.TrimSpace(a.PrivateKeyEnv) != ""
}

// Commit execution modes for the git.commit capability (SubstrateSpec.Commit /
// CredentialProfile.Commit). "local" (the default) is the existing Stem-side
// git path; "api" creates the commit remotely via the GitHub GraphQL
// createCommitOnBranch mutation, server-signed by GitHub. Design RFC.
const (
	CommitModeLocal = "local"
	CommitModeAPI   = "api"
)

// ResolvedCredential is the typed credential the terrarium (slice 3) consumes.
// It never carries a secret to a log: String() redacts TokenValue.
type ResolvedCredential struct {
	Method     CredentialMethod
	TokenEnv   string // env var name the PAT was read from (method pat)
	TokenValue string // resolved PAT secret (method pat) — never log this
	SSHKeyPath string // expanded key path (method ssh)
	App        AppCredential
	Sign       ResolvedSigning
	Identity   ResolvedIdentity
	Checkout   CheckoutSpec
	// CommitMode is the resolved git.commit execution mode: CommitModeLocal
	// (the default) or CommitModeAPI. Never empty after resolution.
	CommitMode string
}

// String redacts the token so a credential is safe to log or %v-print. The
// commit identity is not a secret, so name and email print in full.
func (c ResolvedCredential) String() string {
	token := ""
	if c.TokenValue != "" {
		token = "***"
	}
	return fmt.Sprintf("ResolvedCredential{method:%q env:%q token:%s sshKey:%q sign:%q identityName:%q identityEmail:%q checkout:%q commit:%q}",
		c.Method, c.TokenEnv, token, c.SSHKeyPath, c.Sign.Method, c.Identity.Name, c.Identity.Email, c.Checkout.Mode, c.CommitMode)
}

func isZeroAuthSpec(a AuthSpec) bool {
	return strings.TrimSpace(a.Method) == "" && strings.TrimSpace(a.Env) == "" && strings.TrimSpace(a.Key) == "" &&
		strings.TrimSpace(a.AppID) == "" && strings.TrimSpace(a.PrivateKeyPath) == "" && strings.TrimSpace(a.PrivateKeyEnv) == ""
}

func isZeroSignSpec(s SignSpec) bool {
	return strings.TrimSpace(s.Method) == "" && strings.TrimSpace(s.Key) == ""
}

func isZeroIdentitySpec(i IdentitySpec) bool {
	return strings.TrimSpace(i.Name) == "" && strings.TrimSpace(i.Email) == ""
}

// mergeCredentialProfile applies a named credentials profile as the base for a
// substrate's auth/sign/identity/commit, with any inline (non-zero) spec values
// taking precedence. Returns an error if the profile name is unknown.
func mergeCredentialProfile(spec SubstrateSpec, profiles map[string]CredentialProfile) (AuthSpec, SignSpec, IdentitySpec, string, error) {
	auth := spec.Auth
	sign := spec.Sign
	identity := spec.Identity
	commit := strings.ToLower(strings.TrimSpace(spec.Commit))

	profileName := strings.TrimSpace(spec.Profile)
	if profileName == "" {
		return auth, sign, identity, commit, nil
	}

	profile, ok := profiles[profileName]
	if !ok {
		return auth, sign, identity, commit, fmt.Errorf("references unknown credentials profile %q", profileName)
	}
	if isZeroAuthSpec(auth) {
		auth = profile.Auth
	}
	if isZeroSignSpec(sign) {
		sign = profile.Sign
	}
	if isZeroIdentitySpec(identity) {
		identity = profile.Identity
	}
	if commit == "" {
		commit = strings.ToLower(strings.TrimSpace(profile.Commit))
	}
	return auth, sign, identity, commit, nil
}

// resolveSubstrateCredential turns a substrate spec (+ credential profiles) into
// a typed, resolved credential. It preserves the GITHUB_TOKEN /
// GITHUB_PERSONAL_ACCESS_TOKEN fallback for PAT auth (see resolveAuthTokenValue).
func resolveSubstrateCredential(spec SubstrateSpec, profiles map[string]CredentialProfile) (ResolvedCredential, error) {
	auth, sign, identity, commit, err := mergeCredentialProfile(spec, profiles)
	if err != nil {
		return ResolvedCredential{}, err
	}

	method := CredentialMethod(strings.ToLower(strings.TrimSpace(auth.Method)))
	if method == CredentialUnspecified && strings.TrimSpace(auth.Env) != "" {
		// A bare env with no explicit method is a PAT (matches the scalar form).
		method = CredentialPAT
	}

	commitMode := commit
	if commitMode == "" {
		commitMode = CommitModeLocal
	}

	resolved := ResolvedCredential{
		Method:     method,
		Checkout:   spec.Checkout,
		Sign:       ResolvedSigning{Method: strings.ToLower(strings.TrimSpace(sign.Method)), Key: strings.TrimSpace(sign.Key)},
		Identity:   ResolvedIdentity{Name: strings.TrimSpace(identity.Name), Email: strings.TrimSpace(identity.Email)},
		CommitMode: commitMode,
	}

	switch method {
	case CredentialUnspecified, CredentialNone:
		// No explicit credential material to resolve.
	case CredentialPAT:
		resolved.TokenEnv = strings.TrimSpace(auth.Env)
		resolved.TokenValue = resolveAuthTokenValue(resolved.TokenEnv)
	case CredentialSSH:
		resolved.SSHKeyPath = expandHome(strings.TrimSpace(auth.Key))
	case CredentialApp:
		resolved.App = AppCredential{
			AppID:          strings.TrimSpace(auth.AppID),
			InstallationID: auth.InstallationID,
			PrivateKeyPath: expandHome(strings.TrimSpace(auth.PrivateKeyPath)),
			PrivateKeyEnv:  strings.TrimSpace(auth.PrivateKeyEnv),
		}
	default:
		return ResolvedCredential{}, fmt.Errorf("unknown auth method %q", auth.Method)
	}

	return resolved, nil
}

// gitSSHCommand builds a GIT_SSH_COMMAND that authenticates with only the given
// key (IdentitiesOnly) and accepts a first-seen host key so a foreign clone
// isn't blocked on an interactive known_hosts prompt.
func gitSSHCommand(keyPath string) string {
	return fmt.Sprintf("ssh -i %q -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new", keyPath)
}

// gitTokenCredentialEnvVar is the environment variable the inline git credential
// helper reads the token from. It lives only in the process environment.
const gitTokenCredentialEnvVar = "TENDRIL_GIT_TOKEN"

// gitTokenCredentialEnv returns process env that authenticates git over HTTPS via
// an inline credential helper reading the token from TENDRIL_GIT_TOKEN. The secret
// lives ONLY in the process environment (owner-readable /proc/<pid>/environ) —
// never on the command line (world-readable /proc/<pid>/cmdline) or in
// .git/config. x-access-token as the username works for App and PAT tokens.
func gitTokenCredentialEnv(token string) []string {
	// The helper references $TENDRIL_GIT_TOKEN by name, so the token never appears in
	// the helper text either. VALUE_0="" resets any inherited helper so only ours
	// is consulted; VALUE_1 installs it. Config travels via GIT_CONFIG_* (env),
	// not `-c` args, keeping it out of argv entirely.
	const helper = `!f() { test "$1" = get && printf 'username=x-access-token\npassword=%s\n' "$TENDRIL_GIT_TOKEN"; }; f`
	return []string{
		gitTokenCredentialEnvVar + "=" + token,
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1=" + helper,
	}
}

// materializeGitAuth resolves ready-to-use git authentication for a remote as
// process environment. Design RFC.
//
// Invariants: no secret ever appears in the clone URL, the command line, or the
// persisted .git/config; ssh/none never carry a PAT; the GitHub App token is
// minted fresh here (cached) so both clone and push get a currently-valid token.
func materializeGitAuth(ctx context.Context, cred ResolvedCredential, repoURL string) (env []string, err error) {
	switch cred.Method {
	case CredentialSSH:
		if cred.SSHKeyPath != "" {
			return []string{"GIT_SSH_COMMAND=" + gitSSHCommand(cred.SSHKeyPath)}, nil
		}
		return nil, nil
	case CredentialNone:
		return nil, nil
	case CredentialApp:
		token, tokenErr := githubAppInstallationToken(ctx, cred.App, repoURL)
		if tokenErr != nil {
			return nil, fmt.Errorf("github app auth: %w", tokenErr)
		}
		return gitTokenCredentialEnv(token), nil
	default: // pat or unspecified (legacy)
		token := cred.TokenValue
		if token == "" && cred.Method == CredentialUnspecified && strings.Contains(repoURL, "github.com") {
			if _, pat := resolveGitHubPAT(); pat != "" {
				token = pat
			}
		}
		if token == "" {
			return nil, nil
		}
		return gitTokenCredentialEnv(token), nil
	}
}

// expandHome expands a leading ~ or ~/ to the current user's home directory.
func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// credentialWarning returns a human-readable warning if a resolved credential is
// unusable for its method (env unset, key unreadable, unknown method/profile),
// or "" when it is fine. Used by validateSubstratesConfig — non-fatal at load.
func credentialWarning(spec SubstrateSpec, profiles map[string]CredentialProfile) string {
	resolved, err := resolveSubstrateCredential(spec, profiles)
	if err != nil {
		return err.Error()
	}
	switch resolved.Method {
	case CredentialPAT:
		if resolved.TokenValue == "" {
			return fmt.Sprintf("auth method pat references env %q, which is not set", resolved.TokenEnv)
		}
	case CredentialSSH:
		if resolved.SSHKeyPath == "" {
			return "auth method ssh has no key path"
		}
		if info, statErr := os.Stat(resolved.SSHKeyPath); statErr != nil || info.IsDir() {
			return fmt.Sprintf("auth method ssh key %q is not a readable file", resolved.SSHKeyPath)
		}
	case CredentialApp:
		if resolved.App.AppID == "" {
			return "auth method app has no appId"
		}
		if resolved.App.PrivateKeyPath == "" && resolved.App.PrivateKeyEnv == "" {
			return "auth method app has no privateKeyPath or privateKeyEnv"
		}
		if resolved.App.PrivateKeyEnv == "" && resolved.App.PrivateKeyPath != "" {
			if info, statErr := os.Stat(resolved.App.PrivateKeyPath); statErr != nil || info.IsDir() {
				return fmt.Sprintf("auth method app private key %q is not a readable file", resolved.App.PrivateKeyPath)
			}
		}
	}
	if w := signingWarning(resolved.Sign); w != "" {
		return w
	}
	if resolved.CommitMode == CommitModeAPI && resolved.Method != CredentialApp {
		return fmt.Sprintf("commit mode %q requires auth method \"app\" (GitHub signs the commit server-side); got %q", CommitModeAPI, resolved.Method)
	}
	return ""
}

// signingWarning validates a resolved signing config, or returns "" when unset/ok.
func signingWarning(sign ResolvedSigning) string {
	method := strings.ToLower(strings.TrimSpace(sign.Method))
	if method == "" {
		return ""
	}
	switch method {
	case "ssh", "gpg", "openpgp", "pgp":
	default:
		return fmt.Sprintf("sign method %q is not supported (use ssh or gpg)", sign.Method)
	}
	if strings.TrimSpace(sign.Key) == "" {
		return fmt.Sprintf("sign method %q has no key", sign.Method)
	}
	return ""
}

// signingGitConfigArgs returns the `-c ...` git config flags that make a commit
// signed with the substrate's configured key, or nil when signing is disabled.
// Supports SSH signing (gpg.format=ssh) and GPG/OpenPGP (gpg.format=openpgp).
func signingGitConfigArgs(sign ResolvedSigning) []string {
	method := strings.ToLower(strings.TrimSpace(sign.Method))
	key := strings.TrimSpace(sign.Key)
	if method == "" || key == "" {
		return nil
	}
	var format string
	switch method {
	case "ssh":
		format = "ssh"
	case "gpg", "openpgp", "pgp":
		format = "openpgp"
	default:
		return nil
	}
	return []string{
		"-c", "gpg.format=" + format,
		"-c", "user.signingkey=" + expandHome(key),
		"-c", "commit.gpgsign=true",
	}
}

// identityGitConfigArgs returns the `-c ...` git config flags that attribute a
// commit (both author and committer) to the substrate's configured identity,
// or nil when no identity is configured. When only one of name/email is set,
// only that field is emitted and git falls back to the ambient value for the
// other; when both are empty, nothing is emitted and the ambient git identity
// attributes the commit exactly as before.
func identityGitConfigArgs(identity ResolvedIdentity) []string {
	name := strings.TrimSpace(identity.Name)
	email := strings.TrimSpace(identity.Email)
	var args []string
	if name != "" {
		args = append(args, "-c", "user.name="+name)
	}
	if email != "" {
		args = append(args, "-c", "user.email="+email)
	}
	return args
}

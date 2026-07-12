package conductor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CredentialMethod enumerates the supported substrate authentication methods.
// Design RFC #222 / impl plan #225.
type CredentialMethod string

const (
	// CredentialUnspecified is the legacy default: no explicit method. A PAT is
	// resolved from the referenced env var (or the ambient github.com fallback
	// in the terrarium), preserving pre-RFC-#222 behavior.
	CredentialUnspecified CredentialMethod = ""
	CredentialPAT         CredentialMethod = "pat"
	CredentialSSH         CredentialMethod = "ssh"
	CredentialNone        CredentialMethod = "none"
)

// ResolvedSigning is the resolved commit-signing configuration.
type ResolvedSigning struct {
	Method string // "ssh" | "gpg" | "" (disabled)
	Key    string
}

// ResolvedCredential is the typed credential the terrarium (slice 3) consumes.
// It never carries a secret to a log: String() redacts TokenValue.
type ResolvedCredential struct {
	Method     CredentialMethod
	TokenEnv   string // env var name the PAT was read from (method pat)
	TokenValue string // resolved PAT secret (method pat) — never log this
	SSHKeyPath string // expanded key path (method ssh)
	Sign       ResolvedSigning
	Checkout   CheckoutSpec
}

// String redacts the token so a credential is safe to log or %v-print.
func (c ResolvedCredential) String() string {
	token := ""
	if c.TokenValue != "" {
		token = "***"
	}
	return fmt.Sprintf("ResolvedCredential{method:%q env:%q token:%s sshKey:%q sign:%q checkout:%q}",
		c.Method, c.TokenEnv, token, c.SSHKeyPath, c.Sign.Method, c.Checkout.Mode)
}

func isZeroAuthSpec(a AuthSpec) bool {
	return strings.TrimSpace(a.Method) == "" && strings.TrimSpace(a.Env) == "" && strings.TrimSpace(a.Key) == ""
}

func isZeroSignSpec(s SignSpec) bool {
	return strings.TrimSpace(s.Method) == "" && strings.TrimSpace(s.Key) == ""
}

// mergeCredentialProfile applies a named credentials profile as the base for a
// substrate's auth/sign, with any inline (non-zero) spec values taking
// precedence. Returns an error if the profile name is unknown.
func mergeCredentialProfile(spec SubstrateSpec, profiles map[string]CredentialProfile) (AuthSpec, SignSpec, error) {
	auth := spec.Auth
	sign := spec.Sign

	profileName := strings.TrimSpace(spec.Profile)
	if profileName == "" {
		return auth, sign, nil
	}

	profile, ok := profiles[profileName]
	if !ok {
		return auth, sign, fmt.Errorf("references unknown credentials profile %q", profileName)
	}
	if isZeroAuthSpec(auth) {
		auth = profile.Auth
	}
	if isZeroSignSpec(sign) {
		sign = profile.Sign
	}
	return auth, sign, nil
}

// resolveSubstrateCredential turns a substrate spec (+ credential profiles) into
// a typed, resolved credential. It preserves the GITHUB_TOKEN /
// GITHUB_PERSONAL_ACCESS_TOKEN fallback for PAT auth (see resolveAuthTokenValue).
func resolveSubstrateCredential(spec SubstrateSpec, profiles map[string]CredentialProfile) (ResolvedCredential, error) {
	auth, sign, err := mergeCredentialProfile(spec, profiles)
	if err != nil {
		return ResolvedCredential{}, err
	}

	method := CredentialMethod(strings.ToLower(strings.TrimSpace(auth.Method)))
	if method == CredentialUnspecified && strings.TrimSpace(auth.Env) != "" {
		// A bare env with no explicit method is a PAT (matches the scalar form).
		method = CredentialPAT
	}

	resolved := ResolvedCredential{
		Method:   method,
		Checkout: spec.Checkout,
		Sign:     ResolvedSigning{Method: strings.ToLower(strings.TrimSpace(sign.Method)), Key: strings.TrimSpace(sign.Key)},
	}

	switch method {
	case CredentialUnspecified, CredentialNone:
		// No explicit credential material to resolve.
	case CredentialPAT:
		resolved.TokenEnv = strings.TrimSpace(auth.Env)
		resolved.TokenValue = resolveAuthTokenValue(resolved.TokenEnv)
	case CredentialSSH:
		resolved.SSHKeyPath = expandHome(strings.TrimSpace(auth.Key))
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

// credentialGitInvocation returns the (possibly rewritten) URL and any extra
// environment for a git subprocess, per the credential method. Design RFC #222.
// Invariant: methods "ssh" and "none" NEVER inject a PAT (neither into the URL
// nor the environment); only "pat"/unspecified inject the HTTPS token.
func credentialGitInvocation(url string, cred ResolvedCredential) (string, []string) {
	switch cred.Method {
	case CredentialSSH:
		if cred.SSHKeyPath != "" {
			return url, []string{"GIT_SSH_COMMAND=" + gitSSHCommand(cred.SSHKeyPath)}
		}
		return url, nil
	case CredentialNone:
		return url, nil
	default: // pat or unspecified (legacy)
		tokenValue := cred.TokenValue
		tokenEnv := cred.TokenEnv
		// Preserve the legacy github.com ambient-PAT fallback for unspecified auth.
		if tokenValue == "" && cred.Method == CredentialUnspecified && strings.Contains(url, "github.com") {
			if patRef, pat := resolveGitHubPAT(); pat != "" {
				tokenValue, tokenEnv = pat, patRef
			}
		}
		if tokenValue == "" {
			return url, nil
		}
		if !strings.Contains(url, "@") && strings.HasPrefix(url, "https://") {
			url = strings.Replace(url, "https://", "https://"+tokenValue+"@", 1)
		}
		var env []string
		if tokenEnv != "" {
			env = append(env, tokenEnv+"="+tokenValue)
		}
		return url, env
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
	}
	if w := signingWarning(resolved.Sign); w != "" {
		return w
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

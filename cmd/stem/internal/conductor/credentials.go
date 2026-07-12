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
	return ""
}

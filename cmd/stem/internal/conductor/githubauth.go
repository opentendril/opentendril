package conductor

import (
	"os"
	"strings"
)

// GitHub personal-access-token environment variable names. GITHUB_TOKEN is
// the documented canonical name; GITHUB_PERSONAL_ACCESS_TOKEN is accepted as
// an alternate so older substrate configs keep working.
const (
	gitHubTokenEnv     = "GITHUB_TOKEN"
	gitHubPATLegacyEnv = "GITHUB_PERSONAL_ACCESS_TOKEN"
)

// alternateGitHubPATEnvVar returns the other accepted GitHub PAT variable
// name when the given name is one of the two, and "" otherwise.
func alternateGitHubPATEnvVar(name string) string {
	switch strings.TrimSpace(name) {
	case gitHubTokenEnv:
		return gitHubPATLegacyEnv
	case gitHubPATLegacyEnv:
		return gitHubTokenEnv
	}
	return ""
}

// resolveAuthTokenValue resolves the secret value for a substrate auth
// reference. It reads the referenced environment variable first; when that is
// unset/empty and the reference is one of the GitHub PAT names, it falls back
// to the alternate GitHub PAT name so users can set either GITHUB_TOKEN or
// GITHUB_PERSONAL_ACCESS_TOKEN.
func resolveAuthTokenValue(authRef string) string {
	ref := strings.TrimSpace(authRef)
	if ref == "" {
		return ""
	}
	if value := strings.TrimSpace(os.Getenv(ref)); value != "" {
		return value
	}
	if alt := alternateGitHubPATEnvVar(ref); alt != "" {
		return strings.TrimSpace(os.Getenv(alt))
	}
	return ""
}

// resolveGitHubPAT returns the first non-empty GitHub PAT from the accepted
// environment variables, preferring GITHUB_TOKEN, along with the variable
// name it was read from. Both return values are "" when no PAT is set.
func resolveGitHubPAT() (envName, value string) {
	for _, name := range []string{gitHubTokenEnv, gitHubPATLegacyEnv} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return name, v
		}
	}
	return "", ""
}

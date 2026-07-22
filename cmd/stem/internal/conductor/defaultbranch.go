package conductor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// The default-branch resolver: one answer to "which branch is this
// repository's default?", shared by every path that needs to know.
//
// The rule is absolute: nothing in a git execution path may assume a
// default-branch name. It is read, or it is reported as unknown.
//
// Precedence, most authoritative first:
//
//  1. the substrate's explicitly configured branch — an operator statement wins
//     over anything discovered;
//  2. the GitHub API, when the connection carries a token;
//  3. the local refs/remotes/origin/HEAD symbolic reference — no network and no
//     credential required, so it works offline;
//  4. undetermined — reported as such, never guessed.
//
// See defaultBranchProtectionFloor for what callers must do with step 4:
// protection widens under uncertainty, it never narrows.

// DefaultBranchSource records how a default branch was determined, so callers
// can report it and tests can assert the precedence rather than just the
// answer.
type DefaultBranchSource string

const (
	// DefaultBranchFromConfig means an operator configured it explicitly.
	DefaultBranchFromConfig DefaultBranchSource = "config"
	// DefaultBranchFromAPI means the GitHub API reported it.
	DefaultBranchFromAPI DefaultBranchSource = "api"
	// DefaultBranchFromRemoteHead means the local refs/remotes/origin/HEAD
	// symbolic reference reported it.
	DefaultBranchFromRemoteHead DefaultBranchSource = "remote-head"
	// DefaultBranchUnknown means it could not be determined. Callers must
	// apply the protection floor rather than assume a name.
	DefaultBranchUnknown DefaultBranchSource = "unknown"
)

// defaultBranchProtectionFloorNames is the ONLY place in a git execution path
// where well-known default-branch names legitimately appear, and their role
// here is the inverse of an assumption: they are not "the default branch", they
// are the minimum set treated as protected when the real default branch cannot
// be determined. Protection may widen under uncertainty; it must never narrow.
//
// scripts/check-default-branch-assumptions.sh enforces that these literals do
// not reappear anywhere else in the git paths.
var defaultBranchProtectionFloorNames = []string{"main", "master"}

// DefaultBranchResolution is a resolved (or deliberately unresolved) default
// branch, together with where the answer came from.
type DefaultBranchResolution struct {
	// Branch is the resolved default branch name, or "" when Source is
	// DefaultBranchUnknown. It is never a guess.
	Branch string
	// Source records which precedence step produced Branch.
	Source DefaultBranchSource
}

// Known reports whether the default branch was actually determined.
func (r DefaultBranchResolution) Known() bool {
	return r.Source != DefaultBranchUnknown && strings.TrimSpace(r.Branch) != ""
}

// IsProtected reports whether the named branch must be treated as the
// protected default branch.
//
// When the default branch is known, exactly that branch is protected — a
// repository on "trunk" protects "trunk", and does NOT protect a stray local
// branch that happens to be called "main". When it is unknown, the floor
// applies: the well-known names are protected anyway, because losing
// protection is a worse outcome than over-protecting a branch the caller can
// rename or configure.
func (r DefaultBranchResolution) IsProtected(branch string) bool {
	candidate := normalizeBranchName(branch)
	if candidate == "" {
		return false
	}
	if r.Known() {
		return candidate == r.Branch
	}
	for _, floor := range defaultBranchProtectionFloorNames {
		if candidate == floor {
			return true
		}
	}
	return false
}

// Describe renders the resolution for an error message, so a refusal always
// tells the reader what Tendril believed and why.
func (r DefaultBranchResolution) Describe() string {
	if r.Known() {
		return fmt.Sprintf("%q (from %s)", r.Branch, r.Source)
	}
	return fmt.Sprintf("undetermined, so the protected-name floor %v applies", defaultBranchProtectionFloorNames)
}

// resolveDefaultBranchLocally reads refs/remotes/origin/HEAD, the local record
// of the remote's default branch. It needs no network and no credential, which
// is why it serves the paths that have neither.
func resolveDefaultBranchLocally(ctx context.Context, workspace string) string {
	out, err := runGitCommitCommandFn(ctx, workspace, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return ""
	}
	// The reference reads as "origin/<branch>"; strip the remote prefix.
	ref := strings.TrimSpace(out)
	if ref == "" {
		return ""
	}
	if slash := strings.Index(ref, "/"); slash >= 0 {
		ref = ref[slash+1:]
	}
	return strings.TrimSpace(ref)
}

// resolveDefaultBranchFromAPI asks GitHub for the repository's default branch.
// It is only reached when the connection carries a usable token.
func resolveDefaultBranchFromAPI(ctx context.Context, cred ResolvedCredential, originURL string) string {
	owner, repo, err := parseOwnerRepo(originURL)
	if err != nil {
		return ""
	}
	token, err := pullRequestAPIToken(ctx, cred, originURL)
	if err != nil {
		return ""
	}
	var repository struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := githubRESTRequest(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s", owner, repo), token, nil, &repository); err != nil {
		return ""
	}
	return strings.TrimSpace(repository.DefaultBranch)
}

// ResolveDefaultBranchLocal resolves the default branch WITHOUT any network
// call: configured branch, then the local record of the remote's head, then
// undetermined.
//
// This is the right resolver for a *protection* decision — "is the branch I am
// about to commit on the one I must not commit on?" — for three reasons: it
// puts no network round trip in front of every commit; it works in a sealed or
// offline workspace; and a stale local record still cannot weaken protection,
// because an undetermined answer falls through to the floor rather than to
// permission. The authoritative resolver below is reserved for the case where
// the answer becomes part of a durable artifact.
func ResolveDefaultBranchLocal(ctx context.Context, workspace, configuredBranch string) DefaultBranchResolution {
	if ctx == nil {
		ctx = context.Background()
	}
	if configured := normalizeBranchName(configuredBranch); configured != "" {
		return DefaultBranchResolution{Branch: configured, Source: DefaultBranchFromConfig}
	}
	if branch := resolveDefaultBranchLocally(ctx, workspace); branch != "" {
		return DefaultBranchResolution{Branch: branch, Source: DefaultBranchFromRemoteHead}
	}
	return DefaultBranchResolution{Source: DefaultBranchUnknown}
}

// ResolveDefaultBranch resolves a workspace's default branch by the full
// documented precedence, including the authoritative interface lookup.
// configuredBranch is the substrate's explicit branch setting (may be empty);
// cred supplies the interface step (may be zero, in which case that step is
// skipped rather than failing).
//
// Use this when the resolved name becomes part of something durable — the base
// branch recorded on a pull request — where being right matters more than the
// round trip. For protection checks, prefer ResolveDefaultBranchLocal.
//
// It never returns an error: an undetermined default branch is a valid,
// explicitly-modelled outcome that callers handle with the protection floor.
// Making it an error would tempt callers into a fallback guess, which is the
// behaviour this whole resolver exists to prevent.
func ResolveDefaultBranch(ctx context.Context, workspace, configuredBranch string, cred ResolvedCredential) DefaultBranchResolution {
	if ctx == nil {
		ctx = context.Background()
	}

	if configured := normalizeBranchName(configuredBranch); configured != "" {
		return DefaultBranchResolution{Branch: configured, Source: DefaultBranchFromConfig}
	}

	originURL, err := runGitCommitCommandFn(ctx, workspace, "remote", "get-url", "origin")
	if err == nil {
		if trimmed := strings.TrimSpace(originURL); trimmed != "" {
			if branch := resolveDefaultBranchFromAPI(ctx, cred, trimmed); branch != "" {
				return DefaultBranchResolution{Branch: branch, Source: DefaultBranchFromAPI}
			}
		}
	}

	if branch := resolveDefaultBranchLocally(ctx, workspace); branch != "" {
		return DefaultBranchResolution{Branch: branch, Source: DefaultBranchFromRemoteHead}
	}

	return DefaultBranchResolution{Source: DefaultBranchUnknown}
}

// normalizeBranchName trims whitespace and the refs/heads/ prefix.
func normalizeBranchName(branch string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/"))
}

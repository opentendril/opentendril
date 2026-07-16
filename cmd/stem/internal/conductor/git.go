package conductor

import (
	"context"
	"fmt"
	"strings"
)

// Delegated git commit — the lowest rung of the delegated-execution ladder
// from the Design RFC. RunGitCommit commits the current state of a resolved
// local workspace directory under the substrate's configured commit identity,
// so an external agent never needs to shell out git on the host itself. Like
// commitTerrariumExecution, it runs the Stem's own git against the workspace
// directory on the host — no container is involved.
//
// Attribution rule (security-first, deny-closed): a delegated commit exists
// to be *attributable*, so a missing commit identity — either name or email —
// refuses the whole execution before any git command runs. No commit is ever
// created without a configured identity. This requirement lives ONLY on this
// delegated path: the ordinary Sprout commit path (commitTerrariumExecution)
// keeps its non-breaking ambient-identity default.

// GitCommitExecution is a fully resolved delegated-commit request: a
// workspace on disk, a message, the optional paths to stage, and the
// substrate's resolved credential carrying the commit identity and signing
// configuration.
type GitCommitExecution struct {
	// Workspace is the resolved local workspace directory the commit targets.
	Workspace string
	// Message is the commit message.
	Message string
	// Paths optionally limits staging to the given workspace-relative paths;
	// empty stages all changes.
	Paths []string
	// Credential is the substrate's resolved credential; its Identity must be
	// fully configured (deny-closed) and its Sign configuration is applied
	// when present.
	Credential ResolvedCredential
}

// GitCommitResult reports a finished delegated commit.
type GitCommitResult struct {
	// Status is "committed" when a commit was created, or "nothing-to-commit"
	// when staging produced no changes (no empty commit is ever created —
	// unlike the Sprout status path, which deliberately allows one).
	Status string
	// CommitHash is the created commit's hash (empty when nothing was
	// committed).
	CommitHash string
}

// ResolveSubstrateCredential resolves a substrate spec against the config's
// named credential profiles into the typed credential the delegated commit
// consumes. Exported for the adapter layer, which owns the wiring between the
// Core's transport-free port and this conductor (the Core itself never
// imports the conductor — see internal/core/boundary_test.go).
func ResolveSubstrateCredential(spec SubstrateSpec, config *SubstratesConfig) (ResolvedCredential, error) {
	var profiles map[string]CredentialProfile
	if config != nil {
		profiles = config.Credentials
	}
	return resolveSubstrateCredential(spec, profiles)
}

// runGitCommitCommandFn is the git seam, injectable for tests that exercise
// validation and the deny-closed identity requirement without a real
// repository.
var runGitCommitCommandFn = runGitCommand

// RunGitCommit stages and commits the workspace under the substrate's
// configured commit identity. Enforcement order is deliberate: the identity
// requirement is checked first, so a refused execution aborts before any git
// command (or any other side effect) runs.
func RunGitCommit(ctx context.Context, execution GitCommitExecution) (GitCommitResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" {
		return GitCommitResult{}, fmt.Errorf("git commit workspace is required")
	}
	if strings.TrimSpace(execution.Message) == "" {
		return GitCommitResult{}, fmt.Errorf("git commit message is required")
	}
	// Deny-closed attribution: an unattributable delegated commit must never
	// be created, so both identity fields are required before anything runs.
	if strings.TrimSpace(execution.Credential.Identity.Name) == "" || strings.TrimSpace(execution.Credential.Identity.Email) == "" {
		return GitCommitResult{}, fmt.Errorf("delegated git commit refused: the substrate has no configured commit identity (set identity name and email in substrates.yaml) — an unattributable delegated commit is never created")
	}

	// Stage: everything when no paths are given, else exactly the given paths.
	addArgs := []string{"add", "-A"}
	if len(execution.Paths) > 0 {
		addArgs = append([]string{"add", "--"}, execution.Paths...)
	}
	if _, err := runGitCommitCommandFn(ctx, execution.Workspace, addArgs...); err != nil {
		return GitCommitResult{}, err
	}

	// Nothing staged means nothing to commit: report it cleanly instead of
	// creating an empty commit.
	staged, err := runGitCommitCommandFn(ctx, execution.Workspace, "diff", "--cached", "--name-only")
	if err != nil {
		return GitCommitResult{}, err
	}
	if strings.TrimSpace(staged) == "" {
		return GitCommitResult{Status: "nothing-to-commit"}, nil
	}

	// Signing and identity config (`-c ...`) must precede the `commit`
	// subcommand — same ordering and precedence as commitTerrariumExecution.
	configArgs := append(signingGitConfigArgs(execution.Credential.Sign), identityGitConfigArgs(execution.Credential.Identity)...)
	commitArgs := append(append([]string{}, configArgs...), "commit", "-m", execution.Message)
	if _, err := runGitCommitCommandFn(ctx, execution.Workspace, commitArgs...); err != nil {
		return GitCommitResult{}, err
	}

	commitHash, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "HEAD")
	if err != nil {
		return GitCommitResult{}, err
	}

	return GitCommitResult{Status: "committed", CommitHash: commitHash}, nil
}

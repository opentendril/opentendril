package core

import (
	"context"
	"fmt"
	"strings"
)

// The git capability family. git.commit is the first (and for this slice the
// only) member: the lowest rung of the delegated-execution ladder from the
// Design RFC. It lets an external agent ask the Stem to commit the current
// state of a substrate's workspace — under that substrate's configured commit
// identity — instead of the agent shelling out git on the host itself. The
// family name deliberately leaves room to grow (git.push and friends are
// later slices); only the commit operation-class exists today, and it is
// commit-only by design: no push, no branch, no checkout, no merge.
//
// Attribution model (security-first): a delegated commit exists to be
// *attributable*, so the execution refuses to commit when the resolved
// substrate credential carries no commit identity — deny-closed, enforced in
// the conductor (see RunGitCommit). The git execution itself lives outside
// the Core in the conductor (which the Core is structurally forbidden from
// importing — see boundary_test.go), so it is injected as a transport-free
// function port via WithGit, the same template as PassthroughOperations.

// GitCommitInput asks the Stem to commit the current state of a substrate's
// workspace under the substrate's configured commit identity.
type GitCommitInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Message is the commit message.
	Message string `json:"message"`
	// Paths optionally limits staging to the given workspace-relative paths;
	// empty stages all changes.
	Paths []string `json:"paths,omitempty"`
	// Origin records which surface invoked the commit (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
}

// GitCommitSpec is the fully resolved, transport-free commit request handed
// to the GitOperations port.
type GitCommitSpec struct {
	Substrate string
	Message   string
	Paths     []string
	Origin    string
}

// GitCommitResult is the outcome of a finished delegated commit.
type GitCommitResult struct {
	// Status is "committed" when a commit was created, or "nothing-to-commit"
	// when the workspace held no changes (no empty commit is ever created).
	Status string `json:"status"`
	// CommitHash is the created commit's hash (empty when nothing was
	// committed).
	CommitHash string `json:"commitHash,omitempty"`
}

// GitPushInput asks the Stem to push the substrate's current branch to its
// remote using the substrate's configured credential. The push runs on the
// Stem (the sole secret-holding zone), never inside a sealed Sprout — a
// delegated push is the Stem's mediated egress with the connection's dedicated
// Personal Access Token.
type GitPushInput struct {
	// Substrate is the absolute path or named substrate key of the target
	// workspace.
	Substrate string `json:"substrate"`
	// Branch optionally names the branch to push; empty pushes the workspace's
	// current branch.
	Branch string `json:"branch,omitempty"`
	// Origin records which surface invoked the push (cli, mcp, rest).
	Origin string `json:"origin,omitempty"`
}

// GitPushSpec is the fully resolved, transport-free push request handed to the
// GitOperations port.
type GitPushSpec struct {
	Substrate string
	Branch    string
	Origin    string
}

// GitPushResult is the outcome of a finished delegated push.
type GitPushResult struct {
	// Status is always "pushed" when the push command succeeded (git treats an
	// already-current ref as a successful no-op push).
	Status string `json:"status"`
	// Branch is the branch that was pushed.
	Branch string `json:"branch,omitempty"`
}

// GitOperations is the injection port for delegated git execution. Each member
// may be nil, in which case the corresponding capability reports that it is not
// wired rather than acting.
type GitOperations struct {
	// Commit stages and commits the spec against the resolved workspace under
	// the substrate's configured commit identity. Implementations own
	// substrate resolution, credential resolution, and the deny-closed
	// identity requirement.
	Commit func(ctx context.Context, spec GitCommitSpec) (GitCommitResult, error)
	// Push pushes the resolved workspace's branch to its remote using the
	// substrate's resolved credential. Implementations own substrate
	// resolution, credential resolution, and the Stem-side authenticated push.
	Push func(ctx context.Context, spec GitPushSpec) (GitPushResult, error)
}

// WithGit wires the delegated git execution port onto the Service and returns
// the Service for chaining.
func (s *Service) WithGit(operations GitOperations) *Service {
	s.git = operations
	return s
}

// GitCommit validates the request and runs the delegated commit to completion
// via the injected execution port.
func (s *Service) GitCommit(ctx context.Context, in GitCommitInput) (GitCommitResult, error) {
	if s.git.Commit == nil {
		return GitCommitResult{}, fmt.Errorf("git.commit is not wired: construct the Core with WithGit(GitOperations{Commit: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return GitCommitResult{}, fmt.Errorf("substrate is required")
	}
	if strings.TrimSpace(in.Message) == "" {
		return GitCommitResult{}, fmt.Errorf("message is required")
	}
	// Blank path entries are dropped rather than handed to git verbatim; an
	// all-blank list degrades to "stage all changes", the same as omitting it.
	paths := make([]string, 0, len(in.Paths))
	for _, path := range in.Paths {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	if len(paths) == 0 {
		paths = nil
	}

	spec := GitCommitSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Message:   in.Message,
		Paths:     paths,
		Origin:    in.Origin,
	}
	return s.git.Commit(ctx, spec)
}

// GitPush validates the request and runs the delegated push to completion via
// the injected execution port.
func (s *Service) GitPush(ctx context.Context, in GitPushInput) (GitPushResult, error) {
	if s.git.Push == nil {
		return GitPushResult{}, fmt.Errorf("git.push is not wired: construct the Core with WithGit(GitOperations{Push: …})")
	}
	if strings.TrimSpace(in.Substrate) == "" {
		return GitPushResult{}, fmt.Errorf("substrate is required")
	}
	spec := GitPushSpec{
		Substrate: strings.TrimSpace(in.Substrate),
		Branch:    strings.TrimSpace(in.Branch),
		Origin:    in.Origin,
	}
	return s.git.Push(ctx, spec)
}

// gitCapabilities declares the git family's registry entry, bound to this
// Service's typed method — identical in shape to the other families.
func (s *Service) gitCapabilities() []Capability {
	return []Capability{
		{
			Name:        CapGitCommit,
			Description: "Commit the current state of a substrate's workspace under the substrate's configured commit identity; refused when no identity is configured (deny-closed — an unattributable delegated commit is never created).",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"message":   stringProp("The commit message."),
				"paths": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional workspace-relative paths to stage; empty stages all changes.",
				},
				"origin": stringProp("Interaction origin recorded on the commit (cli, mcp, rest)."),
			}, []string{"substrate", "message"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GitCommitInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GitCommit(ctx, in)
			},
		},
		{
			Name:        CapGitPush,
			Description: "Push a substrate's branch to its remote using the substrate's configured credential; the push runs on the Stem (the secret zone), never inside a sealed Sprout.",
			InputSchema: schemaObject(map[string]any{
				"substrate": stringProp("The absolute path or named substrate key for the target repository workspace."),
				"branch":    stringProp("The branch to push; omit to push the workspace's current branch."),
				"origin":    stringProp("Interaction origin recorded on the push (cli, mcp, rest)."),
			}, []string{"substrate"}),
			Invoke: func(ctx context.Context, input map[string]any) (any, error) {
				var in GitPushInput
				if err := decodeInput(input, &in); err != nil {
					return nil, err
				}
				return s.GitPush(ctx, in)
			},
		},
	}
}

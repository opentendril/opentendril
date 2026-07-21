package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Per-subject workspace isolation for the delegated git ladder.
//
// Without this, every delegated operation ran in one shared directory per
// substrate. Two agents granted the same substrate silently corrupted each
// other: the delegated commit stages the whole tree, so one agent's
// uncommitted files were committed by the other, onto the other's branch,
// under the other's identity — destroying exactly the attribution the
// delegated commit exists to provide. The second agent also branched from
// whatever the shared tree happened to be on, so it branched from the first
// agent's branch rather than from the substrate's.
//
// The fix reuses the mechanism the Sprout path already proves
// (createShadowWorktree): a real git worktree, private to the caller. The
// isolation unit is the DELEGATION SUBJECT, because the subject is already the
// unit of authorization and is already bound at connection time — so it
// requires no new parameter on any operation, and an agent's whole sequence
// (status, branch, commit, push, pull request) lands in one private tree
// without the agent tracking anything.
//
// A worktree shares the repository's object store with the substrate, so
// commits made in it are immediately visible to the substrate as branches —
// which is what makes push, pull requests, and human review work unchanged.
// Git also refuses to check out one branch in two worktrees at once, which
// turns "two subjects on the same branch" from silent corruption into a
// refusal.

// delegatedWorkspaceRoot is where per-subject worktrees live: under the Stem's
// own directory, never inside a substrate's checkout (a repository must not be
// able to widen or observe its own delegation surface).
func delegatedWorkspaceRoot() string {
	return filepath.Join(expandHome("~/.tendril"), "workspaces")
}

// sanitizeWorkspaceComponent makes a substrate or subject name safe as a single
// path component. Both are operator-controlled today, but the subject is the
// key an untrusted caller is identified by, so treating either as a raw path
// component would be the kind of assumption this codebase now explicitly
// refuses to make.
func sanitizeWorkspaceComponent(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	cleaned := strings.Trim(b.String(), ".-")
	if cleaned == "" {
		return "unnamed"
	}
	return cleaned
}

// workspaceLocks serializes operations that target the same workspace.
//
// Isolation removes agent-versus-agent corruption; it does not remove one
// subject issuing two overlapping calls. This is an in-process lock, which
// covers the realistic case — one Stem serving many agents — and deliberately
// does NOT claim to coordinate with a separate process operating on the same
// directory. Claiming more than it delivers would be worse than the honest
// limitation.
var workspaceLocks sync.Map

// LockWorkspace serializes access to one workspace path and returns the
// release function. Callers defer the release.
func LockWorkspace(path string) func() {
	value, _ := workspaceLocks.LoadOrStore(filepath.Clean(path), &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// DelegatedWorkspace describes where an operation will actually run.
type DelegatedWorkspace struct {
	// Path is the directory the operation runs in.
	Path string
	// Subject is the delegation subject it belongs to ("" when the operation
	// is not delegated).
	Subject string
	// Isolated reports whether Path is a per-subject worktree rather than the
	// substrate's own checkout.
	Isolated bool
}

// ResolveDelegatedWorkspace returns the workspace an operation should run in.
//
// With no subject — a human at a terminal — it returns the substrate's own
// checkout unchanged: an operator running `tendril git status` in their working
// copy must see their working copy.
//
// With a subject, it returns that subject's private worktree of the substrate,
// creating it on first use. The worktree starts detached at the substrate's
// current head, so the subject has no branch until it asks for one; the commit
// guard refuses a commit on a detached head, and git.status says so, which
// makes "create a branch first" the read-side's advice rather than a surprise.
func ResolveDelegatedWorkspace(ctx context.Context, substrateName, substratePath, subject string) (DelegatedWorkspace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	base := strings.TrimSpace(substratePath)
	if base == "" {
		return DelegatedWorkspace{}, fmt.Errorf("substrate path is required to resolve a workspace")
	}
	trimmedSubject := strings.TrimSpace(subject)
	if trimmedSubject == "" {
		return DelegatedWorkspace{Path: base}, nil
	}

	name := sanitizeWorkspaceComponent(substrateName)
	if strings.TrimSpace(substrateName) == "" {
		name = sanitizeWorkspaceComponent(filepath.Base(base))
	}
	path := filepath.Join(delegatedWorkspaceRoot(), name, sanitizeWorkspaceComponent(trimmedSubject))

	workspace := DelegatedWorkspace{Path: path, Subject: trimmedSubject, Isolated: true}
	if isGitRepo(path) {
		return workspace, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return DelegatedWorkspace{}, fmt.Errorf("create delegated workspace root: %w", err)
	}
	// --detach: the subject gets the substrate's current state with no branch
	// of its own yet, and cannot collide with a branch another worktree holds.
	if _, err := runGitCommitCommandFn(ctx, base, "worktree", "add", "--detach", path, "HEAD"); err != nil {
		return DelegatedWorkspace{}, fmt.Errorf("create isolated workspace for subject %q on substrate %q: %w", trimmedSubject, substrateName, err)
	}
	return workspace, nil
}

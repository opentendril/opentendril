package conductor

import (
	"path/filepath"
)

// The trusted tier: where the Stem looks for definitions it may act on with
// elevated privilege, and where it looks for the rest.
//
// Trust derives from ownership and unreachability, never from a path's name. A
// definition is trusted because it lives in the Stem's own control plane, which
// belongs to the Stem's principal and is never mounted into a Terrarium. No
// directory confers privilege by being called anything in particular.
//
// The two tiers collapse when the control plane and the workspace resolve to the
// same directory — a Stem running inside the repository a Sprout is editing. In
// that case NOTHING is trusted, because a Sprout could write it.

// controlPlaneDirName is the Stem's own directory, resolved against its working
// directory. It is the same name a workspace uses, which is why the two are
// compared rather than assumed distinct.
const controlPlaneDirName = ".tendril"

// Definition kinds.
const (
	DefinitionKindGenotypes = "genotypes"
	DefinitionKindSequences = "sequences"
)

// DefinitionSearchPath returns the directories searched for definitions of a
// kind, in precedence order, and how many leading entries are trusted.
//
// A trustedCount of zero means the tiers collapsed and nothing found on this
// path may be treated as privileged.
func DefinitionSearchPath(workspace, kind string) (dirs []string, trustedCount int) {
	controlPlane := filepath.Join(controlPlaneDirName, kind)
	workspaceDir := filepath.Join(workspace, controlPlaneDirName, kind)

	if sameDirectory(controlPlane, workspaceDir) {
		return []string{workspaceDir}, 0
	}
	return []string{controlPlane, workspaceDir}, 1
}

// TrustedDefinitionDirs returns only the directories whose contents may be
// treated as privileged. It is empty when the tiers collapse.
func TrustedDefinitionDirs(workspace, kind string) []string {
	dirs, trusted := DefinitionSearchPath(workspace, kind)
	return dirs[:trusted]
}

// QuarantineDir is where a refused script is written for inspection. It lives in
// the control plane so a Sprout can neither read nor alter what was quarantined.
func QuarantineDir() string {
	return filepath.Join(controlPlaneDirName, "quarantine")
}

// sameDirectory reports whether two paths name the same directory, resolving
// symbolic links where it can. Paths that cannot be resolved are compared as
// cleaned absolute paths, and an unanswerable comparison reports true — the
// tiers are treated as collapsed rather than assumed distinct.
func sameDirectory(a, b string) bool {
	resolvedA, okA := resolveDirectory(a)
	resolvedB, okB := resolveDirectory(b)
	if !okA || !okB {
		return true
	}
	return resolvedA == resolvedB
}

func resolveDirectory(path string) (string, bool) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved, true
	}
	// A directory that does not exist yet still has a comparable identity.
	return filepath.Clean(absolute), true
}

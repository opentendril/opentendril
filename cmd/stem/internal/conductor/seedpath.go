package conductor

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

const (
	seedRunWorkspaceMarker    = ".tendril/run-workspaces"
	seedPseudoHomePrefix      = "~/"
	seedVerifyDiagnosticBound = 240
)

// validateSeedCandidatePaths refuses to advance a Seed checkpoint when a newly
// introduced Git path is execution-location leakage rather than repository
// content. Paths that already existed in the starting tree are preserved.
func validateSeedCandidatePaths(ctx context.Context, repo, startRevision, checkpointCommit, hostRunWorkspace string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	startRevision = strings.TrimSpace(startRevision)
	checkpointCommit = strings.TrimSpace(checkpointCommit)
	if repo == "" || checkpointCommit == "" {
		return fmt.Errorf("seed candidate path integrity: checkpoint identity is incomplete")
	}

	checkpointPaths, err := gitTreePathSet(ctx, repo, checkpointCommit)
	if err != nil {
		return fmt.Errorf("seed candidate path integrity: list checkpoint paths: %w", err)
	}
	var startPaths map[string]struct{}
	if startRevision != "" && startRevision != "0000000000000000000000000000000000000000" {
		startPaths, err = gitTreePathSet(ctx, repo, startRevision)
		if err != nil {
			return fmt.Errorf("seed candidate path integrity: list starting paths: %w", err)
		}
	}

	for candidatePath := range checkpointPaths {
		if _, existed := startPaths[candidatePath]; existed {
			continue
		}
		if reason := seedCandidateExecutionLeakReason(candidatePath, hostRunWorkspace); reason != "" {
			return fmt.Errorf("seed candidate path integrity: newly created path %q is execution-location leakage (%s), not repository content", candidatePath, reason)
		}
	}
	return nil
}

func gitTreePathSet(ctx context.Context, repo, revision string) (map[string]struct{}, error) {
	raw, err := runGitCommandRawOutput(ctx, repo, "ls-tree", "-r", "--name-only", "-z", revision)
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{})
	for _, entry := range strings.Split(raw, "\x00") {
		// Git pathnames are identity, including legal leading/trailing
		// whitespace. Empty means the trailing NUL remainder, not a trimmed
		// path. Trimming would collapse distinct tree entries onto one key
		// and let a newly introduced forbidden path look pre-existing.
		if entry == "" {
			continue
		}
		paths[filepath.ToSlash(entry)] = struct{}{}
	}
	return paths, nil
}

func seedCandidateExecutionLeakReason(repoRelPath, hostRunWorkspace string) string {
	normalized := filepath.ToSlash(repoRelPath)
	for strings.Contains(normalized, "//") {
		normalized = strings.ReplaceAll(normalized, "//", "/")
	}
	if normalized == "" || normalized == "." {
		return ""
	}

	if strings.HasPrefix(normalized, seedPseudoHomePrefix) || normalized == "~" {
		return "pseudo-home execution path"
	}
	if path.IsAbs(normalized) {
		return "absolute execution path"
	}
	if strings.Contains(normalized, "/"+seedRunWorkspaceMarker+"/") ||
		strings.HasPrefix(normalized, seedRunWorkspaceMarker+"/") ||
		normalized == seedRunWorkspaceMarker {
		return "Tendril RunWorkspace state represented as repository content"
	}
	if hostPathProjectedIntoGitPath(normalized, hostRunWorkspace) {
		return "host RunWorkspace identity projected into a Git path"
	}
	return ""
}

func hostPathProjectedIntoGitPath(gitPath, hostRunWorkspace string) bool {
	host := filepath.ToSlash(filepath.Clean(strings.TrimSpace(hostRunWorkspace)))
	if host == "" || host == "." {
		return false
	}
	gitPath = filepath.ToSlash(gitPath)
	if gitPath == host || strings.HasPrefix(gitPath, host+"/") || strings.Contains(gitPath, host+"/") {
		return true
	}
	if strings.HasPrefix(host, "/") {
		trimmed := strings.TrimPrefix(host, "/")
		if trimmed != "" && (gitPath == trimmed || strings.HasPrefix(gitPath, trimmed+"/") || strings.Contains(gitPath, "/"+trimmed+"/")) {
			return true
		}
	}
	return false
}

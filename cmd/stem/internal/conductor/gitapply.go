package conductor

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/opentendril/opentendril/cmd/stem/internal/gitpatch"
)

// GitApplyExecution is a fully resolved deterministic patch request. The
// Substrate is an operator-configured name, while Workspace is the existing
// isolated worktree selected for the trusted Pollen.
type GitApplyExecution struct {
	Workspace    string
	Substrate    string
	ExpectedHead string
	Patch        []byte
}

// GitApplyResult reports safe facts about the unchanged branch/HEAD and paths
// changed by the successful application.
type GitApplyResult struct {
	Status       string
	Substrate    string
	Branch       string
	Head         string
	ChangedPaths []string
}

var (
	checkGitApply = gitpatch.Check
	applyGitPatch = gitpatch.Apply
)

// RunGitApply preflights and applies a patch to a clean existing workspace.
// The caller holds LockWorkspace for the entire operation. Git's default path
// safety checks remain enabled; no unsafe-path, reject, three-way, or index
// mode is used.
func RunGitApply(ctx context.Context, execution GitApplyExecution) (GitApplyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(execution.Workspace) == "" || strings.TrimSpace(execution.Substrate) == "" {
		return GitApplyResult{}, fmt.Errorf("git.apply requires a resolved workspace and configured Substrate")
	}
	if !isFullGitObjectID(execution.ExpectedHead) {
		return GitApplyResult{}, fmt.Errorf("expectedHead must be a full Git object ID")
	}
	if !utf8.Valid(execution.Patch) {
		return GitApplyResult{}, fmt.Errorf("patch must be valid UTF-8")
	}

	headOutput, err := runGitCommitCommandFn(ctx, execution.Workspace, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return GitApplyResult{}, fmt.Errorf("unable to read workspace HEAD")
	}
	head := strings.TrimSpace(headOutput)
	if head != execution.ExpectedHead {
		return GitApplyResult{}, fmt.Errorf("expectedHead does not match workspace HEAD")
	}

	branchOutput, err := runGitCommitCommandFn(ctx, execution.Workspace, "branch", "--show-current")
	if err != nil {
		return GitApplyResult{}, fmt.Errorf("unable to read workspace branch")
	}
	branch := strings.TrimSpace(branchOutput)
	if branch == "" {
		return GitApplyResult{}, fmt.Errorf("git.apply requires a workspace on a branch")
	}

	status, err := runGitCommandRawOutput(ctx, execution.Workspace, "status", "--porcelain", "-uall", "--ignore-submodules=none", "-z")
	if err != nil {
		return GitApplyResult{}, fmt.Errorf("unable to inspect workspace state")
	}
	if status != "" {
		return GitApplyResult{}, fmt.Errorf("git.apply requires a clean staged, unstaged, and untracked workspace")
	}
	paths, err := gitpatch.InspectPaths(ctx, execution.Workspace, execution.Patch)
	if err != nil {
		return GitApplyResult{}, gitpatch.ErrPreflightFailed
	}
	if err := validatePatchPathContainment(execution.Workspace, paths); err != nil {
		return GitApplyResult{}, fmt.Errorf("git patch contains an unsafe path")
	}

	if err := checkGitApply(ctx, execution.Workspace, execution.Patch); err != nil {
		return GitApplyResult{}, gitpatch.ErrPreflightFailed
	}
	if err := applyGitPatch(ctx, execution.Workspace, execution.Patch); err != nil {
		return GitApplyResult{}, gitpatch.ErrApplyFailed
	}

	changedOutput, err := runGitCommandRawOutput(ctx, execution.Workspace, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return GitApplyResult{}, fmt.Errorf("unable to read changed workspace paths")
	}
	changedPaths := parsePorcelainPaths(changedOutput)
	return GitApplyResult{
		Status:       "applied",
		Substrate:    strings.TrimSpace(execution.Substrate),
		Branch:       branch,
		Head:         head,
		ChangedPaths: changedPaths,
	}, nil
}

func isFullGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func parsePorcelainPaths(raw string) []string {
	fields := strings.Split(raw, "\x00")
	paths := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		record := fields[i]
		if len(record) < 4 {
			continue
		}
		paths = append(paths, record[3:])
		if (record[0] == 'R' || record[0] == 'C' || record[1] == 'R' || record[1] == 'C') && i+1 < len(fields) && fields[i+1] != "" {
			paths = append(paths, fields[i+1])
			i++
		}
	}
	sort.Strings(paths)
	unique := paths[:0]
	for _, path := range paths {
		if len(unique) == 0 || path != unique[len(unique)-1] {
			unique = append(unique, path)
		}
	}
	return unique
}

func validatePatchPathContainment(workspace string, paths gitpatch.PatchPaths) error {
	for _, original := range paths.Original {
		if patchPathEscapes(original) {
			return fmt.Errorf("unsafe original patch path")
		}
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	root = filepath.Clean(root)
	for _, relative := range paths.Workspace {
		if patchPathEscapes(relative) {
			return fmt.Errorf("unsafe workspace-relative patch path")
		}
		clean := filepath.Clean(filepath.FromSlash(relative))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe workspace-relative patch path")
		}
		target := filepath.Join(root, clean)
		rel, err := filepath.Rel(root, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("patch path escapes workspace")
		}
		current := root
		for _, component := range strings.Split(clean, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			info, statErr := os.Lstat(current)
			if statErr != nil {
				if os.IsNotExist(statErr) {
					break
				}
				return statErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("patch path crosses a symbolic link")
			}
		}
	}
	return nil
}

func patchPathEscapes(value string) bool {
	if value == "" || filepath.IsAbs(filepath.FromSlash(value)) || strings.HasPrefix(value, "/") {
		return true
	}
	portable := strings.ReplaceAll(value, `\`, "/")
	if len(portable) >= 3 && ((portable[0] >= 'A' && portable[0] <= 'Z') || (portable[0] >= 'a' && portable[0] <= 'z')) && portable[1] == ':' && portable[2] == '/' {
		return true
	}
	for _, component := range strings.Split(portable, "/") {
		if component == ".." {
			return true
		}
	}
	return false
}

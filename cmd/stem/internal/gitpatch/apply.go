// Package gitpatch contains the fixed Stem-side Git patch primitive shared by
// delegated Git and Mesh's Terrarium workflow. It has no patch interpretation
// logic: Git itself parses, checks, and applies the supplied bytes.
package gitpatch

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

var (
	ErrPreflightFailed = errors.New("git patch preflight failed")
	ErrApplyFailed     = errors.New("git patch application failed")
	ErrPathInspection  = errors.New("git patch path inspection failed")
)

// PatchPaths are Git's own machine-readable interpretation of the patch's
// original (strip-zero) and workspace-relative (strip-one) paths. No patch
// syntax is parsed in Go.
type PatchPaths struct {
	Original  []string
	Workspace []string
}

type commandRunner func(context.Context, string, []byte, ...string) error

var runCommand commandRunner = runGit

func runGit(ctx context.Context, workspace string, patch []byte, args ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspace
	cmd.Stdin = bytes.NewReader(patch)
	// Git's diagnostic output can contain patch content and filesystem paths.
	// The capability returns stable, safe errors instead of this output.
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// Check asks Git to validate the complete patch without modifying the
// workspace. Its flags are intentionally fixed for every caller.
func Check(ctx context.Context, workspace string, patch []byte) error {
	if strings.TrimSpace(workspace) == "" {
		return ErrPreflightFailed
	}
	if err := runCommand(ctx, workspace, patch, "apply", "--check", "--binary", "--whitespace=nowarn", "-"); err != nil {
		return ErrPreflightFailed
	}
	return nil
}

// Apply applies a fully preflighted patch with Git's normal all-or-nothing
// apply semantics. Reject and three-way modes are never enabled.
func Apply(ctx context.Context, workspace string, patch []byte) error {
	if strings.TrimSpace(workspace) == "" {
		return ErrApplyFailed
	}
	if err := runCommand(ctx, workspace, patch, "apply", "--binary", "--whitespace=nowarn", "-"); err != nil {
		return ErrApplyFailed
	}
	return nil
}

// InspectPaths asks Git to parse its NUL-delimited numstat format at the
// original and normal strip levels. The caller uses these names only for
// containment checks before the complete --check preflight.
func InspectPaths(ctx context.Context, workspace string, patch []byte) (PatchPaths, error) {
	if strings.TrimSpace(workspace) == "" {
		return PatchPaths{}, ErrPathInspection
	}
	original, originalErr := runNumstat(ctx, workspace, patch, "-p0")
	workspacePaths, err := runNumstat(ctx, workspace, patch, "-p1")
	if err != nil {
		return PatchPaths{}, ErrPathInspection
	}
	paths := PatchPaths{Workspace: parseNumstatPaths(workspacePaths)}
	if originalErr == nil {
		paths.Original = parseNumstatPaths(original)
	}
	return paths, nil
}

func runNumstat(ctx context.Context, workspace string, patch []byte, strip string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "--numstat", "-z", strip, "--binary", "--whitespace=nowarn", "-")
	cmd.Dir = workspace
	cmd.Stdin = bytes.NewReader(patch)
	return cmd.Output()
}

// parseNumstatPaths decodes Git's documented --numstat -z output, including
// the two-path record used for renames. It does not read or interpret a patch.
func parseNumstatPaths(raw []byte) []string {
	fields := bytes.Split(raw, []byte{0})
	paths := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		record := fields[i]
		if len(record) == 0 {
			continue
		}
		firstTab := bytes.IndexByte(record, '\t')
		if firstTab < 0 {
			continue
		}
		rest := record[firstTab+1:]
		secondRelative := bytes.IndexByte(rest, '\t')
		if secondRelative < 0 {
			continue
		}
		path := rest[secondRelative+1:]
		if len(path) != 0 {
			paths = append(paths, string(path))
			continue
		}
		if i+2 < len(fields) {
			paths = append(paths, string(fields[i+1]), string(fields[i+2]))
			i += 2
		} else {
			return nil
		}
	}
	return paths
}

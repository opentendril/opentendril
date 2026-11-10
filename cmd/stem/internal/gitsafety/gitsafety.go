// Package gitsafety keeps a remote-git Stem from executing repository-local
// Git hooks, filters, and helpers. Command-line configuration outranks the
// repository and the Stem's global Git configuration. The full posture is
// unchanged: this package returns no arguments unless the resolved posture
// is remote-git.
package gitsafety

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/core"
)

// errRemoteGitSafety is the only error returned to callers. It carries no
// repository path and no configuration key.
var errRemoteGitSafety = errors.New("remote-git git command refused")

const safetyKeyPattern = `^(filter|diff|merge)\.([A-Za-z0-9_-]+)\.(clean|smudge|process|command|textconv|driver)$`

var safetyKeyRegexp = regexp.MustCompile(safetyKeyPattern)

// Args returns command-line Git configuration that disables repository-local
// execution for a remote-git Stem. dir is the repository the Git command will
// use. env is the environment that Git command will receive, and it is
// consulted only to see whether the Stem already supplied credential.helper.
// Posture is resolved from the process environment and the account control
// plane, not from env: a fetch stage replaces HOME and must not hide the
// Stem posture. A full posture returns no arguments. An ambiguous posture
// refuses the command.
func Args(ctx context.Context, dir string, env []string) ([]string, error) {
	plane, err := stemControlPlaneDir()
	if err != nil {
		return nil, errRemoteGitSafety
	}
	posture, err := core.ResolveDeploymentPosture(plane)
	if err != nil || (posture != core.DeploymentPostureFull && posture != core.DeploymentPostureRemoteGit) {
		return nil, errRemoteGitSafety
	}
	if posture != core.DeploymentPostureRemoteGit {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return remoteGitArgs(ctx, dir, env)
}

// Insert places safety arguments after leading -c pairs and before the Git
// subcommand. Later command-line keys win, so the safety keys outrank a
// caller -c for the same key, while caller identity keys still apply.
func Insert(args, safety []string) []string {
	if len(safety) == 0 {
		return args
	}
	index := 0
	for index+1 < len(args) && args[index] == "-c" {
		index += 2
	}
	out := make([]string, 0, len(args)+len(safety))
	out = append(out, args[:index]...)
	out = append(out, safety...)
	out = append(out, args[index:]...)
	return out
}

func stemControlPlaneDir() (string, error) {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".tendril"), nil
	}
	current, err := user.Current()
	if err != nil || current == nil || strings.TrimSpace(current.HomeDir) == "" {
		return "", errRemoteGitSafety
	}
	return filepath.Join(current.HomeDir, ".tendril"), nil
}

func remoteGitArgs(ctx context.Context, dir string, env []string) ([]string, error) {
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=",
		"-c", "diff.external=",
		"-c", "core.sshCommand=",
		"-c", "protocol.ext.allow=never",
	}
	if !credentialHelperSupplied(env) {
		args = append(args, "-c", "credential.helper=")
	}
	dynamic, err := enumeratedArgs(ctx, dir, env)
	if err != nil {
		return nil, err
	}
	return append(args, dynamic...), nil
}

// credentialHelperSupplied reports whether env already names credential.helper
// through GIT_CONFIG_KEY_*. An additional -c credential.helper= would replace
// the Stem helper and break HTTPS authentication.
func credentialHelperSupplied(env []string) bool {
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GIT_CONFIG_KEY_") {
			continue
		}
		rest := strings.TrimPrefix(entry, "GIT_CONFIG_KEY_")
		_, value, ok := strings.Cut(rest, "=")
		if ok && value == "credential.helper" {
			return true
		}
	}
	return false
}

func enumeratedArgs(ctx context.Context, dir string, env []string) ([]string, error) {
	commandArgs := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=",
		"-c", "diff.external=",
		"-c", "core.sshCommand=",
		"config", "--null", "--get-regexp", safetyKeyPattern,
	}
	if dir != "" {
		commandArgs = append([]string{"-C", dir}, commandArgs...)
	}
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	if env != nil {
		cmd.Env = append([]string(nil), env...)
	}
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := string(exitErr.Stderr)
			if strings.Contains(stderr, "not a git repository") {
				return nil, nil
			}
			if exitErr.ExitCode() == 1 && len(bytes.TrimSpace(output)) == 0 && !strings.Contains(stderr, "fatal:") {
				return nil, nil
			}
		}
		return nil, errRemoteGitSafety
	}
	if len(output) > 1<<20 {
		return nil, errRemoteGitSafety
	}
	return overridesForKeys(output)
}

func overridesForKeys(raw []byte) ([]string, error) {
	filters := map[string]struct{}{}
	diffs := map[string]struct{}{}
	merges := map[string]struct{}{}
	for len(raw) > 0 {
		record, rest, found := bytes.Cut(raw, []byte{0})
		raw = rest
		if len(record) == 0 && !found {
			break
		}
		key, _, _ := bytes.Cut(record, []byte{'\n'})
		if len(key) == 0 {
			continue
		}
		match := safetyKeyRegexp.FindStringSubmatch(string(key))
		if match == nil {
			return nil, errRemoteGitSafety
		}
		switch match[1] {
		case "filter":
			switch match[3] {
			case "clean", "smudge", "process":
				filters[match[2]] = struct{}{}
			default:
				return nil, errRemoteGitSafety
			}
		case "diff":
			switch match[3] {
			case "command", "textconv":
				diffs[match[2]] = struct{}{}
			default:
				return nil, errRemoteGitSafety
			}
		case "merge":
			if match[3] != "driver" {
				return nil, errRemoteGitSafety
			}
			merges[match[2]] = struct{}{}
		default:
			return nil, errRemoteGitSafety
		}
		if !found {
			break
		}
	}
	var args []string
	for _, name := range sortedKeys(filters) {
		args = append(args,
			"-c", "filter."+name+".clean=",
			"-c", "filter."+name+".smudge=",
			"-c", "filter."+name+".process=",
			"-c", "filter."+name+".required=false",
		)
	}
	for _, name := range sortedKeys(diffs) {
		args = append(args,
			"-c", "diff."+name+".command=",
			"-c", "diff."+name+".textconv=",
		)
	}
	for _, name := range sortedKeys(merges) {
		args = append(args,
			"-c", "merge."+name+".driver=",
		)
	}
	return args, nil
}

func sortedKeys(values map[string]struct{}) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

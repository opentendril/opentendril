package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opentendril/opentendril/cmd/stem/internal/conductor"
)

func runAdaptCmd(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("adapt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	commits := fs.Int("commits", 50, "number of recent commits to analyze")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *commits <= 0 {
		fmt.Fprintln(os.Stderr, "❌ --commits must be greater than zero")
		os.Exit(1)
	}

	root := resolveRepoRoot("")
	if !gitHasCommits(ctx, root) {
		fmt.Println("🌱 Substrate has no commit history yet — nothing to adapt.")
		return
	}

	hashes, err := gitCommitHashes(ctx, root, *commits)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to read git history: %v\n", err)
		os.Exit(1)
	}

	if len(hashes) == 0 {
		fmt.Println("🌱 No commits found to adapt.")
		return
	}

	fmt.Printf("🧬 Mining %d commit(s) from %s for Epigenetic Traits...\n", len(hashes), root)

	samples := make([]conductor.CommitSample, 0, len(hashes))
	hadError := false

	for index, hash := range hashes {
		shortHash := hash
		if len(shortHash) > 12 {
			shortHash = shortHash[:12]
		}

		fmt.Fprintf(os.Stdout, "🧬 [%d/%d] Extracting commit %s\n", index+1, len(hashes), shortHash)

		commitMessage, err := gitCommitMessage(ctx, root, hash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to read commit message for %s: %v\n", shortHash, err)
			hadError = true
			continue
		}

		diff, err := gitShowCommit(ctx, root, hash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Failed to read diff for %s: %v\n", shortHash, err)
			hadError = true
			continue
		}

		samples = append(samples, conductor.CommitSample{
			Hash:    hash,
			Message: commitMessage,
			Diff:    diff,
		})
	}

	if len(samples) == 0 {
		fmt.Fprintln(os.Stderr, "❌ No commit diffs could be extracted.")
		os.Exit(1)
	}

	chronicler := conductor.NewEpigeneticChronicler(root)
	if err := chronicler.AdaptFromHistory(ctx, samples); err != nil {
		fmt.Printf("⚠️ Extracted %d commit(s); Adaptation was skipped because the Meristem was unreachable: %v\n", len(samples), err)
		return
	}

	if hadError {
		fmt.Printf("⚠️ Adapted %d of %d commit(s); some diffs could not be read.\n", len(samples), len(hashes))
		os.Exit(1)
	}

	fmt.Printf("✅ Adapted %d commit(s) into %s\n", len(samples), filepath.Join(root, ".tendril", "genome", "epigenetics.md"))
}

func gitHasCommits(ctx context.Context, repoRoot string) bool {
	_, err := runGit(ctx, repoRoot, "rev-parse", "--verify", "HEAD")
	return err == nil
}

func gitCommitHashes(ctx context.Context, repoRoot string, commits int) ([]string, error) {
	out, err := runGit(ctx, repoRoot, "log", "-n", fmt.Sprintf("%d", commits), "--pretty=format:%H")
	if err != nil {
		return nil, err
	}

	var hashes []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			hashes = append(hashes, line)
		}
	}

	return hashes, nil
}

func gitCommitMessage(ctx context.Context, repoRoot, hash string) (string, error) {
	out, err := runGit(ctx, repoRoot, "log", "-1", "--pretty=format:%B", hash)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(out), nil
}

func gitShowCommit(ctx context.Context, repoRoot, hash string) (string, error) {
	return runGit(ctx, repoRoot, "show", "--no-color", hash)
}

func runGit(ctx context.Context, repoRoot string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", repoRoot}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}

	return string(output), nil
}

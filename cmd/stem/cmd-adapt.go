package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/orchestrator"
)

func runAdaptCmd(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("adapt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	commits := fs.Int("commits", 5, "number of recent commits to analyze")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *commits <= 0 {
		fmt.Fprintln(os.Stderr, "❌ --commits must be greater than zero")
		os.Exit(1)
	}

	root := resolveRepoRoot("")
	hashes, err := gitCommitHashes(ctx, root, *commits)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to read git history: %v\n", err)
		os.Exit(1)
	}

	if len(hashes) == 0 {
		fmt.Println("No commits found to adapt.")
		return
	}

	chronicler := orchestrator.NewEpigeneticChronicler(root)
	hadError := false
	transcriptionSkipped := false

	for index, hash := range hashes {
		shortHash := hash
		if len(shortHash) > 12 {
			shortHash = shortHash[:12]
		}

		fmt.Fprintf(os.Stdout, "🧬 [%d/%d] Analyzing commit %s\n", index+1, len(hashes), shortHash)

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

		logs := fmt.Sprintf("Commit hash: %s", hash)
		if err := chronicler.TranscribeLearnings(ctx, commitMessage, diff, logs); err != nil {
			fmt.Fprintf(os.Stderr, "⚠️ Epigenetic transcription failed for %s: %v\n", shortHash, err)
			transcriptionSkipped = true
		}
	}

	if hadError {
		os.Exit(1)
	}

	if transcriptionSkipped {
		fmt.Printf("⚠️ Parsed %d commit(s); epigenetic transcription was skipped because no LLM endpoint was reachable.\n", len(hashes))
		return
	}

	fmt.Printf("✅ Adapted %d commit(s) into %s\n", len(hashes), strings.TrimSpace(root))
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

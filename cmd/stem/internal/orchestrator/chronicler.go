package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/llm"
)

const epigeneticGenomeHeader = "# Epigenetic Learnings"
const genomeAutoPushCommitMessage = "chore(genome): epigenetic transcription update [skip ci]"

const (
	defaultMaxSection       = 12000
	defaultGenomeTokenLimit = 2000
	genomeCharsPerToken     = 4
)

// EpigeneticChronicler distills durable learnings from successful Sprout runs.
type EpigeneticChronicler struct {
	workspace string
	client    *llm.Client
}

// NewEpigeneticChronicler constructs a chronicler for the provided workspace.
func NewEpigeneticChronicler(workspace string) *EpigeneticChronicler {
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}

	return &EpigeneticChronicler{
		workspace: repoRoot(workspace),
		client:    llm.NewClientFromEnv(),
	}
}

// TranscribeLearnings asks the host LLM to summarize durable learnings and appends them to the genome.
func (c *EpigeneticChronicler) TranscribeLearnings(ctx context.Context, transcript string, diff string, logs string) error {
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	systemPrompt, userPrompt := buildEpigeneticPrompt(transcript, diff, logs)
	findings, err := c.client.CallPrompt(ctx, systemPrompt, userPrompt)
	if err != nil {
		return err
	}

	findings = normalizeMarkdownBullets(findings)
	if strings.TrimSpace(findings) == "" {
		return nil
	}

	if err := c.appendToGenome(findings); err != nil {
		return err
	}

	targetPath := c.genomePath()
	if reduced, err := c.maybeReduceGenome(ctx, targetPath); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Genome auto-reduction skipped: %v\n", err)
	} else if reduced {
		fmt.Fprintf(os.Stderr, "🧬 Genome auto-reduced at %s\n", targetPath)
	}

	if err := c.maybeAutoPushGenome(targetPath); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️ Genome auto-push skipped: %v\n", err)
	}

	return nil
}

// ReduceGenomeFile consolidates the active epigenetic genome in place.
func (c *EpigeneticChronicler) ReduceGenomeFile(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	targetPath := c.genomePath()
	content, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("epigenetic genome not found at %s", targetPath)
		}
		return fmt.Errorf("read epigenetic genome: %w", err)
	}

	reduced, err := c.reduceGenomeContent(ctx, string(content))
	if err != nil {
		return err
	}

	if err := os.WriteFile(targetPath, []byte(reduced), 0o644); err != nil {
		return fmt.Errorf("write reduced epigenetic genome: %w", err)
	}

	return nil
}

func buildEpigeneticPrompt(transcript string, diff string, logs string) (string, string) {
	transcript = truncateMiddle(strings.TrimSpace(transcript), defaultMaxSection)
	diff = truncateMiddle(strings.TrimSpace(diff), defaultMaxSection)
	logs = truncateMiddle(strings.TrimSpace(logs), defaultMaxSection)

	systemPrompt := strings.TrimSpace(`
You are the OpenTendril Epigenetic Chronicler.
Extract durable, repository-specific learnings from successful task runs.
Return only concise Markdown bullet points.
`)

	userPrompt := fmt.Sprintf(`Analyze the task transcript, git diff, and run logs.
Extract only durable learnings:
- Architectural gotchas or codebase-specific constraints discovered.
- Dependency or version requirements that were used.
- Naming conventions or styling constraints required by the code.

If nothing durable was learned, return:
- No durable learnings.

Task transcript:
%s

Git diff:
%s

Run logs:
%s
`, transcript, diff, logs)

	return systemPrompt, strings.TrimSpace(userPrompt)
}

func buildGenomeReductionPrompt(existing string) (string, string) {
	existing = truncateMiddle(strings.TrimSpace(existing), defaultMaxSection)

	systemPrompt := strings.TrimSpace(`
You are the OpenTendril Genome Reducer.
Compress, deduplicate, and merge the genome into a clean list of high-level, durable principles.
Return only concise Markdown bullet points.
Do not mention temporary implementation details, commit hashes, or duplicated file paths.
`)

	userPrompt := fmt.Sprintf(`Reduce the following epigenetic genome into durable, reusable principles.

Requirements:
- Preserve only long-lived rules that should steer future Tendril runs.
- Merge overlapping bullets.
- Prefer generalized principles over one-off commands or task-specific notes.
- Keep the final list concise, ideally under 12 bullets.

Genome content:
%s
`, existing)

	return systemPrompt, strings.TrimSpace(userPrompt)
}

func (c *EpigeneticChronicler) reduceGenomeContent(ctx context.Context, existing string) (string, error) {
	systemPrompt, userPrompt := buildGenomeReductionPrompt(existing)
	findings, err := c.client.CallPrompt(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", err
	}

	findings = normalizeMarkdownBullets(findings)
	if strings.TrimSpace(findings) == "" {
		return "", fmt.Errorf("LLM returned no genome reduction output")
	}

	return epigeneticGenomeHeader + "\n\n" + findings + "\n", nil
}

func truncateMiddle(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 24 {
		return text[:limit]
	}

	head := (limit - 24) / 2
	tail := limit - head - 24
	if head < 1 {
		head = 1
	}
	if tail < 1 {
		tail = 1
	}

	return text[:head] + "\n... [truncated] ...\n" + text[len(text)-tail:]
}

var bulletPrefix = regexp.MustCompile(`^(?:[-*•]|\d+[.)])\s+`)

func normalizeMarkdownBullets(content string) string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" {
		return ""
	}

	var bullets []string
	seen := make(map[string]struct{})

	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "#") {
			continue
		}

		line = bulletPrefix.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		bullet := "- " + line
		if _, ok := seen[bullet]; ok {
			continue
		}
		seen[bullet] = struct{}{}
		bullets = append(bullets, bullet)
	}

	if len(bullets) == 0 {
		return "- " + strings.TrimSpace(content)
	}

	return strings.Join(bullets, "\n")
}

func (c *EpigeneticChronicler) appendToGenome(findings string) error {
	targetPath := c.genomePath()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create genome directory: %w", err)
	}

	existing, err := os.ReadFile(targetPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing genome: %w", err)
	}

	merged := mergeGenomeContent(string(existing), findings)
	if err := os.WriteFile(targetPath, []byte(merged), 0o644); err != nil {
		return fmt.Errorf("write genome: %w", err)
	}

	return nil
}

func (c *EpigeneticChronicler) genomePath() string {
	return filepath.Join(c.workspace, ".tendril", "genome", "epigenetics.md")
}

func (c *EpigeneticChronicler) maybeReduceGenome(ctx context.Context, targetPath string) (bool, error) {
	info, err := os.Stat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat genome: %w", err)
	}

	if info.Size() <= genomeMaxBytes() {
		return false, nil
	}

	if err := c.ReduceGenomeFile(ctx); err != nil {
		return false, err
	}

	return true, nil
}

func (c *EpigeneticChronicler) maybeAutoPushGenome(targetPath string) error {
	if strings.ToLower(strings.TrimSpace(os.Getenv("TENDRIL_GENOME_AUTO_PUSH"))) != "true" {
		return nil
	}

	if !isGitRepo(c.workspace) {
		return fmt.Errorf("workspace %s is not a git repository", c.workspace)
	}

	relPath, err := filepath.Rel(c.workspace, targetPath)
	if err != nil {
		relPath = targetPath
	}

	addCmd := exec.Command("git", "-C", c.workspace, "add", relPath)
	if output, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	diffCmd := exec.Command("git", "-C", c.workspace, "diff", "--cached", "--quiet", "--", relPath)
	if err := diffCmd.Run(); err == nil {
		return nil
	} else if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		return fmt.Errorf("git diff --cached failed: %w", err)
	}

	commitCmd := exec.Command("git", "-C", c.workspace, "commit", "-m", genomeAutoPushCommitMessage)
	if output, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	pushCmd := exec.Command("git", "-C", c.workspace, "push", "origin", "HEAD")
	if output, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func mergeGenomeContent(existing string, findings string) string {
	existing = strings.TrimSpace(strings.ReplaceAll(existing, "\r\n", "\n"))
	findings = strings.TrimSpace(strings.ReplaceAll(findings, "\r\n", "\n"))

	if existing == "" {
		return epigeneticGenomeHeader + "\n\n" + findings + "\n"
	}

	if strings.HasPrefix(existing, epigeneticGenomeHeader) {
		body := strings.TrimSpace(strings.TrimPrefix(existing, epigeneticGenomeHeader))
		if body != "" {
			return epigeneticGenomeHeader + "\n\n" + body + "\n\n" + findings + "\n"
		}
		return epigeneticGenomeHeader + "\n\n" + findings + "\n"
	}

	return epigeneticGenomeHeader + "\n\n" + existing + "\n\n" + findings + "\n"
}

func genomeMaxBytes() int64 {
	limit := defaultGenomeTokenLimit
	if raw := strings.TrimSpace(os.Getenv("TENDRIL_GENOME_MAX_TOKENS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	return int64(limit * genomeCharsPerToken)
}

func repoRoot(path string) string {
	if strings.TrimSpace(path) == "" {
		path = "."
	}

	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return path
	}

	root := strings.TrimSpace(string(output))
	if root == "" {
		return path
	}

	return root
}

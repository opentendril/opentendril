package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const epigeneticGenomeHeader = "# Epigenetic Learnings"
const genomeAutoPushCommitMessage = "chore(genome): epigenetic transcription update [skip ci]"

type llmMode string

const (
	llmModeAnthropic llmMode = "anthropic"
	llmModeOpenAIish llmMode = "openaiish"

	defaultMaxSection       = 12000
	defaultGenomeTokenLimit = 2000
	genomeCharsPerToken     = 4
)

type providerSpec struct {
	provider    string
	baseURL     string
	baseURLs    []string
	apiKey      string
	model       string
	endpoint    string
	mode        llmMode
	temperature float64
}

// EpigeneticChronicler distills durable learnings from successful Sprout runs.
type EpigeneticChronicler struct {
	workspace string
	client    *http.Client
	spec      providerSpec
}

// NewEpigeneticChronicler constructs a chronicler for the provided workspace.
func NewEpigeneticChronicler(workspace string) *EpigeneticChronicler {
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}

	return &EpigeneticChronicler{
		workspace: repoRoot(workspace),
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
		spec: resolveProviderSpec(),
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
	findings, err := c.callLLM(ctx, systemPrompt, userPrompt)
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

func resolveProviderSpec() providerSpec {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("DEFAULT_LLM_PROVIDER")))
	if provider == "" {
		provider = detectProviderFallback()
	}

	switch provider {
	case "local":
		baseURL := envOr("LOCAL_INFERENCE_URL", "http://host.docker.internal:11434/v1")
		return providerSpec{
			provider:    "local",
			baseURL:     baseURL,
			baseURLs:    localInferenceBaseURLs(baseURL),
			model:       envOr("LOCAL_MODEL_NAME", "llama3.2"),
			endpoint:    "/chat/completions",
			mode:        llmModeOpenAIish,
			temperature: 0.1,
		}
	case "anthropic":
		return providerSpec{
			provider:    "anthropic",
			baseURL:     envOr("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
			apiKey:      os.Getenv("ANTHROPIC_API_KEY"),
			model:       envOr("ANTHROPIC_MODEL_NAME", "claude-sonnet-4-6"),
			endpoint:    "/v1/messages",
			mode:        llmModeAnthropic,
			temperature: 0.1,
		}
	case "openai":
		return providerSpec{
			provider:    "openai",
			baseURL:     envOr("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			apiKey:      os.Getenv("OPENAI_API_KEY"),
			model:       envOr("OPENAI_MODEL_NAME", "gpt-5.4-mini"),
			endpoint:    "/chat/completions",
			mode:        llmModeOpenAIish,
			temperature: 0.1,
		}
	case "grok":
		return providerSpec{
			provider:    "grok",
			baseURL:     envOr("GROK_BASE_URL", "https://api.x.ai/v1"),
			apiKey:      os.Getenv("GROK_API_KEY"),
			model:       envOr("GROK_MODEL_NAME", "grok-4-fast-non-reasoning"),
			endpoint:    "/chat/completions",
			mode:        llmModeOpenAIish,
			temperature: 0.1,
		}
	case "google":
		return providerSpec{
			provider:    "google",
			baseURL:     envOr("GOOGLE_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai"),
			apiKey:      os.Getenv("GOOGLE_API_KEY"),
			model:       envOr("GOOGLE_MODEL_NAME", "gemini-3-flash"),
			endpoint:    "/chat/completions",
			mode:        llmModeOpenAIish,
			temperature: 0.1,
		}
	case "openrouter":
		return providerSpec{
			provider:    "openrouter",
			baseURL:     envOr("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"),
			apiKey:      os.Getenv("OPENROUTER_API_KEY"),
			model:       envOr("OPENROUTER_MODEL_NAME", "anthropic/claude-3.5-sonnet"),
			endpoint:    "/chat/completions",
			mode:        llmModeOpenAIish,
			temperature: 0.1,
		}
	case "opentendril":
		return providerSpec{
			provider:    "opentendril",
			baseURL:     envOr("OPENTENDRIL_BASE_URL", "https://api.opentendril.com/v1"),
			apiKey:      os.Getenv("OPENTENDRIL_API_KEY"),
			model:       envOr("OPENTENDRIL_MODEL_NAME", "anthropic/claude-3.5-sonnet"),
			endpoint:    "/chat/completions",
			mode:        llmModeOpenAIish,
			temperature: 0.1,
		}
	case "nvidia":
		return providerSpec{
			provider:    "nvidia",
			baseURL:     envOr("NVIDIA_BASE_URL", "https://integrate.api.nvidia.com/v1"),
			apiKey:      os.Getenv("NVIDIA_API_KEY"),
			model:       envOr("NVIDIA_MODEL_NAME", "meta/llama-3.1-70b-instruct"),
			endpoint:    "/chat/completions",
			mode:        llmModeOpenAIish,
			temperature: 0.1,
		}
	default:
		baseURL := envOr("LOCAL_INFERENCE_URL", "http://host.docker.internal:11434/v1")
		return providerSpec{
			provider:    "local",
			baseURL:     baseURL,
			baseURLs:    localInferenceBaseURLs(baseURL),
			model:       envOr("LOCAL_MODEL_NAME", "llama3.2"),
			endpoint:    "/chat/completions",
			mode:        llmModeOpenAIish,
			temperature: 0.1,
		}
	}
}

func detectProviderFallback() string {
	if os.Getenv("LOCAL_INFERENCE_URL") != "" || os.Getenv("LOCAL_MODEL_NAME") != "" {
		return "local"
	}
	candidates := []struct {
		provider string
		key      string
	}{
		{provider: "openai", key: "OPENAI_API_KEY"},
		{provider: "anthropic", key: "ANTHROPIC_API_KEY"},
		{provider: "grok", key: "GROK_API_KEY"},
		{provider: "google", key: "GOOGLE_API_KEY"},
		{provider: "openrouter", key: "OPENROUTER_API_KEY"},
		{provider: "opentendril", key: "OPENTENDRIL_API_KEY"},
		{provider: "nvidia", key: "NVIDIA_API_KEY"},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(os.Getenv(candidate.key)) != "" {
			return candidate.provider
		}
	}
	return "local"
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func localInferenceBaseURLs(baseURL string) []string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://host.docker.internal:11434/v1"
	}

	candidates := []string{baseURL}
	switch {
	case strings.Contains(baseURL, "host.docker.internal"):
		candidates = append(candidates,
			strings.ReplaceAll(baseURL, "host.docker.internal", "localhost"),
			strings.ReplaceAll(baseURL, "host.docker.internal", "127.0.0.1"),
		)
	case strings.Contains(baseURL, "localhost"):
		candidates = append(candidates,
			strings.ReplaceAll(baseURL, "localhost", "127.0.0.1"),
			strings.ReplaceAll(baseURL, "localhost", "host.docker.internal"),
		)
	case strings.Contains(baseURL, "127.0.0.1"):
		candidates = append(candidates,
			strings.ReplaceAll(baseURL, "127.0.0.1", "localhost"),
			strings.ReplaceAll(baseURL, "127.0.0.1", "host.docker.internal"),
		)
	default:
		candidates = append(candidates, strings.ReplaceAll(baseURL, "host.docker.internal", "localhost"))
	}

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}

	return out
}

func (c *EpigeneticChronicler) callLLM(ctx context.Context, systemPrompt string, userPrompt string) (string, error) {
	if c.spec.baseURL == "" {
		return "", fmt.Errorf("no LLM base URL configured for provider %q", c.spec.provider)
	}
	if c.spec.model == "" {
		return "", fmt.Errorf("no LLM model configured for provider %q", c.spec.provider)
	}
	if c.spec.provider != "local" && strings.TrimSpace(c.spec.apiKey) == "" {
		return "", fmt.Errorf("no API key configured for provider %q", c.spec.provider)
	}

	candidates := c.spec.baseURLs
	if len(candidates) == 0 {
		candidates = []string{c.spec.baseURL}
	}

	var lastErr error
	for _, baseURL := range candidates {
		content, err := c.callLLMAtBaseURL(ctx, baseURL, systemPrompt, userPrompt)
		if err == nil {
			return content, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("llm request failed for provider %q", c.spec.provider)
	}

	return "", lastErr
}

func (c *EpigeneticChronicler) callLLMAtBaseURL(ctx context.Context, baseURL string, systemPrompt string, userPrompt string) (string, error) {
	var (
		payload []byte
		url     = strings.TrimRight(baseURL, "/") + c.spec.endpoint
		req     *http.Request
		err     error
	)

	switch c.spec.mode {
	case llmModeAnthropic:
		payload, err = json.Marshal(map[string]any{
			"model":       c.spec.model,
			"max_tokens":  1024,
			"temperature": c.spec.temperature,
			"system":      systemPrompt,
			"messages": []map[string]string{
				{"role": "user", "content": userPrompt},
			},
		})
		if err != nil {
			return "", fmt.Errorf("marshal anthropic request: %w", err)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
		if err != nil {
			return "", fmt.Errorf("create anthropic request: %w", err)
		}
		req.Header.Set("x-api-key", c.spec.apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		payload, err = json.Marshal(map[string]any{
			"model":       c.spec.model,
			"temperature": c.spec.temperature,
			"stream":      false,
			"messages": []map[string]string{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": userPrompt},
			},
		})
		if err != nil {
			return "", fmt.Errorf("marshal chat request: %w", err)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
		if err != nil {
			return "", fmt.Errorf("create chat request: %w", err)
		}
		if c.spec.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.spec.apiKey)
		}
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read llm response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	switch c.spec.mode {
	case llmModeAnthropic:
		var decoded struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return "", fmt.Errorf("decode anthropic response: %w", err)
		}
		for _, block := range decoded.Content {
			if strings.TrimSpace(block.Text) != "" {
				return strings.TrimSpace(block.Text), nil
			}
		}
		return "", fmt.Errorf("anthropic response contained no text")
	default:
		var decoded struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return "", fmt.Errorf("decode chat response: %w", err)
		}
		if len(decoded.Choices) == 0 {
			return "", fmt.Errorf("chat response contained no choices")
		}
		content := strings.TrimSpace(decoded.Choices[0].Message.Content)
		if content == "" {
			return "", fmt.Errorf("chat response contained no content")
		}
		return content, nil
	}
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
	findings, err := c.callLLM(ctx, systemPrompt, userPrompt)
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

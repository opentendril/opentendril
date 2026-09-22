package conductor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/opentendril/opentendril/cmd/stem/internal/rhizome"
	"github.com/opentendril/opentendril/roots/llm"
)

const epigeneticGenomeHeader = "# Epigenetic Learnings"

const (
	defaultMaxSection       = 12000
	defaultGenomeTokenLimit = 2000
	genomeCharsPerToken     = 4
)

const (
	genomicFitnessFilename         = "fitness.json"
	genomicEpigeneticsFilename     = "epigenetics.md"
	genomicPlasmidDisableThreshold = -5
	genomicRulePruneThreshold      = -3
)

const (
	mycorrhizalPostRunSourceClass    = "mycorrhizal-post-run-v1"
	mycorrhizalAdaptationSourceClass = "mycorrhizal-adaptation-v1"
	mycorrhizalProposalCategory      = "mycorrhizal-proposal"
)

// promptResultCaller is the Result-bearing prompt seam used by the
// Mycorrhizal chronicler so post-run provider requests can be counted.
// Adaptation and meristem keep textCaller.
type promptResultCaller interface {
	CallPromptWithResult(ctx context.Context, systemPrompt, userPrompt string) (llm.Result, error)
}

type namedMind interface {
	Provider() string
	Model() string
}

// EpigeneticChronicler retains the historical name for the bounded
// Mycorrhizal extraction component. New output is persisted as Rhizome
// proposals and never as active Epigenetic genome material.
type EpigeneticChronicler struct {
	workspace   string
	client      promptResultCaller
	coordinator textCaller
}

type proposalInvocation struct {
	sourcePath  string
	stepID      string
	sessionID   string
	sourceClass string
	sourceID    string
	provenance  string
}

type proposalProvenance struct {
	StepID              string `json:"stepId,omitempty"`
	SessionID           string `json:"sessionId,omitempty"`
	CommitIdentityCount int    `json:"commitIdentityCount,omitempty"`
}

var proposalPersistenceMu sync.Mutex

// GenomicFitness tracks reinforcement scores for rules and active plasmids.
type GenomicFitness struct {
	Rules    map[string]int `json:"rules"`
	Plasmids map[string]int `json:"plasmids"`
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

// TranscribeLearnings asks the Mycorrhizae to summarize durable learnings and
// stores them in the workspace-local Rhizome as proposals. Production run
// paths use TranscribeLearningsWithProvenance to bind safe run identity.
func (c *EpigeneticChronicler) TranscribeLearnings(ctx context.Context, transcript string, diff string, logs string) (PostRunUsage, error) {
	return c.transcribeLearnings(ctx, transcript, diff, logs, proposalInvocation{
		sourcePath:  c.workspace,
		sourceClass: mycorrhizalPostRunSourceClass,
		sourceID:    postRunProposalSourceIdentity(c.workspace, "", ""),
	})
}

// TranscribeLearningsWithProvenance is the production post-run seam. The
// caller supplies safe run identity while the canonical source Substrate owns
// the durable proposal Rhizome.
func (c *EpigeneticChronicler) TranscribeLearningsWithProvenance(ctx context.Context, transcript, diff, logs, stepID, sessionID, sourcePath string) (PostRunUsage, error) {
	if strings.TrimSpace(sourcePath) == "" {
		sourcePath = c.workspace
	}
	return c.transcribeLearnings(ctx, transcript, diff, logs, proposalInvocation{
		sourcePath:  sourcePath,
		stepID:      stepID,
		sessionID:   sessionID,
		sourceClass: mycorrhizalPostRunSourceClass,
		sourceID:    postRunProposalSourceIdentity(sourcePath, stepID, sessionID),
	})
}

func (c *EpigeneticChronicler) transcribeLearnings(ctx context.Context, transcript string, diff string, logs string, invocation proposalInvocation) (PostRunUsage, error) {
	var post PostRunUsage
	if strings.TrimSpace(diff) == "" {
		return post, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	systemPrompt, userPrompt := buildMycorrhizalProposalPrompt(transcript, diff, logs)
	res, err := c.client.CallPromptWithResult(ctx, systemPrompt, userPrompt)
	post = observePostRunRequest(c.client, post, res.Usage)
	if err != nil {
		return post, err
	}

	findings := normalizeProposalContents(res.Text, "No durable learnings")
	if len(findings) == 0 {
		return post, nil
	}

	if err := persistProposalInvocation(ctx, invocation, findings); err != nil {
		return post, err
	}

	return post, nil
}

// ReduceGenomeFile consolidates the active epigenetic genome in place.
func (c *EpigeneticChronicler) ReduceGenomeFile(ctx context.Context) (PostRunUsage, error) {
	var post PostRunUsage
	if ctx == nil {
		ctx = context.Background()
	}

	targetPath := c.genomePath()
	content, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return post, fmt.Errorf("epigenetic genome not found at %s", targetPath)
		}
		return post, fmt.Errorf("read epigenetic genome: %w", err)
	}

	reduced, reqUsage, err := c.reduceGenomeContent(ctx, string(content))
	post = observePostRunRequest(c.client, post, reqUsage)
	if err != nil {
		return post, err
	}

	if err := os.WriteFile(targetPath, []byte(reduced), 0o644); err != nil {
		return post, fmt.Errorf("write reduced epigenetic genome: %w", err)
	}

	return post, nil
}

// RecordGenomicFitness reinforces genome rules and active plasmids after a sprout run.
func RecordGenomicFitness(workspace string, success bool) error {
	workspace = repoRoot(workspace)
	genomeDir := filepath.Join(workspace, ".tendril", "genome")
	fitnessPath := filepath.Join(genomeDir, genomicFitnessFilename)

	fitness, err := loadGenomicFitness(fitnessPath)
	if err != nil {
		return err
	}

	genomeFiles, err := listGenomeMarkdownFiles(genomeDir)
	if err != nil {
		return err
	}

	delta := -1
	if success {
		delta = 1
	}

	for _, path := range genomeFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read genome file %s: %w", path, err)
		}

		for _, rule := range extractGenomeRules(string(content)) {
			fitness.Rules[rule] += delta
		}

		if isActiveGenomePlasmid(path) {
			fitness.Plasmids[filepath.Base(path)] += delta
		}
	}

	return saveGenomicFitness(fitnessPath, fitness)
}

// EvolveGenome prunes low-fitness genome material and rewrites the active epigenetic rules.
func EvolveGenome(ctx context.Context, workspace string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	workspace = repoRoot(workspace)
	genomeDir := filepath.Join(workspace, ".tendril", "genome")
	fitnessPath := filepath.Join(genomeDir, genomicFitnessFilename)
	epigeneticsPath := filepath.Join(genomeDir, genomicEpigeneticsFilename)

	fitness, err := loadGenomicFitness(fitnessPath)
	if err != nil {
		return err
	}

	content, err := os.ReadFile(epigeneticsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("epigenetic genome not found at %s", epigeneticsPath)
		}
		return fmt.Errorf("read epigenetic genome: %w", err)
	}

	genomeFiles, err := listGenomeMarkdownFiles(genomeDir)
	if err != nil {
		return err
	}

	for _, path := range genomeFiles {
		if !isActiveGenomePlasmid(path) {
			continue
		}

		score := fitness.Plasmids[filepath.Base(path)]
		if score > genomicPlasmidDisableThreshold {
			continue
		}

		if _, disableErr := disableGenomePlasmid(path); disableErr != nil {
			return disableErr
		}
	}

	remainingRules := filterGenomeRules(string(content), fitness.Rules)
	systemPrompt, userPrompt := buildGenomeEvolutionPrompt(remainingRules)
	evolvedGenome, err := callGenomeEvolutionPrompt(ctx, systemPrompt, userPrompt)
	if err != nil {
		return err
	}

	evolvedGenome = normalizeMarkdownBullets(evolvedGenome)
	if strings.TrimSpace(evolvedGenome) == "" {
		return fmt.Errorf("LLM returned no genome evolution output")
	}

	if err := os.MkdirAll(genomeDir, 0o755); err != nil {
		return fmt.Errorf("create genome directory: %w", err)
	}
	if err := os.WriteFile(epigeneticsPath, []byte(evolvedGenome+"\n"), 0o644); err != nil {
		return fmt.Errorf("write evolved epigenetic genome: %w", err)
	}

	return nil
}

func buildMycorrhizalProposalPrompt(transcript string, diff string, logs string) (string, string) {
	transcript = truncateMiddle(strings.TrimSpace(transcript), defaultMaxSection)
	diff = truncateMiddle(strings.TrimSpace(diff), defaultMaxSection)
	logs = truncateMiddle(strings.TrimSpace(logs), defaultMaxSection)

	systemPrompt := strings.TrimSpace(`
	You are the OpenTendril Mycorrhizal Chronicler.
	Extract concise, repository-specific proposal statements from a successful task run.
	Return only concise Markdown bullet points. The Stem will store each retained bullet as a proposed Rhizome memory.
	Do not include chain-of-thought, reasoning traces, credentials, secrets, raw logs, raw diffs, or transient execution narration.
`)

	userPrompt := fmt.Sprintf(`Analyze the task transcript, git diff, and run logs.
	Extract only durable proposal statements:
	- Architectural gotchas or codebase-specific constraints discovered.
	- Dependency or version requirements that were used.
	- Naming conventions or styling constraints required by the code.

If nothing durable was learned, return exactly:
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

func normalizeProposalContents(content, sentinel string) []string {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if content == "" {
		return nil
	}

	sentinel = strings.TrimSpace(strings.TrimSuffix(sentinel, "."))
	seen := make(map[string]struct{})
	proposals := make([]string, 0)
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "#") {
			continue
		}
		line = bulletPrefix.ReplaceAllString(line, "")
		line = strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
		if line == "" || strings.EqualFold(strings.TrimSuffix(line, "."), sentinel) {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		proposals = append(proposals, line)
	}
	return proposals
}

func postRunProposalSourceIdentity(sourcePath, stepID, sessionID string) string {
	canonical := repoRoot(sourcePath)
	repositoryName := filepath.Base(filepath.Clean(canonical))
	if repositoryName == "." || repositoryName == "" || repositoryName == string(filepath.Separator) {
		repositoryName = "workspace"
	}
	safeStepID := taskContextSafeCorrelationID(stepID)
	safeSessionID := taskContextSafeCorrelationID(sessionID)
	sum := sha256.Sum256([]byte(strings.Join([]string{repositoryName, safeStepID, safeSessionID}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func adaptationProposalSourceIdentity(commits []CommitSample) string {
	ordered := make([]string, 0, len(commits))
	for _, commit := range commits {
		ordered = append(ordered, strings.TrimSpace(commit.Hash))
	}
	sum := sha256.Sum256([]byte(strings.Join(ordered, "\x00")))
	return hex.EncodeToString(sum[:])
}

func encodeProposalProvenance(stepID, sessionID string, commitIdentityCount int) (string, error) {
	safeStepID := taskContextSafeCorrelationID(stepID)
	safeSessionID := taskContextSafeCorrelationID(sessionID)
	encoded, err := json.Marshal(proposalProvenance{
		StepID:              safeStepID,
		SessionID:           safeSessionID,
		CommitIdentityCount: commitIdentityCount,
	})
	if err != nil {
		return "", fmt.Errorf("encode proposal provenance: %w", err)
	}
	return string(encoded), nil
}

func persistProposalInvocation(ctx context.Context, invocation proposalInvocation, contents []string) error {
	sourcePath := strings.TrimSpace(invocation.sourcePath)
	if sourcePath == "" {
		return fmt.Errorf("proposal source Substrate is empty")
	}
	if strings.TrimSpace(invocation.sourceClass) == "" {
		return fmt.Errorf("proposal source class is empty")
	}
	if strings.TrimSpace(invocation.sourceID) == "" {
		return fmt.Errorf("proposal source identity is empty")
	}

	index, repositoryName, err := openRhizomeIndexFn(ctx, repoRoot(sourcePath))
	if err != nil {
		return fmt.Errorf("open source-local Rhizome for proposal: %w", err)
	}
	defer index.Close()

	provenance := invocation.provenance
	if provenance == "" {
		provenance, err = encodeProposalProvenance(invocation.stepID, invocation.sessionID, 0)
		if err != nil {
			return err
		}
	}
	return persistMycorrhizalProposals(ctx, index, repositoryName, contents, invocation.sourceClass, invocation.sourceID, invocation.sessionID, provenance)
}

func persistMycorrhizalProposals(ctx context.Context, backend rhizome.MemoryBackend, repositoryName string, contents []string, sourceClass, sourceIdentity, sessionID, provenance string) error {
	if backend == nil {
		return fmt.Errorf("proposal Rhizome backend is nil")
	}
	if strings.TrimSpace(repositoryName) == "" {
		return fmt.Errorf("proposal repository identity is empty")
	}

	proposalPersistenceMu.Lock()
	defer proposalPersistenceMu.Unlock()

	type pendingProposal struct {
		memory rhizome.Memory
	}
	pending := make([]pendingProposal, 0, len(contents))
	for _, rawContent := range contents {
		content := strings.Join(strings.Fields(strings.TrimSpace(rawContent)), " ")
		if content == "" {
			continue
		}
		sum := sha256.Sum256([]byte(content))
		contentIdentity := hex.EncodeToString(sum[:])
		title := "proposal:" + contentIdentity
		memory := rhizome.Memory{
			RepositoryName:  repositoryName,
			Category:        mycorrhizalProposalCategory,
			Title:           title,
			Content:         content,
			SessionID:       taskContextSafeCorrelationID(sessionID),
			Origin:          rhizome.OriginMycorrhizal,
			Authority:       rhizome.AuthorityNone,
			Status:          rhizome.StatusProposed,
			Kind:            rhizome.KindObservation,
			Provenance:      provenance,
			SourceClass:     sourceClass,
			SourceIdentity:  sourceIdentity,
			ContentIdentity: contentIdentity,
			StableID:        rhizome.StableMemoryIdentity(repositoryName, title),
		}
		if err := memory.Validate(); err != nil {
			return fmt.Errorf("validate Mycorrhizal proposal %q: %w", title, err)
		}

		existing, found, err := backend.GetMemory(ctx, repositoryName, title)
		if err != nil {
			return fmt.Errorf("check existing proposal %q: %w", title, err)
		}
		if found {
			if strings.Join(strings.Fields(strings.TrimSpace(existing.Content)), " ") != content {
				return fmt.Errorf("proposal identity collision for %q: stored content differs", title)
			}
			continue
		}
		pending = append(pending, pendingProposal{memory: memory})
	}

	for _, proposal := range pending {
		if err := backend.StoreMemory(ctx, proposal.memory); err != nil {
			return fmt.Errorf("store Mycorrhizal proposal %q: %w", proposal.memory.Title, err)
		}
	}
	return nil
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

func buildGenomeEvolutionPrompt(remainingRules []string) (string, string) {
	rulesBlock := "No active rules remain."
	if len(remainingRules) > 0 {
		bullets := make([]string, 0, len(remainingRules))
		for _, rule := range remainingRules {
			bullets = append(bullets, "- "+rule)
		}
		rulesBlock = truncateMiddle(strings.Join(bullets, "\n"), defaultMaxSection)
	}

	systemPrompt := strings.TrimSpace(`
You are the OpenTendril Genome Evolver.
Merge duplicates, consolidate overlapping ideas, and shorten the remaining epigenetic rules into a dense Markdown bullet list.
Preserve durable meaning while removing repetition and implementation-specific clutter.
Return only Markdown bullets. Do not include headings, code fences, or commentary.
`)

	userPrompt := fmt.Sprintf(`Rewrite the surviving epigenetic rules into a compact, high-density Markdown list.

Rules that survived fitness pruning:
%s

If no rules remain, return:
- No active rules remain.
`, rulesBlock)

	return systemPrompt, strings.TrimSpace(userPrompt)
}

// newGenomeEvolutionClientFn is the genome-evolution client seam, injectable
// for tests that exercise the tier-fallback loop without a real roots/llm
// call.
var newGenomeEvolutionClientFn = func(tier llm.ModelTier) textCaller { return llm.NewClientForTier(tier) }

func callGenomeEvolutionPrompt(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	var errs []error
	for _, tier := range []llm.ModelTier{llm.TierStandard, llm.TierCheapest} {
		client := newGenomeEvolutionClientFn(tier)
		content, err := client.CallPrompt(ctx, systemPrompt, userPrompt)
		if err == nil {
			return content, nil
		}
		errs = append(errs, fmt.Errorf("%s tier: %w", tier, err))
	}

	return "", errors.Join(errs...)
}

func (c *EpigeneticChronicler) reduceGenomeContent(ctx context.Context, existing string) (string, llm.Usage, error) {
	systemPrompt, userPrompt := buildGenomeReductionPrompt(existing)
	res, err := c.client.CallPromptWithResult(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", res.Usage, err
	}

	findings := normalizeMarkdownBullets(res.Text)
	if strings.TrimSpace(findings) == "" {
		return "", res.Usage, fmt.Errorf("LLM returned no genome reduction output")
	}

	return epigeneticGenomeHeader + "\n\n" + findings + "\n", res.Usage, nil
}

func observePostRunRequest(client promptResultCaller, post PostRunUsage, req llm.Usage) PostRunUsage {
	aggregateUsage(&post.Usage, req, !post.RequestsMade)
	post.RequestsMade = true
	if named, ok := client.(namedMind); ok {
		if post.Provider == "" {
			post.Provider = named.Provider()
		}
		if post.Model == "" {
			post.Model = named.Model()
		}
	}
	return post
}

func mergePostRunUsage(dst, src PostRunUsage) PostRunUsage {
	if !src.RequestsMade {
		return dst
	}
	if !dst.RequestsMade {
		return src
	}
	aggregateUsage(&dst.Usage, src.Usage, false)
	if dst.Provider == "" {
		dst.Provider = src.Provider
	}
	if dst.Model == "" {
		dst.Model = src.Model
	}
	return dst
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

func (c *EpigeneticChronicler) genomePath() string {
	return filepath.Join(c.workspace, ".tendril", "genome", "epigenetics.md")
}

func loadGenomicFitness(path string) (*GenomicFitness, error) {
	fitness := &GenomicFitness{
		Rules:    map[string]int{},
		Plasmids: map[string]int{},
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fitness, nil
		}
		return nil, fmt.Errorf("read genomic fitness: %w", err)
	}

	if strings.TrimSpace(string(content)) == "" {
		return fitness, nil
	}

	if err := json.Unmarshal(content, fitness); err != nil {
		return nil, fmt.Errorf("decode genomic fitness: %w", err)
	}

	if fitness.Rules == nil {
		fitness.Rules = map[string]int{}
	}
	if fitness.Plasmids == nil {
		fitness.Plasmids = map[string]int{}
	}

	return fitness, nil
}

func saveGenomicFitness(path string, fitness *GenomicFitness) error {
	if fitness == nil {
		return fmt.Errorf("genomic fitness is nil")
	}

	if fitness.Rules == nil {
		fitness.Rules = map[string]int{}
	}
	if fitness.Plasmids == nil {
		fitness.Plasmids = map[string]int{}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create genomic fitness directory: %w", err)
	}

	content, err := json.MarshalIndent(fitness, "", "  ")
	if err != nil {
		return fmt.Errorf("encode genomic fitness: %w", err)
	}

	if err := os.WriteFile(path, append(content, '\n'), 0o644); err != nil {
		return fmt.Errorf("write genomic fitness: %w", err)
	}

	return nil
}

func listGenomeMarkdownFiles(genomeDir string) ([]string, error) {
	info, err := os.Stat(genomeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("stat genome directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("genome path %s is not a directory", genomeDir)
	}

	var files []string
	if err := filepath.WalkDir(genomeDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("scan genome directory: %w", err)
	}

	sort.Strings(files)
	return files, nil
}

func extractGenomeRules(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	rules := make([]string, 0, len(lines))
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "*") {
			continue
		}

		rule := strings.TrimSpace(line[1:])
		if rule == "" {
			continue
		}

		rules = append(rules, rule)
	}

	return rules
}

func filterGenomeRules(content string, ruleScores map[string]int) []string {
	rules := extractGenomeRules(content)
	filtered := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))

	for _, rule := range rules {
		if score := ruleScores[rule]; score <= genomicRulePruneThreshold {
			continue
		}
		if _, ok := seen[rule]; ok {
			continue
		}
		seen[rule] = struct{}{}
		filtered = append(filtered, rule)
	}

	return filtered
}

func isActiveGenomePlasmid(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base != genomicEpigeneticsFilename && strings.HasSuffix(base, ".md")
}

func disableGenomePlasmid(path string) (string, error) {
	disabledPath := path + ".disabled"
	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(disabledPath); os.IsNotExist(err) {
			break
		} else if err != nil {
			return "", fmt.Errorf("check disabled plasmid target %s: %w", disabledPath, err)
		}
		disabledPath = fmt.Sprintf("%s.%d", path+".disabled", suffix)
	}

	if err := os.Rename(path, disabledPath); err != nil {
		return "", fmt.Errorf("disable plasmid %s: %w", path, err)
	}

	return disabledPath, nil
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

	// A managed checkout lives under Tendril's own root. If it is not itself
	// a git repository, git walks up and can land on the Stem home — which
	// also holds rootless Docker's containerd snapshots. Stay inside the
	// checkout so the Rhizome scans the Substrate, not the control plane.
	if escapedManagedCheckout(path, root) {
		return path
	}

	return root
}

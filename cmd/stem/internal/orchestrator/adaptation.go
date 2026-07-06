package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/opentendril/core/cmd/stem/internal/llm"
)

// CommitSample is one mined commit from the substrate's git history.
type CommitSample struct {
	Hash    string
	Message string
	Diff    string
}

const (
	// adaptationChunkMaxChars bounds each Meristem call so massive diffs never
	// overflow a coordinator model's context window (~6k tokens per chunk).
	adaptationChunkMaxChars = 24000
	diffFileBoundary        = "\ndiff --git "
)

// meristem lazily resolves the coordinator model used for Adaptation.
func (c *EpigeneticChronicler) meristem() llmCaller {
	if c.coordinator == nil {
		c.coordinator = llm.NewCoordinatorClientFromEnv()
	}
	return c.coordinator
}

// AdaptFromHistory mines commit samples for Epigenetic Traits and encodes them
// into the genome. Chunks are mapped through the Meristem coordinator
// independently, then reduced into one consolidated trait list before
// Inheritance picks them up on the next Sprout growth.
func (c *EpigeneticChronicler) AdaptFromHistory(ctx context.Context, commits []CommitSample) error {
	if ctx == nil {
		ctx = context.Background()
	}

	chunks := chunkAdaptationCorpus(commits, adaptationChunkMaxChars)
	if len(chunks) == 0 {
		return nil
	}

	caller := c.meristem()
	candidates := make([]string, 0, len(chunks))
	for index, chunk := range chunks {
		systemPrompt, userPrompt := buildTraitExtractionPrompt(chunk)
		traits, err := callMeristemPrompt(ctx, caller, systemPrompt, userPrompt)
		if err != nil {
			return fmt.Errorf("trait extraction failed on chunk %d/%d: %w", index+1, len(chunks), err)
		}

		traits = normalizeMarkdownBullets(traits)
		if strings.TrimSpace(traits) != "" {
			candidates = append(candidates, traits)
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	traits := candidates[0]
	if len(candidates) > 1 {
		systemPrompt, userPrompt := buildTraitConsolidationPrompt(strings.Join(candidates, "\n"))
		consolidated, err := callMeristemPrompt(ctx, caller, systemPrompt, userPrompt)
		if err != nil {
			return fmt.Errorf("trait consolidation failed: %w", err)
		}

		consolidated = normalizeMarkdownBullets(consolidated)
		if strings.TrimSpace(consolidated) == "" {
			return fmt.Errorf("Meristem returned no consolidated traits")
		}
		traits = consolidated
	}

	if err := c.appendToGenome(traits); err != nil {
		return err
	}

	if _, err := c.maybeReduceGenome(ctx, c.genomePath()); err != nil {
		fmt.Printf("⚠️ Genome auto-reduction skipped: %v\n", err)
	}

	return nil
}

// chunkAdaptationCorpus renders commits into Meristem-sized chunks. Whole
// commits are packed greedily; a commit whose diff alone exceeds the limit is
// split at file boundaries, and any single-file diff still over the limit is
// hard-split so no content is silently dropped.
func chunkAdaptationCorpus(commits []CommitSample, maxChars int) []string {
	if maxChars <= 0 {
		maxChars = adaptationChunkMaxChars
	}

	var chunks []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(current.String()))
			current.Reset()
		}
	}

	appendSegment := func(segment string) {
		if current.Len() > 0 && current.Len()+len(segment) > maxChars {
			flush()
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(segment)
	}

	for _, commit := range commits {
		if strings.TrimSpace(commit.Diff) == "" {
			continue
		}

		rendered := renderCommitSample(commit)
		if len(rendered) <= maxChars {
			appendSegment(rendered)
			continue
		}

		header := renderCommitHeader(commit)
		for _, segment := range splitDiffSegments(commit.Diff, maxChars-len(header)-2) {
			appendSegment(header + "\n" + segment)
		}
	}

	flush()
	return chunks
}

func renderCommitSample(commit CommitSample) string {
	return renderCommitHeader(commit) + "\n" + strings.TrimSpace(commit.Diff)
}

func renderCommitHeader(commit CommitSample) string {
	hash := strings.TrimSpace(commit.Hash)
	if len(hash) > 12 {
		hash = hash[:12]
	}

	header := "## Commit " + hash
	if message := strings.TrimSpace(commit.Message); message != "" {
		header += "\n" + message
	}
	return header
}

// splitDiffSegments splits a unified diff at file boundaries, hard-splitting
// any single-file diff that alone exceeds the limit.
func splitDiffSegments(diff string, maxChars int) []string {
	diff = strings.TrimSpace(strings.ReplaceAll(diff, "\r\n", "\n"))
	if diff == "" {
		return nil
	}
	if maxChars < 1024 {
		maxChars = 1024
	}

	parts := strings.Split("\n"+diff, diffFileBoundary)
	var segments []string
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if index > 0 {
			part = "diff --git " + part
		}

		for len(part) > maxChars {
			cut := strings.LastIndex(part[:maxChars], "\n")
			if cut < maxChars/2 {
				cut = maxChars
			}
			segments = append(segments, strings.TrimSpace(part[:cut]))
			part = strings.TrimSpace(part[cut:])
		}
		if part != "" {
			segments = append(segments, part)
		}
	}

	return segments
}

func callMeristemPrompt(ctx context.Context, caller llmCaller, systemPrompt string, userPrompt string) (string, error) {
	return caller.Call(ctx, []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	})
}

func buildTraitExtractionPrompt(chunk string) (string, string) {
	systemPrompt := strings.TrimSpace(`
You are the OpenTendril Meristem running an Adaptation pass.
Extract recurring Epigenetic Traits from the substrate's commit history.
Traits are durable, repository-specific habits: coding styles, architectural patterns,
naming conventions, error-handling idioms, and UI aesthetics (e.g. preferred CSS styles).
Return only concise Markdown bullet points.
`)

	userPrompt := fmt.Sprintf(`Analyze the following commit messages and diffs.
Extract only recurring Epigenetic Traits:
- Coding style and formatting habits (e.g. error wrapping, guard clauses, receiver naming).
- Architectural patterns and package layout conventions.
- Variable, file, and symbol naming conventions.
- Preferred UI/CSS aesthetics if frontend code is present.

Ignore one-off changes, commit hashes, and task-specific details.
If no recurring traits are visible, return:
- No recurring traits.

Commit history:
%s
`, chunk)

	return systemPrompt, strings.TrimSpace(userPrompt)
}

func buildTraitConsolidationPrompt(candidates string) (string, string) {
	candidates = truncateMiddle(strings.TrimSpace(candidates), defaultMaxSection)

	systemPrompt := strings.TrimSpace(`
You are the OpenTendril Meristem consolidating Epigenetic Traits.
Merge duplicate traits, keep only those that recur across chunks, and generalize them.
Return only concise Markdown bullet points.
`)

	userPrompt := fmt.Sprintf(`The following trait candidates were extracted from separate chunks of the same repository's history.
Consolidate them into a single deduplicated list of durable Epigenetic Traits, ideally under 15 bullets.
Drop bullets that say no traits were found.

Trait candidates:
%s
`, candidates)

	return systemPrompt, strings.TrimSpace(userPrompt)
}

# Genomic Evolution: Automated Reinforcement & Mutation (Issue #78)

This document details the design and implementation of **Genomic Evolution** inside the Go Stem orchestrator. This allows OpenTendril to track the effectiveness (fitness) of active repository instructions, rules, and plasmids over successive sprout execution runs, dynamically pruning failing rules and mutating successful ones via LLM consolidation.

---

## 1. Botanical Metaphor: Natural Selection & DNA Mutation

In botany, genomes are not static. They evolve over generations in response to environmental selection pressures. Useful traits (rules that lead to successful compilations and tests) are reinforced, while harmful traits (rules that lead to compilation failures, OOMs, or syntax crashes) are pruned.

OpenTendril maps this evolutionary process directly to its version-controlled `.tendril/genome/`:
*   **Active Rules** and **Plasmids** accumulate a **fitness score** based on the success or failure of execution runs.
*   A periodic **Genomic Evolution Pass** (Natural Selection) uses the LLM to prune low-fitness rules and mutate/sharpen high-fitness rules, keeping the genome compact and optimal.

---

## 2. Proposed Architecture

### A. Fitness Tracking (`fitness.json`)
We will maintain an internal fitness ledger at `.tendril/genome/fitness.json`.
*   After every Sprout execution, Go Stem scans the active `.md` files in `.tendril/genome/` and parses individual bullet points (rules).
*   If the execution succeeds: all active rules and plasmid filenames receive `+1` fitness.
*   If the execution fails: all active rules and plasmid filenames receive `-1` fitness.
*   The ledger format:
    ```json
    {
      "rules": {
        "Naming rule: prefer kebab-case for genome entries.": 5,
        "Legacy rule: do not use legacy zoological terms.": -3
      },
      "plasmids": {
        "go-rules.md": 8,
        "react-style.md": -4
      }
    }
    ```

### B. Genomic Evolution Pass (Mutation / Pruning)
We will add a new CLI command `tendril genome evolve` and support automated execution after a threshold of runs (e.g. 50 runs).
*   **Pruning:** Rules with negative fitness below a threshold (e.g. $\le -3$) are automatically deleted. Plasmids with negative fitness below a threshold (e.g. $\le -5$) are disabled (renamed or moved out of the active genome).
*   **Mutation:** For the remaining rules in `epigenetics.md`, the Go Stem invokes the LLM with an evolutionary prompt containing the active rules and their scores. The LLM consolidates duplicate rules, rewrites them for optimal context efficiency, and groups them logically.
*   The updated `epigenetics.md` is saved back to disk.

---

## 3. Proposed Changes

### Component: Go Stem Orchestrator

#### [MODIFY] [orchestrator/chronicler.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/chronicler.go)
*   Define fitness structs.
*   Implement `RecordGenomicFitness(workspace string, success bool) error` to parse active rules and update scores.
*   Implement `EvolveGenome(ctx context.Context, workspace string) error` to call the LLM and consolidate `epigenetics.md`.

#### [MODIFY] [orchestrator/sequence.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/sequence.go)
*   Call `RecordGenomicFitness` at the end of sprout runs.

#### [NEW] [cmdgenomeevolve.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/cmdgenomeevolve.go)
*   Register subcommand `tendril genome evolve`.

---

## 4. Verification Plan

### Automated Tests
*   **TestRecordGenomicFitness:** Verifies rules are successfully extracted from mock Markdown files and scores are updated correctly in `fitness.json`.
*   **TestGenomeEvolutionPass:** Mocks LLM responses and asserts that low-scoring rules are deleted, plasmids are disabled, and successful rules are merged/saved back.

### Manual Verification
1.  Run a sequence step that succeeds. Verify `fitness.json` is created/updated with positive scores.
2.  Run a sequence step that fails. Verify `fitness.json` records negative scores.
3.  Run `tendril genome evolve` and verify that the epigenetics list is consolidated.

# Cost Optimization & Routing: Prompt Caching & Model Tiering (Issue #22)

This document details the design and implementation of **Cost Optimization & Routing** inside the Go Stem orchestrator. This optimizes LLM token consumption and reduces operational API expenses during ReAct cycles and speculative sprout runs.

---

## 1. Botanical Metaphor: Apical Budding & Nutrient Distribution

In plant biology, the distribution of nutrients (tokens/compute resources) is highly optimized. The plant does not waste expensive metabolic energy on all leaves equally. Instead:
*   The **Apical Bud** (the primary growing tip) receives the highest concentration of auxins and energy to guide vertical growth (Premium Tier reasoning).
*   **Lateral Buds** and minor leaves receive fewer, more targeted nutrients just enough to maintain photosynthesis (Standard/Cheapest Tier confirmation and summarization).

Similarly, OpenTendril structures its model selection to avoid wasting expensive Premium reasoning on simple compiling checks, linting audits, or epigenetic text summarization.

---

## 2. Proposed Architecture

```
                 ┌─────────────────────────────┐
                 │     Activate Meristem       │
                 └──────────────┬──────────────┘
                                │
                    ┌───────────┼───────────┐
                    ▼           ▼           ▼
               (TierPremium) (TierStandard) (TierCheapest)
               ┌───────────┐ ┌───────────┐ ┌───────────┐
               │  Worker   │ │ Verifier  │ │ Epigenetic│
               │  Sprout   │ │ Debugger  │ │Chronicler │
               └───────────┘ └───────────┘ └───────────┘
```

### A. Model Tiering System
We define three logical execution tiers in Go:
*   `TierPremium` (Power): Complex planning, sequence coordination, and code writing.
*   `TierStandard` (Standard): Verification, compilation checks, and resolving linters.
*   `TierCheapest` (Cheapest): Summarization, context stubs, and epigenetic logging.

#### 2026 Curated Model Fallback Map
If specific environment variables are unset, the client resolves to the following defaults:

| Provider | Premium / Power Model | Standard / Fast Model | Cheapest Model |
| :--- | :--- | :--- | :--- |
| **Anthropic** | `claude-sonnet-4-6` | `claude-haiku-4-5` | `claude-haiku-4-5` |
| **OpenAI** | `gpt-5.5` | `gpt-5.4-mini` | `gpt-5.4-nano` |
| **Google** | `gemini-3-pro` | `gemini-3-flash` | `gemini-3-flash` |
| **Grok** | `grok-4` | `grok-4-fast-non-reasoning` | `grok-4-fast-non-reasoning` |
| **OpenRouter** | `anthropic/claude-sonnet-4-6` | `openai/gpt-5.4-mini` | `google/gemini-3-flash` |
| **Local** | `qwen2.5-coder:14b` | `qwen2.5-coder:7b` | `llama3.2` |

---

### B. Anthropic Prompt Caching Integration
For Anthropic provider queries, Go Stem formats system and user prompts to leverage prompt caching:
1.  Add the HTTP header: `anthropic-beta: prompt-caching-2024-07-31`.
2.  Structure the `system` parameter as an array of content blocks, wrapping the system instructions with `"cache_control": {"type": "ephemeral"}`.
3.  Inject ephemeral `cache_control` blocks on large context messages in the message list (e.g. `repomap.md` payloads and preceding transcript logs) to reuse compiled context across consecutive ReAct edits.

*Note:* OpenAI handles prompt caching automatically at the API gateway level on matching prefixes, so no special headers or formatting changes are applied to OpenAI-ish mode payloads.

---

### C. Orchestrator Routing
*   Orchestrator clients resolve their target LLM clients using the configured step/role Tier:
    *   **Meristem Step Planning / Coordinator:** Uses `TierPremium` (complex reasoning).
    *   **Worker Sprouts (making code edits):** Uses `TierPremium` (high accuracy code editing).
    *   **Verifier / Debugger / Compiler checks:** Uses `TierStandard` (fast, cost-effective confirmation).
    *   **Epigenetic Chronicler:** Uses `TierCheapest` (summarization and transcription of diff learnings).

---

## 3. Proposed Changes

### Component: LLM Client (`cmd/stem/internal/llm`)

#### [MODIFY] [client.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/llm/client.go)
*   Add `ModelTier` constants (`TierPremium`, `TierStandard`, `TierCheapest`).
*   Implement `ResolveTierProviderSpec(tier ModelTier) ProviderSpec` checking env keys in the format `<PROVIDER>_<TIER>_MODEL` (e.g. `ANTHROPIC_STANDARD_MODEL`) before falling back to 2026 defaults.
*   Update `callAtBaseURL` to format payloads with prompt caching blocks for `ModeAnthropic` if the header is active.

### Component: Go Stem Orchestrator

#### [MODIFY] [orchestrator/docker.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/docker.go)
*   Add `Tier llm.ModelTier` to `DockerOrchestrator`.
*   Update `resolveLLMClient()` to fetch the correct client using the configured `Tier`.

#### [MODIFY] [orchestrator/sequence.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/sequence.go)
*   Assign appropriate model tiers during step and chronicler execution.

---

## 4. Verification Plan

### Automated Tests
*   **TestModelTierResolution:** Verifies that environment variable fallbacks and specific tier configs resolve to the correct provider specifications.
*   **TestAnthropicPromptCachingPayload:** Verifies that when Anthropic mode is used, the client correctly constructs the system prompt content block and appends the `prompt-caching-2024-07-31` beta header.

### Manual Verification
1.  Configure `DEFAULT_LLM_PROVIDER_CHEAPEST=google` and `DEFAULT_MODEL_NAME_CHEAPEST=gemini-3-flash`.
2.  Run a sequence step.
3.  Verify that the Epigenetic Chronicler successfully executes using the cheaper Gemini 3 Flash provider instead of the default premium Sonnet provider.

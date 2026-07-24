# Component: Roots/LLM — the provider-facing LLM client with model registry and cost-tier routing.

## Purpose

`roots/llm` is the centralized LLM client library for OpenTendril. It acts as the single abstraction layer for communicating with model providers (Anthropic, OpenAI, Google, Grok, local inference, etc.), managing API keys, formatting prompts (including provider-specific caching headers), and parsing completion streams. It also serves as the authoritative registry for model capabilities (vision, reasoning, tool-driving fitness) and handles cost-tier routing to ensure expensive reasoning cycles are directed to premium models while simple tasks use cheaper or faster models.

## Responsibilities

**Does:**

- **Provider transport & prompt caching:** Implement `Call`, `CallStream`, and `ListModels` over standard HTTP with provider-specific payload formatting and Anthropic caching block injection (`roots/llm/client.go`).
- **Model registry & capabilities:** Maintain a curated table of known models and their capability flags (vision, reasoning, tool-driving reliability, context size) and filter available models against required capabilities (`roots/llm/registry.go`, `roots/llm/capabilities.go`).
- **Cost-tier routing & third-party-router handling:** Resolve `premium`, `standard`, and `cheapest` model tiers from environment, config, or discovery; bypass internal routing logic when pinned to a third-party router like OpenRouter or an explicit override (`roots/llm/routing.go`).
- **Provider/key discovery:** Autodetect available providers by probing environment variables for API keys and query endpoints for active models to populate the live registry cache (`roots/llm/discovery.go`).

**Does not:**

- Own the ReAct loop or tool execution logic (that is handled by Conductor and Sprouts).
- Own capability governance or rate-limiting policies (that belongs to Core).
- Persist anything to disk or databases (it is entirely stateless and in-memory).
- Import any OpenTendril internal package (it is a dependency-free leaf).

## Public interface

The package exports approximately 53 symbols. The load-bearing exports include:

| Symbol | Role |
| --- | --- |
| `Client` / `NewClient*` / `Resolve*ProviderSpec` | Core client struct and constructors for environment, tier, and explicit model targets. |
| `Message` | Standardized role/content structure for chat requests. |
| `Mode` | Provider dialect indicator (`ModeAnthropic`, `ModeOpenAIish`). |
| `ModelDefinition` / `ModelFamily` / `ModelTier` | Model metadata, family groupings, and tier mappings. |
| `TierPremium` / `TierStandard` / `TierCheapest` | Cost-tier constants for routing selection. |
| `ProviderSpec` | Connection and configuration details for a specific provider/model. |
| `RouteSelection` / routing functions | Types and functions (`ShouldBypassInternalRouter`, `ShouldUseDynamicRouter`, `ResolveRouteSelection`) governing model routing. |
| `Capabilities` | Required model feature flags (vision, reasoning, tool use, context size) used during model selection. |

## Dependencies

**Fan-out:** none — a dependency-free leaf. It relies only on the Go standard library (e.g., `net/http`, `encoding/json`, `bufio`) and `gopkg.in/yaml.v3` for parsing `.tendril/config.yaml` (`roots/llm/client.go`).

**Fan-in:**
- **Conductor** (`cmd/stem/internal/conductor`): `sprout.go`, `sequence.go`, `docker.go`, `assessor.go`, `chronicler.go`, `adaptation.go`, `parallelsprouting.go`. Note the surprising coupling: conductor (an orchestration layer) reaches into this concrete client directly rather than through a narrow port. This structural coupling is a known architecture concern.
- **Health monitoring**: `cmd/stem/internal/healthmon/checks.go`.
- **Main**: `cmd/stem/main.go`.
- **CLI verbs**: `cmd/stem/cmdassess.go`, `cmd/stem/cmdllm.go`.

## Limitations

- **API key validation is deferred**: API keys are sourced primarily from environment variables (`OPENAI_API_KEY`, etc.) in `roots/llm/discovery.go` and `roots/llm/client.go`, but failure modes are localized. `CallStream` fails early if the provider requires a key and none is set, rather than failing during startup.
- **Static model registry fallback**: The model registry (`roots/llm/registry.go`) falls back to a hard-coded table of `FallbackModels` when discovery fails or for providers lacking a models API. This means new tool-capable models (or retired legacy models) require a code change to update the fallback list, otherwise auto-selected requests may fail.
- **Prompt caching is Anthropic-only in code**: Explicit prompt caching logic in `roots/llm/client.go` (injecting `cache_control` and `anthropic-beta` headers) is hardcoded for `ModeAnthropic`. OpenAI-ish prompt caching relies entirely on the provider's API gateway handling it implicitly.
- **Third-party-router bypass edge cases**: `ShouldBypassInternalRouter` in `roots/llm/routing.go` relies on string matching (e.g., `openrouter/auto`, `router`, `nvidia/`) to detect third-party routers. A locally configured proxy or an unrecognised third-party router might incorrectly trigger the internal dynamic router instead of being bypassed.

## Design & rationale

`roots/llm` implements the Cost Optimization & Routing strategy, cutting token costs during ReAct and speculative Sprout runs by matching task complexity to model capabilities. The orchestrator uses specific tiers: `TierPremium` for complex planning, sequence coordination, and code writing; `TierStandard` for verification, compilation checks, and resolving linters; and `TierCheapest` for summarization, context stubs, and epigenetic logging.

To further reduce token usage during iterative ReAct cycles, the client formats Anthropic queries with ephemeral `cache_control` blocks on large context messages (such as `repomap.md` and transcript logs). This allows compiled context to be reused across consecutive edits without paying full prompt costs each time.

Dynamic routing automatically selects the best available model based on the requested capabilities and cost tier. However, when the internal router detects a third-party router configuration (like OpenRouter's `auto` model) or explicit strict constraints, it bypasses internal dynamic routing entirely to let the third-party manage selection. Where the legacy optimization plan and current code diverge, the code's implementation of discovery and static capability inference is the authoritative model.

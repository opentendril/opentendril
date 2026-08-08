# Component: Roots/LLM — the provider-facing LLM client with model registry and cost-tier routing.

## Purpose

`roots/llm` is the centralized LLM client library for OpenTendril. It acts as the single abstraction layer for communicating with model providers (Anthropic, OpenAI, Google, Grok, local inference, etc.), managing API keys, formatting prompts (including provider-specific caching headers), and parsing completion streams. It also serves as the authoritative registry for model capabilities (vision, reasoning, tool-driving fitness) and handles cost-tier routing to ensure expensive reasoning cycles are directed to premium models while simple tasks use cheaper or faster models.

## Responsibilities

**Does:**

- **Provider transport & prompt caching:** Implement `Call`, `CallStream`, and `ListModels` over standard HTTP, delegating provider-specific wire protocols (like model discovery, request shaping, response parsing, and prompt caching injections) to a `providerAdapter` (`roots/llm/clientadapter.go`). The client supports `anthropicAdapter` (injecting `cache_control` blocks) and `openAIishAdapter` (passing requests through to whatever `BaseURL` is configured, including a caching-capable gateway).
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
| `providerAdapter` | Encapsulates provider-specific wire protocols (e.g. `anthropicAdapter`, `openAIishAdapter`) in `clientadapter.go`. |
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
- **Explicit prompt caching is Anthropic-only in code — by design, not a gap.** `roots/llm` terminates exactly two wire protocols: Anthropic's native API and the OpenAI-compatible shape (`ModeOpenAIish`) shared by OpenAI, Gemini via a compatible shim, Grok, local inference, and any self-hosted backend. Caching correctness lives wherever the protocol-specific mechanism actually lives, and that differs per case: **Anthropic** — `anthropicAdapter` injects `cache_control` blocks and the `anthropic-beta` header directly, because Anthropic's is the one vendor protocol this client itself speaks natively. **OpenAI direct** — caching is automatic server-side; there is no request-level opt-in for a client to add, so nothing is missing here either. **Any other `ModeOpenAIish` backend** — `openAIishAdapter` already sends a plain request to whatever `BaseURL` is configured, with no opinion about what answers it. An operator who wants explicit caching for a self-hosted or third-party backend gets it by pointing that provider's `BaseURL` at a caching-capable gateway (e.g. vLLM+LMCache, LiteLLM) instead of the raw model endpoint — the exact same mechanism that already lets a `BaseURL` point at a local Ollama instance today. This needs zero code change in `roots/llm`; it is a configuration choice, not a missing feature. Caching (and any other provider-specific wire behavior) is expressed per-adapter behind one interface (`providerAdapter`) rather than scattered mode checks, giving the next actionable explicit caching mechanism a clean place to go.
- **Third-party-router bypass edge cases**: `ShouldBypassInternalRouter` in `roots/llm/routing.go` relies on string matching (e.g., `openrouter/auto`, `router`, `nvidia/`) to detect third-party routers by default. A locally configured proxy or an unrecognised third-party router might incorrectly trigger the internal dynamic router instead of being bypassed, or (conversely) a self-hosted model with a coincidental name could be misidentified as a router. To resolve this, operators can set the `is-router` boolean field on the relevant provider block in `.tendril/config.yaml` — `is-router: true` forces bypass regardless of the model name, and `is-router: false` explicitly prevents bypass even when the model name matches a heuristic pattern. The string-matching heuristic remains active for zero-config setups that do not set this field, so existing OpenRouter and NVIDIA router configurations continue working without any change.
- **Endpoints that cannot take tool definitions are discovered, not assumed**: turns are carried with the native tool-calling protocol by default. An operator who already knows an endpoint will not accept a `tools` field — an older self-hosted inference server, a proxy that strips it — can say so with `accepts-tool-definitions: false` on that provider block in `.tendril/config.yaml`, and the run is carried by the prose protocol from its first turn. Leaving the field unset means "attempt native", which is the right default because the alternative is asking every operator to declare a capability most endpoints have. When an endpoint that was attempted natively rejects the definitions (a `400` or `422`), the refusal is detected at request time and the run downgrades itself to the prose protocol mid-flight: this is announced on stderr, published as `sprout-downgraded`, and recorded as the run's carrying protocol, so a growth that produced nothing can answer "was the protocol to blame?" from its own record. The downgrade is never a gate — a gate is for authority, and this is a quality finding. Note that this field describes the *endpoint*; whether the *model* behind it emits tool calls that parse is the separate, measured `DrivesTools` property in `roots/llm/registry.go`, and setting `accepts-tool-definitions` does not relax it.
- **Anthropic output-token limit (`max_tokens`) is sourced from the model registry, not a constant**: Anthropic's Messages API requires `max_tokens` on every request — unlike OpenAI-shaped providers, there is no server-side default. The value is resolved in priority order: (1) the `output-limit` key on the provider's block in `.tendril/config.yaml`, (2) the `OutputLimit` field on the matching `ModelDefinition` in `FallbackModels` (sourced from `docs.anthropic.com/en/docs/about-claude/models/overview` at the time of each entry), (3) the package constant `anthropicOutputFallback` (currently 8192) when neither is set. The OpenAI-shaped adapter (`openAIishAdapter`) never sends `max_tokens` — those providers handle it server-side, and adding a ceiling for providers that do not require one is a regression. A configured value that exceeds the registry limit is sent as-is with a warning on stderr; the configured value is never silently clamped, because silent reduction is the original defect. If Anthropic rejects the value, the resulting 400 is visible and classified by the HTTP-status error path. The `FallbackModels` table carrying `OutputLimit` is flagged as going stale (new models require a code change), but config always wins over that table, so an operator on a model not yet in the registry can declare the limit explicitly.


## Design & rationale

`roots/llm` implements the Cost Optimization & Routing strategy, cutting token costs during ReAct and speculative Sprout runs by matching task complexity to model capabilities. The orchestrator uses specific tiers: `TierPremium` for complex planning, sequence coordination, and code writing; `TierStandard` for verification, compilation checks, and resolving linters; and `TierCheapest` for summarization, context stubs, and epigenetic logging.

The Anthropic adapter injects `cache_control` markers positionally. Every request receives at most four breakpoints: the system block is always marked (it covers the tool definitions that render before it). Up to three additional markers are placed in the message sequence, clustered near the end at roughly 15-block intervals. This spacing keeps each breakpoint within the provider's 20-block lookback window — so the next request can find a prior entry even after a long tool-calling turn that appends many blocks. A short conversation places only as many markers as the content earns; the budget does not inflate for short contexts.

For a provider behind `ModeOpenAIish` that wants the same kind of explicit caching control, the answer is not more caching code in this client — it is pointing that provider's `BaseURL` at a gateway that already implements it (vLLM+LMCache, LiteLLM, or a provider's own caching-aware endpoint). `roots/llm`'s job stops at speaking the wire protocol correctly; a caching-capable gateway's job is caching. Keeping the boundary there is deliberate: it keeps this client a thin, protocol-terminating leaf instead of growing a second bespoke caching implementation for every backend that might eventually want one.

Dynamic routing automatically selects the best available model based on the requested capabilities and cost tier. However, when the internal router detects a third-party router configuration (like OpenRouter's `auto` model) or explicit strict constraints, it bypasses internal dynamic routing entirely to let the third-party manage selection. Where the legacy optimization plan and current code diverge, the code's implementation of discovery and static capability inference is the authoritative model.

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
| `Client.ProbeAuthentication` | Minimal authenticated chat request on the existing client path, used by Stem to discover provider authentication rejection before emergence. |
| `RequestError` / `NewRequestError` | Typed provider HTTP rejection; `StatusCode` is the fact Stem classifies from, and `Body` is a credential-free excerpt. |
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
- **Conductor** (`cmd/stem/internal/conductor`): `sprout.go`, `sequence.go`, `docker.go`, `assessor.go`, `chronicler.go`, `adaptation.go`, `parallelsprouting.go`.
- **Health monitoring**: `cmd/stem/internal/healthmon/checks.go`.
- **Main**: `cmd/stem/main.go`.
- **CLI verbs**: `cmd/stem/cmdassess.go`, `cmd/stem/cmdllm.go`.

## Limitations

- **API key validation occurs when a provider call requires the key**: API keys are sourced primarily from environment variables (`OPENAI_API_KEY`, etc.) in `roots/llm/discovery.go` and `roots/llm/client.go`. `CallStream` fails early when a required key is absent. Stem additionally calls `ProbeAuthentication` on the host before Sprout emergence so an HTTP 401/403/407 is discovered without creating a Terrarium.
- **Static model registry fallback**: The model registry (`roots/llm/registry.go`) falls back to a hard-coded table of `FallbackModels` when discovery fails or for providers lacking a models API. This means new tool-capable models (or retired legacy models) require a code change to update the fallback list, otherwise auto-selected requests may fail.
- **Explicit prompt caching is Anthropic-only in code.** `roots/llm` terminates exactly two wire protocols: Anthropic's native API and the OpenAI-compatible shape (`ModeOpenAIish`). **Anthropic** — `anthropicAdapter` injects `cache_control` blocks and the `anthropic-beta` header directly. **OpenAI direct** — caching is automatic server-side; there is no request-level opt-in. **Any other `ModeOpenAIish` backend** — `openAIishAdapter` sends a plain request to whatever `BaseURL` is configured. Explicit caching for a self-hosted or third-party backend is achieved by pointing that provider's `BaseURL` at a caching-capable gateway (e.g. vLLM+LMCache, LiteLLM) instead of the raw model endpoint. Caching (and any other provider-specific wire behavior) is expressed per-adapter behind one interface (`providerAdapter`).
- **Third-party-router bypass edge cases**: `ShouldBypassInternalRouter` in `roots/llm/routing.go` relies on string matching (e.g., `openrouter/auto`, `router`, `nvidia/`) to detect third-party routers by default. A locally configured proxy or an unrecognised third-party router might incorrectly trigger the internal dynamic router instead of being bypassed, or (conversely) a self-hosted model with a coincidental name could be misidentified as a router. To resolve this, operators can set the `is-router` boolean field on the relevant provider block in `.tendril/config.yaml` — `is-router: true` forces bypass regardless of the model name, and `is-router: false` explicitly prevents bypass even when the model name matches a heuristic pattern. The string-matching heuristic remains active for zero-config setups that do not set this field, so existing OpenRouter and NVIDIA router configurations continue working without any change.
- **Endpoints that cannot take tool definitions are discovered, not assumed**: Turns are carried with the native tool-calling protocol by default. If an endpoint is known to not accept a `tools` field, `accepts-tool-definitions: false` can be set on that provider block in `.tendril/config.yaml`, and the run is carried by the prose protocol from its first turn. Leaving the field unset attempts the native protocol. When an endpoint that was attempted natively rejects the definitions (a `400` or `422`), the refusal is detected at request time and the run downgrades itself to the prose protocol mid-flight: this is announced on stderr, published as `sprout-downgraded`, and recorded as the run's carrying protocol. Note that this field describes the *endpoint*; whether the *model* behind it emits tool calls that parse is the separate `DrivesTools` property in `roots/llm/registry.go`, and setting `accepts-tool-definitions` does not relax it.
- **Anthropic output-token limit (`max_tokens`) is sourced from the model registry**: Anthropic's Messages API requires `max_tokens` on every request. The value is resolved in priority order: (1) the `output-limit` key on the provider's block in `.tendril/config.yaml`, (2) the `OutputLimit` field on the matching `ModelDefinition` in `FallbackModels`, (3) the package constant `anthropicOutputFallback` (currently 8192) when neither is set. The OpenAI-shaped adapter (`openAIishAdapter`) never sends `max_tokens`. A configured value that exceeds the registry limit is sent as-is with a warning on stderr; the configured value is never silently clamped. If Anthropic rejects the value, the resulting 400 is visible and classified by the HTTP-status error path. Config always wins over the `FallbackModels` table, so an operator on a model not yet in the registry can declare the limit explicitly.


## Routing & Caching

`roots/llm` implements tier-based routing. The orchestrator uses specific tiers: `TierPremium` for complex planning, sequence coordination, and code writing; `TierStandard` for verification, compilation checks, and resolving linters; and `TierCheapest` for summarization, context stubs, and epigenetic logging.

The Anthropic adapter injects `cache_control` markers positionally. Every request receives at most four breakpoints: the system block is always marked. Up to three additional markers are placed in the message sequence, clustered near the end at roughly 15-block intervals. A short conversation places only as many markers as the content earns.

Dynamic routing automatically selects the best available model based on the requested capabilities and cost tier. When the internal router detects a third-party router configuration (like OpenRouter's `auto` model) or explicit strict constraints, it bypasses internal dynamic routing entirely to let the third-party manage selection.

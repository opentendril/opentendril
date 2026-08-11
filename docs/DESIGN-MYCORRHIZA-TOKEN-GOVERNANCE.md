# Mycorrhiza Token Governance

## Problem

A Mycorrhiza can request an output-token ceiling that is too large for the selected provider, model, account balance, or local operating posture.

This creates two failures:

1. unattended Sprout runs can fail before doing useful work;
2. provider cost and quota behavior becomes implicit rather than governed.

OpenTendril needs predictable Mycorrhiza budget control before a Botanist can trust long unattended work inside a Terrarium.

## Current shape

OpenTendril already supports provider selection and model tier overrides.

The runtime can select providers such as local, Anthropic, OpenAI, Grok, Google, or OpenRouter. It can also pin provider-specific tier models such as OpenRouter fast and standard models.

The LLM layer also carries an output limit concept in provider configuration.

The missing governance is a consistent policy that resolves output-token ceilings by provider, model tier, and operator intent, then applies that ceiling to every supported request dialect.

## Decision

The Stem must resolve a governed output-token ceiling before a Mycorrhiza request is sent.

The ceiling is determined in this order:

1. explicit step or Sequence policy, when present;
2. configured provider/model override;
3. configured tier default;
4. provider-specific safe default;
5. compiled fallback.

The request must never ask for more output tokens than the resolved ceiling.

## Default policy

Default ceilings should be conservative enough for unattended local work:

- cheapest tier: small output budget suitable for single-file edits and reports;
- standard tier: moderate output budget suitable for multi-file work;
- premium tier: larger output budget, still capped;
- coordinator turns: separate budget, because planning output has a different shape from file-editing output.

OpenRouter should default to conservative ceilings unless explicitly overridden, because router failures can reflect model limit, account credit, provider quota, or route availability.

## Configuration

The operator may configure output ceilings without changing code.

Suggested environment variables:

```text
MYCORRHIZA_CHEAPEST_MAX_OUTPUT_TOKENS=1024
MYCORRHIZA_STANDARD_MAX_OUTPUT_TOKENS=4096
MYCORRHIZA_PREMIUM_MAX_OUTPUT_TOKENS=8192
MYCORRHIZA_COORDINATOR_MAX_OUTPUT_TOKENS=2048
```

Provider-specific OpenRouter override examples:

```text
MYCORRHIZA_OPENROUTER_FAST_MAX_OUTPUT_TOKENS=4096
MYCORRHIZA_OPENROUTER_STANDARD_MAX_OUTPUT_TOKENS=8192
```

## Request application

The Stem resolves the output ceiling policy. Provider adapters must only translate the wire shape.
Adapters must not apply default ceilings or limit logic on their own; they must only apply the ceiling resolved and passed down by the Stem.

## Error reporting

When an output token ceiling failure occurs, the error report must name:
- the provider
- the model
- the tier
- the requested ceiling
- the resolved ceiling
- the ceiling source (e.g. policy, tier default, fallback)
- the provider response

## Non-goals

- Dynamic output limits based on remaining context window.
- Token cost tracking (this is a separate concern).
- Output token streaming limit enforcement (limits are enforced via the provider request).

## Acceptance criteria

- `MYCORRHIZA_*` environment variables configure ceilings.
- OpenRouter overrides are supported.
- The Stem resolves the token ceiling before making the provider request.
- Provider adapters translate the wire shape without adding limit logic.
- Error reports include provider, model, tier, requested ceiling, resolved ceiling, ceiling source, and provider response.

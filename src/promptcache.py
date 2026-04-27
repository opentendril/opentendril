"""
src/promptcache.py — Native prompt caching for supported providers.

Splits the system prompt into a STATIC block (cached at provider level) and
a DYNAMIC block (injected fresh each turn) to maximize cache hits.

Provider support:
  - Anthropic: cache_control ephemeral blocks (up to 4 breakpoints, ~1hr TTL)
  - OpenRouter: passes Anthropic-style headers to supported models
  - Google/OpenAI: passthrough (caching handled server-side automatically)

Cache savings profile (Anthropic):
  - Static block (~2-3k tokens): billed at 10% of normal input price
  - Dynamic block (RAG, history): billed at 100% — but kept small
  - Writes: billed at 125% but amortised over many requests
"""

import logging
from typing import Optional

logger = logging.getLogger(__name__)


def _supports_explicit_cache_control(provider: str, model_name: str = "") -> bool:
    """
    Check if the provider/model supports Anthropic-style cache_control blocks.
    OpenRouter transparently passes these through for claude-* models.
    """
    if provider == "anthropic":
        return True
    if provider == "openrouter" and "claude" in model_name.lower():
        return True
    return False


def build_cached_messages(
    provider: str,
    model_name: str,
    static_system: str,
    dynamic_system: str,
    history: list[dict],
    user_message: str,
) -> list[dict]:
    """
    Construct the messages array with caching annotations where supported.

    For Anthropic-compatible providers, the static portion of the system prompt
    is wrapped in a cache_control block so it is only processed once per TTL
    (approximately 1 hour). The dynamic portion (RAG context, skills) is always
    sent fresh.

    For other providers, falls back to a simple combined system prompt.

    Args:
        provider:       Active provider name (e.g. "anthropic", "openrouter")
        model_name:     Resolved model name (used to detect claude-* on openrouter)
        static_system:  The portion that never changes (persona, tools, guardrails)
        dynamic_system: The portion that changes per-request (RAG, skills, file listing)
        history:        Recent conversation history
        user_message:   The current user turn

    Returns:
        List of message dicts ready for llm.invoke()
    """
    if _supports_explicit_cache_control(provider, model_name):
        # Build a two-block system prompt: static (cached) + dynamic (fresh)
        system_blocks = [
            {
                "type": "text",
                "text": static_system,
                "cache_control": {"type": "ephemeral"},  # Cache this block
            },
        ]
        if dynamic_system.strip():
            system_blocks.append({"type": "text", "text": dynamic_system})

        messages = [{"role": "system", "content": system_blocks}]
        logger.debug(
            f"🗄️  Cache-annotated system prompt for {provider}/{model_name}: "
            f"static={len(static_system)} chars, dynamic={len(dynamic_system)} chars"
        )
    else:
        # Passthrough — combine into a single string as before
        combined = "\n\n".join(filter(None, [static_system, dynamic_system]))
        messages = [{"role": "system", "content": combined}]

    messages += history[-8:]
    messages.append({"role": "user", "content": user_message})
    return messages

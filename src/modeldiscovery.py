"""
Tendril Model Discovery — Live catalogue fetch & tier auto-assignment.

Phase 2 of the Hybrid Model Discovery system (Issue #19).

Priority order for model selection:
  1. .env override (e.g. OPENROUTER_POWER_MODEL=anthropic/claude-opus-4.7)
  2. Auto-discovered from provider API (cached 24h)
  3. Hardcoded default in PROVIDER_CONFIG

Supported providers:
  - openrouter: GET https://openrouter.ai/api/v1/models
  - (extensible: add a fetcher per provider)
"""

import logging
import asyncio
import time
from typing import Optional
import httpx

logger = logging.getLogger(__name__)

# Cache TTL — 24 hours
_CACHE_TTL = 86_400
_cache: dict[str, dict] = {}       # provider -> {"tiers": {...}, "ts": float}


def _is_fresh(provider: str) -> bool:
    entry = _cache.get(provider)
    return bool(entry and time.time() - entry["ts"] < _CACHE_TTL)


def get_cached_tiers(provider: str) -> Optional[dict]:
    """Return cached tier assignments if still fresh."""
    if _is_fresh(provider):
        return _cache[provider]["tiers"]
    return None


async def discover_openrouter(api_key: str) -> dict:
    """
    Fetch live model list from OpenRouter and assign tiers by pricing.

    Returns:
        {"fast": "...", "standard": "...", "power": "..."}
    """
    if _is_fresh("openrouter"):
        return _cache["openrouter"]["tiers"]

    try:
        async with httpx.AsyncClient(timeout=10) as client:
            resp = await client.get(
                "https://openrouter.ai/api/v1/models",
                headers={"Authorization": f"Bearer {api_key}"},
            )
            resp.raise_for_status()
            models = resp.json().get("data", [])
    except Exception as exc:
        logger.warning(f"⚠️  OpenRouter model discovery failed: {exc}. Using defaults.")
        return {}

    if not models:
        return {}

    # Filter to models that are actually accessible and have pricing info
    scored = []
    for m in models:
        pricing = m.get("pricing", {})
        try:
            # Cost per 1M output tokens (USD) — lower = "faster/cheaper"
            cost = float(pricing.get("completion", 0) or 0) * 1_000_000
        except (TypeError, ValueError):
            cost = 0.0
        scored.append({
            "id": m["id"],
            "context": m.get("context_length", 0),
            "cost": cost,
        })

    if not scored:
        return {}

    # Sort by cost ascending — cheapest = fast, most expensive = power
    scored.sort(key=lambda x: x["cost"])

    # Partition into thirds
    n = len(scored)
    fast_pool   = scored[: max(1, n // 3)]
    power_pool  = scored[max(1, 2 * n // 3) :]
    mid_pool    = scored[max(1, n // 3): max(1, 2 * n // 3)]

    # Within power pool, pick the model with the largest context window
    def best_context(pool: list) -> str:
        return max(pool, key=lambda x: x["context"])["id"]

    # Within fast pool, pick the cheapest (already sorted)
    tiers = {
        "fast":     fast_pool[0]["id"],
        "standard": best_context(mid_pool) if mid_pool else fast_pool[-1]["id"],
        "power":    best_context(power_pool) if power_pool else scored[-1]["id"],
    }

    _cache["openrouter"] = {"tiers": tiers, "ts": time.time()}
    logger.info(
        f"🔍 OpenRouter model discovery complete: "
        f"fast={tiers['fast']} | standard={tiers['standard']} | power={tiers['power']}"
    )
    return tiers


async def discover_all(provider_config: dict) -> None:
    """
    Run discovery for all providers that support it.
    Called once at startup; results are cached for 24h.
    """
    tasks = []
    if "openrouter" in provider_config:
        key = provider_config["openrouter"].get("api_key", "")
        if key:
            tasks.append(discover_openrouter(key))

    if tasks:
        await asyncio.gather(*tasks, return_exceptions=True)

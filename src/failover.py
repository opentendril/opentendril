"""
Tendril Model Failover — Resilient multi-provider LLM invocation.

Wraps LLMRouter with automatic failover, exponential backoff,
cost-aware routing, and full event bus observability.

Better than OpenClaw:
  - Exponential backoff (5s → 10s → 20s → 60s → 300s) vs fixed 30s
  - Cost-aware tier ordering (cheap first for fast, expensive first for power)
  - Event bus integration for every attempt/skip/success
  - Dreamer-consumable event history for learning optimal defaults
"""

import time
import logging
from dataclasses import dataclass, field
from typing import Optional

from .llmrouter import LLMRouter, PROVIDER_CONFIG
from .eventbus import event_bus, TendrilEvent, generate_run_id

logger = logging.getLogger(__name__)

# Backoff schedule: consecutive errors → cooldown seconds
BACKOFF_SCHEDULE = [5, 10, 20, 60, 300]

# Cost tiers: lower = cheaper. Used for ordering fallback candidates.
PROVIDER_COST_TIER = {
    "local": 0,
    "grok": 1,
    "google": 2,
    "openai": 3,
    "anthropic": 3,
}


@dataclass
class ProviderState:
    """Tracks per-provider health and performance metrics."""
    provider: str
    cooldown_until: float = 0.0
    consecutive_errors: int = 0
    last_error_reason: str = ""
    total_requests: int = 0
    total_failures: int = 0
    total_latency_ms: float = 0.0

    @property
    def avg_latency_ms(self) -> float:
        successful = self.total_requests - self.total_failures
        if successful <= 0:
            return 0.0
        return self.total_latency_ms / successful

    @property
    def is_in_cooldown(self) -> bool:
        return time.time() < self.cooldown_until

    @property
    def cooldown_remaining_s(self) -> float:
        remaining = self.cooldown_until - time.time()
        return max(0.0, remaining)

    def record_success(self, latency_ms: float):
        self.consecutive_errors = 0
        self.cooldown_until = 0.0
        self.last_error_reason = ""
        self.total_requests += 1
        self.total_latency_ms += latency_ms

    def record_failure(self, reason: str):
        self.consecutive_errors += 1
        self.total_requests += 1
        self.total_failures += 1
        self.last_error_reason = reason

        # Exponential backoff
        idx = min(self.consecutive_errors - 1, len(BACKOFF_SCHEDULE) - 1)
        cooldown_seconds = BACKOFF_SCHEDULE[idx]
        self.cooldown_until = time.time() + cooldown_seconds
        logger.warning(
            f"⚠️  Provider '{self.provider}' failed ({reason}). "
            f"Cooldown {cooldown_seconds}s (error #{self.consecutive_errors})"
        )

    def to_dict(self) -> dict:
        return {
            "provider": self.provider,
            "in_cooldown": self.is_in_cooldown,
            "cooldown_remaining_s": round(self.cooldown_remaining_s, 1),
            "consecutive_errors": self.consecutive_errors,
            "last_error_reason": self.last_error_reason,
            "total_requests": self.total_requests,
            "total_failures": self.total_failures,
            "avg_latency_ms": round(self.avg_latency_ms, 1),
        }


def classify_error(error: Exception) -> str:
    """Classify an LLM error into a failover reason category."""
    msg = str(error).lower()

    if "rate" in msg or "429" in msg or "too many" in msg:
        return "rate_limit"
    if "401" in msg or "403" in msg or "auth" in msg or "invalid api key" in msg:
        return "auth"
    if "timeout" in msg or "timed out" in msg:
        return "timeout"
    if "402" in msg or "billing" in msg or "insufficient" in msg or "quota" in msg:
        return "billing"
    if "overloaded" in msg or "503" in msg or "529" in msg or "capacity" in msg:
        return "overloaded"
    if "500" in msg or "internal" in msg:
        return "server_error"
    return "unknown"


class AllProvidersFailed(Exception):
    """Raised when every provider in the fallback chain has failed."""
    def __init__(self, attempts: list[dict]):
        self.attempts = attempts
        summary = " → ".join(
            f"{a['provider']}({a['reason']})" for a in attempts
        )
        super().__init__(f"All providers failed: {summary}")


class ModelFailover:
    """
    Resilient LLM invocation with automatic failover.

    Wraps LLMRouter.get() with:
      - Per-provider health tracking
      - Exponential backoff cooldowns
      - Cost-aware candidate ordering
      - Full event bus observability
    """

    def __init__(self, router: LLMRouter):
        self.router = router
        self._states: dict[str, ProviderState] = {}

        # Initialize state for all available providers
        for provider in router.available_providers:
            self._states[provider] = ProviderState(provider=provider)

        logger.info(
            f"🛡️  Failover initialized with {len(self._states)} providers: "
            f"{', '.join(self._states.keys())}"
        )

    def _get_state(self, provider: str) -> ProviderState:
        if provider not in self._states:
            self._states[provider] = ProviderState(provider=provider)
        return self._states[provider]

    def _build_candidate_chain(
        self, preferred_provider: Optional[str], tier: str
    ) -> list[str]:
        """
        Build an ordered list of provider candidates.

        Order:
          1. Preferred provider (if specified and available)
          2. Remaining providers, ordered by cost appropriateness for the tier
        """
        available = self.router.available_providers

        if not available:
            return []

        # Start with preferred
        chain = []
        if preferred_provider and preferred_provider in available:
            chain.append(preferred_provider)

        # Add remaining, sorted by cost
        remaining = [p for p in available if p not in chain]

        if tier == "fast":
            # For fast tier: prefer cheap providers
            remaining.sort(key=lambda p: PROVIDER_COST_TIER.get(p, 99))
        elif tier == "power":
            # For power tier: prefer capable (expensive) providers
            remaining.sort(key=lambda p: -PROVIDER_COST_TIER.get(p, 0))
        else:
            # Standard: prefer by reliability (fewest recent errors)
            remaining.sort(
                key=lambda p: self._get_state(p).consecutive_errors
            )

        chain.extend(remaining)
        return chain

    def invoke_with_failover(
        self,
        messages: list,
        provider: Optional[str] = None,
        tier: str = "standard",
        session_id: str = "default",
        run_id: Optional[str] = None,
    ):
        """
        Invoke an LLM with automatic failover across providers.

        On failure, tries the next provider in the chain.
        Returns the response from the first successful provider.

        Args:
            messages: Chat messages to send
            provider: Preferred provider (None = default)
            tier: "fast", "standard", or "power"
            session_id: For event correlation
            run_id: For event correlation (auto-generated if None)

        Returns:
            LLM response object

        Raises:
            AllProvidersFailed: When every provider has failed
        """
        run_id = run_id or generate_run_id()
        candidates = self._build_candidate_chain(provider, tier)

        if not candidates:
            raise AllProvidersFailed([{"provider": "none", "reason": "no_providers_available"}])

        attempts = []

        for i, candidate in enumerate(candidates):
            state = self._get_state(candidate)

            # Skip if in cooldown
            if state.is_in_cooldown:
                reason = state.last_error_reason or "cooldown"
                event_bus.emit(TendrilEvent(
                    run_id=run_id,
                    event_type="failover.skip",
                    session_id=session_id,
                    data={
                        "provider": candidate,
                        "reason": reason,
                        "cooldown_remaining_s": round(state.cooldown_remaining_s, 1),
                        "attempt": i + 1,
                        "total_candidates": len(candidates),
                    },
                ))
                attempts.append({"provider": candidate, "reason": reason, "skipped": True})
                continue

            # Attempt invocation
            start_ms = time.time() * 1000
            try:
                llm = self.router.get(provider=candidate, tier=tier)
                llm_with_tools = None  # Will be bound by caller if needed

                event_bus.emit(TendrilEvent(
                    run_id=run_id,
                    event_type="failover.attempt",
                    session_id=session_id,
                    data={
                        "provider": candidate,
                        "tier": tier,
                        "attempt": i + 1,
                        "total_candidates": len(candidates),
                    },
                ))

                response = llm.invoke(messages)
                latency_ms = time.time() * 1000 - start_ms

                state.record_success(latency_ms)

                event_bus.emit(TendrilEvent(
                    run_id=run_id,
                    event_type="failover.success",
                    session_id=session_id,
                    duration_ms=int(latency_ms),
                    data={
                        "provider": candidate,
                        "tier": tier,
                        "latency_ms": round(latency_ms, 1),
                        "attempt": i + 1,
                        "was_fallback": i > 0,
                    },
                ))

                if i > 0:
                    logger.info(
                        f"🔄 Failover succeeded: {candidates[0]} → {candidate} "
                        f"({round(latency_ms)}ms)"
                    )

                return response

            except Exception as e:
                latency_ms = time.time() * 1000 - start_ms
                reason = classify_error(e)
                state.record_failure(reason)

                event_bus.emit(TendrilEvent(
                    run_id=run_id,
                    event_type="failover.fail",
                    session_id=session_id,
                    duration_ms=int(latency_ms),
                    data={
                        "provider": candidate,
                        "tier": tier,
                        "reason": reason,
                        "error": str(e)[:200],
                        "attempt": i + 1,
                        "total_candidates": len(candidates),
                    },
                ))

                attempts.append({
                    "provider": candidate,
                    "reason": reason,
                    "error": str(e)[:200],
                    "skipped": False,
                })
                continue

        # All candidates exhausted
        event_bus.emit(TendrilEvent(
            run_id=run_id,
            event_type="failover.exhausted",
            session_id=session_id,
            data={"attempts": attempts},
        ))
        raise AllProvidersFailed(attempts)

    def get_provider_health(self) -> dict:
        """Return health status for all providers (for /status endpoint)."""
        return {
            name: state.to_dict()
            for name, state in self._states.items()
        }

    def reset_provider(self, provider: str):
        """Manually clear a provider's cooldown (admin action)."""
        state = self._get_state(provider)
        state.cooldown_until = 0.0
        state.consecutive_errors = 0
        state.last_error_reason = ""
        logger.info(f"🔓 Provider '{provider}' cooldown manually reset.")

"""
Unit tests for src/failover.py — ProviderState, classify_error, ModelFailover.

Tests cover:
  - ProviderState: success/failure recording, cooldown math, backoff schedule
  - classify_error: all error categories
  - ModelFailover: candidate chain ordering, cooldown skipping
"""

import time
import pytest
from unittest.mock import MagicMock, patch

from src.failover import (
    ProviderState,
    ModelFailover,
    AllProvidersFailed,
    BACKOFF_SCHEDULE,
    classify_error,
)


# ---------------------------------------------------------------------------
# ProviderState
# ---------------------------------------------------------------------------

class TestProviderState:

    def test_initial_state_not_in_cooldown(self):
        s = ProviderState(provider="openai")
        assert not s.is_in_cooldown
        assert s.cooldown_remaining_s == 0.0

    def test_record_success_clears_errors(self):
        s = ProviderState(provider="openai")
        s.record_failure("timeout")
        s.record_success(100.0)
        assert s.consecutive_errors == 0
        assert s.last_error_reason == ""
        assert not s.is_in_cooldown

    def test_record_success_tracks_latency(self):
        s = ProviderState(provider="openai")
        s.record_success(200.0)
        s.record_success(400.0)
        assert s.avg_latency_ms == pytest.approx(300.0)

    def test_record_failure_increments_counter(self):
        s = ProviderState(provider="openai")
        s.record_failure("rate_limit")
        assert s.consecutive_errors == 1
        assert s.total_failures == 1
        assert s.last_error_reason == "rate_limit"

    def test_first_failure_uses_first_backoff(self):
        s = ProviderState(provider="openai")
        before = time.time()
        s.record_failure("timeout")
        assert s.is_in_cooldown
        assert s.cooldown_until >= before + BACKOFF_SCHEDULE[0]

    def test_backoff_escalates_with_consecutive_errors(self):
        s = ProviderState(provider="openai")
        s.record_failure("x")
        cooldown_1 = s.cooldown_remaining_s
        s.record_success(0)  # reset
        for _ in range(3):
            s.record_failure("x")
        cooldown_3 = s.cooldown_remaining_s
        assert cooldown_3 >= cooldown_1  # escalated

    def test_backoff_caps_at_max_schedule(self):
        s = ProviderState(provider="openai")
        # Trigger more failures than schedule length
        for _ in range(len(BACKOFF_SCHEDULE) + 5):
            s.record_failure("x")
        assert s.cooldown_until <= time.time() + BACKOFF_SCHEDULE[-1] + 1

    def test_to_dict_contains_expected_keys(self):
        s = ProviderState(provider="openai")
        d = s.to_dict()
        for key in ("provider", "in_cooldown", "cooldown_remaining_s",
                    "consecutive_errors", "last_error_reason",
                    "total_requests", "total_failures", "avg_latency_ms"):
            assert key in d

    def test_avg_latency_zero_when_no_successes(self):
        s = ProviderState(provider="openai")
        assert s.avg_latency_ms == 0.0


# ---------------------------------------------------------------------------
# classify_error
# ---------------------------------------------------------------------------

class TestClassifyError:

    @pytest.mark.parametrize("msg,expected", [
        ("Rate limit exceeded", "rate_limit"),
        ("429 Too Many Requests", "rate_limit"),
        ("401 Unauthorized", "auth"),
        ("Invalid API key provided", "auth"),
        ("Request timed out", "timeout"),
        ("Connection timed out", "timeout"),
        ("Insufficient quota", "billing"),
        ("402 Payment Required", "billing"),
        ("Model is currently overloaded", "overloaded"),
        ("503 Service Unavailable", "overloaded"),
        ("529 capacity", "overloaded"),
        ("500 Internal Server Error", "server_error"),
        ("Some unknown weird error", "unknown"),
    ])
    def test_classification(self, msg, expected):
        assert classify_error(Exception(msg)) == expected


# ---------------------------------------------------------------------------
# ModelFailover — candidate chain
# ---------------------------------------------------------------------------

class TestModelFailoverCandidateChain:

    def _make_failover(self, providers):
        router = MagicMock()
        router.available_providers = providers
        return ModelFailover(router)

    def test_preferred_provider_is_first(self):
        fo = self._make_failover(["openai", "anthropic", "google"])
        chain = fo._build_candidate_chain("anthropic", "standard")
        assert chain[0] == "anthropic"

    def test_fast_tier_orders_by_cheapest(self):
        fo = self._make_failover(["openai", "google", "local"])
        chain = fo._build_candidate_chain(None, "fast")
        # local (0) < google (2) < openai (3)
        non_preferred = [p for p in chain]
        assert non_preferred.index("local") < non_preferred.index("openai")

    def test_power_tier_orders_by_most_expensive(self):
        fo = self._make_failover(["openai", "google", "local"])
        chain = fo._build_candidate_chain(None, "power")
        assert chain.index("openai") < chain.index("local")

    def test_unknown_provider_preference_ignored(self):
        fo = self._make_failover(["openai", "google"])
        chain = fo._build_candidate_chain("nonexistent", "standard")
        assert "nonexistent" not in chain

    def test_empty_providers_returns_empty_chain(self):
        router = MagicMock()
        router.available_providers = []
        fo = ModelFailover(router)
        chain = fo._build_candidate_chain(None, "standard")
        assert chain == []


class TestModelFailoverCooldownSkip:

    def test_cooldown_provider_skipped(self):
        """A provider in cooldown should be skipped during invoke_with_failover."""
        router = MagicMock()
        router.available_providers = ["openai", "google"]
        fo = ModelFailover(router)

        # Put openai in cooldown
        fo._get_state("openai").record_failure("rate_limit")
        assert fo._get_state("openai").is_in_cooldown

        # Mock google to succeed
        mock_llm = MagicMock()
        mock_llm.invoke.return_value = MagicMock(content="ok")
        router.get.return_value = mock_llm

        result = fo.invoke_with_failover(
            messages=[{"role": "user", "content": "hi"}],
            session_id="test",
        )
        # Verify google was called (openai was skipped)
        assert router.get.call_args[1]["provider"] == "google" or \
               router.get.call_args[0][0] == "google" or \
               any(c.kwargs.get("provider") == "google"
                   for c in router.get.call_args_list)

    def test_all_in_cooldown_raises_all_providers_failed(self):
        router = MagicMock()
        router.available_providers = ["openai"]
        fo = ModelFailover(router)
        fo._get_state("openai").record_failure("overloaded")

        with pytest.raises(AllProvidersFailed):
            fo.invoke_with_failover(messages=[], session_id="test")

    def test_success_clears_consecutive_errors(self):
        router = MagicMock()
        router.available_providers = ["openai"]
        fo = ModelFailover(router)
        fo._get_state("openai").record_failure("timeout")
        fo._get_state("openai").record_success(100)  # manual reset
        assert not fo._get_state("openai").is_in_cooldown

    def test_reset_provider_clears_cooldown(self):
        router = MagicMock()
        router.available_providers = ["openai"]
        fo = ModelFailover(router)
        fo._get_state("openai").record_failure("rate_limit")
        assert fo._get_state("openai").is_in_cooldown
        fo.reset_provider("openai")
        assert not fo._get_state("openai").is_in_cooldown

    def test_get_provider_health_returns_all_providers(self):
        router = MagicMock()
        router.available_providers = ["openai", "google"]
        fo = ModelFailover(router)
        health = fo.get_provider_health()
        assert "openai" in health
        assert "google" in health

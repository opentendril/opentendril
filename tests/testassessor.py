"""
tests/test_assessor.py — Unit tests for the complexity assessor.

Tests the classification logic and the fail-safe fallback behaviour
using a mock LLM — no live API calls required.
"""

import pytest
from unittest.mock import MagicMock
from langchain_core.messages import AIMessage

from src.assessor import assess_complexity, assess_and_route


def _mock_llm(response_text: str) -> MagicMock:
    """Return a mock LLM that always responds with response_text."""
    llm = MagicMock()
    llm.invoke.return_value = AIMessage(content=response_text)
    return llm


class TestAssessComplexity:
    def test_returns_fast_for_fast_label(self):
        assert assess_complexity("hi", _mock_llm("fast")) == "fast"

    def test_returns_standard_for_standard_label(self):
        assert assess_complexity("explain this code", _mock_llm("standard")) == "standard"

    def test_returns_power_for_power_label(self):
        assert assess_complexity("rewrite the entire auth module", _mock_llm("power")) == "power"

    def test_trims_whitespace_and_lowercases(self):
        assert assess_complexity("show files", _mock_llm("  FAST  ")) == "fast"

    def test_falls_back_to_standard_on_unexpected_output(self):
        assert assess_complexity("hello", _mock_llm("complex")) == "standard"

    def test_falls_back_to_standard_on_llm_exception(self):
        llm = MagicMock()
        llm.invoke.side_effect = RuntimeError("network error")
        assert assess_complexity("hello", llm) == "standard"

    def test_falls_back_to_standard_on_empty_response(self):
        assert assess_complexity("hello", _mock_llm("")) == "standard"

    def test_only_uses_first_word(self):
        # Model accidentally adds explanation — should still parse correctly
        assert assess_complexity("show me the logs", _mock_llm("fast because it's a simple lookup")) == "fast"


class TestAssessAndRoute:
    def _make_router(self, response: str) -> MagicMock:
        router = MagicMock()
        router.default_provider = "anthropic"
        router.get.return_value = _mock_llm(response)
        return router

    def test_returns_assessed_tier_when_auto(self):
        router = self._make_router("power")
        _, tier = assess_and_route("rewrite everything", router, "anthropic", "auto")
        assert tier == "power"

    def test_respects_explicit_tier_override(self):
        router = self._make_router("fast")
        _, tier = assess_and_route("rewrite everything", router, "anthropic", "power")
        assert tier == "power"  # explicit override — assessor should NOT run

    def test_respects_explicit_fast_tier(self):
        router = self._make_router("power")
        _, tier = assess_and_route("complex task", router, "anthropic", "fast")
        assert tier == "fast"

    def test_falls_back_to_standard_if_router_raises(self):
        router = MagicMock()
        router.default_provider = "anthropic"
        router.get.side_effect = RuntimeError("no provider")
        _, tier = assess_and_route("hello", router, "anthropic", "auto")
        assert tier == "standard"

    def test_provider_is_preserved(self):
        router = self._make_router("standard")
        provider, _ = assess_and_route("fix this bug", router, "openrouter", "auto")
        assert provider == "openrouter"

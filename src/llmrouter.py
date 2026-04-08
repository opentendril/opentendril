"""
Tendril LLM Router — Multi-provider model dispatch.

Routes requests to the best model for the task:
  - grok:      xAI Grok models (fast + reasoning)
  - anthropic: Claude models (best for code editing)
  - openai:    GPT models (general purpose)
  - local:     vLLM on local GPU (free, private)
"""

import logging
from typing import Optional
from langchain_openai import ChatOpenAI

from .config import (
    GROK_API_KEY,
    ANTHROPIC_API_KEY,
    OPENAI_API_KEY,
    GOOGLE_API_KEY,
    DEFAULT_LLM_PROVIDER,
    LOCAL_INFERENCE_URL,
    LOCAL_MODEL_NAME,
)

logger = logging.getLogger(__name__)

# Provider configurations: (base_url, default_model, fast_model)
PROVIDER_CONFIG = {
    "grok": {
        "base_url": "https://api.x.ai/v1",
        "api_key": GROK_API_KEY,
        "models": {
            "fast": "grok-beta",
            "standard": "grok-beta",
            "power": "grok-beta",
        },
    },
    "anthropic": {
        "base_url": "https://api.anthropic.com/v1",
        "api_key": ANTHROPIC_API_KEY,
        "models": {
            "fast": "claude-3-5-haiku-latest",
            "standard": "claude-3-5-sonnet-latest",
            "power": "claude-3-5-sonnet-latest",
        },
    },
    "openai": {
        "base_url": "https://api.openai.com/v1",
        "api_key": OPENAI_API_KEY,
        "models": {
            "fast": "gpt-4o-mini",
            "standard": "gpt-4o",
            "power": "gpt-4o",
        },
    },
    "google": {
        "base_url": "https://generativelanguage.googleapis.com/v1beta/openai/",
        "api_key": GOOGLE_API_KEY,
        "models": {
            "fast": "gemini-2.0-flash",
            "standard": "gemini-2.5-pro-preview-03-25",
            "power": "gemini-2.5-pro-preview-03-25",
        },
    },
    "local": {
        "base_url": LOCAL_INFERENCE_URL,
        "api_key": "not-needed",
        "models": {
            "fast": LOCAL_MODEL_NAME,
            "standard": LOCAL_MODEL_NAME,
            "power": LOCAL_MODEL_NAME,
        },
    },
}


class LLMRouter:
    """
    Routes LLM requests to the appropriate provider and model tier.

    Usage:
        router = LLMRouter()
        llm = router.get("grok", "standard")        # specific provider + tier
        llm = router.get()                           # default provider, standard tier
        llm = router.get(tier="fast")                # default provider, fast tier
    """

    def __init__(self, default_provider: Optional[str] = None):
        self.default_provider = default_provider or DEFAULT_LLM_PROVIDER
        self._cache: dict[str, ChatOpenAI] = {}
        self._available_providers = self._detect_providers()

        if not self._available_providers:
            logger.error("❌ No LLM providers available! Configure at least one API key.")
        else:
            logger.info(f"🔌 LLM Router initialized. Available: {', '.join(self._available_providers)}")

    def _detect_providers(self) -> list[str]:
        """Detect which providers have valid API keys configured."""
        available = []
        for name, config in PROVIDER_CONFIG.items():
            if name == "local":
                # Local is always "available" — it'll fail at runtime if vLLM isn't running
                available.append(name)
                continue
            api_key = config["api_key"]
            if api_key and len(api_key) > 5:
                available.append(name)
        return available

    def get(
        self,
        provider: Optional[str] = None,
        tier: str = "standard",
        temperature: float = 0.2,
    ) -> ChatOpenAI:
        """
        Get a ChatOpenAI instance for the given provider and tier.

        Args:
            provider: "grok", "anthropic", "openai", "local" (None = default)
            tier: "fast", "standard", "power"
            temperature: Model temperature (0.0 - 1.0)

        Returns:
            ChatOpenAI instance ready for .invoke() or .stream()
        """
        provider = provider or self.default_provider

        # Fallback if requested provider isn't available
        if provider not in self._available_providers:
            fallback = self._get_fallback(provider)
            if fallback:
                logger.warning(f"⚠️  Provider '{provider}' unavailable, falling back to '{fallback}'")
                provider = fallback
            else:
                raise RuntimeError(f"No LLM providers available. Cannot route to '{provider}'.")

        config = PROVIDER_CONFIG[provider]
        model_name = config["models"].get(tier, config["models"]["standard"])
        cache_key = f"{provider}:{model_name}:{temperature}"

        if cache_key not in self._cache:
            self._cache[cache_key] = ChatOpenAI(
                model=model_name,
                api_key=config["api_key"],
                base_url=config["base_url"],
                temperature=temperature,
            )
            logger.info(f"🧠 Created LLM instance: {provider}/{model_name} (tier={tier}, temp={temperature})")

        return self._cache[cache_key]

    def _get_fallback(self, failed_provider: str) -> Optional[str]:
        """Find a fallback provider when the requested one isn't available."""
        # Prefer cloud providers over local for fallback
        preference = ["grok", "anthropic", "openai", "google", "local"]
        for p in preference:
            if p != failed_provider and p in self._available_providers:
                return p
        return None

    @property
    def available_providers(self) -> list[str]:
        """List of providers with valid configuration."""
        return self._available_providers.copy()

    def get_provider_info(self) -> dict:
        """Return info about available providers and their models for the UI."""
        info = {}
        for provider in self._available_providers:
            config = PROVIDER_CONFIG[provider]
            info[provider] = {
                "models": config["models"],
                "has_key": bool(config["api_key"] and len(config["api_key"]) > 5),
            }
        return info

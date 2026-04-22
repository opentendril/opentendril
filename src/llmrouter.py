"""
Tendril LLM Router — Multi-provider model dispatch.

Routes requests to the best model for the task:
  - grok:      xAI Grok models (fast + reasoning)
  - anthropic: Claude models (best for code editing)
  - openai:    GPT models (general purpose)
  - google:    Gemini models (via OpenAI-compatible endpoint)
  - local:     vLLM on local GPU (free, private)
"""

import logging
from typing import Optional, Union
from langchain_openai import ChatOpenAI
from langchain_anthropic import ChatAnthropic
from langchain_core.language_models.chat_models import BaseChatModel

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

# Provider configurations
# Updated: April 2026 — current model landscape
PROVIDER_CONFIG = {
    "grok": {
        "base_url": "https://api.x.ai/v1",
        "api_key": GROK_API_KEY,
        "type": "openai",
        "models": {
            "fast": "grok-3-mini",
            "standard": "grok-4-fast-non-reasoning",
            "power": "grok-4.20-0309-reasoning",
        },
    },
    "anthropic": {
        "base_url": None,  # ChatAnthropic handles this internally
        "api_key": ANTHROPIC_API_KEY,
        "type": "anthropic",
        "models": {
            "fast": "claude-haiku-4-5",
            "standard": "claude-sonnet-4-6",
            "power": "claude-opus-4-6",
        },
    },
    "openai": {
        "base_url": "https://api.openai.com/v1",
        "api_key": OPENAI_API_KEY,
        "type": "openai",
        "models": {
            "fast": "gpt-5.4-nano",
            "standard": "gpt-5.4-mini",
            "power": "gpt-5.4",
        },
    },
    "google": {
        "base_url": "https://generativelanguage.googleapis.com/v1beta/openai/",
        "api_key": GOOGLE_API_KEY,
        "type": "openai",
        "models": {
            "fast": "gemini-2.5-flash",
            "standard": "gemini-3-flash",
            "power": "gemini-3.1-pro-preview",
        },
    },
    "local": {
        "base_url": LOCAL_INFERENCE_URL,
        "api_key": "not-needed",
        "type": "openai",
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
        self._cache: dict[str, BaseChatModel] = {}
        self._available_providers = self._detect_providers()

        if not self._available_providers:
            logger.warning("⚠️ No LLM providers available! Interactive setup mode engaged.")
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
        # Nano is always available last (CPU fallback, no key needed)
        from .config import NANO_MODEL_ENABLED
        if NANO_MODEL_ENABLED and "nano" not in available:
            available.append("nano")
        return available

    @property
    def available_providers(self) -> list[str]:
        """Return list of providers with valid API keys."""
        return list(self._available_providers)

    def reconfigure_provider(self, provider_key: str, new_api_key: str) -> bool:
        """Dynamically inject an API key and reload the provider cache."""
        # Find the matching provider prefix (e.g. OPENAI_API_KEY -> openai)
        provider_name = provider_key.lower().replace("_api_key", "")
        
        if provider_name not in PROVIDER_CONFIG:
            logger.error(f"❌ Unknown provider for config key '{provider_key}'")
            return False
            
        import os
        # Update the environment so other parts of the app can see it
        os.environ[provider_key.upper()] = new_api_key
        
        # Update the internal config
        PROVIDER_CONFIG[provider_name]["api_key"] = new_api_key
        
        # Clear cache for this provider to force recreation
        keys_to_delete = [k for k in self._cache.keys() if k.startswith(f"{provider_name}:")]
        for k in keys_to_delete:
            del self._cache[k]
            
        # Re-run detection
        self._available_providers = self._detect_providers()
        logger.info(f"🔄 Dynamically reconfigured provider '{provider_name}'. Available: {', '.join(self._available_providers)}")
        return True

    def get(
        self,
        provider: Optional[str] = None,
        tier: str = "standard",
        temperature: float = 0.2,
    ) -> BaseChatModel:
        """
        Get a chat model instance for the given provider and tier.

        Args:
            provider: "grok", "anthropic", "openai", "google", "local" (None = default)
            tier: "fast", "standard", "power"
            temperature: Model temperature (0.0 - 1.0)

        Returns:
            BaseChatModel instance ready for .invoke() or .stream()
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

        # Nano provider — CPU-only, no PROVIDER_CONFIG entry needed
        if provider == "nano":
            from .providers.nano import NanoProvider
            return NanoProvider()

        config = PROVIDER_CONFIG[provider]
        model_name = config["models"].get(tier, config["models"]["standard"])
        cache_key = f"{provider}:{model_name}:{temperature}"

        if cache_key not in self._cache:
            if config["type"] == "anthropic":
                self._cache[cache_key] = ChatAnthropic(
                    model=model_name,
                    api_key=config["api_key"],
                    temperature=temperature,
                    max_tokens=4096,
                )
            else:
                self._cache[cache_key] = ChatOpenAI(
                    model=model_name,
                    api_key=config["api_key"],
                    base_url=config["base_url"],
                    temperature=temperature,
                )
            logger.info(f"🧠 Created LLM instance: {provider}/{model_name} (tier={tier}, temp={temperature})")

        return self._cache[cache_key]

    def _get_fallback(self, failed_provider: str) -> Optional[str]:
        """Find a fallback provider when the requested one isn't available.
        Nano is always last resort — cloud first, then local, then nano."""
        preference = ["grok", "anthropic", "openai", "google", "local", "nano"]
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

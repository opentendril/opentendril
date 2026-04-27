"""Tendril configuration — loads env vars and validates required settings."""

VERSION: str = "0.1.0"

import os
import logging
from dotenv import load_dotenv

load_dotenv()
logger = logging.getLogger(__name__)

# --- LLM Provider Keys ---
GROK_API_KEY: str = os.getenv("GROK_API_KEY", "")
ANTHROPIC_API_KEY: str = os.getenv("ANTHROPIC_API_KEY", "")
OPENAI_API_KEY: str = os.getenv("OPENAI_API_KEY", "")
GOOGLE_API_KEY: str = os.getenv("GOOGLE_API_KEY", "")
OPENROUTER_API_KEY: str = os.getenv("OPENROUTER_API_KEY", "")

# Default provider for the LLM Router
DEFAULT_LLM_PROVIDER: str = os.getenv("DEFAULT_LLM_PROVIDER", "grok")

# --- Per-Provider Model Tier Overrides ---
# Set these in .env to pin specific model versions without changing code.
# If unset, LLMRouter uses its hardcoded defaults (or auto-discovers via API).
# Format: <PROVIDER>_<TIER>_MODEL=<model-name>
GROK_FAST_MODEL: str = os.getenv("GROK_FAST_MODEL", "")
GROK_STANDARD_MODEL: str = os.getenv("GROK_STANDARD_MODEL", "")
GROK_POWER_MODEL: str = os.getenv("GROK_POWER_MODEL", "")

ANTHROPIC_FAST_MODEL: str = os.getenv("ANTHROPIC_FAST_MODEL", "")
ANTHROPIC_STANDARD_MODEL: str = os.getenv("ANTHROPIC_STANDARD_MODEL", "")
ANTHROPIC_POWER_MODEL: str = os.getenv("ANTHROPIC_POWER_MODEL", "")

OPENAI_FAST_MODEL: str = os.getenv("OPENAI_FAST_MODEL", "")
OPENAI_STANDARD_MODEL: str = os.getenv("OPENAI_STANDARD_MODEL", "")
OPENAI_POWER_MODEL: str = os.getenv("OPENAI_POWER_MODEL", "")

GOOGLE_FAST_MODEL: str = os.getenv("GOOGLE_FAST_MODEL", "")
GOOGLE_STANDARD_MODEL: str = os.getenv("GOOGLE_STANDARD_MODEL", "")
GOOGLE_POWER_MODEL: str = os.getenv("GOOGLE_POWER_MODEL", "")

OPENROUTER_FAST_MODEL: str = os.getenv("OPENROUTER_FAST_MODEL", "")
OPENROUTER_STANDARD_MODEL: str = os.getenv("OPENROUTER_STANDARD_MODEL", "")
OPENROUTER_POWER_MODEL: str = os.getenv("OPENROUTER_POWER_MODEL", "")

# Local inference (optional — GPU via vllm)
LOCAL_MODEL_NAME: str = os.getenv("LOCAL_MODEL_NAME", "Qwen/Qwen3-8B-AWQ")
LOCAL_INFERENCE_URL: str = os.getenv("LOCAL_INFERENCE_URL", "http://inference:8000/v1")

# Nano model — CPU-only zero-config fallback (opt-in via --profile nano)
# Set NANO_MODEL_ENABLED=true or use: docker compose --profile nano up
NANO_MODEL_ENABLED: bool = os.getenv("NANO_MODEL_ENABLED", "false").lower() == "true"
NANO_MODEL_NAME: str = os.getenv("NANO_MODEL_NAME", "Qwen/Qwen2.5-0.5B-Instruct-GGUF")
NANO_MODEL_FILE: str = os.getenv("NANO_MODEL_FILE", "qwen2.5-0.5b-instruct-q4_k_m.gguf")

# Complexity Assessor — auto-routes requests to fast/standard/power tier
# Set to false to always use the default tier (avoids extra fast-model call)
ASSESSOR_ENABLED: bool = os.getenv("ASSESSOR_ENABLED", "true").lower() == "true"

# --- Infrastructure ---
POSTGRES_USER: str = os.getenv("POSTGRES_USER", "postgres")
POSTGRES_PASSWORD: str = os.getenv("POSTGRES_PASSWORD", "")
REDIS_PASSWORD: str = os.getenv("REDIS_PASSWORD", "")
SECRET_KEY: str = os.getenv("SECRET_KEY", "")

# KV Store provider — redis | upstash | memory
KV_STORE_PROVIDER: str = os.getenv("KV_STORE_PROVIDER", "redis")
UPSTASH_REDIS_REST_URL: str = os.getenv("UPSTASH_REDIS_REST_URL", "")
UPSTASH_REDIS_REST_TOKEN: str = os.getenv("UPSTASH_REDIS_REST_TOKEN", "")

# Postgres connection — user is configurable for production
POSTGRES_URL = f"postgresql+psycopg://{POSTGRES_USER}:{POSTGRES_PASSWORD}@postgres:5432/postgres"
REDIS_URL = f"redis://:{REDIS_PASSWORD}@redis:6379/0" if REDIS_PASSWORD else "redis://redis:6379/0"
DB_CONNECTION = POSTGRES_URL

# Embedding config
EMBEDDING_MODEL = "all-MiniLM-L6-v2"
EMBEDDING_DIM = 384

# Project paths (inside Docker container)
PROJECT_ROOT = "/app"
WORKSPACE_ROOT = os.getenv("TENDRIL_WORKSPACE_ROOT", PROJECT_ROOT)

SRC_DIR = os.path.join(WORKSPACE_ROOT, "src")
SKILLS_DIR = os.path.join(WORKSPACE_ROOT, "skills")
DYNAMIC_SKILLS_DIR = os.path.join(PROJECT_ROOT, "data", "dynamic-skills")
LOG_DIR = os.path.join(PROJECT_ROOT, "logs")
DATA_ENV_PATH = os.path.join(PROJECT_ROOT, "data", ".env")

# SDLC Pipeline Configuration
SDLC_MODE: str = os.getenv("SDLC_MODE", "strict").lower()
STRICT_LINTING: bool = os.getenv("STRICT_LINTING", "false").lower() == "true"

# Sandbox Configuration (secure code execution)
SANDBOX_ENABLED: bool = os.getenv("SANDBOX_ENABLED", "true").lower() == "true"
SANDBOX_URL: str = os.getenv("SANDBOX_URL", "http://sandbox:9999")
SANDBOX_TOKEN: str = os.getenv("SANDBOX_TOKEN", "")

# --- Validation ---
def has_active_llm_provider() -> bool:
    """Check if any LLM API key is configured, or nano model is enabled (always available)."""
    if NANO_MODEL_ENABLED:
        return True
    providers = {
        "grok": os.getenv("GROK_API_KEY", ""),
        "anthropic": os.getenv("ANTHROPIC_API_KEY", ""),
        "openai": os.getenv("OPENAI_API_KEY", ""),
        "google": os.getenv("GOOGLE_API_KEY", ""),
        "openrouter": os.getenv("OPENROUTER_API_KEY", ""),
    }
    return any(v and len(v) > 5 for v in providers.values())

def validate_config():
    """Validate system configuration (does not crash on missing LLM keys to allow OOTB setup)."""
    providers = {
        "grok": GROK_API_KEY,
        "anthropic": ANTHROPIC_API_KEY,
        "openai": OPENAI_API_KEY,
        "google": GOOGLE_API_KEY,
        "openrouter": OPENROUTER_API_KEY,
    }
    active = {k: v for k, v in providers.items() if v}

    if not active:
        logger.warning(
            "⚠️  No LLM API keys configured! Tendril will boot into interactive setup mode."
        )
        # We don't return False here anymore so the app can boot
    else:
        logger.info(f"✅ LLM providers available: {', '.join(active.keys())}")

    if active and DEFAULT_LLM_PROVIDER not in active and DEFAULT_LLM_PROVIDER != "local":
        fallback = next(iter(active.keys()))
        logger.warning(
            f"⚠️  Default provider '{DEFAULT_LLM_PROVIDER}' has no API key. "
            f"Falling back to '{fallback}'"
        )

    if not SECRET_KEY or SECRET_KEY == "generate_with_openssl_rand_-hex_32":
        logger.warning("⚠️  SECRET_KEY is not set or is using the example value. Skills signing will be weak.")

    return True
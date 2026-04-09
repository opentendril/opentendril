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

# Default provider for the LLM Router
DEFAULT_LLM_PROVIDER: str = os.getenv("DEFAULT_LLM_PROVIDER", "grok")

# Local inference (optional)
LOCAL_MODEL_NAME: str = os.getenv("LOCAL_MODEL_NAME", "Qwen/Qwen3-8B-AWQ")
LOCAL_INFERENCE_URL: str = os.getenv("LOCAL_INFERENCE_URL", "http://inference:8000/v1")

# --- Infrastructure ---
POSTGRES_USER: str = os.getenv("POSTGRES_USER", "postgres")
POSTGRES_PASSWORD: str = os.getenv("POSTGRES_PASSWORD", "")
REDIS_PASSWORD: str = os.getenv("REDIS_PASSWORD", "")
SECRET_KEY: str = os.getenv("SECRET_KEY", "")

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

# SDLC Pipeline Configuration
STRICT_LINTING: bool = os.getenv("STRICT_LINTING", "false").lower() == "true"

# Sandbox Configuration (secure code execution)
SANDBOX_ENABLED: bool = os.getenv("SANDBOX_ENABLED", "true").lower() == "true"
SANDBOX_URL: str = os.getenv("SANDBOX_URL", "http://sandbox:9999")
SANDBOX_TOKEN: str = os.getenv("SANDBOX_TOKEN", "")

# --- Validation ---
def validate_config():
    """Check that at least one LLM provider is configured."""
    providers = {
        "grok": GROK_API_KEY,
        "anthropic": ANTHROPIC_API_KEY,
        "openai": OPENAI_API_KEY,
        "google": GOOGLE_API_KEY,
    }
    active = {k: v for k, v in providers.items() if v}

    if not active:
        logger.warning(
            "⚠️  No LLM API keys configured. Set at least one of: "
            "GROK_API_KEY, ANTHROPIC_API_KEY, OPENAI_API_KEY"
        )
        return False

    logger.info(f"✅ LLM providers available: {', '.join(active.keys())}")

    if DEFAULT_LLM_PROVIDER not in active and DEFAULT_LLM_PROVIDER != "local":
        fallback = next(iter(active.keys()))
        logger.warning(
            f"⚠️  Default provider '{DEFAULT_LLM_PROVIDER}' has no API key. "
            f"Falling back to '{fallback}'"
        )

    if not SECRET_KEY or SECRET_KEY == "generate_with_openssl_rand_-hex_32":
        logger.warning("⚠️  SECRET_KEY is not set or is using the example value. Skills signing will be weak.")

    return True
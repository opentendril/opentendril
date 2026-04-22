"""
src/providers/nano.py — CPU Nano-Model provider for zero-config operation.

Wraps llama-cpp-python to expose a minimal LangChain-compatible .invoke()
interface. Downloads the chosen GGUF model on first use from HuggingFace,
streams live progress to the event bus so the UI can display it.

Capabilities in Nano Mode:
  ✅ Slash commands (/help, /status, /sdlc, /model, /keys, /repo)
  ✅ Basic conversational Q&A about Tendril
  ❌ Code editing (/edit) — requires cloud or GPU model
  ❌ Complex multi-step agentic tool use

Users are clearly informed of these limitations at every turn.
"""

import logging
import os
import threading
from dataclasses import dataclass, field
from typing import Optional

logger = logging.getLogger(__name__)

# --- Download state shared across threads ---


@dataclass
class NanoModelState:
    """Singleton tracking download and load state of the nano model."""
    status: str = "idle"          # idle | downloading | loading | ready | error | unavailable
    progress_pct: int = 0
    progress_msg: str = ""
    model_path: str = ""
    error: str = ""
    model_name: str = ""
    _lock: threading.Lock = field(default_factory=threading.Lock, repr=False)


nano_state = NanoModelState()
_llama_model = None   # Global llama.Llama instance, loaded once


def is_nano_available() -> bool:
    """Return True if llama-cpp-python is installed."""
    try:
        import llama_cpp  # noqa: F401
        return True
    except ImportError:
        return False


def get_model_cache_path(model_name: str) -> str:
    """Return the local filesystem path where the GGUF will be cached."""
    cache_dir = os.getenv("NANO_MODEL_CACHE_DIR", "/app/data/models")
    os.makedirs(cache_dir, exist_ok=True)
    # Flatten model name to a safe filename
    safe_name = model_name.replace("/", "--").replace(":", "-")
    return os.path.join(cache_dir, f"{safe_name}.gguf")


def download_model(model_name: str, gguf_filename: str, hf_token: Optional[str] = None) -> str:
    """
    Download a GGUF model from HuggingFace to local cache.
    Updates nano_state.progress_pct during download.
    Returns the local file path on success.
    Raises RuntimeError on failure.
    """
    from huggingface_hub import hf_hub_download
    from huggingface_hub import constants as hf_constants

    cache_path = get_model_cache_path(model_name)
    if os.path.exists(cache_path):
        logger.info(f"Nano model already cached at {cache_path}")
        return cache_path

    nano_state.status = "downloading"
    nano_state.progress_pct = 0
    nano_state.progress_msg = f"Starting download of {model_name}..."
    nano_state.model_name = model_name

    logger.info(f"Downloading nano model: {model_name}/{gguf_filename}")

    try:
        # Use HuggingFace Hub with progress callback
        from tqdm import tqdm as _tqdm

        class _ProgressBar(_tqdm):
            def update(self, n=1):
                super().update(n)
                if self.total:
                    pct = int((self.n / self.total) * 100)
                    mb_done = self.n / 1024 / 1024
                    mb_total = self.total / 1024 / 1024
                    nano_state.progress_pct = pct
                    nano_state.progress_msg = f"⬇️ Downloading {model_name}... **{pct}%** ({mb_done:.0f}MB / {mb_total:.0f}MB)"

        downloaded = hf_hub_download(
            repo_id=model_name,
            filename=gguf_filename,
            token=hf_token,
            local_dir=os.path.dirname(cache_path),
            local_dir_use_symlinks=False,
        )

        # Rename to canonical cache path if needed
        if downloaded != cache_path:
            os.rename(downloaded, cache_path)

        nano_state.progress_pct = 100
        nano_state.progress_msg = f"✅ Download complete. Loading model..."
        logger.info(f"Nano model downloaded to {cache_path}")
        return cache_path

    except Exception as e:
        nano_state.status = "error"
        nano_state.error = str(e)
        raise RuntimeError(f"Failed to download nano model: {e}") from e


def load_model(model_path: str) -> None:
    """Load the GGUF into memory via llama-cpp-python. Sets global _llama_model."""
    global _llama_model

    if not is_nano_available():
        nano_state.status = "unavailable"
        nano_state.error = "llama-cpp-python is not installed."
        return

    nano_state.status = "loading"
    nano_state.progress_msg = "Loading model into memory..."

    try:
        from llama_cpp import Llama
        _llama_model = Llama(
            model_path=model_path,
            n_ctx=2048,
            n_threads=max(1, os.cpu_count() - 1),
            verbose=False,
        )
        nano_state.status = "ready"
        nano_state.model_path = model_path
        nano_state.progress_msg = "✅ Nano model loaded and ready."
        logger.info(f"✅ Nano model loaded from {model_path}")
    except Exception as e:
        nano_state.status = "error"
        nano_state.error = str(e)
        logger.error(f"Failed to load nano model: {e}")


class NanoMessage:
    """Minimal response object mimicking LangChain AIMessage."""
    def __init__(self, content: str):
        self.content = content
        self.tool_calls = []


class NanoProvider:
    """
    Lightweight LangChain-compatible wrapper around llama-cpp-python.

    Exposes .invoke(prompt) returning a NanoMessage. Does NOT support
    tool binding — any call to bind_tools() returns self unchanged.
    """

    NANO_DISCLAIMER = (
        "\n\n---\n⚡ *Running in Nano Mode (CPU-only). "
        "For code editing, complex reasoning, and tool use, configure a cloud API key "
        "via `/keys` or enable GPU mode via `docker compose --profile gpu up`.*"
    )

    def invoke(self, prompt) -> NanoMessage:
        global _llama_model

        if nano_state.status != "ready" or _llama_model is None:
            return NanoMessage(
                "⚠️ Nano model is not ready yet. "
                f"Status: `{nano_state.status}`. "
                f"{nano_state.progress_msg}"
            )

        # Extract text from prompt (handle both str and list of dicts)
        if isinstance(prompt, str):
            prompt_text = prompt
        elif isinstance(prompt, list):
            prompt_text = "\n".join(
                m.get("content", "") if isinstance(m, dict) else str(m)
                for m in prompt
                if isinstance(m, dict) and m.get("role") != "system"
            )
        else:
            prompt_text = str(prompt)

        try:
            response = _llama_model(
                prompt_text,
                max_tokens=512,
                temperature=0.7,
                stop=["<|endoftext|>", "\n\nHuman:", "\n\nUser:"],
                echo=False,
            )
            content = response["choices"][0]["text"].strip()
            return NanoMessage(content + self.NANO_DISCLAIMER)
        except Exception as e:
            logger.error(f"Nano model inference error: {e}")
            return NanoMessage(
                f"❌ Nano model inference failed: {str(e)}{self.NANO_DISCLAIMER}"
            )

    def bind_tools(self, tools) -> "NanoProvider":
        """Nano model does not support tool binding. Returns self unchanged."""
        logger.debug("Nano model: bind_tools() called but tool binding is not supported.")
        return self

import os
import threading
from typing import Optional
from .config import DATA_ENV_PATH, has_active_llm_provider
from .dependencies import llm_router

def append_to_env(key: str, val: str):
    os.makedirs(os.path.dirname(DATA_ENV_PATH), exist_ok=True)
    with open(DATA_ENV_PATH, "a") as f:
        f.write(f"\\n{key}={val}\\n")

def intercept_slash_commands(message: str, session_id: str = "system") -> Optional[str]:
    """
    Intercepts and handles CLI slash commands (/help, /repo, etc.)
    Returns a response string if intercepted, or None to proceed to the LLM.
    """
    msg_strip = message.strip()
    
    if not msg_strip.startswith("/"):
        if not has_active_llm_provider():
            return (
                "🌱 **Welcome to Tendril!** The system is live, but no LLM API keys are configured.\n\n"
                "To start chatting, please provide an API key using the `/keys` command:\n"
                "`/keys ANTHROPIC_API_KEY sk-ant-...`\n\n"
                "Type `/help` to see all available commands."
            )

        # Check if running in nano mode (no cloud key, nano is the only active provider)
        from .config import NANO_MODEL_ENABLED, NANO_MODEL_NAME, NANO_MODEL_FILE
        from .providers.nano import nano_state
        cloud_keys = [
            os.getenv("GROK_API_KEY", ""), os.getenv("ANTHROPIC_API_KEY", ""),
            os.getenv("OPENAI_API_KEY", ""), os.getenv("GOOGLE_API_KEY", ""),
        ]
        has_cloud_key = any(k and len(k) > 5 for k in cloud_keys)

        if NANO_MODEL_ENABLED and not has_cloud_key and nano_state.status == "idle":
            return (
                "🌱 **Welcome to Tendril!** No API keys are configured yet.\n\n"
                "To get started immediately **without an API key**, I can download a lightweight local model "
                "that runs entirely on your CPU (~400MB).\n\n"
                "> ⚠️ *Nano Mode is for slash commands and basic Q&A only. "
                "Code editing requires a cloud or GPU model.*\n\n"
                "**Which model would you like?**\n"
                "- Reply `/nano 1` → **Qwen2.5-0.5B** (default, ~400MB, fastest CPU)\n"
                "- Reply `/nano 2` → **TinyLlama-1.1B** (~700MB, slightly smarter)\n"
                "- Reply `/nano skip` → Skip — I'll configure a cloud key via `/keys`\n\n"
                "Or jump straight to a cloud key:\n"
                "`/keys ANTHROPIC_API_KEY sk-ant-...`"
            )

        return None

    # Parse command and args
    parts = msg_strip.split(maxsplit=1)
    cmd = parts[0].lower()
    args = parts[1] if len(parts) > 1 else ""

    if cmd == "/help":
        return """## Tendril Command Center

**Configuration**
- `/keys <PROVIDER> <KEY>` : Set an API key (e.g., `/keys OPENAI_API_KEY sk-...`)
- `/model <PROVIDER>` : Switch default LLM provider (e.g., `/model grok`)
- `/repo <PATH>` : Switch the active codebase (e.g., `/repo /tmp/app`)
- `/local` : Enable the local GPU-accelerated Qwen-3 model
- `/nano <1|2|skip>` : Download a CPU nano-model (no API key required)
- `/nano status` : Check nano model download progress

**System**
- `/sdlc <strict|simple>` : Toggle strict security and testing gates
- `/secure` : Promote to sandboxed container execution if Docker is online
- `/status` : View current system configuration
- `/test` : Run system health checks
- `/restart` : Restart the Tendril container
"""

    elif cmd == "/status":
        from .config import DEFAULT_LLM_PROVIDER, WORKSPACE_ROOT, SDLC_MODE
        active = ", ".join(llm_router.available_providers) if llm_router.available_providers else "None"
        return f"""### System Status
- **Workspace:** `{WORKSPACE_ROOT}`
- **Active Keys:** {active}
- **Default Model:** `{DEFAULT_LLM_PROVIDER}`
- **SDLC Mode:** `{SDLC_MODE}`
"""

    elif cmd == "/keys":
        key_parts = args.split(maxsplit=1)
        if len(key_parts) != 2:
            return "❌ Invalid format. Use: `/keys OPENAI_API_KEY sk-...`"
            
        key, val = key_parts[0].strip(), key_parts[1].strip()
        success = llm_router.reconfigure_provider(key, val)
        if not success:
            return "❌ Failed to configure. Ensure the key matches a supported provider (OPENAI_API_KEY, ANTHROPIC_API_KEY, GROK_API_KEY)."
            
        try:
            append_to_env(key, val)
        except Exception as e:
            return f"⚠️ Applied dynamically but failed to save to disk: {e}"
            
        from .eventbus import event_bus, TendrilEvent, generate_run_id
        event_bus.emit(TendrilEvent(
            run_id=generate_run_id(),
            event_type="onboarding.complete",
            session_id=session_id,
            data={"configured_key": key}
        ))

        return (
            f"✅ **Key saved!**\n\n"
            f"🚀 **Onboarding Complete!** Tendril is fully initialized, telemetry has been logged, and I am actively watching your workspace.\n\n"
            f"You are now ready to type your first development prompt!"
        )

    elif cmd == "/model":
        if not args:
            return "❌ Missing provider. Use: `/model anthropic`"
        try:
            append_to_env("DEFAULT_LLM_PROVIDER", args.strip().lower())
            return f"✅ Default model set to `{args}`. The CLI will restart the server to apply this change."
        except Exception as e:
            return f"❌ Failed to save to disk: {e}"

    elif cmd == "/repo":
        if not args:
            return "❌ Missing path. Use: `/repo /absolute/path`"
        try:
            append_to_env("TENDRIL_PROJECT_PATH", args.strip())
            return f"✅ Workspace updated to `{args}`. The CLI will now restart the server to mount the new folder."
        except Exception as e:
            return f"❌ Failed to save to disk: {e}"

    elif cmd == "/init":
        from .config import WORKSPACE_ROOT
        from .eventbus import event_bus, TendrilEvent, generate_run_id
        from collections import Counter
        import urllib.request
        import json

        args_str = args.strip()

        # Handle sub-options
        if args_str.startswith("1"):
            try:
                append_to_env("DEFAULT_LLM_PROVIDER", "opentendril")
                append_to_env("OPENTENDRIL_API_KEY", "free-trial")
                return (
                    "✅ **Free Trial Cloud configured!**\n\n"
                    "Default provider set to `opentendril`.\n"
                    "The CLI will restart the server to apply changes."
                )
            except Exception as e:
                return f"❌ Failed to save to disk: {e}"

        elif args_str.startswith("2"):
            ollama_models = []
            try:
                req = urllib.request.Request("http://localhost:11434/api/tags", method="GET")
                with urllib.request.urlopen(req, timeout=1.0) as response:
                    if response.status == 200:
                        data = json.loads(response.read().decode("utf-8"))
                        ollama_models = [m["name"] for m in data.get("models", [])]
            except Exception:
                return "❌ **Ollama is offline or unreachable on http://localhost:11434.** Please start Ollama and try again, or select another option."

            if not ollama_models:
                return "❌ **Ollama is active, but no models are downloaded.** Please run `ollama pull qwen2.5-coder` first."

            detected_model = "qwen2.5-coder"
            coder_models = [m for m in ollama_models if "coder" in m.lower()]
            qwen_models = [m for m in ollama_models if "qwen" in m.lower()]
            llama_models = [m for m in ollama_models if "llama" in m.lower()]
            
            if coder_models:
                detected_model = coder_models[0]
            elif qwen_models:
                detected_model = qwen_models[0]
            elif llama_models:
                detected_model = llama_models[0]
            else:
                detected_model = ollama_models[0]

            try:
                append_to_env("DEFAULT_LLM_PROVIDER", "local")
                append_to_env("LOCAL_INFERENCE_URL", "http://localhost:11434/v1")
                append_to_env("LOCAL_MODEL_NAME", detected_model)
                return (
                    f"✅ **Local Ollama configured!**\n\n"
                    f"Detected model: `{detected_model}`.\n"
                    f"Default provider set to `local` using base URL `http://localhost:11434/v1`.\n"
                    f"The CLI will restart the server to apply changes."
                )
            except Exception as e:
                return f"❌ Failed to save to disk: {e}"

        elif args_str.startswith("3"):
            subargs = args_str.split(maxsplit=2)
            if len(subargs) < 3:
                return (
                    "🔑 **API Key Configuration**\n\n"
                    "Please reply with your API key using the following format:\n"
                    "👉 `/init 3 <PROVIDER_KEY> <API_KEY>`\n\n"
                    "Supported provider keys:\n"
                    "- `ANTHROPIC_API_KEY` (e.g. `/init 3 ANTHROPIC_API_KEY sk-ant-...`)\n"
                    "- `OPENAI_API_KEY` (e.g. `/init 3 OPENAI_API_KEY sk-...`)\n"
                    "- `GROK_API_KEY` (e.g. `/init 3 GROK_API_KEY sk-...`)\n"
                    "- `GOOGLE_API_KEY` (e.g. `/init 3 GOOGLE_API_KEY AIzaSy...`)\n"
                    "- `OPENROUTER_API_KEY` (e.g. `/init 3 OPENROUTER_API_KEY sk-or-...`)"
                )
            
            provider_key = subargs[1].strip().upper()
            val = subargs[2].strip()
            
            provider_name = provider_key.lower().replace("_api_key", "")
            
            success = llm_router.reconfigure_provider(provider_key, val)
            if not success:
                return "❌ Failed to configure key. Ensure the key matches a supported provider (OPENAI_API_KEY, ANTHROPIC_API_KEY, GROK_API_KEY, GOOGLE_API_KEY, OPENROUTER_API_KEY)."
                
            try:
                append_to_env(provider_key, val)
                append_to_env("DEFAULT_LLM_PROVIDER", provider_name)
                return (
                    f"✅ **API Key configured!**\n\n"
                    f"Saved `{provider_key}` and set default provider to `{provider_name}`.\n"
                    f"The CLI will restart the server to apply changes."
                )
            except Exception as e:
                return f"❌ Failed to save to disk: {e}"

        # Otherwise, run the initial survey & check Ollama status
        ignores = {".git", "venv", "__pycache__", "node_modules"}
        ext_counter = Counter()

        for root, dirs, files in os.walk(WORKSPACE_ROOT):
            dirs[:] = [d for d in dirs if d not in ignores and not d.startswith(".")]
            for f in files:
                ext = os.path.splitext(f)[1].lower()
                if ext:
                    ext_counter[ext] += 1

        top_exts = ext_counter.most_common(3)
        detected_languages = {ext: count for ext, count in ext_counter.items()}
        
        dominant_text = ""
        if top_exts:
            top_ext_names = [f"{ext} ({count})" for ext, count in top_exts]
            dominant_text = f" Detected dominant extensions: {', '.join(top_ext_names)}."

        # Autodetect Ollama
        ollama_active = False
        detected_models = []
        try:
            req = urllib.request.Request("http://localhost:11434/api/tags", method="GET")
            with urllib.request.urlopen(req, timeout=1.0) as response:
                if response.status == 200:
                    data = json.loads(response.read().decode("utf-8"))
                    ollama_active = True
                    detected_models = [m["name"] for m in data.get("models", [])]
        except Exception:
            pass

        ollama_status = f"Active ({len(detected_models)} models detected)" if ollama_active else "Inactive (offline)"
            
        event_bus.emit(TendrilEvent(
            run_id=generate_run_id(),
            event_type="onboarding.survey_complete",
            session_id=session_id,
            data={"detected_languages": detected_languages}
        ))

        return (
            f"✅ **Initialization Wizard Triggered.**\n\n"
            f"🌱 Workspace Survey Complete:{dominant_text}\n"
            f"🤖 Local Ollama Status: **{ollama_status}**\n\n"
            f"Please select a reasoning provider to configure:\n"
            f"1️⃣ **Option 1: Free Trial Cloud** (anonymous routing via `api.opentendril.com`)\n"
            f"   👉 Reply: `/init 1`\n"
            f"2️⃣ **Option 2: Local Ollama** (auto-detect coding models)\n"
            f"   👉 Reply: `/init 2`\n"
            f"3️⃣ **Option 3: API Key Configuration** (custom Anthropic/OpenAI keys)\n"
            f"   👉 Reply: `/init 3`"
        )

    elif cmd == "/local":
        try:
            append_to_env("DEFAULT_LLM_PROVIDER", "local")
            append_to_env("LOCAL_MODEL_NAME", "Qwen/Qwen3-8B-AWQ")
            return "✅ Local model configured (`Qwen/Qwen3-8B-AWQ`). The CLI will now restart the server using the GPU profile."
        except Exception as e:
            return f"❌ Failed to save to disk: {e}"

    elif cmd == "/sdlc":
        if not args or args.strip().lower() not in ["strict", "simple"]:
            return "❌ Invalid mode. Use: `/sdlc strict` or `/sdlc simple`"
        try:
            mode = args.strip().lower()
            append_to_env("SDLC_MODE", mode)
            from . import config
            config.SDLC_MODE = mode  # Apply instantly in memory as well
            return f"✅ SDLC Mode updated to `{mode}`."
        except Exception as e:
            return f"❌ Failed to save to disk: {e}"
            
    elif cmd == "/nano":
        from .config import NANO_MODEL_ENABLED
        if not NANO_MODEL_ENABLED:
            return "❌ Nano model is disabled. Set `NANO_MODEL_ENABLED=true` in your `.env` to enable it."

        choice = args.strip().lower()

        if choice == "skip":
            return (
                "✅ Skipping nano model. Configure a cloud key when ready:\n"
                "`/keys ANTHROPIC_API_KEY sk-ant-...`"
            )

        # Map choice to model details
        NANO_MODELS = {
            "1": ("Qwen/Qwen2.5-0.5B-Instruct-GGUF", "qwen2.5-0.5b-instruct-q4_k_m.gguf"),
            "2": ("TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF", "tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf"),
        }

        if not choice or choice not in NANO_MODELS:
            return "❌ Invalid choice. Reply `/nano 1`, `/nano 2`, or `/nano skip`."

        model_name, model_file = NANO_MODELS[choice]

        try:
            append_to_env("NANO_MODEL_NAME", model_name)
            append_to_env("NANO_MODEL_FILE", model_file)
        except Exception:
            pass

        # Trigger download in background thread so the UI doesn't block
        def _download_and_load():
            from .providers.nano import download_model, load_model, nano_state
            try:
                hf_token = os.getenv("HF_TOKEN", None)
                path = download_model(model_name, model_file, hf_token=hf_token)
                load_model(path)
            except Exception as e:
                nano_state.status = "error"
                nano_state.error = str(e)

        threading.Thread(target=_download_and_load, daemon=True).start()

        return (
            f"✅ Starting download of **{model_name}** in the background.\n\n"
            "⬇️ You can check progress with `/nano status` — the model will be ready in a few minutes.\n\n"
            "While you wait, you can configure a cloud key at any time:\n"
            "`/keys ANTHROPIC_API_KEY sk-ant-...`"
        )

    elif cmd == "/nano" and args.strip().lower() == "status":
        from .providers.nano import nano_state
        return (
            f"**Nano Model Status**\n"
            f"- Status: `{nano_state.status}`\n"
            f"- Progress: {nano_state.progress_pct}%\n"
            f"- Message: {nano_state.progress_msg or 'N/A'}"
        )

    elif cmd == "/secure":
        from .config import _is_docker_active
        if not _is_docker_active():
            return "❌ **Docker is offline.** Cannot enable sandbox container isolation. Please start Docker and run `/secure` again."
        try:
            append_to_env("SANDBOX_ENABLED", "true")
            return "✅ **Docker is online! Sandbox container isolation enabled.** The CLI will now restart the server to mount the secure sandboxed container."
        except Exception as e:
            return f"❌ Failed to save to disk: {e}"

    # Legacy /config handler
    elif cmd == "/config":
        return "⚠️ The `/config` command has been replaced. Please use `/help` to see the new command menu."

    return "❌ Unknown command. Type `/help` for a list of commands."

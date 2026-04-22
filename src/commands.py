import os
from typing import Optional
from .config import DATA_ENV_PATH, has_active_llm_provider
from .dependencies import llm_router

def append_to_env(key: str, val: str):
    os.makedirs(os.path.dirname(DATA_ENV_PATH), exist_ok=True)
    with open(DATA_ENV_PATH, "a") as f:
        f.write(f"\\n{key}={val}\\n")

def intercept_slash_commands(message: str) -> Optional[str]:
    """
    Intercepts and handles CLI slash commands (/help, /repo, etc.)
    Returns a response string if intercepted, or None to proceed to the LLM.
    """
    msg_strip = message.strip()
    
    if not msg_strip.startswith("/"):
        if not has_active_llm_provider():
            return (
                "🌱 **Welcome to Tendril!** The system is live, but no LLM API keys are configured.\\n\\n"
                "To start chatting, please provide an API key using the `/keys` command:\\n"
                "`/keys ANTHROPIC_API_KEY sk-ant-...`\\n\\n"
                "Type `/help` to see all available commands."
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

**System**
- `/sdlc <strict|simple>` : Toggle strict security and testing gates
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
        return f"✅ **Key saved!** Tendril is operational."

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
            
    # Legacy /config handler
    elif cmd == "/config":
        return "⚠️ The `/config` command has been replaced. Please use `/help` to see the new command menu."

    return "❌ Unknown command. Type `/help` for a list of commands."

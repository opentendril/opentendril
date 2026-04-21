"""
Tendril — Main Application

Self-building AI orchestrator with multi-LLM routing,
file editing, and enterprise-ready security.

Endpoints:
  GET  /             → Redirect to /chat
  GET  /chat         → Chat UI (served from static/index.html)
  POST /chat/message → Send a message (returns HTML fragments via HTMX)
  GET  /chat/stream  → SSE stream for response tokens
  GET  /chat/history → Chat history sidebar
  POST /chat/clear   → Clear conversation
  POST /edit         → Self-building: edit project files via LLM
  GET  /health       → Health check
  POST /v1/chat      → JSON API for programmatic access
  GET  /api/providers → Available LLM providers (JSON)
  GET  /events       → Recent system events
  GET  /health/providers → Provider health status
"""

import html
import logging
import os
import asyncio
import json
import uuid
import dotenv
import time
from typing import List, Optional
from datetime import datetime
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request, Form, HTTPException, Cookie, Response
from fastapi.responses import HTMLResponse, StreamingResponse, RedirectResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from fastapi.middleware.cors import CORSMiddleware

from slowapi import Limiter
from slowapi.util import get_remote_address
from slowapi.errors import RateLimitExceeded

from pydantic import BaseModel, Field
from apscheduler.schedulers.asyncio import AsyncIOScheduler

from .config import validate_config, LOG_DIR, has_active_llm_provider, DATA_ENV_PATH
from .llmrouter import LLMRouter
from .memory import Memory
from .skillsmanager import SkillsManager
from .tendril import Orchestrator
from .editor import FileEditor
from .approval import ApprovalGate
from .dreamer import dream, dreamer_state
from .credits import credit_manager
from .waitlist import router as waitlist_router
from .eventbus import event_bus

# --- Setup ---
os.makedirs(LOG_DIR, exist_ok=True)

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s %(message)s",
    handlers=[
        logging.FileHandler(os.path.join(LOG_DIR, "tendril.log")),
        logging.StreamHandler(),
    ],
)
logger = logging.getLogger(__name__)

# Validate configuration on startup
validate_config()

# Core components
llm_router = LLMRouter()
memory = Memory()
skills_manager = SkillsManager()
editor = FileEditor()
approval = ApprovalGate(auto_approve=True)
orchestrator = Orchestrator(memory, skills_manager, llm_router, editor, approval)

# Wire event bus to Redis (shares connection with Memory)
if memory.redis:
    event_bus.set_redis(memory.redis)

# Async scheduler for dreaming
scheduler = AsyncIOScheduler()
scheduler.add_job(dream, "interval", hours=1, args=[memory, llm_router])

# Rate limiting
limiter = Limiter(key_func=get_remote_address, default_limits=["200/hour"])

app = FastAPI(title="Tendril", version="0.1.0", description="Self-building AI orchestrator")
app.state.limiter = limiter

# Mount static assets
os.makedirs("static", exist_ok=True)
app.mount("/static", StaticFiles(directory="static"), name="static")

# Add CORS for external lead capture (e.g. from Framer/Webflow)
# For production, restrict this to your actual marketing domain (e.g., https://opentendril.com)
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"], # Update this before hard launch!
    allow_credentials=True,
    allow_methods=["GET", "POST", "OPTIONS"],
    allow_headers=["*"],
)

# Include routers
app.include_router(waitlist_router)

@app.on_event("startup")
async def start_scheduler():
    scheduler.start()
    logger.info("⏰ Background dreamer scheduler started.")


@app.exception_handler(RateLimitExceeded)
async def rate_limit_handler(request: Request, exc: RateLimitExceeded):
    logger.warning(f"Rate limit exceeded: {request.client.host}")
    return JSONResponse(
        status_code=429,
        content={"error": "Rate limit exceeded. Please slow down."},
    )


# --- Helper: HTML Escaping ---
def safe(text: str) -> str:
    """Escape text for safe HTML rendering."""
    return html.escape(str(text))


# --- Helper: Slash Command Engine ---
def append_to_env(key: str, val: str):
    import os
    os.makedirs(os.path.dirname(DATA_ENV_PATH), exist_ok=True)
    with open(DATA_ENV_PATH, "a") as f:
        f.write(f"\n{key}={val}\n")

def intercept_slash_commands(message: str) -> Optional[str]:
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
- `/status` : View current system configuration
- `/test` : Run system health checks
- `/restart` : Restart the Tendril container
"""

    elif cmd == "/status":
        from .config import DEFAULT_LLM_PROVIDER, WORKSPACE_ROOT
        active = ", ".join(llm_router.available_providers) if llm_router.available_providers else "None"
        return f"""### System Status
- **Workspace:** `{WORKSPACE_ROOT}`
- **Active Keys:** {active}
- **Default Model:** `{DEFAULT_LLM_PROVIDER}`
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
            
    # Legacy /config handler
    elif cmd == "/config":
        return "⚠️ The `/config` command has been replaced. Please use `/help` to see the new command menu."

    return "❌ Unknown command. Type `/help` for a list of commands."


# --- Root ---
@app.get("/")
async def root():
    return RedirectResponse(url="/chat")


# --- Health & Status ---
@app.get("/health")
async def health():
    return {
        "status": "healthy",
        "version": "0.1.0",
        "skills_loaded": len(skills_manager.skills),
        "llm_providers": llm_router.available_providers,
    }


@app.get("/status")
async def get_status():
    """Detailed system status for the Root Agent."""
    return {
        "kernel": {
            "name": "Tendril",
            "version": "0.1.0",
            "identity": "The Root Agent",
        },
        "inventory": {
            "skills": skills_manager.skills,
            "memory_type": "Chroma (Local)",
            "credit_mode": credit_manager.mode.value,
        },
        "connectivity": {
            "llm_gateways": llm_router.available_providers,
            "database": "Postgres (PGVector)",
            "cache": "Redis",
        },
        "pulse": {
            "last_active": datetime.now().isoformat(),
            "log_path": os.path.join(LOG_DIR, "tendril.log"),
        }
    }


# --- Dreamer Endpoints ---
@app.get("/dreamer/status")
async def get_dreamer_status():
    """Current state of the background Dreamer loop (JSON API)."""
    state = dreamer_state
    return {
        "status": getattr(state, 'last_status', 'idle'),
        "last_run": state.last_run.isoformat() if state.last_run else None,
        "run_count": getattr(state, 'total_runs', 0),
        "insight_count": getattr(state, 'total_insights', 0),
        "last_error": getattr(state, 'last_error', None),
    }


@app.get("/dreamer/widget", response_class=HTMLResponse)
async def get_dreamer_widget():
    """HTML fragment for the sidebar dreamer status widget (polled by HTMX)."""
    try:
        state = dreamer_state
        last_status = getattr(state, 'last_status', 'idle')
        total_runs = getattr(state, 'total_runs', 0)
        total_insights = getattr(state, 'total_insights', 0)
        last_run = getattr(state, 'last_run', None)
        last_error = getattr(state, 'last_error', None)

        if last_status == "idle" and total_runs == 0:
            status_text = "Waiting for first cycle..."
            dot_color = "var(--text-muted)"
        elif last_status == "running":
            status_text = "Dreaming..."
            dot_color = "#8b5cf6"
        elif last_error:
            status_text = f"Error: {str(last_error)[:30]}"
            dot_color = "var(--danger)"
        else:
            status_text = f"{total_insights} insights · {total_runs} runs"
            dot_color = "var(--accent)"

        if last_run and hasattr(last_run, 'strftime'):
            last_run_display = last_run.strftime("%Y-%m-%d %H:%M")
        else:
            last_run_display = "Never"

    except Exception as e:
        logger.warning(f"Dreamer widget error (non-fatal): {e}")
        status_text = "Initializing..."
        dot_color = "var(--text-muted)"
        last_run_display = "Never"

    return f'''
    <div style="display:flex;align-items:center;gap:6px;margin-bottom:4px;">
        <span style="font-size:14px;">💭</span>
        <span style="font-size:11px;font-weight:600;color:var(--text-secondary);">Dreamer</span>
        <span style="width:6px;height:6px;border-radius:50%;background:{dot_color};margin-left:auto;"></span>
    </div>
    <div style="font-size:10px;color:var(--text-muted);">{safe(status_text)}</div>
    <div style="font-size:9px;color:var(--text-muted);margin-top:2px;">Last: {safe(last_run_display)}</div>
    '''


@app.post("/dreamer/trigger")
async def trigger_dream():
    """Manually trigger a dream cycle (for testing or on-demand synthesis)."""
    try:
        await asyncio.to_thread(dream, memory, llm_router)
        return {"status": "completed", "insight_count": dreamer_state.insight_count}
    except Exception as e:
        logger.error(f"Manual dream trigger failed: {e}")
        return {"status": "error", "error": str(e)}


# --- API Endpoints ---
@app.get("/api/providers")
async def get_providers():
    """Return available LLM providers for the UI selector."""
    info = llm_router.get_provider_info()
    return [
        {"value": name, "label": f"{name.capitalize()} ({info[name]['models']['standard']})"}
        for name in sorted(info.keys())
        if info[name]["has_key"] or name == "local"
    ]


@app.get("/events/{session_id}")
async def get_session_events(session_id: str, limit: int = 50):
    """Get recent events for a session (for debugging and observability)."""
    return event_bus.get_session_events(session_id, limit=min(limit, 200))


@app.get("/events")
async def get_recent_events(limit: int = 20):
    """Get most recent events across all sessions."""
    return event_bus.get_recent(limit=min(limit, 100))


@app.get("/health/providers")
async def get_provider_health():
    """Get health status of all LLM providers (cooldowns, error rates)."""
    return orchestrator.failover.get_provider_health()


# --- Chat UI ---
# Frontend served from static/index.html, static/styles.css, static/app.js
# Provider options loaded dynamically via GET /api/providers

@app.get("/chat", response_class=HTMLResponse)
async def get_chat_ui(request: Request, response: Response):
    # Ensure a persistent session cookie exists
    session_id = request.cookies.get("tendril_session")
    if not session_id:
        session_id = f"sess-{uuid.uuid4().hex[:12]}"
        response.set_cookie(
            key="tendril_session",
            value=session_id,
            max_age=60 * 60 * 24 * 30,  # 30 days
            httponly=True,
            samesite="lax",
        )
    logger.info(f"Chat UI session: {session_id}")

    # Serve static frontend
    html_path = os.path.join(os.path.dirname(__file__), "..", "static", "index.html")
    with open(html_path, "r", encoding="utf-8") as f:
        html_content = f.read()
    response.body = html_content.encode()
    response.status_code = 200
    response.media_type = "text/html"
    return response


@app.get("/chat/history", response_class=HTMLResponse)
async def get_history(tendril_session: str = Cookie(default="default")):
    history = memory.get_convo(tendril_session)
    if not history:
        return '<p style="color: var(--text-muted); text-align: center; font-size: 12px; font-style: italic; margin-top: 40px;">No conversations yet</p>'

    items = ""
    user_msgs = [m for m in history if m["role"] == "user"]
    for msg in reversed(user_msgs):
        content = msg["content"][:40] + "..." if len(msg["content"]) > 40 else msg["content"]
        items += f'''<div class="history-item">
            <p>{safe(content)}</p>
            <div class="meta">Recent</div>
        </div>'''
    return items


@app.post("/chat/message", response_class=HTMLResponse)
async def post_message(message: str = Form(...), provider: str = Form("default"), tendril_session: str = Cookie(default="default")):
    import time
    # NOTE: Do NOT store user message here — the stream endpoint handles memory storage
    # to avoid duplicates in the conversation history.
    escaped = safe(message)
    provider_param = safe(provider)
    
    # Unique ID for this specific SSE stream connection (not the user session)
    stream_id = f"chat-{int(time.time()*1000)}"

    # Return user bubble + SSE stream container
    return f'''<div class="msg-row user">
        <div class="msg-bubble user">{escaped}</div>
    </div>
    <div class="msg-row assistant"
         id="{stream_id}"
         hx-ext="sse"
         sse-connect="/chat/stream?message={escaped}&provider={provider_param}&session={stream_id}&sid={safe(tendril_session)}"
         sse-swap="message">
        <div class="msg-bubble assistant">
            <div class="thinking">
                <div class="thinking-dot"></div>
                <span>Thinking...</span>
            </div>
        </div>
    </div>'''


@app.get("/chat/stream")
async def stream_chat(message: str, provider: str = "default", session: str = "", sid: str = "default"):
    async def event_generator():
        try:
            # Check Slash Command interception
            intercepted = intercept_slash_commands(message)
            if intercepted:
                response_text = intercepted
            else:
                prov = None if provider == "default" else provider
                response_text = await asyncio.to_thread(
                    orchestrator.process, sid, message, provider=prov
                )
            
            # Store both user message and assistant response here (single source of truth)
            memory.store_convo(sid, "user", message)
            memory.store_convo(sid, "assistant", response_text)

            # Format response with full Markdown rendering
            escaped_text = safe(response_text)
            
            import re
            def apply_markdown(text: str) -> str:
                """Convert Markdown to safe HTML."""
                # Code blocks first (before other substitutions)
                text = re.sub(r'```(?:\w+)?\n(.*?)\n?```', r'<pre><code>\1</code></pre>', text, flags=re.DOTALL)
                # Inline code
                text = re.sub(r'`([^`]+)`', r'<code>\1</code>', text)
                # Bold
                text = re.sub(r'\*\*(.*?)\*\*', r'<b>\1</b>', text)
                # Italic
                text = re.sub(r'\*(.*?)\*', r'<em>\1</em>', text)
                # ATX headings (## Heading)
                text = re.sub(r'^### (.+)$', r'<h3>\1</h3>', text, flags=re.MULTILINE)
                text = re.sub(r'^## (.+)$', r'<h2>\1</h2>', text, flags=re.MULTILINE)
                text = re.sub(r'^# (.+)$', r'<h1>\1</h1>', text, flags=re.MULTILINE)
                # Unordered lists (- item or * item)
                text = re.sub(r'^[-*] (.+)$', r'<li>\1</li>', text, flags=re.MULTILINE)
                text = re.sub(r'(<li>.*?</li>\n?)+', r'<ul>\g<0></ul>', text, flags=re.DOTALL)
                # Horizontal rules
                text = re.sub(r'^---+$', r'<hr>', text, flags=re.MULTILINE)
                # Paragraphs
                text = text.replace("\n\n", "</p><p>")
                text = text.replace("\n", "<br>")
                return f"<p>{text}</p>"
            
            formatted = apply_markdown(escaped_text)

            # Stream word-by-word for UX
            words = response_text.split(" ")
            accumulated = ""
            for i, word in enumerate(words):
                accumulated += word + (" " if i < len(words) - 1 else "")
                
                current_formatted = apply_markdown(safe(accumulated))
                
                # If this is the final word, use the fully-rendered final version
                if i == len(words) - 1 and session:
                    yield f'event: message\ndata: <div hx-swap-oob="outerHTML:#{session}" class="msg-row assistant"><div class="msg-bubble assistant">{formatted}</div></div>\n\n'
                else:
                    yield f'event: message\ndata: <div class="msg-bubble assistant">{current_formatted}</div>\n\n'
                    
                await asyncio.sleep(0.01)

        except Exception as e:
            logger.error(f"Stream error: {e}")
            display = f'<span class="error-msg">Error: {safe(str(e))}</span>'
            if session:
                yield f'event: message\ndata: <div hx-swap-oob="outerHTML:#{session}" class="msg-row assistant"><div class="msg-bubble assistant">{display}</div></div>\n\n'
            else:
                yield f'event: message\ndata: <div class="msg-bubble assistant">{display}</div>\n\n'

    return StreamingResponse(event_generator(), media_type="text/event-stream")


@app.post("/chat/clear", response_class=HTMLResponse)
async def clear_chat(tendril_session: str = Cookie(default="default")):
    try:
        memory.redis.delete(f"convo:{tendril_session}")
    except Exception:
        pass
    return '<p style="color: var(--text-muted); text-align: center; font-size: 12px; font-style: italic; margin-top: 40px;">Conversation cleared</p>'


# --- Edit Endpoint (Self-Building) ---
class EditRequest(BaseModel):
    file: str = Field(..., description="File path relative to project src/")
    instruction: str = Field(..., description="What to do to the file")
    provider: str = Field(default="default", description="LLM provider to use")


@app.post("/edit")
async def edit_endpoint(req: EditRequest):
    """
    Self-building endpoint: instruct an LLM to edit a project file.

    1. Reads the current file content
    2. Sends it + instruction to the LLM
    3. Returns the proposed diff
    4. Applies the change (auto-approved in dev mode)
    """
    try:
        # Credit Check
        if not credit_manager.validate_request():
            raise HTTPException(status_code=402, detail="Insufficient credits.")
            
        # Read current file
        try:
            current_content = editor.read(req.file)
        except FileNotFoundError:
            current_content = ""

        # Ask LLM to generate the edit
        prov = None if req.provider == "default" else req.provider
        llm = llm_router.get(provider=prov, tier="standard")

        prompt = f"""You are editing a file in the Tendril project.

File: {req.file}
Current content:
```
{current_content}
```

Instruction: {req.instruction}

Respond with ONLY the complete new file content. No explanations, no markdown fences."""

        resp = await asyncio.to_thread(llm.invoke, prompt)
        new_content = resp.content.strip()

        # Remove markdown code fences if the LLM added them
        if new_content.startswith("```"):
            lines = new_content.split("\n")
            new_content = "\n".join(lines[1:-1]) if lines[-1].strip() == "```" else "\n".join(lines[1:])

        # Generate diff
        diff = editor.generate_diff(req.file, new_content)

        # 1. Apply Change Temporarily for SDLC Testing
        result = editor.write(req.file, new_content)
        
        # 2. CI / Automated Tests (The SDLC Loop)
        check_msg = "Skipped syntax & CI checks (not a .py file)"
        test_passed = True
        
        if req.file.endswith(".py"):
            from .config import STRICT_LINTING
            lint_cmd = f"ruff check {req.file}" if STRICT_LINTING else f"ruff check --select E,F {req.file}"
            
            # Phase A: Linting
            lint_output = await orchestrator.tester.run_command(lint_cmd, safe=True)
            if "❌" in lint_output and "Command failed" in lint_output:
                test_passed = False
                check_msg = f"Linting failed:\n{lint_output}"
            else:
                # Phase B: Compile & Test
                test_cmd = f"python -m py_compile {req.file} && pytest tests/"
                test_output = await orchestrator.tester.run_command(test_cmd, safe=True)
                
                if "❌" in test_output:
                    test_passed = False
                    check_msg = f"Automated tests failed:\n{test_output}"
                else:
                    check_msg = "✅ All linting and CI tests passed."

        # 3. Auto-Revert on Failure
        if not test_passed:
            editor.write(req.file, current_content)  # Rollback instantly
            return {
                "status": "rejected",
                "file": req.file,
                "diff": diff,
                "error": check_msg,
                "approval": "auto_reverted"
            }

        # 4. Code Review (Human Approval)
        approval_req = await approval.request_approval(
            action="file_edit",
            description=f"Edit {req.file}: {req.instruction[:100]}\n\nCI Status: {check_msg}",
            diff=diff,
        )

        if approval_req.status.value in ("approved", "auto_approved"):
            # 5. Document & Commit
            commit_msg = f"tendril(/edit): Updated {req.file} - {req.instruction[:120]}"
            git_result = orchestrator.git.commit_changes(commit_msg)

            return {
                "status": "applied",
                "file": req.file,
                "action": result["action"],
                "diff": diff,
                "test": check_msg,
                "commit": git_result,
                "approval": approval_req.status.value,
            }
        else:
            # Human rejected: Rollback
            editor.write(req.file, current_content)
            return {
                "status": "rejected",
                "file": req.file,
                "diff": diff,
                "approval": approval_req.status.value,
            }

    except PermissionError as e:
        raise HTTPException(status_code=403, detail=str(e))
    except Exception as e:
        logger.error(f"Edit error: {e}")
        raise HTTPException(status_code=500, detail=f"Edit failed: {str(e)}")


# --- JSON API (Programmatic Access) ---
class ChatMessage(BaseModel):
    role: str
    content: str

class ChatCompletionRequest(BaseModel):
    model: str
    messages: List[ChatMessage]
    stream: Optional[bool] = False
    temperature: Optional[float] = None

@app.post("/v1/chat/completions")
async def openai_chat_completions(req: ChatCompletionRequest):
    """
    OpenAI-compatible chat completions endpoint.
    Allows 3rd party TUIs (Aider, Crush, OpenCode) to connect seamlessly.
    """
    logger.info(f"OpenAI API chat: model={req.model} stream={req.stream}")
    try:
        # 1. Map requested model to our internal providers
        provider = "default"
        model_name = req.model.lower()
        if "gpt" in model_name or "o1" in model_name:
            provider = "openai"
        elif "claude" in model_name:
            provider = "anthropic"
        elif "grok" in model_name or "xai" in model_name:
            provider = "grok"

        # 2. Extract user message
        user_msg = ""
        for msg in reversed(req.messages):
            if msg.role == "user":
                user_msg = msg.content
                break
                
        if not user_msg:
            raise HTTPException(status_code=400, detail="No user message found")
            
        session_id = f"ext-{uuid.uuid4().hex[:8]}"

        if req.stream:
            async def event_generator():
                queue = asyncio.Queue()
                loop = asyncio.get_running_loop()
                
                def subscriber(event):
                    if event.session_id == session_id:
                        try:
                            loop.call_soon_threadsafe(queue.put_nowait, event)
                        except Exception:
                            pass
                            
                event_bus.subscribe(subscriber)
                
                try:
                    # Start orchestrator in background
                    task = asyncio.create_task(
                        asyncio.to_thread(orchestrator.process, session_id, user_msg, provider=provider)
                    )

                    while True:
                        get_task = asyncio.create_task(queue.get())
                        done, pending = await asyncio.wait(
                            [get_task, task],
                            return_when=asyncio.FIRST_COMPLETED
                        )
                        
                        if get_task in done:
                            event = get_task.result()
                            chunk_content = ""
                            
                            if event.event_type == "tool.start":
                                name = event.data.get('name', '')
                                args_str = json.dumps(event.data.get('args', {}))
                                chunk_content = f"\n\n🛠️ **Running Tool**: `{name}` with args: `{args_str}`...\n"
                            elif event.event_type == "tool.end":
                                name = event.data.get('name', '')
                                chunk_content = f"✅ **Tool `{name}` completed.**\n\n"
                            elif event.event_type == "request.end":
                                content = event.data.get('content', '')
                                chunk_content = f"\n{content}"
                            elif event.event_type == "request.error":
                                chunk_content = f"\n\n❌ **Error**: {event.data.get('error', '')}\n"
                                
                            if chunk_content:
                                chunk = {
                                    "id": f"chatcmpl-{session_id}",
                                    "object": "chat.completion.chunk",
                                    "created": int(time.time()),
                                    "model": req.model,
                                    "choices": [{"index": 0, "delta": {"content": chunk_content}, "finish_reason": None}]
                                }
                                yield f"data: {json.dumps(chunk)}\n\n"
                                
                            if event.event_type in ["request.end", "request.error"]:
                                break
                                
                        if task in done and queue.empty():
                            if task.exception():
                                logger.error(f"OpenAI stream task error: {task.exception()}")
                                error_chunk = {
                                    "id": f"chatcmpl-{session_id}",
                                    "object": "chat.completion.chunk",
                                    "created": int(time.time()),
                                    "model": req.model,
                                    "choices": [{"index": 0, "delta": {"content": f"\n\n**Error:** {str(task.exception())}"}, "finish_reason": "stop"}]
                                }
                                yield f"data: {json.dumps(error_chunk)}\n\n"
                            break
                            
                    # Final chunk
                    final_chunk = {
                        "id": f"chatcmpl-{session_id}",
                        "object": "chat.completion.chunk",
                        "created": int(time.time()),
                        "model": req.model,
                        "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]
                    }
                    yield f"data: {json.dumps(final_chunk)}\n\n"
                    yield "data: [DONE]\n\n"
                    
                finally:
                    event_bus.unsubscribe(subscriber)
                    memory.store_convo(session_id, "user", user_msg)
                    # Note: We rely on orchestrator to store assistant convo

            return StreamingResponse(event_generator(), media_type="text/event-stream")
            
        else:
            # Non-streaming response
            response_text = await asyncio.to_thread(
                orchestrator.process, session_id, user_msg, provider=provider
            )
            memory.store_convo(session_id, "user", user_msg)
            memory.store_convo(session_id, "assistant", response_text)
            
            return {
                "id": f"chatcmpl-{session_id}",
                "object": "chat.completion",
                "created": int(time.time()),
                "model": req.model,
                "choices": [
                    {
                        "index": 0,
                        "message": {
                            "role": "assistant",
                            "content": response_text
                        },
                        "finish_reason": "stop"
                    }
                ],
                "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
            }

    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"API error: {e}")
        raise HTTPException(status_code=500, detail=str(e))


class ChatRequest(BaseModel):
    session_id: str = Field(default="default", max_length=64)
    message: str = Field(..., max_length=4000)
    provider: str = Field(default="default", description="LLM provider")


@app.post("/v1/chat")
async def chat_api(req: ChatRequest):
    logger.info(f"API chat: session={req.session_id} msg='{req.message[:100]}'")
    try:
        intercepted = intercept_slash_commands(req.message)
        if intercepted:
            response = intercepted
        else:
            prov = None if req.provider == "default" else req.provider
            response = await asyncio.to_thread(
                orchestrator.process, req.session_id, req.message, provider=prov
            )
        memory.store_convo(req.session_id, "user", req.message)
        memory.store_convo(req.session_id, "assistant", response)
        return {"response": response, "provider": req.provider}
    except Exception as e:
        logger.error(f"API error: {e}")
        raise HTTPException(status_code=500, detail="Internal error processing request.")


@app.get("/settings", response_class=HTMLResponse)
async def get_settings():
    return f'''
    <div class="settings-panel">
        <h2>Kernel Configuration</h2>
        <form hx-post="/settings/save" hx-target="#chat-messages" hx-swap="innerHTML">
            <div class="form-group">
                <label>TENDRIL_WORKSPACE_ROOT</label>
                <input type="text" name="workspace" class="form-input" value="{safe(os.getenv('TENDRIL_WORKSPACE_ROOT', '/app'))}" placeholder="/app">
            </div>
            <div class="form-group">
                <label>GITHUB_TOKEN</label>
                <input type="password" name="github_token" class="form-input" value="{safe(os.getenv('GITHUB_TOKEN', ''))}" placeholder="ghp_...">
            </div>
            <div class="form-group">
                <label>GROK_API_KEY</label>
                <input type="password" name="grok_key" class="form-input" value="{safe(os.getenv('GROK_API_KEY', ''))}" placeholder="xai-...">
            </div>
            <div id="chat-messages" class="messages">
            <div class="welcome">
                <img src="/static/favicon.png" alt="Tendril Logo" style="height: 80px; width: 80px; margin-bottom: 24px; opacity: 1;">
                <h3>I am the Root Agent.</h3>
                <p>Turn your frustrations into skills via <code style="background:var(--bg-tertiary);padding:2px 6px;border-radius:4px;border:1px solid var(--border)">/edit</code>.</p>
                <p style="margin-top:16px;font-size:13px;color:var(--text-muted);">Type <code style="background:var(--bg-tertiary);padding:2px 5px;border-radius:4px;">/help</code> to see all available commands, or just say hello.</p>
            </div>
            </div>
            <div class="settings-actions">
                <button type="button" class="btn-secondary" style="width: auto;" onclick="window.location.reload()">Cancel</button>
                <button type="submit" class="btn-primary">Save Settings</button>
            </div>
        </form>
    </div>
    '''

@app.post("/settings/save", response_class=HTMLResponse)
async def save_settings(workspace: str = Form(""), grok_key: str = Form(""), github_token: str = Form("")):
    env_file = ".env"
    
    # Touch .env if missing
    if not os.path.exists(env_file):
        open(env_file, 'a').close()
        
    dotenv.set_key(env_file, "TENDRIL_WORKSPACE_ROOT", workspace)
    dotenv.set_key(env_file, "GROK_API_KEY", grok_key)
    dotenv.set_key(env_file, "GITHUB_TOKEN", github_token)
    
    os.environ["TENDRIL_WORKSPACE_ROOT"] = workspace
    os.environ["GROK_API_KEY"] = grok_key
    os.environ["GITHUB_TOKEN"] = github_token
    
    return '''
    <div class="settings-panel" style="text-align: center;">
        <h2 style="color: var(--accent);">✅ Configuration Saved</h2>
        <p style="color: var(--text-secondary); font-size: 13px; margin-bottom: 24px;">Root variables have been persisted and loaded into the active kernel memory.</p>
        <button type="button" class="btn-primary" onclick="window.location.reload()">Return to Kernel</button>
    </div>
    '''

@app.get("/v1/credits", response_class=HTMLResponse)
async def get_credits_ui():
    """Returns HTML snippet for the credits widget based on real balance."""
    balance = credit_manager.get_balance()
    is_local = credit_manager.mode.value == "local"
    
    label = "Local Compute" if is_local else "Cloud Credits"
    
    return f'''<div>
                <div class="credits-val">{safe(balance)}</div>
                <div class="credits-label">{safe(label)}</div>
              </div>'''


# --- Approval Endpoints ---
@app.get("/approvals/pending")
async def get_pending_approvals():
    return {"pending": approval.get_pending()}


@app.post("/approvals/{request_id}/approve")
async def approve_request(request_id: str):
    if approval.approve(request_id):
        return {"status": "approved", "request_id": request_id}
    raise HTTPException(status_code=404, detail="Request not found or already resolved")


@app.post("/approvals/{request_id}/reject")
async def reject_request(request_id: str):
    if approval.reject(request_id):
        return {"status": "rejected", "request_id": request_id}
    raise HTTPException(status_code=404, detail="Request not found or already resolved")


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8080)

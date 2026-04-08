"""
Tendril — Main Application

Self-building AI orchestrator with multi-LLM routing,
file editing, and enterprise-ready security.

Endpoints:
  GET  /           → Redirect to /chat
  GET  /chat       → Chat UI (SSE streaming, history, LLM selector)
  POST /chat/message → Send a message (returns HTML fragments via HTMX)
  GET  /chat/stream  → SSE stream for response tokens
  GET  /chat/history → Chat history sidebar
  POST /chat/clear   → Clear conversation
  POST /edit         → Self-building: edit project files via LLM
  GET  /health       → Health check
  POST /v1/chat      → JSON API for programmatic access
"""

import html
import logging
import os
import asyncio
import json
import dotenv
from datetime import datetime
from contextlib import asynccontextmanager

from fastapi import FastAPI, Request, Form, HTTPException
from fastapi.responses import HTMLResponse, StreamingResponse, RedirectResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from fastapi.middleware.cors import CORSMiddleware

from slowapi import Limiter
from slowapi.util import get_remote_address
from slowapi.errors import RateLimitExceeded

from pydantic import BaseModel, Field
from apscheduler.schedulers.asyncio import AsyncIOScheduler

from .config import validate_config, LOG_DIR
from .llmrouter import LLMRouter
from .memory import Memory
from .skillsmanager import SkillsManager
from .tendril import Orchestrator
from .editor import FileEditor
from .approval import ApprovalGate
from .dreamer import dream
from .credits import credit_manager
from .waitlist import router as waitlist_router

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

# Async scheduler for dreaming
scheduler = AsyncIOScheduler()
scheduler.add_job(dream, "interval", hours=1, args=[memory, llm_router])

# Rate limiting
limiter = Limiter(key_func=get_remote_address, default_limits=["200/hour"])

app = FastAPI(title="Tendril", version="0.1.0", description="Self-building AI orchestrator")
app.state.limiter = limiter

# Mount static assets
import os
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

app.state.limiter = limiter

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
            "uptime": "Calculating...", # Placeholder for future uptime logic
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


# --- Chat UI ---
CHAT_HTML = """<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Tendril | The Root Agent</title>
    <meta name="description" content="Tendril — The Root Agent. OpenClaw's successor that fixes itself while it works.">
    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700;800&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <script src="https://unpkg.com/htmx.org/dist/ext/sse.js"></script>
    <link rel="icon" type="image/png" href="/static/tendril-logo.png">
    <style>
        *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

        :root {
            --bg-primary: #09090b;
            --bg-secondary: #18181b;
            --bg-tertiary: #27272a;
            --border: #3f3f46;
            --border-hover: #52525b;
            --text-primary: #fafafa;
            --text-secondary: #a1a1aa;
            --text-muted: #71717a;
            --accent: #10b981;
            --accent-dim: rgba(16, 185, 129, 0.15);
            --accent-glow: rgba(16, 185, 129, 0.3);
            --danger: #ef4444; /* Lobster red */
            --accent-secondary: #ef4444; /* OpenClaw legacy */
            --radius: 16px;
            --radius-sm: 10px;
            --font-sans: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif;
            --font-mono: 'JetBrains Mono', 'Fira Code', monospace;
        }

        body {
            font-family: var(--font-sans);
            background: var(--bg-primary);
            color: var(--text-primary);
            height: 100vh;
            display: flex;
            overflow: hidden;
            -webkit-font-smoothing: antialiased;
        }

        /* Sidebar */
        .sidebar {
            width: 300px;
            background: var(--bg-secondary);
            border-right: 1px solid var(--border);
            display: flex;
            flex-direction: column;
        }

        .sidebar-header {
            padding: 20px 24px;
            border-bottom: 1px solid var(--border);
            display: flex;
            justify-content: space-between;
            align-items: center;
        }

        .sidebar-header h2 {
            font-size: 15px;
            font-weight: 700;
            letter-spacing: -0.02em;
        }

        .btn-text {
            background: none;
            border: none;
            color: var(--text-muted);
            font-size: 11px;
            font-weight: 600;
            text-transform: uppercase;
            letter-spacing: 0.08em;
            cursor: pointer;
            transition: color 0.2s;
            font-family: var(--font-sans);
        }

        .btn-text:hover { color: var(--text-primary); }

        .sidebar-content {
            flex: 1;
            overflow-y: auto;
            padding: 12px;
            scrollbar-width: none;
        }

        .sidebar-content::-webkit-scrollbar { display: none; }

        .history-item {
            padding: 10px 14px;
            border-radius: var(--radius-sm);
            cursor: pointer;
            transition: all 0.15s;
            border: 1px solid transparent;
            margin-bottom: 4px;
        }

        .history-item:hover {
            background: var(--bg-tertiary);
            border-color: var(--border);
        }

        .history-item p { font-size: 13px; color: var(--text-secondary); }
        .history-item .meta { font-size: 10px; color: var(--text-muted); margin-top: 4px; text-transform: uppercase; letter-spacing: 0.05em; }

        .sidebar-footer {
            padding: 16px;
            border-top: 1px solid var(--border);
        }

        .btn-secondary {
            display: block;
            width: 100%;
            padding: 10px;
            background: var(--bg-tertiary);
            border: 1px solid var(--border);
            border-radius: var(--radius-sm);
            color: var(--text-secondary);
            font-size: 11px;
            font-weight: 600;
            text-transform: uppercase;
            letter-spacing: 0.06em;
            text-align: center;
            text-decoration: none;
            cursor: pointer;
            transition: all 0.2s;
            font-family: var(--font-sans);
        }

        .btn-secondary:hover {
            background: var(--border);
            color: var(--text-primary);
        }

        .credits-widget {
            margin-top: 12px;
            padding: 12px;
            background: linear-gradient(135deg, rgba(234, 179, 8, 0.1), rgba(234, 179, 8, 0.02));
            border: 1px solid rgba(234, 179, 8, 0.2);
            border-radius: var(--radius-sm);
            display: flex;
            align-items: center;
            justify-content: space-between;
        }

        .credits-widget.local {
            background: linear-gradient(135deg, rgba(16, 185, 129, 0.1), rgba(16, 185, 129, 0.02));
            border-color: rgba(16, 185, 129, 0.2);
        }

        .credits-val {
            font-family: var(--font-mono);
            font-weight: 700;
            font-size: 13px;
        }

        .credits-widget.local .credits-val { color: var(--accent); }
        .credits-widget:not(.local) .credits-val { color: #eab308; } /* Gold color for hosted */

        .credits-label {
            font-size: 10px;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            color: var(--text-muted);
        }

        /* Main Chat */
        .main {
            flex: 1;
            display: flex;
            flex-direction: column;
        }

        .topbar {
            height: 56px;
            border-bottom: 1px solid var(--border);
            display: flex;
            align-items: center;
            justify-content: space-between;
            padding: 0 24px;
            background: rgba(9, 9, 11, 0.8);
            backdrop-filter: blur(12px);
            z-index: 10;
        }

        .topbar-left {
            display: flex;
            align-items: center;
            gap: 10px;
        }

        .status-dot {
            width: 8px;
            height: 8px;
            background: var(--accent);
            border-radius: 50%;
            box-shadow: 0 0 8px var(--accent-glow);
            animation: pulse 2s ease-in-out infinite;
        }

        @keyframes pulse {
            0%, 100% { opacity: 1; box-shadow: 0 0 8px var(--accent-glow); }
            50% { opacity: 0.6; box-shadow: 0 0 16px var(--accent-glow); }
        }

        .topbar h1 {
            font-size: 15px;
            font-weight: 700;
            letter-spacing: -0.02em;
        }

        .provider-select {
            background: var(--bg-secondary);
            border: 1px solid var(--border);
            border-radius: 8px;
            color: var(--text-secondary);
            font-size: 12px;
            font-family: var(--font-mono);
            padding: 6px 12px;
            cursor: pointer;
            outline: none;
            transition: border-color 0.2s;
        }

        .provider-select:hover,
        .provider-select:focus {
            border-color: var(--accent);
        }

        /* Messages */
        .messages {
            flex: 1;
            overflow-y: auto;
            padding: 32px 24px;
            scrollbar-width: none;
        }

        .messages::-webkit-scrollbar { display: none; }

        .welcome {
            text-align: center;
            padding: 80px 20px;
            opacity: 0.25;
        }

        .welcome h3 {
            font-size: 32px;
            font-weight: 800;
            letter-spacing: -0.04em;
            margin-bottom: 12px;
        }

        .welcome p {
            font-family: var(--font-mono);
            font-size: 12px;
            color: var(--text-muted);
        }

        .msg-row {
            max-width: 720px;
            margin: 0 auto 16px;
            display: flex;
            animation: fadeIn 0.3s ease;
        }

        @keyframes fadeIn {
            from { opacity: 0; transform: translateY(8px); }
            to { opacity: 1; transform: translateY(0); }
        }

        .msg-row.user { justify-content: flex-end; }
        .msg-row.assistant { justify-content: flex-start; }

        .msg-bubble {
            padding: 12px 18px;
            border-radius: var(--radius);
            font-size: 14px;
            line-height: 1.6;
            max-width: 85%;
            word-wrap: break-word;
        }

        .msg-bubble.user {
            background: var(--bg-tertiary);
            border: 1px solid var(--border);
            border-bottom-right-radius: 4px;
        }

        .msg-bubble.assistant {
            background: var(--bg-secondary);
            border: 1px solid var(--border);
            border-bottom-left-radius: 4px;
            color: var(--text-secondary);
        }

        .msg-bubble pre {
            background: var(--bg-primary);
            border: 1px solid var(--border);
            border-radius: 8px;
            padding: 12px 16px;
            margin: 8px 0;
            overflow-x: auto;
            font-family: var(--font-mono);
            font-size: 13px;
        }

        .msg-bubble code {
            font-family: var(--font-mono);
            font-size: 13px;
            background: var(--bg-primary);
            padding: 2px 6px;
            border-radius: 4px;
        }

        .thinking {
            display: flex;
            align-items: center;
            gap: 8px;
            color: var(--accent);
            font-family: var(--font-mono);
            font-size: 12px;
        }

        .thinking-dot {
            width: 6px;
            height: 6px;
            background: var(--accent);
            border-radius: 50%;
            animation: blink 1s ease-in-out infinite;
        }

        @keyframes blink {
            0%, 100% { opacity: 1; }
            50% { opacity: 0.2; }
        }

        .error-msg {
            color: var(--danger);
            font-size: 13px;
            font-family: var(--font-mono);
        }

        /* Input Area */
        .input-area {
            padding: 20px 24px 24px;
            background: linear-gradient(to top, var(--bg-primary) 60%, transparent);
        }

        .input-wrapper {
            max-width: 720px;
            margin: 0 auto;
            position: relative;
        }

        .input-form {
            display: flex;
            gap: 8px;
        }

        .chat-input {
            flex: 1;
            background: var(--bg-secondary);
            border: 1px solid var(--border);
            border-radius: 14px;
            padding: 14px 20px;
            color: var(--text-primary);
            font-size: 14px;
            font-family: var(--font-sans);
            outline: none;
            transition: border-color 0.2s, box-shadow 0.2s;
        }

        .chat-input::placeholder { color: var(--text-muted); }
        .chat-input:focus {
            border-color: var(--accent);
            box-shadow: 0 0 0 3px var(--accent-dim);
        }

        .btn-send {
            background: var(--accent-secondary);
            border: none;
            border-radius: 12px;
            padding: 14px 24px;
            color: #fff;
            font-size: 13px;
            font-weight: 700;
            font-family: var(--font-sans);
            cursor: pointer;
            transition: all 0.15s;
        }

        .btn-send:hover { filter: brightness(1.1); }
        .btn-send:active { transform: scale(0.97); }

        .powered-by {
            text-align: center;
            margin-top: 12px;
            font-size: 10px;
            color: var(--text-muted);
            text-transform: uppercase;
            letter-spacing: 0.15em;
        }

        /* Settings Panel */
        .settings-panel {
            background: rgba(24, 24, 27, 0.8);
            backdrop-filter: blur(12px);
            border: 1px solid var(--border);
            border-radius: var(--radius);
            padding: 32px;
            max-width: 500px;
            margin: 40px auto;
            box-shadow: 0 4px 24px rgba(0, 0, 0, 0.3);
            animation: fadeIn 0.3s ease;
        }
        .settings-panel h2 {
            margin-bottom: 24px;
            font-size: 18px;
            font-weight: 700;
        }
        .form-group { margin-bottom: 16px; text-align: left; }
        .form-group label {
            display: block;
            font-size: 11px;
            font-weight: 600;
            color: var(--text-muted);
            margin-bottom: 8px;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            font-family: var(--font-mono);
        }
        .form-input {
            width: 100%;
            padding: 12px 14px;
            background: var(--bg-tertiary);
            border: 1px solid var(--border);
            border-radius: 8px;
            color: var(--text-primary);
            outline: none;
            font-family: var(--font-mono);
            font-size: 13px;
            transition: border-color 0.2s;
        }
        .form-input:focus { border-color: var(--accent); }
        .settings-actions {
            display: flex;
            justify-content: flex-end;
            gap: 12px;
            margin-top: 32px;
        }
        .btn-primary {
            background: var(--accent);
            color: #fff;
            padding: 10px 20px;
            border-radius: 8px;
            border: none;
            font-size: 13px;
            font-weight: 600;
            font-family: var(--font-sans);
            cursor: pointer;
            transition: all 0.15s;
        }
        .btn-primary:hover { filter: brightness(1.1); }
        
        /* Responsive */
        @media (max-width: 768px) {
            .sidebar { display: none; }
            .messages { padding: 16px; }
        }
    </style>
</head>
<body>
    <aside class="sidebar">
        <div class="sidebar-header">
            <h2>History</h2>
            <button class="btn-text"
                    hx-post="/chat/clear"
                    hx-target="#chat-history"
                    hx-swap="innerHTML">Clear</button>
        </div>
        <div id="chat-history" class="sidebar-content"
             hx-get="/chat/history" hx-trigger="load">
        </div>
        <div class="sidebar-footer">
            <a href="#" class="btn-secondary" style="margin-bottom: 8px; display: block; text-align: center;" 
               hx-get="/settings" hx-target="#chat-messages" hx-swap="innerHTML">⚙️ Configuration</a>
            <a href="/health" class="btn-secondary" target="_blank">System Status</a>
            <div class="credits-widget local" hx-get="/v1/credits" hx-trigger="load">
                <div>
                    <div class="credits-val">∞</div>
                    <div class="credits-label">Local Credits</div>
                </div>
            </div>
        </div>
    </aside>

    <main class="main">
        <header class="topbar">
            <div class="topbar-left">
                <div class="status-dot"></div>
                <img src="/static/tendril-logo.png" alt="Tendril Logo" style="height: 24px; width: 24px;">
                <h1>Tendril</h1>
            </div>
            <select id="provider-select" class="provider-select">
                <option value="default">Auto (Default)</option>
                PROVIDER_OPTIONS_PLACEHOLDER
            </select>
        </header>

        <div id="chat-messages" class="messages">
            <div class="welcome">
                <img src="/static/tendril-logo.png" alt="Tendril Logo" style="height: 80px; width: 80px; margin-bottom: 24px; opacity: 1;">
                <h3>I am the Root Agent.</h3>
                <p>Turn your frustrations into skills via <code style="background:var(--bg-tertiary);padding:2px 6px;border-radius:4px;border:1px solid var(--border)">/edit</code>.</p>
            </div>
        </div>

        <div class="input-area">
            <div class="input-wrapper">
                <form class="input-form"
                      hx-post="/chat/message"
                      hx-target="#chat-messages"
                      hx-swap="beforeend"
                      hx-on::after-request="this.reset(); document.getElementById('chat-messages').scrollTo({top: document.getElementById('chat-messages').scrollHeight, behavior: 'smooth'});">
                    <input type="hidden" id="provider-hidden" name="provider" value="default">
                    <input name="message" type="text" placeholder="Describe what you want to build..."
                           required autocomplete="off" class="chat-input" id="chat-input">
                    <button type="submit" class="btn-send">Send</button>
                </form>
                <p class="powered-by">Tendril v0.1 — The agent that builds agents.</p>
            </div>
        </div>
    </main>

    <script>
        // Sync provider selector with hidden form field
        const sel = document.getElementById('provider-select');
        const hidden = document.getElementById('provider-hidden');
        sel.addEventListener('change', () => { hidden.value = sel.value; });

        // Auto-scroll on new messages
        const observer = new MutationObserver(() => {
            const container = document.getElementById('chat-messages');
            container.scrollTo({ top: container.scrollHeight, behavior: 'smooth' });
        });
        observer.observe(document.getElementById('chat-messages'), { childList: true, subtree: true });
    </script>
</body>
</html>"""


@app.get("/chat", response_class=HTMLResponse)
async def get_chat_ui():
    # Build provider options dynamically
    provider_info = llm_router.get_provider_info()
    options = ""
    for name in sorted(provider_info.keys()):
        if provider_info[name]["has_key"] or name == "local":
            label = name.capitalize()
            model = provider_info[name]["models"]["standard"]
            options += f'<option value="{safe(name)}">{safe(label)} ({safe(model)})</option>\n'

    return CHAT_HTML.replace("PROVIDER_OPTIONS_PLACEHOLDER", options)


@app.get("/chat/history", response_class=HTMLResponse)
async def get_history():
    history = memory.get_convo("default")
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
async def post_message(message: str = Form(...), provider: str = Form("default")):
    import time
    # Store user message
    memory.store_convo("default", "user", message)
    escaped = safe(message)
    provider_param = safe(provider)
    
    session_id = f"chat-{int(time.time()*1000)}"

    # Return user bubble + SSE stream container
    return f'''<div class="msg-row user">
        <div class="msg-bubble user">{escaped}</div>
    </div>
    <div class="msg-row assistant"
         id="{session_id}"
         hx-ext="sse"
         sse-connect="/chat/stream?message={escaped}&provider={provider_param}&session={session_id}"
         sse-swap="message">
        <div class="msg-bubble assistant">
            <div class="thinking">
                <div class="thinking-dot"></div>
                <span>Thinking...</span>
            </div>
        </div>
    </div>'''


@app.get("/chat/stream")
async def stream_chat(message: str, provider: str = "default", session: str = ""):
    async def event_generator():
        try:
            prov = None if provider == "default" else provider
            response_text = await asyncio.to_thread(
                orchestrator.process, "default", message, provider=prov
            )
            memory.store_convo("default", "assistant", response_text)

            # Format response (basic markdown-like rendering)
            escaped_text = safe(response_text)
            
            import re
            formatted = re.sub(r'\*\*(.*?)\*\*', r'<b>\1</b>', escaped_text)
            formatted = re.sub(r'`(.*?)`', r'<code>\1</code>', formatted)
            formatted = re.sub(r'```(.*?)\n(.*?)\n?```', r'<pre><code>\2</code></pre>', formatted, flags=re.DOTALL)
            formatted = formatted.replace("\n\n", "</p><p>")
            formatted = formatted.replace("\n", "<br>")
            formatted = f"<p>{formatted}</p>"

            # Stream word-by-word for UX
            words = response_text.split(" ")
            accumulated = ""
            for i, word in enumerate(words):
                accumulated += word + (" " if i < len(words) - 1 else "")
                
                current_safe = safe(accumulated)
                current_formatted = re.sub(r'\*\*(.*?)\*\*', r'<b>\1</b>', current_safe)
                current_formatted = re.sub(r'`(.*?)`', r'<code>\1</code>', current_formatted)
                
                if "```" in current_formatted:
                    if current_formatted.count("```") % 2 != 0:
                        current_formatted += "\n... (coding) ..."
                    current_formatted = re.sub(
                        r'```(.*?)\n(.*?)(?:```|$)', 
                        r'<pre><code>\2</code></pre>', 
                        current_formatted, 
                        flags=re.DOTALL
                    )
                
                current_formatted = current_formatted.replace("\n\n", "</p><p>").replace("\n", "<br>")
                display = f"<p>{current_formatted}</p>"
                
                # If this is the final final word, swap out the entire container to kill SSE!
                if i == len(words) - 1 and session:
                    yield f'event: message\ndata: <div hx-swap-oob="outerHTML:#{session}" class="msg-row assistant"><div class="msg-bubble assistant">{display}</div></div>\n\n'
                else:
                    yield f'event: message\ndata: <div class="msg-bubble assistant">{display}</div>\n\n'
                    
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
async def clear_chat():
    try:
        memory.redis.delete("convo:default")
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
            # Pass the full context for the Chronicler's automated documentation
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
class ChatRequest(BaseModel):
    session_id: str = Field(default="default", max_length=64)
    message: str = Field(..., max_length=4000)
    provider: str = Field(default="default", description="LLM provider")


@app.post("/v1/chat")
async def chat_api(req: ChatRequest):
    logger.info(f"API chat: session={req.session_id} msg='{req.message[:100]}'")
    try:
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
    
    widget_class = "credits-widget local" if is_local else "credits-widget"
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

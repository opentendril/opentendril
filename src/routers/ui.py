"""
UI Router — HTML-rendering endpoints for the HTMX/SSE chat interface.

Responsibilities: chat UI, message streaming, history, settings.
"""

import asyncio
import logging
import os
import re
import time
import uuid
import dotenv
from typing import Optional

from fastapi import APIRouter, Cookie, Form, Request, Response
from fastapi.responses import HTMLResponse, StreamingResponse, RedirectResponse

from ..dependencies import llm_router, memory, orchestrator
from ..commands import intercept_slash_commands
from ..utils import safe

logger = logging.getLogger(__name__)
router = APIRouter()


def apply_markdown(text: str) -> str:
    """Convert Markdown to safe HTML. Input must already be HTML-escaped."""
    text = re.sub(r'```(?:\w+)?\n(.*?)\n?```', r'<pre><code>\1</code></pre>', text, flags=re.DOTALL)
    text = re.sub(r'`([^`]+)`', r'<code>\1</code>', text)
    text = re.sub(r'\*\*(.*?)\*\*', r'<b>\1</b>', text)
    text = re.sub(r'\*(.*?)\*', r'<em>\1</em>', text)
    text = re.sub(r'^### (.+)$', r'<h3>\1</h3>', text, flags=re.MULTILINE)
    text = re.sub(r'^## (.+)$', r'<h2>\1</h2>', text, flags=re.MULTILINE)
    text = re.sub(r'^# (.+)$', r'<h1>\1</h1>', text, flags=re.MULTILINE)
    text = re.sub(r'^[-*] (.+)$', r'<li>\1</li>', text, flags=re.MULTILINE)
    text = re.sub(r'(<li>.*?</li>\n?)+', r'<ul>\g<0></ul>', text, flags=re.DOTALL)
    text = re.sub(r'^---+$', r'<hr>', text, flags=re.MULTILINE)
    text = text.replace("\n\n", "</p><p>")
    text = text.replace("\n", "<br>")
    return f"<p>{text}</p>"


@router.get("/", response_class=RedirectResponse)
async def root():
    return RedirectResponse(url="/chat")


@router.get("/chat", response_class=HTMLResponse)
async def get_chat_ui(request: Request, response: Response):
    session_id = request.cookies.get("tendril_session")
    if not session_id:
        session_id = f"sess-{uuid.uuid4().hex[:12]}"
        response.set_cookie(
            key="tendril_session",
            value=session_id,
            max_age=60 * 60 * 24 * 30,
            httponly=True,
            samesite="lax",
        )
    logger.info(f"Chat UI session: {session_id}")
    html_path = os.path.join(os.path.dirname(__file__), "..", "..", "static", "index.html")
    with open(html_path, "r", encoding="utf-8") as f:
        html_content = f.read()
    response.body = html_content.encode()
    response.status_code = 200
    response.media_type = "text/html"
    return response


@router.get("/chat/history", response_class=HTMLResponse)
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


@router.post("/chat/message", response_class=HTMLResponse)
async def post_message(
    message: str = Form(...),
    provider: str = Form("default"),
    tendril_session: str = Cookie(default="default"),
):
    escaped = safe(message)
    provider_param = safe(provider)
    stream_id = f"chat-{int(time.time()*1000)}"
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


@router.get("/chat/stream")
async def stream_chat(
    message: str,
    provider: str = "default",
    session: str = "",
    sid: str = "default",
):
    async def event_generator():
        try:
            intercepted = intercept_slash_commands(message)
            if intercepted:
                response_text = intercepted
            else:
                prov = None if provider == "default" else provider
                response_text = await asyncio.to_thread(
                    orchestrator.process, sid, message, provider=prov
                )

            memory.store_convo(sid, "user", message)
            memory.store_convo(sid, "assistant", response_text)

            formatted = apply_markdown(safe(response_text))

            words = response_text.split(" ")
            accumulated = ""
            for i, word in enumerate(words):
                accumulated += word + (" " if i < len(words) - 1 else "")
                current_formatted = apply_markdown(safe(accumulated))

                if i == len(words) - 1 and session:
                    yield (
                        f'event: message\ndata: <div hx-swap-oob="outerHTML:#{session}" '
                        f'class="msg-row assistant"><div class="msg-bubble assistant">'
                        f'{formatted}</div></div>\n\n'
                    )
                else:
                    yield f'event: message\ndata: <div class="msg-bubble assistant">{current_formatted}</div>\n\n'

                await asyncio.sleep(0.01)

        except Exception as e:
            logger.error(f"Stream error: {e}")
            display = f'<span class="error-msg">Error: {safe(str(e))}</span>'
            if session:
                yield (
                    f'event: message\ndata: <div hx-swap-oob="outerHTML:#{session}" '
                    f'class="msg-row assistant"><div class="msg-bubble assistant">'
                    f'{display}</div></div>\n\n'
                )
            else:
                yield f'event: message\ndata: <div class="msg-bubble assistant">{display}</div>\n\n'

    return StreamingResponse(event_generator(), media_type="text/event-stream")


@router.post("/chat/clear", response_class=HTMLResponse)
async def clear_chat(tendril_session: str = Cookie(default="default")):
    try:
        memory.redis.delete(f"convo:{tendril_session}")
    except Exception:
        pass
    return '<p style="color: var(--text-muted); text-align: center; font-size: 12px; font-style: italic; margin-top: 40px;">Conversation cleared</p>'


@router.get("/settings", response_class=HTMLResponse)
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


@router.post("/settings/save", response_class=HTMLResponse)
async def save_settings(
    workspace: str = Form(""),
    grok_key: str = Form(""),
    github_token: str = Form(""),
):
    env_file = ".env"
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

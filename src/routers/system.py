"""
System Router — Observability and infrastructure endpoints.

Responsibilities: health, status, dreamer, events, provider info, credits.
"""

import asyncio
import logging
import os
from datetime import datetime

from fastapi import APIRouter, HTTPException
from fastapi.responses import HTMLResponse

from ..dependencies import llm_router, memory, skills_manager, orchestrator
from ..dreamer import dream, dreamer_state
from ..credits import credit_manager
from ..eventbus import event_bus
from ..utils import safe
from ..config import LOG_DIR

logger = logging.getLogger(__name__)
router = APIRouter()


@router.get("/health")
async def health():
    return {
        "status": "healthy",
        "version": "0.1.0",
        "skills_loaded": len(skills_manager.skills),
        "llm_providers": llm_router.available_providers,
    }


@router.get("/status")
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


@router.get("/dreamer/status")
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


@router.get("/dreamer/widget", response_class=HTMLResponse)
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

        last_run_display = last_run.strftime("%Y-%m-%d %H:%M") if last_run and hasattr(last_run, 'strftime') else "Never"

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


@router.post("/dreamer/trigger")
async def trigger_dream():
    """Manually trigger a dream cycle (for testing or on-demand synthesis)."""
    try:
        await asyncio.to_thread(dream, memory, llm_router)
        return {"status": "completed", "insight_count": dreamer_state.insight_count}
    except Exception as e:
        logger.error(f"Manual dream trigger failed: {e}")
        return {"status": "error", "error": str(e)}


@router.get("/api/providers")
async def get_providers():
    """Return available LLM providers for the UI selector."""
    info = llm_router.get_provider_info()
    return [
        {"value": name, "label": f"{name.capitalize()} ({info[name]['models']['standard']})"}
        for name in sorted(info.keys())
        if info[name]["has_key"] or name == "local"
    ]


@router.get("/events/{session_id}")
async def get_session_events(session_id: str, limit: int = 50):
    """Get recent events for a session (for debugging and observability)."""
    return event_bus.get_session_events(session_id, limit=min(limit, 200))


@router.get("/events")
async def get_recent_events(limit: int = 20):
    """Get most recent events across all sessions."""
    return event_bus.get_recent(limit=min(limit, 100))


@router.get("/health/providers")
async def get_provider_health():
    """Get health status of all LLM providers (cooldowns, error rates)."""
    return orchestrator.failover.get_provider_health()


@router.get("/v1/credits", response_class=HTMLResponse)
async def get_credits_ui():
    """Returns HTML snippet for the credits widget based on real balance."""
    balance = credit_manager.get_balance()
    label = "Local Compute" if credit_manager.mode.value == "local" else "Cloud Credits"
    return f'''<div>
                <div class="credits-val">{safe(balance)}</div>
                <div class="credits-label">{safe(label)}</div>
              </div>'''


@router.get("/approvals/pending")
async def get_pending_approvals():
    from ..dependencies import approval
    return {"pending": approval.get_pending()}


@router.post("/approvals/{request_id}/approve")
async def approve_request(request_id: str):
    from ..dependencies import approval
    if approval.approve(request_id):
        return {"status": "approved", "request_id": request_id}
    raise HTTPException(status_code=404, detail="Request not found or already resolved")


@router.post("/approvals/{request_id}/reject")
async def reject_request(request_id: str):
    from ..dependencies import approval
    if approval.reject(request_id):
        return {"status": "rejected", "request_id": request_id}
    raise HTTPException(status_code=404, detail="Request not found or already resolved")

"""
Tendril Dreamer — Periodic reflection and insight generation.

Runs on a schedule to review recent interactions and generate
insights that are stored in long-term memory.

Exposes state for UI visibility via the DreamerState singleton.
"""

import logging
import asyncio
from datetime import datetime, timedelta
from typing import Optional, List, Dict

from .config import DEFAULT_LLM_PROVIDER
from .memory import Memory

logger = logging.getLogger(__name__)


class DreamerState:
    """Tracks the Dreamer's execution state for UI visibility."""

    def __init__(self):
        self.last_run: Optional[datetime] = None
        self.last_status: str = "idle"
        self.last_insight: Optional[str] = None
        self.total_runs: int = 0
        self.total_insights: int = 0
        self.history: List[Dict] = []  # Last 10 dream results

    def record_run(self, status: str, insight: Optional[str] = None):
        self.last_run = datetime.now()
        self.last_status = status
        self.total_runs += 1
        if insight:
            self.last_insight = insight[:200]
            self.total_insights += 1

        entry = {
            "timestamp": self.last_run.isoformat(),
            "status": status,
            "insight_preview": insight[:100] + "..." if insight and len(insight) > 100 else insight,
        }
        self.history.insert(0, entry)
        self.history = self.history[:10]  # Keep last 10

    def to_dict(self) -> dict:
        return {
            "last_run": self.last_run.isoformat() if self.last_run else None,
            "last_status": self.last_status,
            "last_insight": self.last_insight,
            "total_runs": self.total_runs,
            "total_insights": self.total_insights,
            "history": self.history,
        }


# Global singleton
dreamer_state = DreamerState()


async def dream(memory: Memory, llm_router=None):
    """
    Review recent interactions and generate insights.

    This is an ASYNC function — called from AsyncIOScheduler.
    Uses the LLM Router if available, otherwise skips silently.
    """
    if llm_router is None:
        logger.debug("Dreamer skipped — no LLM router available")
        dreamer_state.record_run("skipped_no_router")
        return

    try:
        dreamer_state.last_status = "running"
        logger.info("💭 Dreamer cycle starting...")

        # Look at recent interactions (no session filter — dreamer sees global patterns)
        docs = memory.retrieve_relevant("recent conversations and interactions", k=20)

        if not docs or len(docs) < 3:
            logger.debug("Dreamer skipped — not enough recent data to dream about")
            dreamer_state.record_run("skipped_insufficient_data")
            return

        context = "\n".join(doc.page_content for doc in docs[:15])

        llm = llm_router.get(tier="fast", temperature=0.7)
        prompt = (
            "You are Tendril's Dream Engine. Review these recent interactions "
            "and generate 2-3 concise insights or patterns you notice. "
            "Focus on: recurring user needs, mistakes to avoid, "
            "potential improvements.\n\n"
            f"Recent activity:\n{context}\n\n"
            "Insights (be concise, actionable):"
        )

        resp = await asyncio.to_thread(llm.invoke, prompt)
        if resp.content:
            memory.store_longterm(
                f"[Dream Insight] {resp.content}",
                {"type": "dream", "timestamp": datetime.now().isoformat()},
            )
            logger.info(f"💭 Dream cycle complete — stored insight ({len(resp.content)} chars)")
            dreamer_state.record_run("success", resp.content)
        else:
            dreamer_state.record_run("empty_response")

    except Exception as e:
        logger.warning(f"💭 Dream cycle failed (non-critical): {e}")
        dreamer_state.record_run(f"error: {str(e)[:80]}")

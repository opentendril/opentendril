"""
Tendril Event Bus — Centralized observability and structured event system.

Emits, stores, and streams structured events across all Tendril subsystems.
Events are persisted in Redis (24h TTL) and queryable by run_id or session_id.

Usage:
    from .eventbus import event_bus, TendrilEvent
    event_bus.emit(TendrilEvent(
        run_id="abc123",
        event_type="failover.success",
        session_id="sess-xyz",
        data={"provider": "anthropic", "model": "claude-sonnet-4.6", "latency_ms": 230},
    ))
"""

import json
import logging
import time
import uuid
from dataclasses import dataclass, field, asdict
from datetime import datetime
from typing import Callable, Optional

logger = logging.getLogger(__name__)

# Maximum events stored per session in Redis
MAX_EVENTS_PER_SESSION = 500
EVENT_TTL_SECONDS = 86400  # 24 hours


@dataclass
class TendrilEvent:
    """A single structured event emitted by any Tendril subsystem."""
    run_id: str
    event_type: str
    session_id: str
    data: dict = field(default_factory=dict)
    timestamp: float = field(default_factory=time.time)
    duration_ms: Optional[int] = None

    def to_dict(self) -> dict:
        """Serialize for JSON storage and streaming."""
        return {
            "run_id": self.run_id,
            "event_type": self.event_type,
            "session_id": self.session_id,
            "data": self.data,
            "timestamp": self.timestamp,
            "iso_time": datetime.fromtimestamp(self.timestamp).isoformat(),
            "duration_ms": self.duration_ms,
        }

    def to_json(self) -> str:
        return json.dumps(self.to_dict())


def generate_run_id() -> str:
    """Generate a unique run ID for correlating events across a single request."""
    return f"run-{uuid.uuid4().hex[:12]}"


class EventBus:
    """
    Centralized event bus for Tendril observability.

    Features:
      - Structured JSON logging for every event
      - Redis persistence with per-session event history (24h TTL)
      - Subscriber callbacks for real-time streaming (SSE)
      - Queryable by run_id and session_id
    """

    def __init__(self, redis_client=None):
        self._redis = redis_client
        self._subscribers: list[Callable[[TendrilEvent], None]] = []
        self._recent: list[dict] = []  # In-memory fallback when Redis unavailable
        self._max_recent = 200
        logger.info("📡 Event Bus initialized.")

    def set_redis(self, redis_client):
        """Set Redis client (called after startup when Redis is available)."""
        self._redis = redis_client

    def emit(self, event: TendrilEvent):
        """
        Emit an event to all channels:
          1. Structured log
          2. Redis persistence
          3. Subscriber callbacks (SSE streaming)
          4. In-memory buffer
        """
        event_dict = event.to_dict()

        # 1. Structured log
        logger.info(f"[EVENT] {event.event_type} | run={event.run_id} | {json.dumps(event.data)}")

        # 2. Redis persistence
        if self._redis:
            try:
                key = f"events:{event.session_id}"
                self._redis.lpush(key, json.dumps(event_dict))
                self._redis.ltrim(key, 0, MAX_EVENTS_PER_SESSION - 1)
                self._redis.expire(key, EVENT_TTL_SECONDS)
            except Exception as e:
                logger.warning(f"Event Redis write failed: {e}")

        # 3. Notify subscribers
        for sub in self._subscribers:
            try:
                sub(event)
            except Exception as e:
                logger.warning(f"Event subscriber error: {e}")

        # 4. In-memory buffer (fallback + debugging)
        self._recent.append(event_dict)
        if len(self._recent) > self._max_recent:
            self._recent = self._recent[-self._max_recent:]

    def subscribe(self, callback: Callable[[TendrilEvent], None]):
        """Register a callback for real-time event streaming."""
        self._subscribers.append(callback)

    def unsubscribe(self, callback: Callable[[TendrilEvent], None]):
        """Remove a subscriber."""
        self._subscribers = [s for s in self._subscribers if s is not callback]

    def get_session_events(self, session_id: str, limit: int = 50) -> list[dict]:
        """Get recent events for a session from Redis (or in-memory fallback)."""
        if self._redis:
            try:
                key = f"events:{session_id}"
                raw = self._redis.lrange(key, 0, limit - 1)
                return [json.loads(r) for r in raw]
            except Exception as e:
                logger.warning(f"Event Redis read failed: {e}")

        # Fallback: filter in-memory
        return [
            e for e in reversed(self._recent)
            if e.get("session_id") == session_id
        ][:limit]

    def get_run_events(self, run_id: str, session_id: Optional[str] = None) -> list[dict]:
        """Get all events for a specific run ID."""
        if session_id:
            events = self.get_session_events(session_id, limit=MAX_EVENTS_PER_SESSION)
        else:
            events = list(reversed(self._recent))
        return [e for e in events if e.get("run_id") == run_id]

    def get_recent(self, limit: int = 20) -> list[dict]:
        """Get the most recent events across all sessions (from memory)."""
        return list(reversed(self._recent[-limit:]))


# Global singleton — initialized without Redis, wired up at app startup
event_bus = EventBus()

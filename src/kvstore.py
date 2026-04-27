"""
src/kvstore.py — Provider-agnostic Key-Value Store abstraction.

Decouples Tendril from its Redis container dependency by providing
swappable backend implementations behind a common interface.

Backends:
  redis    — Default. Requires Redis container (local) or Redis URL.
  upstash  — Serverless Redis over HTTP (zero infrastructure, SaaS-ready).
  memory   — In-process dict with TTL. Zero dependencies. Great for testing
             and lightweight local dev (data lost on restart).

Configuration (src/config.py / .env):
  KV_STORE_PROVIDER=redis | upstash | memory
  UPSTASH_REDIS_REST_URL=https://...
  UPSTASH_REDIS_REST_TOKEN=...

Interface (all backends implement):
  get(key)               -> str | None
  set(key, value, ttl?)  -> None
  delete(key)            -> None
  lpush(key, *values)    -> None   (list prepend)
  lrange(key, start, end)-> list[str]
  ltrim(key, start, end) -> None
  expire(key, seconds)   -> None
"""

import json
import logging
import time
import threading
from typing import Optional, Protocol, runtime_checkable

logger = logging.getLogger(__name__)


# ---------------------------------------------------------------------------
# Protocol (structural interface — no ABC inheritance needed)
# ---------------------------------------------------------------------------

@runtime_checkable
class KVStore(Protocol):
    """Minimal KV interface used by EventBus and Memory."""

    def get(self, key: str) -> Optional[str]: ...
    def set(self, key: str, value: str, ttl: Optional[int] = None) -> None: ...
    def delete(self, key: str) -> None: ...
    def lpush(self, key: str, *values: str) -> None: ...
    def lrange(self, key: str, start: int, end: int) -> list[str]: ...
    def ltrim(self, key: str, start: int, end: int) -> None: ...
    def expire(self, key: str, seconds: int) -> None: ...


# ---------------------------------------------------------------------------
# Backend: Redis (wraps the existing redis-py client)
# ---------------------------------------------------------------------------

class RedisStore:
    """Thin wrapper around redis-py to conform to KVStore interface."""

    def __init__(self, url: str):
        from redis import Redis
        self._r = Redis.from_url(url, decode_responses=True)
        logger.info("🗄️  KVStore: Redis backend connected.")

    def get(self, key: str) -> Optional[str]:
        return self._r.get(key)

    def set(self, key: str, value: str, ttl: Optional[int] = None) -> None:
        if ttl:
            self._r.setex(key, ttl, value)
        else:
            self._r.set(key, value)

    def delete(self, key: str) -> None:
        self._r.delete(key)

    def lpush(self, key: str, *values: str) -> None:
        self._r.lpush(key, *values)

    def lrange(self, key: str, start: int, end: int) -> list[str]:
        return self._r.lrange(key, start, end)

    def ltrim(self, key: str, start: int, end: int) -> None:
        self._r.ltrim(key, start, end)

    def expire(self, key: str, seconds: int) -> None:
        self._r.expire(key, seconds)


# ---------------------------------------------------------------------------
# Backend: In-Memory (zero dependencies, TTL via timestamp)
# ---------------------------------------------------------------------------

class InMemoryStore:
    """
    Thread-safe in-process KV store with TTL support.

    - Data is lost on process restart.
    - Perfect for: testing, lightweight local dev, single-process deployments.
    - No external dependencies.
    """

    def __init__(self):
        self._data: dict[str, str] = {}
        self._lists: dict[str, list[str]] = {}
        self._expiry: dict[str, float] = {}
        self._lock = threading.Lock()
        logger.info("🗄️  KVStore: InMemory backend initialized (data is not persisted).")

    def _is_expired(self, key: str) -> bool:
        exp = self._expiry.get(key)
        return exp is not None and time.time() > exp

    def _evict(self, key: str) -> None:
        self._data.pop(key, None)
        self._lists.pop(key, None)
        self._expiry.pop(key, None)

    def get(self, key: str) -> Optional[str]:
        with self._lock:
            if self._is_expired(key):
                self._evict(key)
                return None
            return self._data.get(key)

    def set(self, key: str, value: str, ttl: Optional[int] = None) -> None:
        with self._lock:
            self._data[key] = value
            if ttl:
                self._expiry[key] = time.time() + ttl

    def delete(self, key: str) -> None:
        with self._lock:
            self._evict(key)

    def lpush(self, key: str, *values: str) -> None:
        with self._lock:
            if self._is_expired(key):
                self._evict(key)
            lst = self._lists.setdefault(key, [])
            for v in values:
                lst.insert(0, v)

    def lrange(self, key: str, start: int, end: int) -> list[str]:
        with self._lock:
            if self._is_expired(key):
                self._evict(key)
                return []
            lst = self._lists.get(key, [])
            end_idx = None if end == -1 else end + 1
            return lst[start:end_idx]

    def ltrim(self, key: str, start: int, end: int) -> None:
        with self._lock:
            lst = self._lists.get(key)
            if lst is not None:
                self._lists[key] = lst[start: end + 1]

    def expire(self, key: str, seconds: int) -> None:
        with self._lock:
            if key in self._data or key in self._lists:
                self._expiry[key] = time.time() + seconds


# ---------------------------------------------------------------------------
# Backend: Upstash (serverless Redis over HTTP)
# ---------------------------------------------------------------------------

class UpstashStore:
    """
    Upstash Redis REST API client.

    Zero infrastructure — works in any environment with outbound HTTP.
    Ideal for SaaS/managed deployments where running a Redis container
    adds operational overhead.

    Requires:
      UPSTASH_REDIS_REST_URL  — e.g. https://your-db.upstash.io
      UPSTASH_REDIS_REST_TOKEN — from Upstash dashboard
    """

    def __init__(self, url: str, token: str):
        import httpx
        self._base = url.rstrip("/")
        self._headers = {"Authorization": f"Bearer {token}"}
        self._client = httpx.Client(timeout=5)
        logger.info("🗄️  KVStore: Upstash backend connected.")

    def _cmd(self, *args) -> object:
        """Execute a Redis command via Upstash REST API."""
        resp = self._client.post(
            f"{self._base}/pipeline",
            headers=self._headers,
            json=[list(args)],
        )
        resp.raise_for_status()
        results = resp.json()
        return results[0].get("result") if results else None

    def get(self, key: str) -> Optional[str]:
        result = self._cmd("GET", key)
        return str(result) if result is not None else None

    def set(self, key: str, value: str, ttl: Optional[int] = None) -> None:
        if ttl:
            self._cmd("SETEX", key, str(ttl), value)
        else:
            self._cmd("SET", key, value)

    def delete(self, key: str) -> None:
        self._cmd("DEL", key)

    def lpush(self, key: str, *values: str) -> None:
        self._cmd("LPUSH", key, *values)

    def lrange(self, key: str, start: int, end: int) -> list[str]:
        result = self._cmd("LRANGE", key, str(start), str(end))
        return result if isinstance(result, list) else []

    def ltrim(self, key: str, start: int, end: int) -> None:
        self._cmd("LTRIM", key, str(start), str(end))

    def expire(self, key: str, seconds: int) -> None:
        self._cmd("EXPIRE", key, str(seconds))


# ---------------------------------------------------------------------------
# Factory
# ---------------------------------------------------------------------------

def create_kv_store(provider: str, **kwargs) -> KVStore:
    """
    Instantiate the appropriate KVStore backend.

    Args:
        provider: "redis" | "upstash" | "memory"
        **kwargs: Provider-specific config (url, token, etc.)

    Returns:
        A KVStore-compatible instance.
    """
    if provider == "upstash":
        url = kwargs.get("url", "")
        token = kwargs.get("token", "")
        if not url or not token:
            logger.warning("⚠️  Upstash URL/token not configured, falling back to InMemory.")
            return InMemoryStore()
        return UpstashStore(url=url, token=token)

    if provider == "memory":
        return InMemoryStore()

    # Default: Redis
    url = kwargs.get("url", "redis://redis:6379/0")
    try:
        return RedisStore(url=url)
    except Exception as exc:
        logger.warning(f"⚠️  Redis connection failed ({exc}), falling back to InMemory.")
        return InMemoryStore()

"""
Tendril — Main Application Entrypoint

Initialises the FastAPI app, wires all routers, and starts background services.
All business logic lives in the routers/ directory (Unix philosophy: one job each).

Router Map:
  src/routers/ui.py     → HTML chat UI, settings
  src/routers/api.py    → /edit, /v1/chat, /v1/chat/completions
  src/routers/system.py → /health, /status, /dreamer, /events, /approvals
  src/commands.py       → /help, /sdlc, /model, /keys, /repo slash commands
"""

import logging
import os

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from fastapi.staticfiles import StaticFiles
from fastapi.middleware.cors import CORSMiddleware
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from slowapi import Limiter
from slowapi.util import get_remote_address
from slowapi.errors import RateLimitExceeded

from .config import validate_config, LOG_DIR
from .dependencies import llm_router, memory
from .dreamer import dream
from .eventbus import event_bus
from .waitlist import router as waitlist_router
from .routers import ui, api, system

# --- Logging ---
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

# --- Config validation ---
validate_config()

# --- Wire event bus to Redis ---
if memory.redis:
    event_bus.set_redis(memory.redis)

# --- Background scheduler ---
scheduler = AsyncIOScheduler()
scheduler.add_job(dream, "interval", hours=1, args=[memory, llm_router])

# --- Rate limiting ---
limiter = Limiter(key_func=get_remote_address, default_limits=["200/hour"])

# --- App ---
app = FastAPI(
    title="Tendril",
    version="0.1.0",
    description="Self-building AI orchestrator — The Root Agent",
)
app.state.limiter = limiter

# --- Static assets ---
os.makedirs("static", exist_ok=True)
app.mount("/static", StaticFiles(directory="static"), name="static")

# --- CORS ---
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],   # Restrict before hard launch
    allow_credentials=True,
    allow_methods=["GET", "POST", "OPTIONS"],
    allow_headers=["*"],
)

# --- Routers ---
app.include_router(waitlist_router)
app.include_router(ui.router)
app.include_router(api.router)
app.include_router(system.router)


# --- Lifecycle ---
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


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8080)

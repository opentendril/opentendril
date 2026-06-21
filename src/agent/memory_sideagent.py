"""
Background memory sideagent.
Listens to the EventBus for `memory.ingest` events and lazily batches
them into the vector store, ensuring the main chat loop is never blocked.
"""
import asyncio
import logging
from typing import List

from ..eventbus import event_bus, TendrilEvent

logger = logging.getLogger(__name__)

class MemorySideagent:
    def __init__(self, memory_service):
        self.memory = memory_service
        self._queue: asyncio.Queue[TendrilEvent] = asyncio.Queue()
        self._worker_task = None
        
        # Subscribe to all events, we filter for memory.ingest
        event_bus.subscribe(self._handle_event)

    def _handle_event(self, event: TendrilEvent):
        if event.event_type == "memory.ingest":
            # Push to async queue without blocking the event bus
            try:
                self._queue.put_nowait(event)
            except Exception as e:
                logger.error(f"Failed to queue memory event: {e}")

    async def _worker_loop(self):
        logger.info("🧠 Memory sideagent background worker started.")
        while True:
            try:
                event = await self._queue.get()
                role = event.data.get("role")
                content = event.data.get("content")
                session_id = event.session_id
                timestamp = event.data.get("timestamp")
                
                if role and content:
                    text_to_embed = f"{role}: {content}"
                    metadata = {"session_id": session_id, "timestamp": timestamp}
                    
                    # This calls vectorstore property lazily
                    # We wrap the synchronous add_texts call in a thread to avoid blocking the event loop
                    await asyncio.to_thread(self.memory.store_longterm, text_to_embed, metadata)
                    logger.debug(f"Silently embedded memory for session {session_id}")
                    
                self._queue.task_done()
            except asyncio.CancelledError:
                break
            except Exception as e:
                logger.error(f"Memory sideagent error: {e}")
                
    def start(self):
        if self._worker_task is None:
            self._worker_task = asyncio.create_task(self._worker_loop())

    def stop(self):
        if self._worker_task:
            self._worker_task.cancel()
            self._worker_task = None
            event_bus.unsubscribe(self._handle_event)

def start_memory_sideagent(memory_service):
    agent = MemorySideagent(memory_service)
    agent.start()
    return agent

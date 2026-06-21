import pytest
import asyncio
from unittest.mock import MagicMock
from src.agent.memorysideagent import MemorySideagent, start_memory_sideagent
from src.eventbus import TendrilEvent

@pytest.mark.asyncio
async def test_memory_sideagent_ingestion():
    # Mock the memory service
    mock_memory = MagicMock()
    
    # Start the sideagent
    agent = start_memory_sideagent(mock_memory)
    
    # Emit a fake event
    event = TendrilEvent(
        run_id="run-123",
        event_type="memory.ingest",
        session_id="sess-xyz",
        data={"role": "user", "content": "I like python.", "timestamp": "2026-06-21T05:00:00Z"}
    )
    
    agent._handle_event(event)
    
    # Yield control to the event loop so the worker task can process the queue
    await asyncio.sleep(0.1)
    
    # Check that store_longterm was called with the correct text
    mock_memory.store_longterm.assert_called_once_with(
        "user: I like python.", 
        {"session_id": "sess-xyz", "timestamp": "2026-06-21T05:00:00Z"}
    )
    
    # Cleanup
    agent.stop()

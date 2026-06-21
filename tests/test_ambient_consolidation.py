import pytest
from unittest.mock import MagicMock
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from src.agent.ambient_consolidation import AmbientConsolidator, schedule_consolidation

@pytest.mark.asyncio
async def test_ambient_consolidation_runs():
    mock_memory = MagicMock()
    mock_llm_router = MagicMock()
    
    consolidator = AmbientConsolidator(mock_memory, mock_llm_router)
    
    # We just run it to make sure it doesn't crash
    await consolidator.consolidate()
    
def test_schedule_consolidation():
    mock_scheduler = MagicMock(spec=AsyncIOScheduler)
    mock_memory = MagicMock()
    mock_llm_router = MagicMock()
    
    schedule_consolidation(mock_scheduler, mock_memory, mock_llm_router)
    
    # Verify the job was added
    mock_scheduler.add_job.assert_called_once()
    args, kwargs = mock_scheduler.add_job.call_args
    assert kwargs.get("interval") or args[1] == "interval"
    assert kwargs.get("hours") == 6

import pytest
from unittest.mock import MagicMock
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from src.ambientconsolidation import AmbientConsolidator, schedule_consolidation

@pytest.mark.asyncio
async def test_ambient_consolidation_runs():
    mock_memory = MagicMock()
    # Mock the ambient queue to return 2 conflicting facts
    mock_memory.get_and_clear_ambient_queue.return_value = [
        {"id": "id1", "content": "user prefers python 3.9", "metadata": {"session_id": "sess1"}},
        {"id": "id2", "content": "user upgraded to python 3.12", "metadata": {"session_id": "sess1"}}
    ]
    
    mock_llm = MagicMock()
    mock_llm.invoke.return_value.content = "user uses python 3.12"
    mock_llm_router = MagicMock()
    mock_llm_router.get.return_value = mock_llm
    
    # Test dry run (default in schedule_consolidation)
    consolidator = AmbientConsolidator(mock_memory, mock_llm_router, dry_run=True)
    await consolidator.consolidate()
    
    mock_memory.get_and_clear_ambient_queue.assert_called_once()
    mock_llm.invoke.assert_called_once()
    
    # Verify no deletes happened because dry_run=True
    mock_memory.delete_nodes.assert_not_called()
    mock_memory.store_longterm.assert_not_called()
    
    # Test active run
    mock_memory.reset_mock()
    mock_memory.get_and_clear_ambient_queue.return_value = [
        {"id": "id1", "content": "user prefers python 3.9", "metadata": {"session_id": "sess1"}},
        {"id": "id2", "content": "user upgraded to python 3.12", "metadata": {"session_id": "sess1"}}
    ]
    
    consolidator_active = AmbientConsolidator(mock_memory, mock_llm_router, dry_run=False)
    await consolidator_active.consolidate()
    
    mock_memory.delete_nodes.assert_called_once_with(["id1", "id2"])
    mock_memory.store_longterm.assert_called_once_with(
        "user uses python 3.12",
        {"session_id": "sess1", "type": "consolidated_summary"}
    )
    
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

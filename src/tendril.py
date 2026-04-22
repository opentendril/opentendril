"""
src/tendril.py — Backwards-compatibility shim.

The Orchestrator has been modularised into src/agent/:
  src/agent/orchestrator.py  — Core process() loop
  src/agent/tools.py         — ToolFactory: all LangChain @tool definitions
  src/agent/system_prompt.py — System prompt builder

Any code importing `from .tendril import Orchestrator` continues to work.
"""
from .agent.orchestrator import Orchestrator

__all__ = ["Orchestrator"]

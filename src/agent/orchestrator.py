"""
src/agent/orchestrator.py — Core Orchestrator: process() loop and agent lifecycle.

Responsibilities:
  - Initialise all dependencies and ToolFactory
  - Run the agentic loop (LLM → tool calls → LLM → ...)
  - Emit structured events via the event bus
  - Delegate system prompt construction to system_prompt.py
  - Delegate tool definitions to tools.py

This file should NOT define tools or system prompt content.
"""

import difflib
import logging
import os
import time as _time
from typing import Optional

from ..config import WORKSPACE_ROOT, PROJECT_ROOT
from ..llmrouter import LLMRouter
from ..memory import Memory
from ..skillsmanager import SkillsManager
from ..editor import FileEditor
from ..approval import ApprovalGate
from ..gitmanager import GitManager
from ..testrunner import TestRunner
from ..credits import credit_manager
from ..failover import ModelFailover, classify_error
from ..eventbus import event_bus, TendrilEvent, generate_run_id
from ..patcher import format_patch_for_prompt
from .tools import ToolFactory
from .system_prompt import build_system_prompt

logger = logging.getLogger(__name__)


class Orchestrator:
    """
    Tendril's central orchestrator.

    Coordinates between LLMs (via Router), file editing (via Editor),
    memory (via RAG), skills, and git tooling to process user requests.
    """

    def __init__(
        self,
        memory: Memory,
        skills_manager: SkillsManager,
        llm_router: Optional[LLMRouter] = None,
        editor: Optional[FileEditor] = None,
        approval: Optional[ApprovalGate] = None,
    ):
        self.memory = memory
        self.skills_manager = skills_manager
        self.router = llm_router or LLMRouter()
        self.editor = editor or FileEditor(WORKSPACE_ROOT)
        self.approval = approval or ApprovalGate(auto_approve=True)

        git_root = os.path.join(WORKSPACE_ROOT, ".git")
        self.git = GitManager() if os.path.isdir(git_root) else None
        self.tester = TestRunner(self.approval)
        self.failover = ModelFailover(self.router)

        factory = ToolFactory(
            memory=self.memory,
            editor=self.editor,
            git=self.git,
            tester=self.tester,
            router=self.router,
            skills_manager=self.skills_manager,
        )
        self.tools = factory.build()

    def process(
        self,
        session_id: str,
        message: str,
        provider: Optional[str] = None,
        tier: str = "standard",
    ) -> str:
        """
        Process a user message through the agentic loop.

        1. Credit check
        2. Build context (history, RAG, skills)
        3. Select LLM provider via failover chain
        4. Agentic loop: LLM → tool calls → LLM (max 20 iterations)
        5. Emit structured events throughout
        """
        if not credit_manager.validate_request(session_id):
            return "❌ Access Denied: Insufficient credits. Please upgrade at cloud.opentendril.com"

        run_id = generate_run_id()
        event_bus.emit(TendrilEvent(
            run_id=run_id,
            event_type="request.start",
            session_id=session_id,
            data={"message_preview": message[:100], "provider": provider or "default", "tier": tier},
        ))

        # --- Build context ---
        history = self.memory.get_convo(session_id)
        relevant_docs = self.memory.retrieve_relevant(message, session_id=session_id)
        rag_context = "\n".join(doc.page_content for doc in relevant_docs) if relevant_docs else "None"
        skills_context = self.skills_manager.get_context() or "No skills loaded."

        tool_descriptions = "\n".join(
            f"  - {t.name}: {t.description}" for t in self.tools
        )

        # Survey files in external project mode
        file_listing = ""
        if WORKSPACE_ROOT != PROJECT_ROOT:
            try:
                project_files = self.editor.list_files()
                file_listing = "\n".join(
                    f"  {f['path']} ({f['size']} bytes)" for f in project_files[:80]
                )
                if len(project_files) > 80:
                    file_listing += f"\n  ... and {len(project_files) - 80} more files"
            except Exception:
                file_listing = "  (could not scan project files)"

        system_prompt = build_system_prompt(
            tool_descriptions=tool_descriptions,
            skills_context=skills_context,
            rag_context=rag_context,
            file_listing=file_listing,
        )

        messages = [{"role": "system", "content": system_prompt}] + history[-8:] + [
            {"role": "user", "content": message}
        ]

        # --- Provider selection via failover chain ---
        selected_provider = provider
        for candidate in self.failover._build_candidate_chain(provider, tier):
            if not self.failover._get_state(candidate).is_in_cooldown:
                selected_provider = candidate
                break
        else:
            event_bus.emit(TendrilEvent(
                run_id=run_id, event_type="request.error",
                session_id=session_id, data={"error": "All providers in cooldown"},
            ))
            return "⚠️ All LLM providers are currently in cooldown. Please try again in a few seconds."

        try:
            llm = self.router.get(provider=selected_provider, tier=tier)
        except Exception as e:
            return f"⚠️ Failed to initialize LLM provider: {str(e)}"

        event_bus.emit(TendrilEvent(
            run_id=run_id, event_type="failover.selected",
            session_id=session_id,
            data={"provider": selected_provider, "tier": tier, "was_fallback": selected_provider != provider},
        ))

        try:
            llm_with_tools = llm.bind_tools(self.tools)
        except Exception:
            llm_with_tools = llm

        # --- Agentic loop ---
        max_iterations = 20
        last_response_content = None

        for i in range(max_iterations):
            try:
                _start = _time.time()
                resp = llm_with_tools.invoke(messages)
                _latency = (_time.time() - _start) * 1000
                self.failover._get_state(selected_provider).record_success(_latency)
                last_response_content = resp.content
            except Exception as e:
                reason = classify_error(e)
                self.failover._get_state(selected_provider).record_failure(reason)
                logger.error(f"LLM invocation error: {e}")
                event_bus.emit(TendrilEvent(
                    run_id=run_id, event_type="request.error",
                    session_id=session_id,
                    data={"error": str(e)[:200], "iteration": i, "reason": reason},
                ))
                return f"Sorry, I encountered an error communicating with the LLM: {str(e)}"

            if not resp.tool_calls:
                credit_manager.consume_request(session_id)
                event_bus.emit(TendrilEvent(
                    run_id=run_id, event_type="request.end",
                    session_id=session_id,
                    data={"iterations": i + 1, "content": str(resp.content)},
                ))
                self._check_drift(session_id, message, last_response_content)
                return resp.content or "I processed your request but have no text response."

            # Execute tool calls
            messages.append(resp)
            for tool_call in resp.tool_calls:
                tool_name = tool_call["name"]
                tool_args = tool_call["args"]

                event_bus.emit(TendrilEvent(
                    run_id=run_id, event_type="tool.start",
                    session_id=session_id,
                    data={"name": tool_name, "args": tool_args},
                ))

                tool_func = next((t for t in self.tools if t.name == tool_name), None)
                try:
                    tool_result = tool_func.invoke(tool_args) if tool_func else f"Unknown tool: {tool_name}"
                except Exception as e:
                    tool_result = f"Tool error: {str(e)}"

                event_bus.emit(TendrilEvent(
                    run_id=run_id, event_type="tool.end",
                    session_id=session_id,
                    data={"name": tool_name, "result_preview": str(tool_result)[:200]},
                ))

                messages.append({
                    "role": "tool",
                    "tool_call_id": tool_call["id"],
                    "name": tool_name,
                    "content": str(tool_result),
                })

        self._check_drift(session_id, message, last_response_content)
        return "⚠️ Reached maximum tool iterations. The task may be too complex — try breaking it into smaller steps."

    def _check_drift(self, session_id: str, message: str, last_response: Optional[str]) -> None:
        """Log a warning if the response has drifted too far from the user's intent."""
        if not last_response:
            return
        similarity = difflib.SequenceMatcher(None, message, last_response).ratio()
        if similarity < 0.15:
            logger.warning(
                f"Drift detected in session {session_id}: similarity {similarity:.2f} "
                f"between user message and last response"
            )

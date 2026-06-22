"""
src/agent/orchestrator.py — Core Orchestrator: process() loop and agent lifecycle.

Responsibilities:
  - Initialise all dependencies and ToolFactory
  - Run the agentic loop (LLM → tool calls → LLM → ...)
  - Emit structured events via the event bus
  - Delegate system prompt construction to systemprompt.py
  - Delegate tool definitions to tools.py

This file should NOT define tools or system prompt content.
"""

import difflib
import logging
import os
import time as _time
from typing import Optional

from .config import WORKSPACE_ROOT, PROJECT_ROOT
from .llmrouter import LLMRouter
from .memory import Memory

from .editor import FileEditor
from .approval import ApprovalGate
from .gitmanager import GitManager
from .testrunner import TestRunner
from .credits import credit_manager
from .failover import ModelFailover, classify_error
from .eventbus import event_bus, TendrilEvent, generate_run_id
from .patcher import format_patch_for_prompt
from .meristem import ToolFactory
from .systemprompt import build_static_prompt, build_dynamic_prompt, build_system_prompt
from .promptcache import build_cached_messages
from .assessor import assess_and_route, revise_execution_plan
from langchain_core.messages import AIMessage, SystemMessage

logger = logging.getLogger(__name__)


class TendrilLoop:
    """
    Tendril's execution loop.

    Coordinates between LLMs (via Router), file editing (via Editor),
    memory (via RAG), skills, and git tooling to process user requests.
    """

    def __init__(
        self,
        memory: Memory,
        llm_router: Optional[LLMRouter] = None,
        editor: Optional[FileEditor] = None,
        approval: Optional[ApprovalGate] = None,
    ):
        self.memory = memory
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
        )
        self.tools = factory.build()

    def process(
        self,
        session_id: str,
        message: str,
        provider: Optional[str] = None,
        tier: str = "auto",
    ) -> str:
        """
        Process a user message through the agentic loop.

        1. Credit check
        2. Complexity assessment (auto-selects tier unless explicitly set)
        3. Build context (history, RAG, skills)
        4. Select LLM provider via failover chain
        5. Agentic loop: LLM → tool calls → LLM (max 20 iterations)
        6. Emit structured events throughout
        """
        if not credit_manager.validate_request(session_id):
            return "❌ Access Denied: Insufficient credits. Please upgrade at cloud.opentendril.com"

        # --- Complexity assessment (auto-tier routing) ---
        from .config import ASSESSOR_ENABLED
        _active_provider = provider or self.router.default_provider
        if ASSESSOR_ENABLED:
            _active_provider, tier = assess_and_route(
                message=message,
                router=self.router,
                provider=_active_provider,
                requested_tier=tier,
            )
        elif tier == "auto":
            tier = "standard"  # Safe fallback when assessor is disabled

        run_id = generate_run_id()
        event_bus.emit(TendrilEvent(
            run_id=run_id,
            event_type="request.start",
            session_id=session_id,
            data={"message_preview": message[:100], "provider": _active_provider, "tier": tier},
        ))

        # --- Build context ---
        history = self.memory.get_convo(session_id)
        relevant_docs = self.memory.retrieve_relevant(message, session_id=session_id)
        rag_context = "\n".join(doc.page_content for doc in relevant_docs) if relevant_docs else "None"
        skills_context = "No skills loaded."

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

        static_prompt = build_static_prompt(tool_descriptions)
        dynamic_prompt = build_dynamic_prompt(
            skills_context=skills_context,
            rag_context=rag_context,
            file_listing=file_listing,
        )

        # Resolve the active provider & model name for cache annotation
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

        from .llmrouter import PROVIDER_CONFIG, _resolve_model
        _provider_cfg = PROVIDER_CONFIG.get(selected_provider, {})
        _default_model = _provider_cfg.get("models", {}).get(tier, "")
        resolved_model = _resolve_model(selected_provider, tier, _default_model)

        messages = build_cached_messages(
            provider=selected_provider,
            model_name=resolved_model,
            static_system=static_prompt,
            dynamic_system=dynamic_prompt,
            history=history,
            user_message=message,
        )

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
        consecutive_failures = 0

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
            circuit_broken = False
            
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

                is_failure = isinstance(tool_result, str) and (
                    tool_result.startswith("❌") or 
                    "Tool error:" in tool_result or 
                    "AssertionError" in tool_result or
                    "error" in tool_result.lower()
                )

                if is_failure:
                    consecutive_failures += 1
                    event_bus.emit(TendrilEvent(
                        run_id=run_id, event_type="orchestrator.pruned", 
                        session_id=session_id, data={"tool": tool_name}
                    ))
                    
                    # 1. IMMUTABILITY GUARD: Clone the message to prune massive arguments safely
                    if hasattr(resp, "tool_calls"):
                        pruned_calls = []
                        for tc_old in resp.tool_calls:
                            if tc_old["id"] == tool_call["id"]:
                                safe_args = {k: "[PRUNED DUE TO VALIDATION FAILURE]" if isinstance(v, str) and len(v) > 200 else v for k, v in tc_old.get("args", {}).items()}
                                pruned_calls.append({"name": tc_old["name"], "args": safe_args, "id": tc_old["id"]})
                            else:
                                pruned_calls.append(tc_old)
                        
                        # Replace resp in messages with cloned AIMessage
                        cloned_resp = AIMessage(
                            content=resp.content,
                            tool_calls=pruned_calls,
                            id=resp.id if getattr(resp, "id", None) and isinstance(resp.id, str) else None
                        )
                        messages[-1] = cloned_resp
                        resp = cloned_resp # keep local ref updated
                else:
                    consecutive_failures = 0

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
                
                # 2. CIRCUIT BREAKER
                if consecutive_failures >= 3:
                    event_bus.emit(TendrilEvent(
                        run_id=run_id, event_type="orchestrator.circuit_breaker", 
                        session_id=session_id, data={"failures": consecutive_failures}
                    ))
                    logger.warning(f"Circuit breaker triggered in session {session_id} after {consecutive_failures} failures.")
                    
                    plan_json = revise_execution_plan(message, str(tool_result), llm, session_id)
                    
                    # Reset context completely
                    messages = build_cached_messages(
                        provider=selected_provider,
                        model_name=resolved_model,
                        static_system=static_prompt,
                        dynamic_system=dynamic_prompt,
                        history=history,
                        user_message=message,
                    )
                    messages.append(SystemMessage(content=f"CIRCUIT BREAKER TRIGGERED. Previous attempts failed severely. Execute this revised JSON master plan natively:\n{plan_json}"))
                    
                    consecutive_failures = 0
                    circuit_broken = True
                    break

            if circuit_broken:
                continue


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

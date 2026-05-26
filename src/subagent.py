"""
src/subagent.py — Ephemeral Sub-Agent Orchestration (Issue #18).

The Root Agent can spawn short-lived, expert Worker Agents for specialised tasks.
Each worker has:
  - A tailored persona (SubAgentProfile.system_prompt)
  - A restricted tool whitelist (principle of least privilege)
  - An isolated agentic loop (max 10 iterations, no bleed into parent context)
  - Structured output returned to the Root Agent as a string

Built-in profiles:
  security_auditor  — Read-only code security analysis
  code_reviewer     — Read-only review with style + logic feedback
  test_writer       — Read + write test files
  documenter        — Read + write markdown/docstrings
  linter            — Read + run linting commands

Usage (via Root Agent's spawn_sub_agent tool):
  spawn_sub_agent(profile="security_auditor", task="Audit src/auth.py for vulnerabilities")
"""

import logging
from dataclasses import dataclass, field
from typing import Optional
from langchain_core.messages import AIMessage
from .eventbus import event_bus, TendrilEvent

logger = logging.getLogger(__name__)

# Max agentic loop iterations for any sub-agent
_MAX_ITERATIONS = 10


# ---------------------------------------------------------------------------
# Profile Registry
# ---------------------------------------------------------------------------

@dataclass
class SubAgentProfile:
    """Defines the persona and tool access for a Worker Agent."""
    name: str
    description: str
    system_prompt: str
    allowed_tools: list[str]         # Whitelist of tool names from parent's tool list
    tier: str = "standard"           # LLM tier to use for this worker


_PROFILES: dict[str, SubAgentProfile] = {
    "security_auditor": SubAgentProfile(
        name="security_auditor",
        description="Read-only security expert. Audits code for vulnerabilities.",
        system_prompt="""You are a senior application security engineer.
Your task is to audit the provided code for security vulnerabilities including:
- Injection attacks (SQL, command, prompt)
- Authentication/authorisation flaws
- Secrets or credentials in source
- Insecure dependencies or configurations
- Data exposure risks

Provide a structured report with: Severity (Critical/High/Medium/Low), Location, Description, and Recommended Fix.
You may READ files but MUST NOT write, modify, or execute anything.
Be precise and cite specific line numbers.""",
        allowed_tools=["read_file", "list_project_files", "search_project"],
        tier="power",   # Security needs deep reasoning
    ),

    "code_reviewer": SubAgentProfile(
        name="code_reviewer",
        description="Read-only code reviewer. Checks style, logic, and best practices.",
        system_prompt="""You are a senior software engineer conducting a code review.
Evaluate the code for:
- Logic correctness and edge cases
- Code clarity and maintainability
- Adherence to Python best practices (PEP 8, type hints, docstrings)
- Potential performance issues
- Missing error handling

Provide actionable, line-specific feedback. Be constructive and precise.
You may READ files but MUST NOT write or execute anything.""",
        allowed_tools=["read_file", "list_project_files", "search_project", "search_memory"],
        tier="standard",
    ),

    "test_writer": SubAgentProfile(
        name="test_writer",
        description="Reads source files and writes pytest unit tests for them.",
        system_prompt="""You are a senior test engineer specialising in Python pytest.
Your task is to write comprehensive, isolated unit tests.
Guidelines:
- Use pytest fixtures and parametrize for edge cases
- Mock all external dependencies (no live API calls, no DB)
- Test happy paths, error paths, and boundary conditions
- Place tests in the tests/ directory, mirroring the source structure
- Aim for 80%+ branch coverage

You may read source files and write test files. Do NOT modify source code.""",
        allowed_tools=["read_file", "list_project_files", "search_project", "write_file"],
        tier="standard",
    ),

    "documenter": SubAgentProfile(
        name="documenter",
        description="Reads source files and writes/updates docstrings and markdown docs.",
        system_prompt="""You are a technical writer and Python documentation expert.
Your task is to improve documentation quality:
- Write clear module, class, and function docstrings (Google style)
- Update or create markdown documentation in docs/
- Ensure parameters, return types, and exceptions are documented
- Keep explanations accurate and concise

You may read files and update docstrings/markdown. Do NOT change logic or tests.""",
        allowed_tools=["read_file", "list_project_files", "search_project", "write_file"],
        tier="fast",
    ),

    "linter": SubAgentProfile(
        name="linter",
        description="Runs linting and formatting tools and reports issues.",
        system_prompt="""You are a code quality automation agent.
Run the appropriate linting/formatting tools (ruff, mypy, black --check) on the
specified files and summarise the results. Report:
- Linting errors with file, line, and rule ID
- Type errors with severity
- Whether auto-fix is safe

Do NOT modify source files directly. Report findings for human review.""",
        allowed_tools=["read_file", "list_project_files", "search_project", "run_bash_command"],
        tier="fast",
    ),
}


def list_profiles() -> dict[str, str]:
    """Return a {name: description} map of all available sub-agent profiles."""
    return {name: p.description for name, p in _PROFILES.items()}


# ---------------------------------------------------------------------------
# SubAgent Runner
# ---------------------------------------------------------------------------

class SubAgentRunner:
    """
    Runs a bounded agentic loop for a Worker Agent.

    Receives a parent tool list, restricts it to the profile's whitelist,
    builds a specialised system prompt, and executes the task.
    """

    def __init__(self, profile: SubAgentProfile, parent_tools: list, router):
        self.profile = profile
        self.router = router
        # Apply tool whitelist — only allow tools the profile has declared
        self.tools = [
            t for t in parent_tools
            if hasattr(t, "name") and t.name in profile.allowed_tools
        ]
        logger.info(
            f"🤖 SubAgent '{profile.name}' spawned | "
            f"tools={[t.name for t in self.tools]} | tier={profile.tier}"
        )

    def run(self, task: str) -> str:
        """
        Execute the sub-agent's task and return its final response.

        Args:
            task: The specific task description sent to this worker.

        Returns:
            The worker's final text response, or an error message.
        """
        try:
            llm = self.router.get(tier=self.profile.tier, temperature=0.2)
        except Exception as e:
            return f"❌ SubAgent '{self.profile.name}' could not initialise LLM: {e}"

        try:
            llm_with_tools = llm.bind_tools(self.tools) if self.tools else llm
        except Exception:
            llm_with_tools = llm

        messages: list[dict] = [
            {"role": "system", "content": self.profile.system_prompt},
            {"role": "user", "content": task},
        ]

        last_content: Optional[str] = None

        for i in range(_MAX_ITERATIONS):
            try:
                resp = llm_with_tools.invoke(messages)
                last_content = resp.content
            except Exception as e:
                logger.error(f"SubAgent '{self.profile.name}' LLM error at iteration {i}: {e}")
                return f"❌ SubAgent error at iteration {i}: {e}"

            # No tool calls → final answer
            if not resp.tool_calls:
                logger.info(f"🤖 SubAgent '{self.profile.name}' finished in {i + 1} iteration(s).")
                return resp.content or "⚠️ Sub-agent produced no output."

            # Execute tool calls
            messages.append(resp)
            for tc in resp.tool_calls:
                tool_fn = next((t for t in self.tools if t.name == tc["name"]), None)
                if tool_fn is None:
                    result = f"❌ Tool '{tc['name']}' is not in this sub-agent's allowlist."
                    logger.warning(f"SubAgent '{self.profile.name}' attempted disallowed tool: {tc['name']}")
                else:
                    try:
                        result = tool_fn.invoke(tc["args"])
                    except Exception as e:
                        result = f"Tool error: {e}"

                is_failure = isinstance(result, str) and (
                    result.startswith("❌") or 
                    "Tool error:" in result or 
                    "AssertionError" in result or
                    "error" in result.lower()
                )

                if is_failure:
                    event_bus.emit(TendrilEvent(
                        run_id="subagent", event_type="orchestrator.pruned", 
                        session_id="system", data={"tool": tc["name"]}
                    ))
                    if hasattr(resp, "tool_calls"):
                        pruned_calls = []
                        for tc_old in resp.tool_calls:
                            if tc_old["id"] == tc["id"]:
                                safe_args = {k: "[PRUNED DUE TO VALIDATION FAILURE]" if isinstance(v, str) and len(v) > 200 else v for k, v in tc_old.get("args", {}).items()}
                                pruned_calls.append({"name": tc_old["name"], "args": safe_args, "id": tc_old["id"]})
                            else:
                                pruned_calls.append(tc_old)
                        cloned_resp = AIMessage(
                            content=resp.content,
                            tool_calls=pruned_calls,
                            id=resp.id if getattr(resp, "id", None) and isinstance(resp.id, str) else None
                        )
                        messages[-1] = cloned_resp
                        resp = cloned_resp

                messages.append({
                    "role": "tool",
                    "tool_call_id": tc["id"],
                    "name": tc["name"],
                    "content": str(result),
                })

        return last_content or "⚠️ Sub-agent reached max iterations without a final answer."


def spawn(profile_name: str, task: str, parent_tools: list, router) -> str:
    """
    Public entry point. Validates profile, runs the sub-agent, returns result.

    Args:
        profile_name: Name of the sub-agent profile (see list_profiles())
        task:         The task to execute
        parent_tools: Tool list from the Root Agent (will be filtered by profile)
        router:       LLMRouter instance

    Returns:
        The sub-agent's final response string.
    """
    profile = _PROFILES.get(profile_name)
    if not profile:
        available = ", ".join(_PROFILES.keys())
        return f"❌ Unknown profile '{profile_name}'. Available: {available}"

    runner = SubAgentRunner(profile=profile, parent_tools=parent_tools, router=router)
    return runner.run(task)

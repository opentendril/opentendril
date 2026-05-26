"""
src/assessor.py — Complexity Assessor: Automatic task triaging & model tier routing.

Classifies an incoming user request into one of three complexity tiers,
then returns the appropriate tier label for the LLMRouter.

Tier definitions:
  fast     — Simple lookups, status checks, single-line changes, "what is X"
  standard — Multi-file reads, moderate refactors, debugging, explanations
  power    — Architecture changes, security audits, full rewrites, deep reasoning

Fail-safe: any error (network, parsing, timeout) returns "standard" so the
user always gets a reasonable response even if the assessor is unavailable.

Cost profile: uses the "fast" tier model (~$0.0001 per assessment).
Latency target: < 500ms added overhead per request.
"""

import logging
from typing import Literal

from langchain_core.language_models.chat_models import BaseChatModel

from .eventbus import event_bus, TendrilEvent

logger = logging.getLogger(__name__)

Tier = Literal["fast", "standard", "power"]

_ASSESSOR_PROMPT = """\
You are a task complexity classifier for an AI coding assistant.

Classify the user's request into exactly one tier:

fast     — Trivial: greetings, status checks, simple lookups, single-word/line changes,
           "what is X", "list files", "show me Y", no code reasoning required.

standard — Moderate: reading multiple files, explaining code, simple bug fixes,
           adding a function, writing tests, refactoring a single module.

power    — Complex: cross-file architecture changes, security analysis, full rewrites,
           deep multi-step reasoning, designing systems, resolving subtle bugs,
           anything requiring sustained chain-of-thought.

Respond with ONLY one word: fast, standard, or power. No explanation.\
"""


def assess_complexity(message: str, llm: BaseChatModel) -> Tier:
    """
    Classify a user message into a complexity tier.

    Args:
        message: The raw user request text.
        llm:     A fast/cheap LLM instance to use for classification.

    Returns:
        "fast", "standard", or "power" — defaults to "standard" on any error.
    """
    try:
        response = llm.invoke([
            {"role": "system", "content": _ASSESSOR_PROMPT},
            {"role": "user", "content": message[:1000]},  # Trim to avoid large inputs
        ])
        raw = str(response.content).strip().lower().split()[0]
        if raw in ("fast", "standard", "power"):
            logger.info(f"🎯 Complexity assessment: '{raw}' for message: {message[:60]!r}")
            return raw  # type: ignore[return-value]
        logger.warning(f"⚠️  Unexpected assessor output: {raw!r}, defaulting to 'standard'")
    except Exception as exc:
        logger.warning(f"⚠️  Complexity assessor failed ({exc}), defaulting to 'standard'")
    return "standard"


def assess_and_route(
    message: str,
    router,
    provider: str,
    requested_tier: str = "auto",
) -> tuple[str, str]:
    """
    Determine the final provider and tier for a request.

    If tier is "auto", runs the complexity assessor using the provider's fast
    model and returns the assessed tier. Otherwise returns the requested tier
    unchanged (honouring explicit user overrides).

    Args:
        message:        The user's request.
        router:         LLMRouter instance.
        provider:       Active provider name.
        requested_tier: "auto" | "fast" | "standard" | "power"

    Returns:
        Tuple of (provider, resolved_tier).
    """
    if requested_tier != "auto":
        return provider, requested_tier

    try:
        fast_llm = router.get(provider=provider, tier="fast", temperature=0.0)
        tier = assess_complexity(message, fast_llm)
    except Exception as exc:
        logger.warning(f"⚠️  Could not build fast LLM for assessor ({exc}), defaulting to 'standard'")
        tier = "standard"

    return provider, tier


_REPLAN_PROMPT = """\
You are the Strategic Architect for an AI coding assistant.
The agent previously attempted to solve the following task but encountered severe validation failures.

Original Task:
{task}

Failure Trace:
{error_trace}

Your objective is to revise the execution plan to circumvent these errors.
Output a strict JSON array of objects representing the revised plan steps.
Example:
[
  { "step": 1, "action": "spawn_sub_agent", "profile": "security_auditor", "instruction": "Audit the file before modifications." },
  { "step": 2, "action": "direct_modification", "instruction": "Apply minimal fix to X without touching Y." }
]
Output ONLY valid JSON.\
"""

def revise_execution_plan(task: str, error_trace: str, llm: BaseChatModel, session_id: str = "system") -> str:
    """
    Generate a revised execution plan based on previous failures.
    """
    event_bus.emit(TendrilEvent(
        run_id="replan",
        event_type="assessor.replan_start",
        session_id=session_id,
        data={"task_preview": task[:100], "error_preview": error_trace[:100]}
    ))
    
    try:
        prompt = _REPLAN_PROMPT.format(task=task, error_trace=error_trace)
        response = llm.invoke([{"role": "user", "content": prompt}])
        plan = str(response.content).strip()
        # strip markdown block if present
        if plan.startswith("```json"):
            plan = plan[7:]
        if plan.endswith("```"):
            plan = plan[:-3]
        return plan.strip()
    except Exception as exc:
        logger.warning(f"⚠️  Assessor replan failed ({exc})")
        return '[\n  { "step": 1, "action": "fallback", "instruction": "Retry cautiously with a smaller scope." }\n]'

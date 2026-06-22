"""
=============================================================================
Tendril: ElongationTendril
Description: Executed inside the ephemeral container. Handles the LLM interaction loop and task execution.
Revision History:
- 2026-06-21: Initial creation (OpenTendril Bot)
=============================================================================
"""
import os
import json
import sys
import logging
from typing import Optional, List

# Basic logging setup for the Tendril container
logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")
logger = logging.getLogger("tendril.elongation")

def load_persona(persona_name: str) -> dict:
    """Load the persona markdown file and extract metadata."""
    # This is a naive search. In production, we'd recursively search all persona dirs.
    # For now, we check personas/onboarding/
    workspace = os.environ.get("TENDRIL_WORKSPACE_ROOT", "/workspace")
    path = os.path.join(workspace, "personas", "onboarding", f"{persona_name}.md")
    
    if not os.path.exists(path):
        return {"error": f"Persona '{persona_name}' not found at {path}"}
        
    with open(path, "r", encoding="utf-8") as f:
        content = f.read()
        
    # Naive extraction of Allowed Tools (if present)
    allowed_tools = "all"
    tier = "standard"
    
    for line in content.splitlines():
        clean_line = line.replace("-", "").replace("*", "").strip().lower()
        if clean_line.startswith("allowed tools:"):
            tools_raw = clean_line.split(":", 1)[1].strip()
            if tools_raw == "all":
                allowed_tools = "all"
            else:
                # parse [read_file, write_file]
                clean_tools = tools_raw.replace("[", "").replace("]", "").replace('"', '').replace("'", "")
                allowed_tools = [t.strip() for t in clean_tools.split(",") if t.strip()]
        elif clean_line.startswith("tier:"):
            tier = clean_line.split(":", 1)[1].strip()
            
    return {
        "content": content,
        "allowed_tools": allowed_tools,
        "tier": tier
    }

def elongate():
    """
    Phase 3: Elongation.
    This script is executed INSIDE the ephemeral Tendril container/process.
    It actively searches for its goal by executing the LLM inference loop, 
    returns the result via stdout, and terminates.
    """
    tendril_id = os.environ.get("TENDRIL_ID")
    persona_name = os.environ.get("TENDRIL_PERSONA")
    task = os.environ.get("TENDRIL_TASK")
    workspace = os.environ.get("TENDRIL_WORKSPACE_ROOT", "/workspace")
    
    if not all([tendril_id, persona_name, task]):
        print(json.dumps({"error": "Missing required Tendril context in environment."}))
        sys.exit(1)

    # Load Genome (Context Seeds)
    genome_dir = os.path.join(workspace, ".tendril", "genome")
    genome_context = []
    if os.path.isdir(genome_dir):
        import logging
        logging.info("🧬 Reading Genome context seeds...")
        for f in sorted(os.listdir(genome_dir)):
            if f.endswith(".md"):
                try:
                    with open(os.path.join(genome_dir, f), 'r') as fp:
                        genome_context.append(f"\n### {f} ###\n{fp.read().strip()}")
                except Exception as e:
                    logging.warning(f"Failed to read genome file {f}: {e}")
    
    combined_genome = "\n".join(genome_context)
    # The combined_genome is now ready to be injected into the system prompt when the ReAct loop executes.
        
    logger.info(f"🌿 Elongation initiated for Tendril '{tendril_id}' (Persona: {persona_name})")
    
    # 1. Load Persona
    persona_data = load_persona(persona_name)
    if "error" in persona_data:
        print(json.dumps({"error": persona_data["error"]}))
        sys.exit(1)
        
    # 2. Setup Headless Dependencies
    # In an isolated container, we boot our own router, editor, etc.
    from src.llmrouter import LLMRouter
    from src.editor import FileEditor
    from src.gitmanager import GitManager
    from src.testrunner import TestRunner
    from src.approval import ApprovalGate
    from src.memory import Memory
    from src.skillsmanager import SkillsManager
    from src.agent.tools import ToolFactory
    from langchain_core.messages import AIMessage
    
    router = LLMRouter()
    editor = FileEditor(sandbox_root=workspace, enforce_protection=True)
    git = GitManager(repo_path=workspace)
    approval = ApprovalGate(auto_approve=True)
    tester = TestRunner(approval_gate=approval, cwd=workspace)
    memory = Memory()
    skills_manager = SkillsManager()
    
    factory = ToolFactory(memory, editor, git, tester, router, skills_manager)
    all_tools = factory.build()
    
    # 3. Filter Tools based on Persona Allowlist
    allowed = persona_data["allowed_tools"]
    if allowed == "all":
        tools = all_tools
    else:
        tools = [t for t in all_tools if getattr(t, "name", "") in allowed]
        
    logger.info(f"🔧 Tendril armed with {len(tools)} tools: {[getattr(t, 'name', 'unknown') for t in tools]}")
    
    # 4. Initialize LLM
    try:
        llm = router.get(tier=persona_data["tier"], temperature=0.2)
        llm_with_tools = llm.bind_tools(tools) if tools else llm
    except Exception as e:
        print(json.dumps({"error": f"LLM Initialization failed: {e}"}))
        sys.exit(1)
        
    system_content = persona_data["content"]
    if combined_genome:
        system_content += f"\n\n# Project Genome (Core Architectural Rules)\n{combined_genome}"
        
    messages = [
        {"role": "system", "content": system_content},
        {"role": "user", "content": task}
    ]
    
    last_content = None
    MAX_ITERATIONS = 15
    
    # 5. Elongation Loop (ReAct)
    for i in range(MAX_ITERATIONS):
        logger.info(f"🔄 LLM Iteration {i+1}/{MAX_ITERATIONS}...")
        try:
            resp = llm_with_tools.invoke(messages)
            last_content = resp.content
        except Exception as e:
            print(json.dumps({"error": f"LLM execution error at iteration {i}: {e}"}))
            sys.exit(1)
            
        if not resp.tool_calls:
            # Reached a final answer
            break
            
        messages.append(resp)
        for tc in resp.tool_calls:
            tool_name = tc["name"]
            tool_args = tc["args"]
            logger.info(f"🛠️  Executing tool: {tool_name}")
            
            tool_fn = next((t for t in tools if getattr(t, "name", "") == tool_name), None)
            if tool_fn is None:
                result = f"❌ Tool '{tool_name}' is not in this Tendril's allowlist."
            else:
                try:
                    result = tool_fn.invoke(tool_args)
                except Exception as e:
                    result = f"Tool error: {e}"
                    
            messages.append({
                "role": "tool",
                "tool_call_id": tc["id"],
                "name": tool_name,
                "content": str(result)
            })
    else:
        # Loop completely exhausted
        last_content = last_content or "⚠️ Tendril reached max iterations without a final answer."

    result_payload = {
        "tendril_id": tendril_id,
        "persona": persona_name,
        "status": "completed",
        "output": last_content or "⚠️ Tendril produced no output."
    }
    
    # Output JSON exactly on the last line for the Stem to read
    print(json.dumps(result_payload))

if __name__ == "__main__":
    elongate()

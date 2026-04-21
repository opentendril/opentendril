"""
Tendril Orchestrator — The brain that ties everything together.

Uses LLM Router for multi-model dispatch, File Editor for self-building,
Model Failover for resilient invocation, and Approval Gate for safe operations.
"""

import json
import os
import hmac
import hashlib
import logging
from typing import Optional

from langchain_core.tools import tool

from .config import SECRET_KEY, WORKSPACE_ROOT, PROJECT_ROOT
from .llmrouter import LLMRouter
from .memory import Memory
from .skillsmanager import SkillsManager
from .editor import FileEditor
from .approval import ApprovalGate
from .gitmanager import GitManager
from .testrunner import TestRunner
from .credits import credit_manager
from .chronicler import chronicler
from .failover import ModelFailover, AllProvidersFailed
from .eventbus import event_bus, TendrilEvent, generate_run_id
from .patcher import parse_patch, validate_patch, apply_patch, format_patch_for_prompt, PatchParseError

logger = logging.getLogger(__name__)


@tool
def calculator(expression: str) -> str:
    """Solve math problems with expressions like 2+2*(3**2)."""
    try:
        from sympy import sympify
        result = sympify(expression).evalf()
        return str(result)
    except Exception:
        return "Invalid expression. Use basic math ops: + - * / ** ()"


class Orchestrator:
    """
    Tendril's central orchestrator.

    Coordinates between LLMs (via Router), file editing (via Editor),
    memory (via RAG), and skills to process user requests.
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
        self.git = GitManager()
        self.tester = TestRunner(self.approval)
        self.failover = ModelFailover(self.router)
        self.tools = self._create_tools()

    def _create_tools(self) -> list:
        router = self.router
        memory = self.memory
        skills_manager = self.skills_manager
        editor = self.editor
        git = self.git
        tester = self.tester

        @tool
        def search_memory(query: str) -> str:
            """Search long-term memory and past conversations for relevant info."""
            docs = memory.retrieve_relevant(query, k=5)
            if not docs:
                return "No relevant memories found."
            return "\n---\n".join(doc.page_content for doc in docs)

        @tool
        def build_skill(description: str) -> str:
            """Build a new signed skill to extend Tendril's capabilities. Describe what it should do."""
            llm = router.get(tier="standard")
            gen_prompt = (
                f"Generate JSON for a new skill:\n{description}\n\n"
                f'Format: {{"name": "snake_case_name", "description": "brief", '
                f'"system_prompt": "detailed instructions for using this skill"}}'
            )
            resp = llm.invoke(gen_prompt)
            try:
                skill_data = json.loads(resp.content)
                content_str = json.dumps(
                    {k: v for k, v in skill_data.items() if k != "signature"},
                    sort_keys=True,
                )
                sig = hmac.new(
                    SECRET_KEY.encode(), content_str.encode(), hashlib.sha256
                ).hexdigest()
                skill_data["signature"] = sig

                dyn_dir = "/app/data/dynamic-skills"
                os.makedirs(dyn_dir, exist_ok=True)
                fname = f"{skill_data['name']}.skill.json"
                path = os.path.join(dyn_dir, fname)
                with open(path, "w") as f:
                    json.dump(skill_data, f, indent=2)

                skills_manager.load_skills()
                return f"✅ Built and loaded skill '{skill_data['name']}' at {path}"
            except Exception as e:
                return f"❌ Skill build failed: {str(e)}"

        @tool
        def read_file(filepath: str) -> str:
            """Read a file from the project source directory."""
            try:
                content = editor.read(filepath)
                return f"--- {filepath} ---\n{content}"
            except Exception as e:
                return f"❌ Cannot read {filepath}: {str(e)}"

        @tool
        def write_file(filepath: str, content: str) -> str:
            """Write or update a file in the project source directory. Shows diff of changes."""
            try:
                diff = editor.generate_diff(filepath, content)
                result = editor.write(filepath, content)
                return f"✅ {result['action'].title()} {filepath}\n\nDiff:\n{diff}"
            except Exception as e:
                return f"❌ Cannot write {filepath}: {str(e)}"

        @tool
        def apply_code_patch(patch_text: str) -> str:
            """Apply a structured multi-file patch. Use the *** Begin Patch / *** End Patch format for surgical edits."""
            try:
                operations = parse_patch(patch_text)
                errors = validate_patch(operations, editor)
                if errors:
                    return f"❌ Patch validation failed:\n" + "\n".join(f"  - {e}" for e in errors)
                result = apply_patch(operations, editor)
                return f"✅ Patch applied: {result.file_count} file(s)\n{result.summary}"
            except PatchParseError as e:
                return f"❌ Patch parse error: {str(e)}"
            except Exception as e:
                return f"❌ Patch failed: {str(e)}"

        @tool
        def staged_edit(filepath: str, patch_text: str, description: str) -> str:
            """Safely modify a PROTECTED file through the staging pipeline.

            This is the ONLY way to modify kernel files (main.py, tendril.py, etc).
            It creates a git branch, applies a surgical patch, runs validation, commits,
            and switches back to main for human review.

            Args:
                filepath: The file to modify (can be a protected file)
                patch_text: A patch in *** Begin Patch / *** End Patch format describing the surgical change
                description: Brief description of the change (used as commit message)
            """
            import subprocess
            import re
            from datetime import datetime
            from .editor import FileEditor

            # Create a staging editor with protection DISABLED
            staging_editor = FileEditor(sandbox_root=editor.sandbox_root, enforce_protection=False)

            try:
                # 1. Generate branch name from description
                slug = re.sub(r'[^a-z0-9]+', '-', description.lower().strip())[:40].strip('-')
                branch_name = f"staging/{slug}-{datetime.now().strftime('%H%M%S')}"

                # 2. Create branch
                try:
                    git.create_branch(branch_name)
                except Exception as e:
                    return f"❌ Cannot create branch '{branch_name}': {str(e)}"

                # 3. Apply the patch (surgical edit, not full rewrite)
                try:
                    operations = parse_patch(patch_text)
                    errors = validate_patch(operations, staging_editor)
                    if errors:
                        git.checkout("main")
                        git._run_git("branch", "-D", branch_name)
                        return f"❌ Patch validation failed:\n" + "\n".join(f"  - {e}" for e in errors)
                    result = apply_patch(operations, staging_editor)
                except PatchParseError as e:
                    git.checkout("main")
                    git._run_git("branch", "-D", branch_name)
                    return f"❌ Patch parse error: {str(e)}"

                # 4. Syntax validation for Python files
                if filepath.endswith('.py'):
                    try:
                        resolved = staging_editor._resolve_path(filepath)
                        subprocess.run(
                            ["python3", "-m", "py_compile", resolved],
                            capture_output=True, text=True, check=True
                        )
                    except subprocess.CalledProcessError as e:
                        # Syntax error — revert and switch back to main
                        git._run_git("checkout", "main", "--", filepath)
                        git._run_git("checkout", "main")
                        git._run_git("branch", "-D", branch_name)
                        return (
                            f"❌ SYNTAX ERROR — change rejected and reverted.\n"
                            f"Branch '{branch_name}' deleted.\n"
                            f"Error: {e.stderr}"
                        )

                # 5. Commit on the staging branch (never GPG sign in container)
                git._run_git("add", filepath)
                git._run_git("commit", "--no-gpg-sign", "-m", f"staging: {description}",
                             "-m", "Co-authored-by: Tendril <tendril@jurnx.com>")

                # 6. Switch back to main (leave branch for testing/merging)
                git.checkout("main")

                return (
                    f"✅ Staged edit committed on branch '{branch_name}'\n"
                    f"Patch applied: {result.file_count} file(s)\n"
                    f"{result.summary}\n\n"
                    f"To test this change:\n"
                    f"  git checkout {branch_name}\n"
                    f"  docker compose up --build\n\n"
                    f"To merge after testing:\n"
                    f"  git checkout main\n"
                    f"  git merge {branch_name}\n"
                    f"  git push origin main\n"
                )

            except Exception as e:
                # Safety: always try to get back to main
                try:
                    git.checkout("main")
                except Exception:
                    pass
                return f"❌ Staged edit failed: {str(e)}"

        @tool
        def list_project_files(directory: str = "") -> str:
            """List all editable files in the project source directory."""
            try:
                files = editor.list_files(directory)
                if not files:
                    return "No files found."
                lines = [f"  {f['path']} ({f['size']} bytes)" for f in files]
                return f"Project files ({len(files)} total):\n" + "\n".join(lines)
            except Exception as e:
                return f"❌ Cannot list files: {str(e)}"
                
        @tool
        def search_project(query: str, directory: str = "") -> str:
            """Search for a specific string across all editable project files. Returns filename and line context."""
            try:
                results = editor.search_project(query, directory)
                if not results:
                    return f"No results found for '{query}'."
                
                formatted = [f"{r['file']}:{r['line']} - {r['content']}" for r in results]
                # Limit output size so we don't blow context
                max_results = 20
                if len(formatted) > max_results:
                    formatted = formatted[:max_results] + [f"...and {len(results) - max_results} more results."]
                
                return f"Search results for '{query}':\n" + "\n".join(formatted)
            except Exception as e:
                return f"❌ Cannot search project: {str(e)}"
                
        @tool
        def read_logs(lines: int = 50) -> str:
            """Read the most recent application logs to diagnose errors or monitor health."""
            try:
                log_path = os.path.join("/app/logs/tendril.log")
                if not os.path.exists(log_path):
                    # Fallback to outside container config if needed
                    log_path = os.path.join(os.path.dirname(os.path.dirname(__file__)), "logs", "tendril.log")
                    if not os.path.exists(log_path):
                        return "❌ Log file not found."
                
                with open(log_path, 'r', encoding='utf-8') as f:
                    all_lines = f.readlines()
                    last_lines = all_lines[-lines:]
                    
                # Basic redaction to prevent accidental token leakage in output
                redacted_lines = []
                for line in last_lines:
                    if "api_key" in line.lower() or "password" in line.lower() or "secret" in line.lower():
                        redacted_lines.append(line.split(":")[0] + ": [REDACTED_BY_SYSTEM]")
                    else:
                        redacted_lines.append(line)
                        
                return "".join(redacted_lines)
            except Exception as e:
                return f"❌ Cannot read logs: {str(e)}"
                
        @tool
        def git_commit(message: str) -> str:
            """Commit all changes in the project with the given message."""
            try:
                result = git.commit_changes(message)
                if "✅" in result:
                    chronicler.log_commit(message)
                return result
            except Exception as e:
                return f"❌ Git commit failed: {str(e)}"
                
        @tool
        def git_create_branch(branch_name: str) -> str:
            """Create and checkout a new git branch."""
            try:
                return git.create_branch(branch_name)
            except Exception as e:
                return f"❌ Git branch failed: {str(e)}"
                
        @tool
        def git_status() -> str:
            """Get the current git status."""
            try:
                return git.status()
            except Exception as e:
                return f"❌ Git status failed: {str(e)}"
                
        @tool
        def create_pull_request(title: str, body: str, head_branch: str) -> str:
            """Create a pull request on GitHub to opentendril/core."""
            try:
                return git.create_pull_request("opentendril/core", title, body, head_branch)
            except Exception as e:
                return f"❌ PR creation failed: {str(e)}"
                
        @tool
        async def run_bash_command(command: str) -> str:
            """Run a bash command or test suite (e.g. 'pytest', 'npm test'). Will ask for approval."""
            try:
                return await tester.run_command(command, safe=False)
            except Exception as e:
                return f"❌ Command execution failed: {str(e)}"

        return [
            calculator, search_memory, build_skill, read_file, write_file,
            apply_code_patch, staged_edit,
            list_project_files, search_project, read_logs,
            git_commit, git_create_branch, git_status, create_pull_request,
            run_bash_command
        ]

    def process(
        self,
        session_id: str,
        message: str,
        provider: Optional[str] = None,
        tier: str = "standard",
    ) -> str:
        """
        Process a user message through the orchestrator.

        Args:
            session_id: Conversation session ID
            message: User's message
            provider: LLM provider override (None = default)
            tier: Model tier ("fast", "standard", "power")

        Returns:
            Response text from the LLM
        """
        # Credit Check
        if not credit_manager.validate_request(session_id):
            return "❌ Access Denied: Insufficient credits. Please upgrade at cloud.opentendril.com"

        run_id = generate_run_id()
        event_bus.emit(TendrilEvent(
            run_id=run_id,
            event_type="request.start",
            session_id=session_id,
            data={"message_preview": message[:100], "provider": provider or "default", "tier": tier},
        ))

        history = self.memory.get_convo(session_id)
        relevant_docs = self.memory.retrieve_relevant(message, session_id=session_id)
        rag_context = "\n".join(doc.page_content for doc in relevant_docs) if relevant_docs else "None"
        skills_context = self.skills_manager.get_context() or "No skills loaded."
        patch_format = format_patch_for_prompt()

        # Build tool descriptions for the system prompt
        tool_descriptions = "\n".join(
            f"  - {t.name}: {t.description}" for t in self.tools
        )

        # Detect external project mode
        is_external = WORKSPACE_ROOT != PROJECT_ROOT

        if is_external:
            # Survey the external project files for context
            try:
                project_files = self.editor.list_files()
                file_listing = "\n".join(
                    f"  {f['path']} ({f['size']} bytes)" for f in project_files[:80]
                )
                if len(project_files) > 80:
                    file_listing += f"\n  ... and {len(project_files) - 80} more files"
            except Exception:
                file_listing = "  (could not scan project files)"

            system_prompt = f"""You are Tendril — an AI coding assistant. You help developers read, understand, edit, and improve their code.

## Your Workspace
You are working on an EXTERNAL PROJECT mounted at {WORKSPACE_ROOT}.
This is NOT Tendril's own source code. You are a coding assistant for this project.

Project files:
{file_listing}

There are NO protected files. You can read and write any file in the workspace.

## Available Tools
{tool_descriptions}

## Relevant Memories
{rag_context}

{patch_format}

## Behavioral Guidelines
- Use tools via function calls when helpful
- For multi-file or surgical edits, prefer apply_code_patch over write_file
- When editing files, always show the diff
- Use list_project_files and read_file to understand the project before making changes
- Use search_project to find where things are defined or used
- Use git_commit to save your changes with descriptive commit messages
- Be concise unless the user asks for detail
- If you're not sure about the project structure, explore it first with list_project_files and read_file"""
        else:
            system_prompt = f"""You are Tendril — The Root Agent. You are an AI software development orchestrator that helps users build, debug, and modify software.

## What You Are
You are running inside the Tendril kernel, a self-building AI agent system. You ARE the orchestrator — the brain that processes user requests using tools, memory, and LLM reasoning.

## Your Project Structure
You are deployed in a Docker container. Your workspace is: {os.getenv('TENDRIL_WORKSPACE_ROOT', '/app')}

Key directories and files you can READ:
  src/main.py          - FastAPI server (your API gateway)
  src/tendril.py       - Your own orchestrator code (this file)
  src/editor.py        - File editor with sandbox protection
  src/llmrouter.py     - Multi-provider LLM routing
  src/failover.py      - Model failover with exponential backoff
  src/eventbus.py      - Structured event logging
  src/patcher.py       - Surgical patch format
  src/dreamer.py       - Background insight generation
  src/memory.py        - RAG/vector memory
  static/index.html    - Web UI (HTML)
  static/styles.css    - Web UI (CSS styles)
  static/app.js        - Web UI (JavaScript)
  gateway/             - Go WebSocket Chat Gateway
  cli/                 - Go CLI client

## ⚠️ PROTECTED FILES — DO NOT MODIFY
The following files are PROTECTED and CANNOT be written to via write_file or apply_code_patch:
  src/main.py, src/tendril.py, src/config.py, src/editor.py, src/patcher.py,
  src/llmrouter.py, src/failover.py, src/eventbus.py, src/memory.py,
  static/styles.css, static/index.html, static/app.js,
  GUARDRAILS.md, DECISIONS.md, ARCHITECTURE.md, docker-compose.yml,
  Dockerfile, requirements.txt

To modify protected files, use the `staged_edit` tool. It safely:
  1. Creates a git branch (staging/your-change-name)
  2. Applies the change with protection bypassed
  3. Runs syntax validation (auto-revert on failure)
  4. Commits and pushes for human review / PR
  5. Returns instructions to test and merge

NEVER use write_file or apply_code_patch on protected files — they will fail with PermissionError.
ALWAYS use staged_edit for protected files — it is the safe, reviewable path.

## Your Chat Interface
The user is talking to you through EITHER:
  1. The Web UI at http://localhost:8080/chat (HTML page with a chat interface)
  2. The tendril-cli terminal client
  3. The WebSocket gateway on port 9090

When the user refers to "the chat", "the UI", "the text box", "the screen" — they mean the Web UI files (static/index.html, static/styles.css, static/app.js). You CAN read these files and modify them via staged_edit.

## Available Tools
{tool_descriptions}

## Loaded Skills
{skills_context}

## Relevant Memories
{rag_context}

{patch_format}

## Behavioral Guidelines
- Use tools via function calls when helpful
- For multi-file or surgical edits, prefer apply_code_patch over write_file
- When editing files, always show the diff
- Self-Diagnosis: Use `read_logs` and `search_project` proactively if a user reports a bug
- Be concise unless the user asks for detail
- For PROTECTED files: use `staged_edit` (creates branch, validates, commits for review)
- For UNPROTECTED files: use `write_file` or `apply_code_patch` as normal
- When using staged_edit, tell the user: the change is on a branch, here's how to test and merge it"""

        messages = [
            {"role": "system", "content": system_prompt},
        ] + history[-8:] + [
            {"role": "user", "content": message},
        ]

        # Select provider using failover chain (skip providers in cooldown)
        selected_provider = provider
        for candidate in self.failover._build_candidate_chain(provider, tier):
            state = self.failover._get_state(candidate)
            if not state.is_in_cooldown:
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

        # Bind tools to the LLM for function calling
        try:
            llm_with_tools = llm.bind_tools(self.tools)
        except Exception:
            # Some models/providers don't support tool binding
            llm_with_tools = llm

        # Agentic loop: call LLM, execute tools, repeat
        # Increased to 20 to allow for complex multi-file read/write self-edits
        max_iterations = 20
        last_response_content = None
        for i in range(max_iterations):
            try:
                import time as _time
                _start = _time.time()
                resp = llm_with_tools.invoke(messages)
                _latency = (_time.time() - _start) * 1000
                self.failover._get_state(selected_provider).record_success(_latency)
                last_response_content = resp.content
            except Exception as e:
                from .failover import classify_error
                reason = classify_error(e)
                self.failover._get_state(selected_provider).record_failure(reason)
                logger.error(f"LLM invocation error: {e}")
                event_bus.emit(TendrilEvent(
                    run_id=run_id, event_type="request.error",
                    session_id=session_id, data={"error": str(e)[:200], "iteration": i, "reason": reason},
                ))
                return f"Sorry, I encountered an error communicating with the LLM: {str(e)}"

            # If no tool calls, return the text response
            if not resp.tool_calls:
                credit_manager.consume_request(session_id)
                event_bus.emit(TendrilEvent(
                    run_id=run_id, event_type="request.end",
                    session_id=session_id, data={"iterations": i + 1, "content": str(resp.content)},
                ))
                # Drift detection check
                if last_response_content:
                    import difflib
                    similarity = difflib.SequenceMatcher(None, message, last_response_content).ratio()
                    if similarity < 0.15:
                        logger.warning(f"Drift detected in session {session_id}: similarity {similarity:.2f} between user message and last response")
                return resp.content or "I processed your request but have no text response."

            # Execute tool calls
            messages.append(resp)
            for tool_call in resp.tool_calls:
                tool_name = tool_call["name"]
                tool_args = tool_call["args"]

                event_bus.emit(TendrilEvent(
                    run_id=run_id, event_type="tool.start",
                    session_id=session_id, data={"name": tool_name, "args": tool_args}
                ))

                tool_func = next((t for t in self.tools if t.name == tool_name), None)

                if tool_func:
                    try:
                        tool_result = tool_func.invoke(tool_args)
                    except Exception as e:
                        tool_result = f"Tool error: {str(e)}"
                else:
                    tool_result = f"Unknown tool: {tool_name}"

                event_bus.emit(TendrilEvent(
                    run_id=run_id, event_type="tool.end",
                    session_id=session_id, data={"name": tool_name, "result_preview": str(tool_result)[:200]}
                ))

                messages.append({
                    "role": "tool",
                    "tool_call_id": tool_call["id"],
                    "name": tool_name,
                    "content": str(tool_result),
                })

        # Drift detection check after loop
        if last_response_content:
            import difflib
            similarity = difflib.SequenceMatcher(None, message, last_response_content).ratio()
            if similarity < 0.15:
                logger.warning(f"Drift detected in session {session_id}: similarity {similarity:.2f} between user message and last response")

        return "⚠️ Reached maximum tool iterations. The task may be too complex — try breaking it into smaller steps."

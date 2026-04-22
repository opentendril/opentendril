"""
src/agent/tools.py — ToolFactory: all LangChain @tool definitions for the Root Agent.

Isolated here so each tool can be individually read, understood, and modified
without touching orchestration logic. Max file size target: 300 lines.

Tools registered:
  Core:    search_memory, build_skill, read_file, write_file, apply_code_patch,
           list_project_files, search_project, read_logs, run_bash_command, calculator
  Git:     git_commit, git_create_branch, git_status, create_pull_request,
           staged_edit, merge_staging_branch, cleanup_staging_branches
"""

import json
import os
import re
import hmac
import hashlib
import logging
import subprocess
import time as _time
from datetime import datetime

from langchain_core.tools import tool

from ..config import SECRET_KEY
from ..patcher import parse_patch, validate_patch, apply_patch, PatchParseError
from ..chronicler import chronicler

logger = logging.getLogger(__name__)


@tool
def calculator(expression: str) -> str:
    """Solve math problems with expressions like 2+2*(3**2)."""
    try:
        from sympy import sympify
        return str(sympify(expression).evalf())
    except Exception:
        return "Invalid expression. Use basic math ops: + - * / ** ()"


class ToolFactory:
    """
    Builds the full list of LangChain tools available to the Root Agent.
    Dependencies are injected via constructor to allow testing and future
    container-level isolation.
    """

    def __init__(self, memory, editor, git, tester, router, skills_manager):
        self.memory = memory
        self.editor = editor
        self.git = git
        self.tester = tester
        self.router = router
        self.skills_manager = skills_manager
        self.git_available = git is not None

    def build(self) -> list:
        """Return the full list of tools for the current environment."""
        tools = self._core_tools()
        if self.git_available:
            tools += self._git_tools()
            logger.info("🔀 Git tools registered (repo detected).")
        else:
            logger.info("⚠️  No .git directory found — git tools and staged_edit disabled.")
        return tools

    # -------------------------------------------------------------------------
    # Core Tools
    # -------------------------------------------------------------------------

    def _core_tools(self) -> list:
        memory = self.memory
        editor = self.editor
        tester = self.tester
        router = self.router
        skills_manager = self.skills_manager

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
                    {k: v for k, v in skill_data.items() if k != "signature"}, sort_keys=True
                )
                sig = hmac.new(SECRET_KEY.encode(), content_str.encode(), hashlib.sha256).hexdigest()
                skill_data["signature"] = sig
                dyn_dir = "/app/data/dynamic-skills"
                os.makedirs(dyn_dir, exist_ok=True)
                path = os.path.join(dyn_dir, f"{skill_data['name']}.skill.json")
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
                return f"--- {filepath} ---\n{editor.read(filepath)}"
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
                    return "❌ Patch validation failed:\n" + "\n".join(f"  - {e}" for e in errors)
                result = apply_patch(operations, editor)
                return f"✅ Patch applied: {result.file_count} file(s)\n{result.summary}"
            except PatchParseError as e:
                return f"❌ Patch parse error: {str(e)}"
            except Exception as e:
                return f"❌ Patch failed: {str(e)}"

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
                log_path = "/app/logs/tendril.log"
                if not os.path.exists(log_path):
                    log_path = os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(__file__))), "logs", "tendril.log")
                    if not os.path.exists(log_path):
                        return "❌ Log file not found."
                with open(log_path, "r", encoding="utf-8") as f:
                    all_lines = f.readlines()
                redacted = []
                for line in all_lines[-lines:]:
                    if any(kw in line.lower() for kw in ("api_key", "password", "secret")):
                        redacted.append(line.split(":")[0] + ": [REDACTED_BY_SYSTEM]")
                    else:
                        redacted.append(line)
                return "".join(redacted)
            except Exception as e:
                return f"❌ Cannot read logs: {str(e)}"

        @tool
        async def run_bash_command(command: str) -> str:
            """Run a bash command or test suite (e.g. 'pytest', 'npm test'). Will ask for approval."""
            try:
                return await tester.run_command(command, safe=False)
            except Exception as e:
                return f"❌ Command execution failed: {str(e)}"

        return [
            calculator, search_memory, build_skill, read_file, write_file,
            apply_code_patch, list_project_files, search_project,
            read_logs, run_bash_command,
        ]

    # -------------------------------------------------------------------------
    # Git Tools (only when .git directory exists)
    # -------------------------------------------------------------------------

    def _git_tools(self) -> list:
        git = self.git
        editor = self.editor

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
            """Get the current git status of the project."""
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
        def staged_edit(filepath: str, patch_text: str, description: str, skip_canary: bool = False) -> str:
            """Safely modify a PROTECTED file through the staging pipeline.

            This is the ONLY way to modify kernel files (main.py, tendril.py, etc).
            It creates a git branch, applies a surgical patch, runs validation,
            commits, and switches back to the default branch for human review.

            Args:
                filepath:     The file to modify (can be a protected file)
                patch_text:   A patch in *** Begin Patch / *** End Patch format
                description:  Brief description of the change (used as commit message)
                skip_canary:  If True, skip the Docker canary-build check.
            """
            from ..editor import FileEditor

            staging_editor = FileEditor(sandbox_root=editor.sandbox_root, enforce_protection=False)

            def _default_branch() -> str:
                try:
                    ref = git._run_git("symbolic-ref", "refs/remotes/origin/HEAD").strip().replace("refs/remotes/origin/", "")
                    if ref:
                        return ref
                except Exception:
                    pass
                try:
                    return git._run_git("rev-parse", "--abbrev-ref", "HEAD").strip()
                except Exception:
                    return "main"

            default_branch = _default_branch()
            stash_ref = None
            try:
                stash_out = git._run_git("stash", "push", "--include-untracked", "-m", "tendril-staged-edit-temp")
                if "No local changes" not in stash_out:
                    stash_ref = "stash@{0}"
            except Exception:
                pass

            try:
                slug = re.sub(r'[^a-z0-9]+', '-', description.lower().strip())[:40].strip('-')
                branch_name = f"staging/{slug}-{datetime.now().strftime('%H%M%S')}"

                try:
                    git.create_branch(branch_name)
                except Exception as e:
                    return f"❌ Cannot create branch '{branch_name}': {str(e)}"

                try:
                    operations = parse_patch(patch_text)
                    errors = validate_patch(operations, staging_editor)
                    if errors:
                        git.checkout(default_branch)
                        git._run_git("branch", "-D", branch_name)
                        return "❌ Patch validation failed:\n" + "\n".join(f"  - {e}" for e in errors)
                    result = apply_patch(operations, staging_editor)
                except PatchParseError as e:
                    git.checkout(default_branch)
                    git._run_git("branch", "-D", branch_name)
                    return f"❌ Patch parse error: {str(e)}"

                if filepath.endswith(".py"):
                    try:
                        resolved = staging_editor._resolve_path(filepath)
                        subprocess.run(["python3", "-m", "py_compile", resolved], capture_output=True, text=True, check=True)
                    except subprocess.CalledProcessError as e:
                        git._run_git("checkout", default_branch, "--", filepath)
                        git._run_git("checkout", default_branch)
                        git._run_git("branch", "-D", branch_name)
                        return f"❌ SYNTAX ERROR — change rejected and reverted.\nBranch '{branch_name}' deleted.\nError: {e.stderr}"

                git._run_git("add", filepath)
                git._run_git("commit", "--no-gpg-sign", "-m", f"staging: {description}", "-m", "Co-authored-by: Tendril <tendril@jurnx.com>")

                if skip_canary:
                    canary_result = "⚠️ Canary boot skipped (skip_canary=True)."
                else:
                    canary_result = "⚠️ Canary boot skipped (Docker not available in this environment)."
                    try:
                        canary = subprocess.run(["docker", "compose", "build", "tendril"], capture_output=True, text=True, timeout=120, cwd=str(editor.sandbox_root))
                        if canary.returncode != 0:
                            git.checkout(default_branch)
                            git._run_git("branch", "-D", branch_name)
                            return f"❌ Canary build FAILED — change reverted.\nBuild error:\n{canary.stderr[-1000:]}"
                        canary_result = "✅ Canary build passed."
                    except FileNotFoundError:
                        canary_result = "⚠️ Docker not found — canary boot skipped."
                    except subprocess.TimeoutExpired:
                        canary_result = "⚠️ Canary build timed out — branch left for manual review."

                git.checkout(default_branch)
                if stash_ref:
                    try:
                        git._run_git("stash", "pop")
                    except Exception:
                        pass

                return (
                    f"✅ Staged edit committed on branch '{branch_name}'\n"
                    f"Patch applied: {result.file_count} file(s)\n"
                    f"{result.summary}\n"
                    f"{canary_result}\n\n"
                    f"To merge after review:\n"
                    f"  git checkout {default_branch} && git merge {branch_name} && git push origin {default_branch}\n"
                )

            except Exception as e:
                try:
                    git.checkout(default_branch)
                except Exception:
                    pass
                if stash_ref:
                    try:
                        git._run_git("stash", "pop")
                    except Exception:
                        pass
                return f"❌ Staged edit failed: {str(e)}"

        @tool
        def merge_staging_branch(branch_name: str, delete_after_merge: bool = True) -> str:
            """Merge a verified staging branch into the default branch and optionally delete it.
            Use this ONLY after a staged_edit has been reviewed and is ready to ship."""
            try:
                try:
                    default = git._run_git("symbolic-ref", "refs/remotes/origin/HEAD").strip().replace("refs/remotes/origin/", "")
                except Exception:
                    default = "main"
                git.checkout(default)
                current = git._run_git("rev-parse", "--abbrev-ref", "HEAD").strip()
                if current != default:
                    return f"❌ Could not switch to {default} (currently on '{current}')"
                if not git._run_git("branch", "--list", branch_name).strip():
                    return f"❌ Branch '{branch_name}' not found."
                merge_out = git._run_git("merge", "--no-ff", "--no-gpg-sign", branch_name, "-m", f"merge: {branch_name} into {default}\n\nCo-authored-by: Tendril <tendril@jurnx.com>")
                result = f"✅ Merged '{branch_name}' into {default}.\n{merge_out}"
                if delete_after_merge:
                    try:
                        git._run_git("branch", "-d", branch_name)
                        result += f"\n🗑️  Branch '{branch_name}' deleted."
                    except Exception as e:
                        result += f"\n⚠️  Could not delete branch: {e}"
                return result
            except Exception as e:
                return f"❌ Merge failed: {str(e)}"

        @tool
        def cleanup_staging_branches(older_than_days: int = 7) -> str:
            """Delete stale staging/* branches older than N days (default: 7).
            Safe to call periodically — only deletes branches prefixed with 'staging/'."""
            try:
                try:
                    default = git._run_git("symbolic-ref", "refs/remotes/origin/HEAD").strip().replace("refs/remotes/origin/", "")
                except Exception:
                    default = "main"
                try:
                    git.checkout(default)
                except Exception:
                    pass
                raw = git._run_git("for-each-ref", "--format=%(refname:short) %(committerdate:unix)", "refs/heads/staging/").strip()
                if not raw:
                    return "✅ No staging branches found — nothing to clean up."
                now = _time.time()
                cutoff = now - (older_than_days * 86400)
                deleted, skipped = [], []
                for line in raw.splitlines():
                    parts = line.strip().split()
                    if len(parts) < 2:
                        continue
                    branch, ts_str = parts[0], parts[1]
                    try:
                        ts = float(ts_str)
                    except ValueError:
                        skipped.append(f"{branch} (bad timestamp)")
                        continue
                    if ts < cutoff:
                        try:
                            git._run_git("branch", "-D", branch)
                            deleted.append(branch)
                        except Exception as e:
                            skipped.append(f"{branch} ({e})")
                    else:
                        skipped.append(f"{branch} (recent, kept)")
                lines = [f"🧹 Staging branch cleanup ({older_than_days}d threshold):"]
                if deleted:
                    lines += [f"\nDeleted ({len(deleted)}):"] + [f"  ✅ {b}" for b in deleted]
                if skipped:
                    lines += [f"\nSkipped ({len(skipped)}):"] + [f"  ⏭️  {b}" for b in skipped]
                return "\n".join(lines)
            except Exception as e:
                return f"❌ Cleanup failed: {str(e)}"

        return [
            git_commit, git_create_branch, git_status, create_pull_request,
            staged_edit, merge_staging_branch, cleanup_staging_branches,
        ]

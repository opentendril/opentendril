"""
Shared pytest fixtures for the Tendril test suite.

Provides:
  - tmp_git_repo:     An isolated temp directory with a real git repo,
                      ready for staged_edit integration tests.
  - git_pipeline:     The raw git-pipeline tool functions (staged_edit,
                      merge_staging_branch, cleanup_staging_branches)
                      wired directly to the tmp_git_repo without importing
                      heavy infrastructure (Redis, Postgres, Chroma, etc.).

Design note
-----------
We do NOT import src.tendril.Orchestrator in this fixture because tendril.py
transitively imports memory.py which has a bare `from redis import Redis`
at module level — unavailable in the local venv where Redis is a Docker
service.  Instead, we recreate *only* the git tool closures the tests need,
using real FileEditor and GitManager instances pointed at the temp repo.
This is the right level of isolation: we are testing the git pipeline,
not LLM routing or vector memory.
"""

import os
import subprocess
import textwrap
import pytest


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _git(repo, *args):
    result = subprocess.run(
        ["git"] + list(args),
        cwd=str(repo), capture_output=True, text=True, check=True
    )
    return result.stdout.strip()


# ---------------------------------------------------------------------------
# tmp_git_repo
# ---------------------------------------------------------------------------

@pytest.fixture
def tmp_git_repo(tmp_path):
    """
    Minimal isolated git repo in a temp directory.

    Layout:
        <tmp_path>/
            src/sample.py   ← Python file for syntax-check tests
            cli/main.go     ← non-protected Go target
            .git/           ← real git repo (not Tendril's)
    """
    repo = tmp_path

    _git(repo, "init")
    # Ensure default branch is 'main' (git may default to 'master' depending on config)
    _git(repo, "config", "user.email", "test@tendril.local")
    _git(repo, "config", "user.name", "Tendril Test")
    _git(repo, "checkout", "-b", "main")

    # src/sample.py — editable Python file
    (repo / "src").mkdir()
    (repo / "src" / "sample.py").write_text(textwrap.dedent("""\
        \"\"\"Sample module for pipeline tests.\"\"\"


        def greet(name: str) -> str:
            return f'Hello, {name}!'


        def add(a: int, b: int) -> int:
            return a + b
    """))

    # cli/main.go — the Go CLI target
    (repo / "cli").mkdir()
    (repo / "cli" / "main.go").write_text(textwrap.dedent("""\
        package main

        import "fmt"

        func main() {
        \tfmt.Println("🌱 Tendril CLI v0.1.0")
        \t// TODO: connect to Brain API
        }
    """))

    _git(repo, "add", "-A")
    _git(repo, "commit", "--no-gpg-sign", "-m", "chore: initial test repo state")

    return repo


# ---------------------------------------------------------------------------
# git_pipeline
# ---------------------------------------------------------------------------

@pytest.fixture
def git_pipeline(tmp_git_repo, monkeypatch):
    """
    Returns a namespace with the three pipeline tool functions
    (staged_edit, merge_staging_branch, cleanup_staging_branches)
    wired to tmp_git_repo.

    These are the raw Python functions, NOT LangChain tool wrappers.
    Call them directly:  git_pipeline.staged_edit(filepath=..., ...)
    """
    import re
    import subprocess
    import time as _time
    from datetime import datetime

    from src.editor import FileEditor
    from src.gitmanager import GitManager
    from src.patcher import parse_patch, validate_patch, apply_patch, PatchParseError

    monkeypatch.setenv("TENDRIL_WORKSPACE_ROOT", str(tmp_git_repo))

    editor = FileEditor(sandbox_root=str(tmp_git_repo), enforce_protection=False)
    git = GitManager(repo_path=str(tmp_git_repo))

    # ------------------------------------------------------------------
    # staged_edit
    # ------------------------------------------------------------------
    def staged_edit(
        filepath: str,
        patch_text: str,
        description: str,
        skip_canary: bool = True,  # default True in tests — no Docker in CI
    ) -> str:
        staging_editor = FileEditor(
            sandbox_root=str(tmp_git_repo), enforce_protection=False
        )

        stash_ref = None
        try:
            stash_out = git._run_git(
                "stash", "push", "--include-untracked", "-m", "tendril-staged-edit-temp"
            )
            if "No local changes" not in stash_out:
                stash_ref = "stash@{0}"
        except Exception:
            pass

        try:
            slug = re.sub(r"[^a-z0-9]+", "-", description.lower().strip())[:40].strip("-")
            branch_name = f"staging/{slug}-{datetime.now().strftime('%H%M%S')}"

            try:
                git.create_branch(branch_name)
            except Exception as e:
                return f"❌ Cannot create branch '{branch_name}': {str(e)}"

            # Apply patch
            try:
                operations = parse_patch(patch_text)
                errors = validate_patch(operations, staging_editor)
                if errors:
                    git.checkout("main")
                    git._run_git("branch", "-D", branch_name)
                    return "❌ Patch validation failed:\n" + "\n".join(f"  - {e}" for e in errors)
                result = apply_patch(operations, staging_editor)
            except PatchParseError as e:
                git.checkout("main")
                git._run_git("branch", "-D", branch_name)
                return f"❌ Patch parse error: {str(e)}"

            # Syntax check for Python files
            if filepath.endswith(".py"):
                try:
                    resolved = staging_editor._resolve_path(filepath)
                    subprocess.run(
                        ["python3", "-m", "py_compile", resolved],
                        capture_output=True, text=True, check=True,
                    )
                except subprocess.CalledProcessError as e:
                    git._run_git("checkout", "main", "--", filepath)
                    git._run_git("checkout", "main")
                    git._run_git("branch", "-D", branch_name)
                    return (
                        f"❌ SYNTAX ERROR — change rejected and reverted.\n"
                        f"Branch '{branch_name}' deleted.\n"
                        f"Error: {e.stderr}"
                    )

            # Commit on staging branch
            git._run_git("add", filepath)
            git._run_git(
                "commit", "--no-gpg-sign", "-m", f"staging: {description}",
                "-m", "Co-authored-by: Tendril <tendril@jurnx.com>",
            )

            # Canary boot
            if skip_canary:
                canary_result = "⚠️ Canary boot skipped (skip_canary=True)."
            else:
                canary_result = "⚠️ Canary boot skipped (Docker not available in this environment)."

            git.checkout("main")

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
                f"To merge: git merge {branch_name}\n"
            )

        except Exception as e:
            try:
                git.checkout("main")
            except Exception:
                pass
            if stash_ref:
                try:
                    git._run_git("stash", "pop")
                except Exception:
                    pass
            return f"❌ Staged edit failed: {str(e)}"

    # ------------------------------------------------------------------
    # merge_staging_branch
    # ------------------------------------------------------------------
    def merge_staging_branch(branch_name: str, delete_after_merge: bool = True) -> str:
        try:
            git.checkout("main")
            current = git._run_git("rev-parse", "--abbrev-ref", "HEAD").strip()
            if current != "main":
                return f"❌ Could not switch to main (currently on '{current}')"

            branches = git._run_git("branch", "--list", branch_name).strip()
            if not branches:
                return f"❌ Branch '{branch_name}' not found."

            merge_out = git._run_git(
                "merge", "--no-ff", "--no-gpg-sign", branch_name,
                "-m", f"merge: {branch_name} into main\n\nCo-authored-by: Tendril <tendril@jurnx.com>",
            )
            result = f"✅ Merged '{branch_name}' into main.\n{merge_out}"

            if delete_after_merge:
                try:
                    git._run_git("branch", "-d", branch_name)
                    result += f"\n🗑️  Branch '{branch_name}' deleted."
                except Exception as e:
                    result += f"\n⚠️  Could not delete branch: {e}"

            return result
        except Exception as e:
            return f"❌ Merge failed: {str(e)}"

    # ------------------------------------------------------------------
    # cleanup_staging_branches
    # ------------------------------------------------------------------
    def cleanup_staging_branches(older_than_days: int = 7) -> str:
        try:
            # Always return to main before deleting branches
            try:
                git.checkout("main")
            except Exception:
                pass
            raw = git._run_git(
                "for-each-ref",
                "--format=%(refname:short) %(committerdate:unix)",
                "refs/heads/staging/",
            ).strip()

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
                lines.append(f"\nDeleted ({len(deleted)}):")
                lines += [f"  ✅ {b}" for b in deleted]
            if skipped:
                lines.append(f"\nSkipped ({len(skipped)}):")
                lines += [f"  ⏭️  {b}" for b in skipped]

            return "\n".join(lines)
        except Exception as e:
            return f"❌ Cleanup failed: {str(e)}"

    # ------------------------------------------------------------------
    # Return as a simple namespace
    # ------------------------------------------------------------------
    class Pipeline:
        pass

    p = Pipeline()
    p.staged_edit = staged_edit
    p.merge_staging_branch = merge_staging_branch
    p.cleanup_staging_branches = cleanup_staging_branches
    p.git = git
    p.editor = editor
    return p

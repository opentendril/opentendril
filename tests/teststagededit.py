"""
Integration tests for the staged_edit pipeline.

Tests the full git lifecycle machinery WITHOUT an LLM:
  Branch creation → Patch application → Syntax check → Commit → Merge → Cleanup

Uses the `git_pipeline` fixture (conftest.py), which wires real FileEditor
and GitManager to an isolated temp git repo — no Redis, Postgres, or LLM.
"""

import subprocess
import pytest


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _git(repo, *args):
    result = subprocess.run(
        ["git"] + list(args),
        cwd=str(repo), capture_output=True, text=True, check=True,
    )
    return result.stdout.strip()


def _current_branch(repo):
    return _git(repo, "rev-parse", "--abbrev-ref", "HEAD")


def _all_branches(repo):
    raw = _git(repo, "branch", "--list")
    return [b.strip().lstrip("* ") for b in raw.splitlines() if b.strip()]


# ---------------------------------------------------------------------------
# Known-good patch — used across multiple tests
# ---------------------------------------------------------------------------

GOOD_PATCH = """\
*** Begin Patch
*** Update File: src/sample.py
@@ greet
-    return f'Hello, {name}!'
+    return f'Hello, {name}! 👋'
*** End Patch"""

DESCRIPTION = "add emoji to greet function"


# ---------------------------------------------------------------------------
# TestStagedEditBranchLifecycle
# ---------------------------------------------------------------------------

class TestStagedEditBranchLifecycle:

    def test_creates_staging_branch(self, tmp_git_repo, git_pipeline):
        """staged_edit should create a staging/* branch."""
        result = git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text=GOOD_PATCH,
            description=DESCRIPTION,
        )
        assert "✅" in result, f"Expected success, got: {result}"

        branches = _all_branches(tmp_git_repo)
        assert any(b.startswith("staging/") for b in branches), (
            f"No staging/* branch found. Branches: {branches}"
        )

    def test_returns_to_main_after_staged_edit(self, tmp_git_repo, git_pipeline):
        """After staged_edit, HEAD must be on main."""
        git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text=GOOD_PATCH,
            description=DESCRIPTION,
        )
        assert _current_branch(tmp_git_repo) == "main"

    def test_staging_branch_contains_commit(self, tmp_git_repo, git_pipeline):
        """The staging branch should have at least one commit ahead of main."""
        git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text=GOOD_PATCH,
            description=DESCRIPTION,
        )

        branches = [b for b in _all_branches(tmp_git_repo) if b.startswith("staging/")]
        assert branches, "No staging branch found"
        staging_branch = branches[0]

        ahead = _git(tmp_git_repo, "log", f"main..{staging_branch}", "--oneline")
        assert ahead, "Staging branch has no commits ahead of main"

    def test_file_modified_on_staging_branch_not_on_main(self, tmp_git_repo, git_pipeline):
        """Change must be on staging branch but NOT on main."""
        git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text=GOOD_PATCH,
            description=DESCRIPTION,
        )

        branches = [b for b in _all_branches(tmp_git_repo) if b.startswith("staging/")]
        staging_branch = branches[0]

        content_staging = _git(tmp_git_repo, "show", f"{staging_branch}:src/sample.py")
        assert "👋" in content_staging, "Emoji not found on staging branch"

        content_main = _git(tmp_git_repo, "show", "main:src/sample.py")
        assert "👋" not in content_main, "Emoji leaked to main before merge"


# ---------------------------------------------------------------------------
# TestStagedEditPatchValidation
# ---------------------------------------------------------------------------

class TestStagedEditPatchValidation:

    def test_malformed_patch_returns_error(self, tmp_git_repo, git_pipeline):
        """A malformed patch should return an error without creating a branch."""
        result = git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text="this is not a valid patch at all",
            description="bad patch test",
        )
        assert "❌" in result

        branches = _all_branches(tmp_git_repo)
        assert not any(b.startswith("staging/") for b in branches), (
            "staging branch was created despite malformed patch"
        )

    def test_nonexistent_file_returns_error(self, tmp_git_repo, git_pipeline):
        """Patching a file that doesn't exist should return a validation error."""
        patch = """\
*** Begin Patch
*** Update File: src/ghost_file.py
@@ fn
- old_line
+ new_line
*** End Patch"""
        result = git_pipeline.staged_edit(
            filepath="src/ghost_file.py",
            patch_text=patch,
            description="nonexistent file",
        )
        assert "❌" in result

    def test_syntax_error_rejected(self, tmp_git_repo, git_pipeline):
        """A patch introducing a Python syntax error should be rejected."""
        bad_patch = """\
*** Begin Patch
*** Update File: src/sample.py
@@ greet
-    return f'Hello, {name}!'
+    def broken syntax here !!!
*** End Patch"""
        result = git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text=bad_patch,
            description="syntax error test",
        )
        assert "❌" in result

        # Main file must be untouched
        content_main = _git(tmp_git_repo, "show", "main:src/sample.py")
        assert "def broken syntax" not in content_main

    def test_main_branch_clean_after_rejection(self, tmp_git_repo, git_pipeline):
        """After a rejected patch, HEAD must be on main with a clean worktree."""
        git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text="bad patch content",
            description="rejection cleanup test",
        )
        assert _current_branch(tmp_git_repo) == "main"
        status = _git(tmp_git_repo, "status", "--porcelain")
        assert status == "", f"Dirty working tree after rejection: {status}"


# ---------------------------------------------------------------------------
# TestMergeStagingBranch
# ---------------------------------------------------------------------------

class TestMergeStagingBranch:

    def _do_staged_edit(self, git_pipeline):
        return git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text=GOOD_PATCH,
            description=DESCRIPTION,
        )

    def _get_staging_branch(self, tmp_git_repo):
        branches = [b for b in _all_branches(tmp_git_repo) if b.startswith("staging/")]
        assert branches, "No staging branch found"
        return branches[0]

    def test_merge_succeeds(self, tmp_git_repo, git_pipeline):
        self._do_staged_edit(git_pipeline)
        branch = self._get_staging_branch(tmp_git_repo)

        result = git_pipeline.merge_staging_branch(
            branch_name=branch, delete_after_merge=False
        )
        assert "✅" in result, f"Merge failed: {result}"

    def test_merge_creates_no_ff_commit(self, tmp_git_repo, git_pipeline):
        """Merge must be a merge commit (--no-ff), not fast-forward."""
        self._do_staged_edit(git_pipeline)
        branch = self._get_staging_branch(tmp_git_repo)
        git_pipeline.merge_staging_branch(branch_name=branch, delete_after_merge=False)

        merges = _git(tmp_git_repo, "log", "--merges", "--oneline", "-1")
        assert merges, "No merge commit found — fast-forward occurred"

    def test_merge_makes_change_visible_on_main(self, tmp_git_repo, git_pipeline):
        self._do_staged_edit(git_pipeline)
        branch = self._get_staging_branch(tmp_git_repo)
        git_pipeline.merge_staging_branch(branch_name=branch, delete_after_merge=False)

        content = _git(tmp_git_repo, "show", "main:src/sample.py")
        assert "👋" in content

    def test_merge_with_delete_removes_branch(self, tmp_git_repo, git_pipeline):
        self._do_staged_edit(git_pipeline)
        branch = self._get_staging_branch(tmp_git_repo)

        git_pipeline.merge_staging_branch(branch_name=branch, delete_after_merge=True)

        remaining = _all_branches(tmp_git_repo)
        assert branch not in remaining, (
            f"Branch {branch} still exists after merge+delete"
        )

    def test_merge_nonexistent_branch_returns_error(self, tmp_git_repo, git_pipeline):
        result = git_pipeline.merge_staging_branch(
            branch_name="staging/does-not-exist-999999",
            delete_after_merge=False,
        )
        assert "❌" in result


# ---------------------------------------------------------------------------
# TestCleanupStagingBranches
# ---------------------------------------------------------------------------

class TestCleanupStagingBranches:

    def test_cleanup_removes_old_branches(self, tmp_git_repo, git_pipeline):
        """cleanup_staging_branches(0) should delete any staging/* branch immediately."""
        git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text=GOOD_PATCH,
            description=DESCRIPTION,
        )
        assert any(b.startswith("staging/") for b in _all_branches(tmp_git_repo))

        result = git_pipeline.cleanup_staging_branches(older_than_days=0)
        assert "Deleted" in result

        remaining = [b for b in _all_branches(tmp_git_repo) if b.startswith("staging/")]
        assert not remaining, f"Staging branches still present: {remaining}"

    def test_cleanup_skips_recent_branches(self, tmp_git_repo, git_pipeline):
        """cleanup_staging_branches(7) should not delete a branch just created."""
        git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text=GOOD_PATCH,
            description=DESCRIPTION,
        )

        result = git_pipeline.cleanup_staging_branches(older_than_days=7)

        remaining = [b for b in _all_branches(tmp_git_repo) if b.startswith("staging/")]
        assert remaining, "Recent staging branch was incorrectly deleted"
        assert "recent, kept" in result or "Skipped" in result

    def test_cleanup_no_branches_returns_clean_message(self, tmp_git_repo, git_pipeline):
        result = git_pipeline.cleanup_staging_branches(older_than_days=7)
        assert "No staging branches" in result or "✅" in result


# ---------------------------------------------------------------------------
# TestFullPipelineRoundTrip
# ---------------------------------------------------------------------------

class TestFullPipelineRoundTrip:

    def test_full_lifecycle(self, tmp_git_repo, git_pipeline):
        """
        End-to-end machine-verifiable proof of the self-build lifecycle:
          staged_edit → merge_staging_branch → cleanup_staging_branches
        """
        # Step 1: Staged edit
        se_result = git_pipeline.staged_edit(
            filepath="src/sample.py",
            patch_text=GOOD_PATCH,
            description=DESCRIPTION,
        )
        assert "✅" in se_result, f"staged_edit failed: {se_result}"
        assert _current_branch(tmp_git_repo) == "main"

        branches = [b for b in _all_branches(tmp_git_repo) if b.startswith("staging/")]
        assert branches, "No staging branch created"
        staging_branch = branches[0]

        # Step 2: Merge (no-ff)
        merge_result = git_pipeline.merge_staging_branch(
            branch_name=staging_branch, delete_after_merge=False
        )
        assert "✅" in merge_result, f"merge failed: {merge_result}"

        content = _git(tmp_git_repo, "show", "main:src/sample.py")
        assert "👋" in content, "Change not present on main after merge"

        merges = _git(tmp_git_repo, "log", "--merges", "--oneline", "-1")
        assert merges, "No merge commit — fast-forward occurred"

        # Step 3: Cleanup (threshold=0 to force deletion now)
        cleanup_result = git_pipeline.cleanup_staging_branches(older_than_days=0)
        assert "Deleted" in cleanup_result

        remaining = [b for b in _all_branches(tmp_git_repo) if b.startswith("staging/")]
        assert not remaining, f"Stale branches after cleanup: {remaining}"

        # Repo must be clean on main — tracked files only (untracked __pycache__ is OK)
        assert _current_branch(tmp_git_repo) == "main"
        # git status --porcelain shows '??' for untracked, ' M' / 'M ' for modified
        # We only care that no tracked files are dirty
        status_lines = _git(tmp_git_repo, "status", "--porcelain").splitlines()
        dirty_tracked = [l for l in status_lines if l and not l.startswith("??")]
        assert not dirty_tracked, f"Dirty tracked files after full lifecycle: {dirty_tracked}"

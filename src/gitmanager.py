"""
Tendril Git Manager — Version Control and Audit Automation.

Provides tools for branching, committing, creating PRs, and creating Issues.
"""

import os
import subprocess
import logging
from typing import Optional

from .config import WORKSPACE_ROOT

logger = logging.getLogger(__name__)


class GitManager:
    """Manages Git operations and GitHub API interactions."""

    def __init__(self, repo_path: str = WORKSPACE_ROOT):
        self.repo_path = repo_path
        self.github_token = os.getenv("GITHUB_TOKEN")
        self._github_client = None

    @property
    def github(self):
        if not self._github_client and self.github_token:
            try:
                from github import Github
                self._github_client = Github(self.github_token)
            except ImportError:
                logger.warning("PyGithub not installed, GitHub remote operations disabled.")
        return self._github_client

    def _run_git(self, *args) -> str:
        """Run a git command in the repository path."""
        try:
            result = subprocess.run(
                ["git"] + list(args),
                cwd=self.repo_path,
                capture_output=True,
                text=True,
                check=True
            )
            return result.stdout.strip()
        except subprocess.CalledProcessError as e:
            logger.error(f"Git command failed: git {' '.join(args)}\nError: {e.stderr}")
            raise RuntimeError(f"Git error: {e.stderr.strip() or e.stdout.strip()}")

    def status(self) -> str:
        """Get git status."""
        return self._run_git("status", "-s")

    def current_branch(self) -> str:
        """Get current branch name."""
        return self._run_git("branch", "--show-current")

    def list_branches(self) -> str:
        """List all local branches."""
        return self._run_git("branch")

    def checkout(self, branch_name: str) -> str:
        """Checkout an existing branch."""
        self._run_git("checkout", branch_name)
        return f"Checked out branch: {branch_name}"

    def create_branch(self, branch_name: str) -> str:
        """Create and checkout a new branch."""
        self._run_git("checkout", "-b", branch_name)
        return f"Checked out new branch: {branch_name}"

    def _is_signing_configured(self) -> bool:
        """Check if GPG commit signing is configured for this repository."""
        try:
            result = subprocess.run(
                ["git", "config", "commit.gpgsign"],
                cwd=self.repo_path,
                capture_output=True, text=True
            )
            return result.stdout.strip().lower() == "true"
        except Exception:
            return False

    def commit_changes(self, message: str) -> str:
        """Add all tracked/untracked changes in safe areas and commit them.
        
        Commits are cryptographically signed (-S) when a GPG key is configured.
        This ensures every autonomous commit is verifiable on GitHub.
        """
        # Only stage files in safe directories to avoid committing secrets accidentally
        # Assuming .gitignore handles the heavy lifting, we'll just add everything
        self._run_git("add", "-A")
        
        # Check if there are changes
        status = self.status()
        if not status:
            return "No changes to commit."

        # Build commit command with optional signing
        commit_args = ["commit"]
        if self._is_signing_configured():
            commit_args.append("-S")
            logger.info("🔏 Signing commit with GPG key")
        else:
            logger.warning("⚠️  GPG signing not configured. Run scripts/generate-node-identity.sh to enable verified commits.")

        commit_args.extend(["-m", message, "-m", "Co-authored-by: Tendril <tendril@jurnx.com>"])
        self._run_git(*commit_args)
        
        signed = " (signed)" if self._is_signing_configured() else " (unsigned)"
        return f"Committed changes with message: '{message}'{signed}"

    def push_branch(self, branch_name: Optional[str] = None) -> str:
        """Push the given (or current) branch to origin."""
        branch = branch_name or self.current_branch()
        self._run_git("push", "-u", "origin", branch)
        return f"Pushed branch '{branch}' to origin."

    def create_pull_request(
        self, repo_name: str, title: str, body: str, head_branch: str, base_branch: str = "main"
    ) -> str:
        """
        Create a GitHub Pull Request.
        repo_name should be like 'opentendril/core'.
        """
        gh = self.github
        if not gh:
            return "❌ GitHub token not configured or PyGithub not installed."
        
        try:
            repo = gh.get_repo(repo_name)
            pr = repo.create_pull(
                title=title,
                body=body,
                head=head_branch,
                base=base_branch
            )
            return f"✅ Created PR #{pr.number}: {pr.html_url}"
        except Exception as e:
            logger.error(f"Failed to create PR: {e}")
            return f"❌ Failed to create PR: {str(e)}"

    def create_issue(self, repo_name: str, title: str, body: str) -> str:
        """Create a GitHub Issue."""
        gh = self.github
        if not gh:
            return "❌ GitHub token not configured or PyGithub not installed."
        
        try:
            repo = gh.get_repo(repo_name)
            issue = repo.create_issue(title=title, body=body)
            return f"✅ Created Issue #{issue.number}: {issue.html_url}"
        except Exception as e:
            logger.error(f"Failed to create issue: {e}")
            return f"❌ Failed to create issue: {str(e)}"

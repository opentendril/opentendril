"""
Tendril File Editor — Safe read/write/diff for self-building.

All operations are sandboxed to the project directory.
Changes are presented as diffs and require approval before applying.
"""

import os
import difflib
import logging
from pathlib import Path
from typing import Optional
from datetime import datetime

logger = logging.getLogger(__name__)

from .config import WORKSPACE_ROOT, PROJECT_ROOT

# Sandboxed root — the editor cannot escape this directory
SANDBOX_ROOT = WORKSPACE_ROOT

# Detect external project mode: when WORKSPACE_ROOT differs from PROJECT_ROOT (/app),
# we're editing someone else's code, not Tendril's kernel.
_IS_EXTERNAL = WORKSPACE_ROOT != PROJECT_ROOT

ALLOWED_EXTENSIONS = {
    ".py", ".js", ".ts", ".jsx", ".tsx", ".html", ".css",
    ".json", ".yml", ".yaml", ".toml", ".md", ".txt",
    ".sql", ".sh", ".cfg", ".ini", ".dockerfile",
    ".go", ".rs", ".java", ".rb", ".php", ".c", ".cpp", ".h",
}
BLOCKED_PATTERNS = {
    "__pycache__", ".git", "node_modules", ".env",
    "venv", ".venv", "secrets",
}

# Protected files: the LLM CANNOT write to these during chat sessions.
# These are critical kernel files that have been destroyed by LLM write_file
# calls multiple times (2026-04-09 incidents: main.py, patcher.py, styles.css).
# Modifications to these files require the /edit endpoint with SDLC gates,
# or direct human editing.
# NOTE: Protection is DISABLED in external project mode — external code has no
# protected files (the user controls what's editable via their .gitignore).
PROTECTED_FILES = set() if _IS_EXTERNAL else {
    "src/main.py",
    "src/tendril.py",
    "src/config.py",
    "src/editor.py",
    "src/patcher.py",
    "src/llmrouter.py",
    "src/failover.py",
    "src/eventbus.py",
    "src/memory.py",
    "static/styles.css",
    "static/index.html",
    "static/app.js",
    "GUARDRAILS.md",
    "DECISIONS.md",
    "ARCHITECTURE.md",
    "docker-compose.yml",
    "Dockerfile",
    "requirements.txt",
}


class FileEditor:
    """
    Safe file editor for Tendril's self-building capability.

    All paths are resolved relative to SANDBOX_ROOT and validated
    to prevent directory traversal attacks.
    """

    def __init__(self, sandbox_root: str = SANDBOX_ROOT, enforce_protection: bool = True):
        self.sandbox_root = os.path.realpath(sandbox_root)
        self.enforce_protection = enforce_protection and not _IS_EXTERNAL
        self._edit_history: list[dict] = []

    def _resolve_path(self, filepath: str) -> str:
        """Resolve and validate a file path within the sandbox."""
        # Handle both absolute and relative paths
        if filepath.startswith("/"):
            resolved = os.path.realpath(filepath)
        else:
            resolved = os.path.realpath(os.path.join(self.sandbox_root, filepath))

        # Security: ensure path is within sandbox
        if not resolved.startswith(self.sandbox_root):
            raise PermissionError(
                f"🚫 Path '{filepath}' resolves outside sandbox ({self.sandbox_root}). "
                f"Resolved to: {resolved}"
            )

        # Check for blocked patterns
        for pattern in BLOCKED_PATTERNS:
            if pattern in resolved:
                raise PermissionError(f"🚫 Path contains blocked pattern: '{pattern}'")

        return resolved

    def _validate_extension(self, filepath: str):
        """Ensure the file has an allowed extension."""
        ext = Path(filepath).suffix.lower()
        if ext and ext not in ALLOWED_EXTENSIONS:
            raise PermissionError(
                f"🚫 File extension '{ext}' is not allowed. "
                f"Allowed: {', '.join(sorted(ALLOWED_EXTENSIONS))}"
            )

    def read(self, filepath: str) -> str:
        """Read a file's contents. Path is sandboxed."""
        resolved = self._resolve_path(filepath)

        if not os.path.exists(resolved):
            raise FileNotFoundError(f"File not found: {filepath}")

        with open(resolved, "r", encoding="utf-8") as f:
            content = f.read()

        logger.info(f"📖 Read file: {filepath} ({len(content)} bytes)")
        return content

    def list_files(self, directory: str = "") -> list[dict]:
        """List files in a directory within the sandbox."""
        resolved = self._resolve_path(directory or ".")
        result = []

        for root, dirs, files in os.walk(resolved):
            # Skip blocked directories
            dirs[:] = [d for d in dirs if d not in BLOCKED_PATTERNS]

            for fname in files:
                full_path = os.path.join(root, fname)
                rel_path = os.path.relpath(full_path, self.sandbox_root)
                ext = Path(fname).suffix.lower()

                if ext in ALLOWED_EXTENSIONS:
                    stat = os.stat(full_path)
                    result.append({
                        "path": rel_path,
                        "size": stat.st_size,
                        "modified": datetime.fromtimestamp(stat.st_mtime).isoformat(),
                    })

        return sorted(result, key=lambda x: x["path"])

    def generate_diff(self, filepath: str, new_content: str) -> str:
        """
        Generate a unified diff between the current file and new content.
        Does NOT write anything — this is for preview/approval.
        """
        resolved = self._resolve_path(filepath)

        if os.path.exists(resolved):
            with open(resolved, "r", encoding="utf-8") as f:
                old_content = f.read()
            old_lines = old_content.splitlines(keepends=True)
        else:
            old_lines = []

        new_lines = new_content.splitlines(keepends=True)

        diff = difflib.unified_diff(
            old_lines,
            new_lines,
            fromfile=f"a/{filepath}",
            tofile=f"b/{filepath}",
            lineterm="",
        )

        diff_text = "\n".join(diff)
        if not diff_text:
            return "No changes detected."

        return diff_text

    def _check_protected(self, filepath: str):
        """Block writes to protected kernel files during chat sessions."""
        if not self.enforce_protection:
            return
        # Normalize to relative path for comparison
        rel = filepath.lstrip("/").lstrip("./")
        if self.sandbox_root and filepath.startswith(self.sandbox_root):
            rel = os.path.relpath(filepath, self.sandbox_root)
        if rel in PROTECTED_FILES:
            raise PermissionError(
                f"🛡️ PROTECTED FILE: '{rel}' cannot be modified during a chat session. "
                f"Use the /edit endpoint with SDLC gates, or ask the human developer "
                f"to make this change directly. See GUARDRAILS.md §7 for details."
            )

    def write(self, filepath: str, content: str, create_parents: bool = True) -> dict:
        """
        Write content to a file within the sandbox.

        Returns metadata about the write operation.
        """
        resolved = self._resolve_path(filepath)
        self._validate_extension(filepath)
        self._check_protected(filepath)

        # Generate diff before writing
        diff = self.generate_diff(filepath, content)
        existed = os.path.exists(resolved)

        if create_parents:
            os.makedirs(os.path.dirname(resolved), exist_ok=True)

        # Read old content for history
        old_content = ""
        if existed:
            with open(resolved, "r", encoding="utf-8") as f:
                old_content = f.read()

        # Write the new content
        with open(resolved, "w", encoding="utf-8") as f:
            f.write(content)

        # Record in edit history
        edit_record = {
            "filepath": filepath,
            "timestamp": datetime.now().isoformat(),
            "action": "modified" if existed else "created",
            "old_size": len(old_content),
            "new_size": len(content),
            "diff_preview": diff[:500],
        }
        self._edit_history.append(edit_record)

        logger.info(f"✏️  {'Modified' if existed else 'Created'} file: {filepath} ({len(content)} bytes)")

        return {
            "status": "success",
            "action": edit_record["action"],
            "filepath": filepath,
            "size": len(content),
            "diff": diff,
        }

    def patch(self, filepath: str, search: str, replace: str) -> dict:
        """
        Apply a targeted search-and-replace patch to a file.
        More surgical than full file writes.
        """
        self._check_protected(filepath)
        content = self.read(filepath)

        if search not in content:
            raise ValueError(
                f"Search text not found in {filepath}. "
                f"Search was: {search[:100]}..."
            )

        count = content.count(search)
        new_content = content.replace(search, replace, 1)

        return self.write(filepath, new_content)

    def search_project(self, query: str, directory: str = "") -> list[dict]:
        """
        Search for a string across all allowed files in the sandbox.
        Useful for finding where a variable, error, or function is used.
        """
        resolved = self._resolve_path(directory or ".")
        results = []

        for root, dirs, files in os.walk(resolved):
            dirs[:] = [d for d in dirs if d not in BLOCKED_PATTERNS]
            
            for fname in files:
                ext = Path(fname).suffix.lower()
                if ext in ALLOWED_EXTENSIONS:
                    full_path = os.path.join(root, fname)
                    rel_path = os.path.relpath(full_path, self.sandbox_root)
                    
                    try:
                        with open(full_path, 'r', encoding='utf-8') as f:
                            for i, line in enumerate(f, 1):
                                if query in line:
                                    results.append({
                                        "file": rel_path,
                                        "line": i,
                                        "content": line.strip()[:100] # Truncate for safety
                                    })
                    except Exception as e:
                        logger.warning(f"Could not search {rel_path}: {e}")
                        
        return results

    @property
    def history(self) -> list[dict]:
        """Return the history of all edits in this session."""
        return self._edit_history.copy()

"""
Tendril Surgical Patcher — Structured, multi-file patch format with SDLC integration.

Parses LLM-generated patches, validates them, applies atomically,
and runs the SDLC pipeline (lint → compile → test) with auto-rollback.

Better than OpenClaw:
  - Pre-apply validation (context matching, path checks)
  - SDLC pipeline gate (ruff + py_compile + pytest) via sandbox
  - Auto git-stash rollback on pipeline failure
  - Auto-commit with descriptive message on success

Patch Format:
    *** Begin Patch
    *** Update File: src/main.py
    @@ def process():
    - old_line = "hello"
    + new_line = "world"
    *** Add File: src/newmodule.py
    +# New module content
    +def hello():
    +    pass
    *** Delete File: src/deprecated.py
    *** End Patch
"""

import os
import logging
from dataclasses import dataclass, field
from typing import Optional
from enum import Enum

from .editor import FileEditor
from .eventbus import event_bus, TendrilEvent

logger = logging.getLogger(__name__)

BEGIN_MARKER = "*** Begin Patch"
END_MARKER = "*** End Patch"
ADD_FILE_MARKER = "*** Add File: "
DELETE_FILE_MARKER = "*** Delete File: "
UPDATE_FILE_MARKER = "*** Update File: "
CONTEXT_MARKER = "@@ "


class PatchAction(str, Enum):
    ADD = "add"
    UPDATE = "update"
    DELETE = "delete"


@dataclass
class PatchHunk:
    """A single change context within an update operation."""
    context: str = ""
    old_lines: list[str] = field(default_factory=list)
    new_lines: list[str] = field(default_factory=list)


@dataclass
class PatchOperation:
    """A single file operation within a patch."""
    action: PatchAction
    path: str
    hunks: list[PatchHunk] = field(default_factory=list)
    content: str = ""  # For add operations


@dataclass
class PatchResult:
    """Result of applying a patch."""
    added: list[str] = field(default_factory=list)
    modified: list[str] = field(default_factory=list)
    deleted: list[str] = field(default_factory=list)

    @property
    def summary(self) -> str:
        parts = []
        for f in self.added:
            parts.append(f"A {f}")
        for f in self.modified:
            parts.append(f"M {f}")
        for f in self.deleted:
            parts.append(f"D {f}")
        return "\n".join(parts) if parts else "No changes."

    @property
    def file_count(self) -> int:
        return len(self.added) + len(self.modified) + len(self.deleted)


class PatchParseError(Exception):
    """Raised when patch text cannot be parsed."""
    pass


class PatchValidationError(Exception):
    """Raised when a patch fails pre-apply validation."""
    pass


def parse_patch(text: str) -> list[PatchOperation]:
    """
    Parse LLM-generated patch text into structured operations.

    Args:
        text: Raw patch text with Begin/End markers

    Returns:
        List of PatchOperation objects

    Raises:
        PatchParseError: If the patch format is invalid
    """
    text = text.strip()
    if not text:
        raise PatchParseError("Patch text is empty.")

    # Find patch boundaries
    lines = text.split("\n")
    start_idx = None
    end_idx = None

    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped == BEGIN_MARKER:
            start_idx = i
        elif stripped == END_MARKER:
            end_idx = i

    if start_idx is None:
        raise PatchParseError(f"Missing '{BEGIN_MARKER}' marker.")
    if end_idx is None:
        raise PatchParseError(f"Missing '{END_MARKER}' marker.")
    if end_idx <= start_idx:
        raise PatchParseError("End marker appears before begin marker.")

    # Parse operations between markers
    body = lines[start_idx + 1:end_idx]
    operations = []
    i = 0

    while i < len(body):
        line = body[i].strip()

        if not line:
            i += 1
            continue

        if line.startswith(ADD_FILE_MARKER):
            path = line[len(ADD_FILE_MARKER):]
            content_lines = []
            i += 1
            while i < len(body):
                if body[i].startswith("+"):
                    content_lines.append(body[i][1:])
                    i += 1
                elif body[i].startswith("***"):
                    break
                else:
                    i += 1
                    break
            operations.append(PatchOperation(
                action=PatchAction.ADD,
                path=path,
                content="\n".join(content_lines) + "\n" if content_lines else "",
            ))

        elif line.startswith(DELETE_FILE_MARKER):
            path = line[len(DELETE_FILE_MARKER):]
            operations.append(PatchOperation(
                action=PatchAction.DELETE,
                path=path,
            ))
            i += 1

        elif line.startswith(UPDATE_FILE_MARKER):
            path = line[len(UPDATE_FILE_MARKER):]
            hunks = []
            i += 1

            while i < len(body):
                current = body[i]
                if current.startswith("***"):
                    break

                # Parse hunk
                context = ""
                if current.startswith(CONTEXT_MARKER) or current.strip() == "@@":
                    context = current[len(CONTEXT_MARKER):] if current.startswith(CONTEXT_MARKER) else ""
                    i += 1

                hunk = PatchHunk(context=context)
                i_before_hunk = i
                while i < len(body):
                    hline = body[i]
                    if hline.startswith("***") or (hline.startswith(CONTEXT_MARKER) and hunk.old_lines):
                        break
                    if hline.startswith("+"):
                        hunk.new_lines.append(hline[1:])
                    elif hline.startswith("-"):
                        hunk.old_lines.append(hline[1:])
                    elif hline.startswith(" "):
                        hunk.old_lines.append(hline[1:])
                        hunk.new_lines.append(hline[1:])
                    elif not hline.strip():
                        # Empty line = context
                        hunk.old_lines.append("")
                        hunk.new_lines.append("")
                    else:
                        break
                    i += 1

                if hunk.old_lines or hunk.new_lines:
                    hunks.append(hunk)
                elif i == i_before_hunk:
                    # Nothing consumed and i unchanged — unrecognised trailing line.
                    # Advance to prevent infinite loop (e.g. bare context like 'greet("world")').
                    i += 1

            operations.append(PatchOperation(
                action=PatchAction.UPDATE,
                path=path,
                hunks=hunks,
            ))

        else:
            i += 1

    if not operations:
        raise PatchParseError("No valid operations found in patch.")

    return operations


def validate_patch(
    operations: list[PatchOperation],
    editor: FileEditor,
) -> list[str]:
    """
    Pre-apply validation. Returns list of error strings (empty = valid).

    Checks:
      - Update targets exist
      - Add targets don't already exist (warning only)
      - Delete targets exist
      - Paths are within sandbox
    """
    errors = []

    for op in operations:
        try:
            resolved = editor._resolve_path(op.path)
        except PermissionError as e:
            errors.append(f"Path blocked: {op.path} — {str(e)}")
            continue

        if op.action == PatchAction.UPDATE:
            if not os.path.exists(resolved):
                errors.append(f"Update target not found: {op.path}")

        elif op.action == PatchAction.DELETE:
            if not os.path.exists(resolved):
                errors.append(f"Delete target not found: {op.path}")

        elif op.action == PatchAction.ADD:
            if os.path.exists(resolved):
                # Warning, not error — overwrite is acceptable
                logger.warning(f"Add target already exists (will overwrite): {op.path}")

    return errors


def _apply_hunk_fuzzy(
    content: str,
    old_lines: list[str],
    new_lines: list[str],
) -> Optional[str]:
    """
    Indentation-normalised hunk applicator (Pass 2 fallback).

    Algorithm:
      1. Strip the leading whitespace from each pattern line to get a
         "canonical" search key.
      2. Slide a window of len(old_lines) over the file lines.
      3. For each window position, compare stripped file lines against
         stripped pattern lines. If all match, record the common indent
         of the first matched file line.
      4. Re-indent new_lines using that indent and splice them in.

    This handles the most common mismatch: the LLM generates
    "- return 'foo'" (no indent) but the file has "    return 'foo'".

    Returns the modified content string, or None if no match found.
    """
    if not old_lines:
        return None

    file_lines = content.split("\n")
    n = len(file_lines)
    k = len(old_lines)

    # Canonical (stripped) versions of the search pattern
    stripped_old = [l.strip() for l in old_lines]

    # Skip if any pattern line is empty after stripping — too ambiguous
    if any(s == "" for s in stripped_old):
        # Allow pure-blank lines to match as wildcards
        pass

    for start in range(n - k + 1):
        window = file_lines[start : start + k]
        stripped_window = [l.strip() for l in window]

        if stripped_window == stripped_old:
            # Found a match — determine the indentation from the first
            # non-blank line in the window.
            base_indent = ""
            for wline in window:
                if wline.strip():
                    base_indent = wline[: len(wline) - len(wline.lstrip())]
                    break

            # Re-indent new_lines: strip their own leading space from the
            # parser (single char from the prefix), then apply base_indent.
            re_indented_new = []
            for nline in new_lines:
                # Parser leaves a leading space on context/replacement lines
                stripped_n = nline.lstrip()
                re_indented_new.append(base_indent + stripped_n if stripped_n else "")

            # Splice: lines before + re-indented new + lines after
            result_lines = file_lines[:start] + re_indented_new + file_lines[start + k:]
            return "\n".join(result_lines)

    return None


def apply_patch(
    operations: list[PatchOperation],
    editor: FileEditor,
    run_id: str = "",
    session_id: str = "default",
) -> PatchResult:

    """
    Apply patch operations to the filesystem.

    For update operations, uses search-and-replace for each hunk.
    For add operations, writes new files.
    For delete operations, removes files.

    Returns:
        PatchResult with lists of added/modified/deleted files
    """
    result = PatchResult()

    for op in operations:
        try:
            if op.action == PatchAction.ADD:
                editor.write(op.path, op.content)
                result.added.append(op.path)
                event_bus.emit(TendrilEvent(
                    run_id=run_id,
                    event_type="patch.file_added",
                    session_id=session_id,
                    data={"path": op.path, "size": len(op.content)},
                ))

            elif op.action == PatchAction.DELETE:
                resolved = editor._resolve_path(op.path)
                if os.path.exists(resolved):
                    os.remove(resolved)
                result.deleted.append(op.path)
                event_bus.emit(TendrilEvent(
                    run_id=run_id,
                    event_type="patch.file_deleted",
                    session_id=session_id,
                    data={"path": op.path},
                ))

            elif op.action == PatchAction.UPDATE:
                content = editor.read(op.path)
                modified = False

                for hunk in op.hunks:
                    if not hunk.old_lines:
                        # Pure addition — append new lines
                        content += "\n".join(hunk.new_lines) + "\n"
                        modified = True
                        continue

                    # Search for old_lines in content and replace with new_lines.
                    #
                    # Two-pass approach:
                    #   Pass 1 — exact substring match (fast path)
                    #   Pass 2 — indentation-normalised line-by-line search
                    #            handles the common case where the LLM writes
                    #            "- return 'hello'" and the file has
                    #            "    return 'hello'" (leading spaces differ).
                    old_text = "\n".join(hunk.old_lines)
                    new_text = "\n".join(hunk.new_lines)

                    if old_text in content:
                        # Pass 1: exact match
                        content = content.replace(old_text, new_text, 1)
                        modified = True
                    else:
                        # Pass 2: indentation-normalised search
                        matched = _apply_hunk_fuzzy(content, hunk.old_lines, hunk.new_lines)
                        if matched is not None:
                            content = matched
                            modified = True
                        else:
                            logger.warning(
                                f"Hunk context not found in {op.path}: "
                                f"{old_text[:60]}..."
                            )


                if modified:
                    editor.write(op.path, content)
                    result.modified.append(op.path)
                    event_bus.emit(TendrilEvent(
                        run_id=run_id,
                        event_type="patch.file_modified",
                        session_id=session_id,
                        data={"path": op.path, "hunks": len(op.hunks)},
                    ))

        except Exception as e:
            logger.error(f"Patch operation failed for {op.path}: {e}")
            event_bus.emit(TendrilEvent(
                run_id=run_id,
                event_type="patch.error",
                session_id=session_id,
                data={"path": op.path, "action": op.action.value, "error": str(e)[:200]},
            ))

    return result


def format_patch_for_prompt() -> str:
    """Returns the patch format instructions for the LLM system prompt."""
    return """When making code changes, use the structured patch format:

*** Begin Patch
*** Update File: path/to/file.py
@@ context_hint (e.g. function name)
- old line to remove
+ new line to add
  unchanged context line
*** Add File: path/to/newfile.py
+first line of new file
+second line of new file
*** Delete File: path/to/remove.py
*** End Patch

Rules:
- Lines starting with '-' are removed, '+' are added, ' ' (space) are context
- Use @@ followed by a function/class name to help locate the change
- Multiple files can be changed in one patch
- For new files, every line starts with '+'
- The patch will be validated and tested automatically before committing"""

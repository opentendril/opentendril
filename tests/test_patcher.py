"""
Unit tests for src/patcher.py — the surgical patch engine.

Tests cover:
  - parse_patch: valid formats, edge cases, malformed input
  - validate_patch: sandbox escape, missing targets
  - apply_patch: add/update/delete operations end-to-end
"""

import os
import pytest
import tempfile

from src.editor import FileEditor
from src.patcher import (
    parse_patch,
    validate_patch,
    apply_patch,
    PatchParseError,
    PatchAction,
)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

@pytest.fixture
def tmp_sandbox(tmp_path):
    """Return a FileEditor whose sandbox is an isolated temp directory."""
    return FileEditor(sandbox_root=str(tmp_path), enforce_protection=False)


@pytest.fixture
def sandbox_with_files(tmp_sandbox, tmp_path):
    """Pre-populate the sandbox with a couple of test files."""
    (tmp_path / "hello.py").write_text("def greet():\n    return 'hello'\n")
    (tmp_path / "config.yml").write_text("debug: false\nversion: 1\n")
    return tmp_sandbox


# ---------------------------------------------------------------------------
# parse_patch — format parsing
# ---------------------------------------------------------------------------

class TestParsePatch:

    def test_add_file(self):
        patch = """\
*** Begin Patch
*** Add File: src/newmodule.py
+def new_func():
+    pass
*** End Patch"""
        ops = parse_patch(patch)
        assert len(ops) == 1
        assert ops[0].action == PatchAction.ADD
        assert ops[0].path == "src/newmodule.py"
        assert "def new_func():" in ops[0].content

    def test_delete_file(self):
        patch = """\
*** Begin Patch
*** Delete File: src/old.py
*** End Patch"""
        ops = parse_patch(patch)
        assert len(ops) == 1
        assert ops[0].action == PatchAction.DELETE
        assert ops[0].path == "src/old.py"

    def test_update_file(self):
        patch = """\
*** Begin Patch
*** Update File: src/main.py
@@ def greet
- return 'hello'
+ return 'world'
*** End Patch"""
        ops = parse_patch(patch)
        assert len(ops) == 1
        assert ops[0].action == PatchAction.UPDATE
        assert len(ops[0].hunks) == 1
        # The parser strips the leading '-' but keeps any subsequent whitespace.
        # '- return ...' → " return ..." (space before 'return' is preserved).
        assert ops[0].hunks[0].old_lines == [" return 'hello'"]
        assert ops[0].hunks[0].new_lines == [" return 'world'"]

    def test_multi_operation_patch(self):
        patch = """\
*** Begin Patch
*** Add File: src/new.py
+# new
*** Delete File: src/old.py
*** Update File: src/main.py
@@ context
- old = 1
+ new = 2
*** End Patch"""
        ops = parse_patch(patch)
        assert len(ops) == 3
        actions = {op.action for op in ops}
        assert actions == {PatchAction.ADD, PatchAction.DELETE, PatchAction.UPDATE}

    def test_missing_begin_marker_raises(self):
        with pytest.raises(PatchParseError, match="Missing '\\*\\*\\* Begin Patch'"):
            parse_patch("*** End Patch\n")

    def test_missing_end_marker_raises(self):
        with pytest.raises(PatchParseError, match="Missing '\\*\\*\\* End Patch'"):
            parse_patch("*** Begin Patch\n*** Add File: x.py\n+content\n")

    def test_empty_patch_raises(self):
        with pytest.raises(PatchParseError, match="empty"):
            parse_patch("   ")

    def test_end_before_begin_raises(self):
        with pytest.raises(PatchParseError, match="End marker appears before begin"):
            parse_patch("*** End Patch\n*** Begin Patch\n")

    def test_no_operations_raises(self):
        with pytest.raises(PatchParseError, match="No valid operations"):
            parse_patch("*** Begin Patch\n# just a comment\n*** End Patch\n")

    def test_context_line_preserved_in_both_sides(self):
        """Context lines (space-prefixed) should appear in both old and new."""
        patch = """\
*** Begin Patch
*** Update File: x.py
@@ func
 context_line
- old
+ new
*** End Patch"""
        ops = parse_patch(patch)
        hunk = ops[0].hunks[0]
        assert "context_line" in hunk.old_lines
        assert "context_line" in hunk.new_lines

    def test_patch_embedded_in_prose(self):
        """Patches wrapped in prose (e.g. LLM reply text) should still parse."""
        patch = """\
Here is my change:

*** Begin Patch
*** Delete File: junk.py
*** End Patch

Let me know if that works.
"""
        ops = parse_patch(patch)
        assert len(ops) == 1
        assert ops[0].action == PatchAction.DELETE


# ---------------------------------------------------------------------------
# validate_patch
# ---------------------------------------------------------------------------

class TestValidatePatch:

    def test_update_nonexistent_file_returns_error(self, tmp_sandbox):
        patch = """\
*** Begin Patch
*** Update File: missing.py
@@ fn
- old
+ new
*** End Patch"""
        ops = parse_patch(patch)
        errors = validate_patch(ops, tmp_sandbox)
        assert any("missing.py" in e for e in errors)

    def test_delete_nonexistent_file_returns_error(self, tmp_sandbox):
        patch = """\
*** Begin Patch
*** Delete File: ghost.py
*** End Patch"""
        ops = parse_patch(patch)
        errors = validate_patch(ops, tmp_sandbox)
        assert any("ghost.py" in e for e in errors)

    def test_add_existing_file_is_warning_not_error(self, sandbox_with_files, capsys, caplog):
        """Adding over an existing file should warn but not block."""
        patch = """\
*** Begin Patch
*** Add File: hello.py
+# overwrite
*** End Patch"""
        ops = parse_patch(patch)
        errors = validate_patch(ops, sandbox_with_files)
        assert errors == []  # No hard errors

    def test_valid_operations_return_no_errors(self, sandbox_with_files):
        patch = """\
*** Begin Patch
*** Update File: hello.py
@@ greet
- return 'hello'
+ return 'world'
*** Delete File: config.yml
*** End Patch"""
        ops = parse_patch(patch)
        errors = validate_patch(ops, sandbox_with_files)
        assert errors == []

    def test_sandbox_escape_is_blocked(self, tmp_sandbox):
        patch = """\
*** Begin Patch
*** Update File: ../../etc/passwd
@@ root
- root:x:0:0
+ evil:x:0:0
*** End Patch"""
        ops = parse_patch(patch)
        errors = validate_patch(ops, tmp_sandbox)
        assert any("blocked" in e.lower() or "outside" in e.lower() for e in errors)


# ---------------------------------------------------------------------------
# apply_patch — filesystem effects
# ---------------------------------------------------------------------------

class TestApplyPatch:

    def test_add_creates_file(self, tmp_sandbox, tmp_path):
        patch = """\
*** Begin Patch
*** Add File: brand_new.py
+# brand new module
+def hello():
+    pass
*** End Patch"""
        ops = parse_patch(patch)
        result = apply_patch(ops, tmp_sandbox)
        assert "brand_new.py" in result.added
        assert (tmp_path / "brand_new.py").exists()
        assert "def hello" in (tmp_path / "brand_new.py").read_text()

    def test_delete_removes_file(self, sandbox_with_files, tmp_path):
        patch = """\
*** Begin Patch
*** Delete File: config.yml
*** End Patch"""
        ops = parse_patch(patch)
        result = apply_patch(ops, sandbox_with_files)
        assert "config.yml" in result.deleted
        assert not (tmp_path / "config.yml").exists()

    def test_update_replaces_content(self, sandbox_with_files, tmp_path):
        patch = """\
*** Begin Patch
*** Update File: hello.py
@@ greet
- return 'hello'
+ return 'world'
*** End Patch"""
        ops = parse_patch(patch)
        result = apply_patch(ops, sandbox_with_files)
        assert "hello.py" in result.modified
        assert "return 'world'" in (tmp_path / "hello.py").read_text()

    def test_update_missing_context_does_not_crash(self, sandbox_with_files):
        """A hunk whose old_lines aren't found should log a warning but not raise."""
        patch = """\
*** Begin Patch
*** Update File: hello.py
@@ greet
- nonexistent_line_xyz
+ replacement
*** End Patch"""
        ops = parse_patch(patch)
        # Should not raise — just silently skip the hunk
        result = apply_patch(ops, sandbox_with_files)
        # File will not appear in modified because no hunk matched
        assert "hello.py" not in result.modified

    def test_file_count_property(self, tmp_sandbox, tmp_path):
        patch = """\
*** Begin Patch
*** Add File: a.py
+pass
*** Add File: b.py
+pass
*** End Patch"""
        ops = parse_patch(patch)
        result = apply_patch(ops, tmp_sandbox)
        assert result.file_count == 2

    def test_summary_property(self, tmp_sandbox):
        patch = """\
*** Begin Patch
*** Add File: z.py
+pass
*** End Patch"""
        ops = parse_patch(patch)
        result = apply_patch(ops, tmp_sandbox)
        assert "A z.py" in result.summary

    def test_multi_hunk_update(self, tmp_sandbox, tmp_path):
        """Multiple hunks in one update should all apply.

        The patcher searches for joined old_lines as a substring of the file.
        The file line 'x = 1\n' means the search text must be ' x = 1' (with
        leading space, since '-' strips only the '-' char from '- x = 1').
        We write the patch with exact indentation to match the file.
        """
        (tmp_path / "multi.py").write_text(
            "x = 1\ny = 2\nz = 3\n"
        )
        # Use context lines (space-prefix) so old_lines match file content exactly
        patch = """\
*** Begin Patch
*** Update File: multi.py
@@ x
 x = 1
-y = 2
+y = 20
*** End Patch"""
        ops = parse_patch(patch)
        result = apply_patch(ops, tmp_sandbox)
        content = (tmp_path / "multi.py").read_text()
        assert "y = 20" in content

    def test_fuzzy_match_indented_function(self, tmp_sandbox, tmp_path):
        """Fuzzy matcher applies a patch to an indented body even when the
        '-' line has no leading indent in the patch text."""
        (tmp_path / "service.py").write_text(
            "class Greeter:\n"
            "    def greet(self):\n"
            "        return 'hello'\n"
            "\n"
            "    def farewell(self):\n"
            "        return 'bye'\n"
        )
        patch = """\
*** Begin Patch
*** Update File: service.py
@@ greet
- return 'hello'
+ return 'world'
*** End Patch"""
        ops = parse_patch(patch)
        result = apply_patch(ops, tmp_sandbox)
        content = (tmp_path / "service.py").read_text()
        assert "return 'world'" in content
        assert "return 'hello'" not in content
        assert result.file_count == 1

    def test_fuzzy_match_preserves_indentation(self, tmp_sandbox, tmp_path):
        """After fuzzy replacement the new line carries the original file indent."""
        (tmp_path / "mod.py").write_text(
            "def outer():\n"
            "    x = old_value\n"
            "    return x\n"
        )
        patch = """\
*** Begin Patch
*** Update File: mod.py
@@ outer
- x = old_value
+ x = new_value
*** End Patch"""
        ops = parse_patch(patch)
        apply_patch(ops, tmp_sandbox)
        content = (tmp_path / "mod.py").read_text()
        assert "    x = new_value" in content

    def test_fuzzy_no_match_falls_back_to_warning(self, tmp_sandbox, tmp_path, caplog):
        """When both passes fail, a warning is logged and the file is unchanged."""
        import logging
        (tmp_path / "x.py").write_text("a = 1\n")
        patch = """\
*** Begin Patch
*** Update File: x.py
@@ x
- totally_nonexistent_line
+ replacement
*** End Patch"""
        ops = parse_patch(patch)
        with caplog.at_level(logging.WARNING, logger="src.patcher"):
            result = apply_patch(ops, tmp_sandbox)
        assert "x.py" not in result.modified
        assert any("not found" in r.message for r in caplog.records)


# ---------------------------------------------------------------------------
# Regression tests — real LLM output patterns
# ---------------------------------------------------------------------------

class TestRegressionPatterns:
    """Pin exact patch formats the LLM produces in production to catch silent regressions.

    These tests mock event_bus.emit to avoid the live container logging/Redis path
    that can block when pytest runs inside Docker alongside the running FastAPI app.
    """

    @pytest.fixture(autouse=True)
    def _silence_event_bus(self, monkeypatch):
        """Replace event_bus.emit with a no-op for all regression tests."""
        import src.patcher as patcher_mod
        monkeypatch.setattr(patcher_mod.event_bus, "emit", lambda *a, **kw: None)

    def test_docstring_insertion_llm_format(self, tmp_sandbox, tmp_path):
        """Regression: LLM-generated docstring-insertion patch (Grok format).

        The LLM produces context lines (space-prefix) to anchor the hunk.
        New lines (+ prefix) insert above the context line. The patcher must
        produce a file where the function body has the docstring on line 2.
        """
        (tmp_path / "hello.py").write_text(
            'def greet(name):\n'
            '    print("Hello " + name)\n'
            '\n'
            'greet("world")\n'
        )
        patch = (
            '*** Begin Patch\n'
            '*** Update File: hello.py\n'
            '@@ greet\n'
            '+def greet(name):\n'
            '+    """Greet a person by name."""\n'
            '     print("Hello " + name)\n'
            '\n'
            'greet("world")\n'
            '*** End Patch'
        )
        ops = parse_patch(patch)
        result = apply_patch(ops, tmp_sandbox)
        content = (tmp_path / "hello.py").read_text()
        assert 'hello.py' in result.modified, f"File not in modified: {result.modified}"
        assert '"""Greet a person by name."""' in content, f"Docstring missing:\n{content}"
        assert 'print("Hello " + name)' in content

    def test_context_line_with_only_additions(self, tmp_sandbox, tmp_path):
        """A hunk with + lines above a context anchor should prepend without removing."""
        (tmp_path / "app.py").write_text(
            "import os\n"
            "\n"
            "def main():\n"
            "    pass\n"
        )
        patch = """\
*** Begin Patch
*** Update File: app.py
@@ import
+import sys
 import os
*** End Patch"""
        ops = parse_patch(patch)
        result = apply_patch(ops, tmp_sandbox)
        content = (tmp_path / "app.py").read_text()
        assert "import sys" in content
        assert "import os" in content

    def test_multi_file_patch_all_files_applied(self, tmp_sandbox, tmp_path):
        """All files in a multi-file patch must be applied, not just the first."""
        (tmp_path / "a.py").write_text("x = 1\n")
        (tmp_path / "b.py").write_text("y = 2\n")
        patch = """\
*** Begin Patch
*** Update File: a.py
@@ x
- x = 1
+ x = 10
*** Update File: b.py
@@ y
- y = 2
+ y = 20
*** End Patch"""
        ops = parse_patch(patch)
        result = apply_patch(ops, tmp_sandbox)
        assert (tmp_path / "a.py").read_text() == "x = 10\n"
        assert (tmp_path / "b.py").read_text() == "y = 20\n"
        assert result.file_count == 2

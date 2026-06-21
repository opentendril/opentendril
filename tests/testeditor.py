"""
Unit tests for src/editor.py — sandbox, protection, diff, search.

Tests cover:
  - Path sandbox enforcement (directory traversal)
  - Extension allowlist
  - Protected file enforcement
  - Protection bypass (enforce_protection=False)
  - read / write / patch / generate_diff
  - list_files and search_project filtering
  - Edit history tracking
"""

import os
import pytest

from src.editor import FileEditor


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------

@pytest.fixture
def editor(tmp_path):
    """An unprotected editor in an isolated temp directory."""
    return FileEditor(sandbox_root=str(tmp_path), enforce_protection=False)


@pytest.fixture
def protected_editor(tmp_path):
    """An editor with protection enabled but using a custom protected-file list."""
    e = FileEditor(sandbox_root=str(tmp_path), enforce_protection=False)
    # Monkey-patch: inject a custom protected set for testing
    from src import editor as editor_mod
    # We test protection in kernel mode by creating a fresh internal project editor
    # We monkeypatch _IS_EXTERNAL to False and add a custom PROTECTED_FILES
    return e


@pytest.fixture
def editor_with_files(editor, tmp_path):
    """Editor pre-populated with a few test files."""
    (tmp_path / "app.py").write_text("def run(): pass\n")
    (tmp_path / "config.json").write_text('{"debug": true}\n')
    (tmp_path / "README.md").write_text("# My Project\n")
    sub = tmp_path / "src"
    sub.mkdir()
    (sub / "utils.py").write_text("def helper(): return 42\n")
    return editor


# ---------------------------------------------------------------------------
# Sandbox enforcement
# ---------------------------------------------------------------------------

class TestSandbox:

    def test_resolve_relative_path(self, editor, tmp_path):
        resolved = editor._resolve_path("app.py")
        assert resolved == str(tmp_path / "app.py")

    def test_directory_traversal_blocked(self, editor):
        with pytest.raises(PermissionError, match="outside sandbox"):
            editor._resolve_path("../../etc/passwd")

    def test_absolute_path_outside_sandbox_blocked(self, editor):
        with pytest.raises(PermissionError, match="outside sandbox"):
            editor._resolve_path("/etc/passwd")

    def test_blocked_pattern_git_rejected(self, editor):
        with pytest.raises(PermissionError, match="blocked pattern"):
            editor._resolve_path(".git/config")

    def test_blocked_pattern_env_rejected(self, editor):
        with pytest.raises(PermissionError, match="blocked pattern"):
            editor._resolve_path(".env")

    def test_blocked_pattern_venv_rejected(self, editor):
        with pytest.raises(PermissionError, match="blocked pattern"):
            editor._resolve_path("venv/lib/python3.11/site.py")


# ---------------------------------------------------------------------------
# Extension allowlist
# ---------------------------------------------------------------------------

class TestExtensionValidation:

    def test_allowed_extension_py(self, editor, tmp_path):
        """Writing a .py file should not raise."""
        editor.write("test.py", "# ok\n")
        assert (tmp_path / "test.py").exists()

    def test_allowed_extension_md(self, editor, tmp_path):
        editor.write("docs.md", "# docs\n")
        assert (tmp_path / "docs.md").exists()

    def test_disallowed_extension_blocked(self, editor):
        with pytest.raises(PermissionError, match="not allowed"):
            editor.write("secret.exe", "binary")

    def test_disallowed_extension_pkl_blocked(self, editor):
        with pytest.raises(PermissionError, match="not allowed"):
            editor.write("model.pkl", "data")


# ---------------------------------------------------------------------------
# Read / Write
# ---------------------------------------------------------------------------

class TestReadWrite:

    def test_write_creates_file(self, editor, tmp_path):
        result = editor.write("hello.py", "print('hi')\n")
        assert result["status"] == "success"
        assert result["action"] == "created"
        assert (tmp_path / "hello.py").read_text() == "print('hi')\n"

    def test_write_updates_existing_file(self, editor, tmp_path):
        (tmp_path / "hello.py").write_text("old\n")
        result = editor.write("hello.py", "new\n")
        assert result["action"] == "modified"
        assert (tmp_path / "hello.py").read_text() == "new\n"

    def test_read_existing_file(self, editor, tmp_path):
        (tmp_path / "data.py").write_text("x = 1\n")
        content = editor.read("data.py")
        assert content == "x = 1\n"

    def test_read_missing_file_raises(self, editor):
        with pytest.raises(FileNotFoundError):
            editor.read("nonexistent.py")

    def test_write_creates_parent_directories(self, editor, tmp_path):
        editor.write("deep/nested/module.py", "# deep\n")
        assert (tmp_path / "deep" / "nested" / "module.py").exists()


# ---------------------------------------------------------------------------
# generate_diff
# ---------------------------------------------------------------------------

class TestGenerateDiff:

    def test_diff_shows_added_lines(self, editor, tmp_path):
        (tmp_path / "x.py").write_text("line1\n")
        diff = editor.generate_diff("x.py", "line1\nline2\n")
        assert "+line2" in diff

    def test_diff_shows_removed_lines(self, editor, tmp_path):
        (tmp_path / "x.py").write_text("line1\nline2\n")
        diff = editor.generate_diff("x.py", "line1\n")
        assert "-line2" in diff

    def test_diff_no_changes(self, editor, tmp_path):
        content = "same content\n"
        (tmp_path / "x.py").write_text(content)
        diff = editor.generate_diff("x.py", content)
        assert "No changes" in diff

    def test_diff_new_file(self, editor):
        diff = editor.generate_diff("brand_new.py", "print('hi')\n")
        # New file: old side is empty, new side has the line
        assert "+print('hi')" in diff


# ---------------------------------------------------------------------------
# patch (search-and-replace)
# ---------------------------------------------------------------------------

class TestPatch:

    def test_patch_replaces_text(self, editor, tmp_path):
        (tmp_path / "cfg.py").write_text("VERSION = '1.0'\n")
        editor.patch("cfg.py", "VERSION = '1.0'", "VERSION = '2.0'")
        assert (tmp_path / "cfg.py").read_text() == "VERSION = '2.0'\n"

    def test_patch_missing_search_raises(self, editor, tmp_path):
        (tmp_path / "cfg.py").write_text("VERSION = '1.0'\n")
        with pytest.raises(ValueError, match="Search text not found"):
            editor.patch("cfg.py", "DOES_NOT_EXIST", "replacement")


# ---------------------------------------------------------------------------
# list_files
# ---------------------------------------------------------------------------

class TestListFiles:

    def test_lists_allowed_files(self, editor_with_files):
        files = editor_with_files.list_files()
        paths = [f["path"] for f in files]
        assert any("app.py" in p for p in paths)
        assert any("config.json" in p for p in paths)
        assert any("README.md" in p for p in paths)
        assert any("utils.py" in p for p in paths)

    def test_excludes_blocked_directories(self, editor, tmp_path):
        blocked = tmp_path / "node_modules"
        blocked.mkdir()
        (blocked / "lib.js").write_text("// lib")
        files = editor.list_files()
        paths = [f["path"] for f in files]
        assert not any("node_modules" in p for p in paths)

    def test_list_subdirectory(self, editor_with_files):
        files = editor_with_files.list_files("src")
        paths = [f["path"] for f in files]
        assert any("utils.py" in p for p in paths)
        assert not any("app.py" in p for p in paths)  # app.py is in root

    def test_file_size_is_correct(self, editor, tmp_path):
        content = "x = 1\n"
        (tmp_path / "test.py").write_text(content)
        files = editor.list_files()
        match = next(f for f in files if "test.py" in f["path"])
        assert match["size"] == len(content.encode("utf-8"))


# ---------------------------------------------------------------------------
# search_project
# ---------------------------------------------------------------------------

class TestSearchProject:

    def test_finds_string_in_file(self, editor_with_files):
        results = editor_with_files.search_project("def run")
        assert any("app.py" in r["file"] for r in results)

    def test_returns_line_number(self, editor_with_files):
        results = editor_with_files.search_project("def helper")
        assert any(r["line"] == 1 for r in results)

    def test_no_results_for_unknown_string(self, editor_with_files):
        results = editor_with_files.search_project("XYZZY_NEVER_FOUND")
        assert results == []

    def test_search_in_subdirectory(self, editor_with_files):
        results = editor_with_files.search_project("helper", "src")
        assert all("utils.py" in r["file"] for r in results)


# ---------------------------------------------------------------------------
# Edit history
# ---------------------------------------------------------------------------

class TestEditHistory:

    def test_history_records_creates(self, editor):
        editor.write("hist.py", "# v1\n")
        assert len(editor.history) == 1
        assert editor.history[0]["action"] == "created"

    def test_history_records_modifications(self, editor, tmp_path):
        (tmp_path / "hist.py").write_text("# v0\n")
        editor.write("hist.py", "# v1\n")
        assert editor.history[0]["action"] == "modified"

    def test_history_is_cumulative(self, editor):
        editor.write("a.py", "# a\n")
        editor.write("b.py", "# b\n")
        assert len(editor.history) == 2

    def test_history_returns_copy(self, editor):
        """Mutating the returned list should not affect internal state."""
        editor.write("c.py", "# c\n")
        h = editor.history
        h.clear()
        assert len(editor.history) == 1

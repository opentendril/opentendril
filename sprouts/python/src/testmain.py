import json
import os
import signal
import subprocess
import time
from pathlib import Path

import pytest
import main

def test_read_file_tool(tmp_path: Path):
    file_path = tmp_path / "test.txt"
    file_path.write_text("hello world", encoding="utf-8")

    # Success
    resp = main.read_file_tool(tmp_path, {"path": "test.txt"})
    assert resp.status == "success"
    assert resp.output["content"] == "hello world"
    assert resp.output["path"] == "test.txt"

    # Missing path
    resp = main.read_file_tool(tmp_path, {})
    assert resp.status == "error"
    assert "requires a path" in resp.error

    # File does not exist
    resp = main.read_file_tool(tmp_path, {"path": "missing.txt"})
    assert resp.status == "error"
    assert "No such file or directory" in resp.error

    # Escapes workspace root
    resp = main.read_file_tool(tmp_path, {"path": "../escaped.txt"})
    assert resp.status == "error"
    assert "escapes the workspace root" in resp.error

def test_write_file_tool(tmp_path: Path):
    # Success overwrite
    resp = main.write_file_tool(tmp_path, {"path": "out.txt", "content": "first"})
    assert resp.status == "success"
    assert resp.output["mode"] == "overwrite"
    assert (tmp_path / "out.txt").read_text(encoding="utf-8") == "first"

    resp = main.write_file_tool(tmp_path, {"path": "out.txt", "content": "second"})
    assert resp.status == "success"
    assert resp.output["mode"] == "overwrite"
    assert (tmp_path / "out.txt").read_text(encoding="utf-8") == "second"

    # Success append
    resp = main.write_file_tool(tmp_path, {"path": "out.txt", "content": " third", "append": True})
    assert resp.status == "success"
    assert resp.output["mode"] == "append"
    assert (tmp_path / "out.txt").read_text(encoding="utf-8") == "second third"

    # Missing content (None) vs Empty string ("")
    resp = main.write_file_tool(tmp_path, {"path": "out.txt"})
    assert resp.status == "error"
    assert "requires content" in resp.error

    resp = main.write_file_tool(tmp_path, {"path": "empty.txt", "content": ""})
    assert resp.status == "success"
    assert (tmp_path / "empty.txt").read_text(encoding="utf-8") == ""

    # Missing path
    resp = main.write_file_tool(tmp_path, {"content": "data"})
    assert resp.status == "error"
    assert "requires a path" in resp.error

    # Create parents
    resp = main.write_file_tool(tmp_path, {"path": "a/b/c/new.txt", "content": "deep"})
    assert resp.status == "success"
    assert (tmp_path / "a/b/c/new.txt").read_text(encoding="utf-8") == "deep"

def test_list_files_tool(tmp_path: Path):
    (tmp_path / "b.txt").write_text("b")
    (tmp_path / "a.txt").write_text("a")
    (tmp_path / "dir").mkdir()
    (tmp_path / "dir" / "c.txt").write_text("c")

    # Success, sorted case-insensitively
    resp = main.list_files_tool(tmp_path, {})
    assert resp.status == "success"
    entries = resp.output["entries"]
    paths = [e["path"] for e in entries]
    assert paths == ["a.txt", "b.txt", "dir", "dir/c.txt"]
    
    types = [e["type"] for e in entries]
    assert types == ["file", "file", "dir", "file"]

    # SKIP_DIRS exclusion
    (tmp_path / ".git").mkdir()
    (tmp_path / ".git" / "config").write_text("config")
    resp = main.list_files_tool(tmp_path, {})
    paths = [e["path"] for e in resp.output["entries"]]
    assert not any(".git" in p for p in paths)

    # maxEntries truncation
    resp = main.list_files_tool(tmp_path, {"maxEntries": 2})
    assert resp.status == "success"
    assert resp.output["truncated"] is True
    assert len(resp.output["entries"]) == 2

    # maxDepth
    resp = main.list_files_tool(tmp_path, {"maxDepth": 1})
    assert resp.status == "success"
    paths = [e["path"] for e in resp.output["entries"]]
    assert "dir/c.txt" not in paths
    assert "a.txt" in paths

@pytest.fixture
def git_repo(tmp_path: Path) -> Path:
    subprocess.run(["git", "init"], cwd=tmp_path, check=True, capture_output=True)
    subprocess.run(["git", "config", "user.name", "TestUser"], cwd=tmp_path, check=True)
    subprocess.run(["git", "config", "user.email", "test@test.com"], cwd=tmp_path, check=True)
    return tmp_path

def test_git_commit_tool(git_repo: Path):
    (git_repo / "file1.txt").write_text("1")
    
    # Success
    resp = main.git_commit_tool(git_repo, {"message": "first commit"})
    assert resp.status == "success"
    assert resp.output["committed"] is True
    assert resp.output["hash"]

    log = subprocess.run(["git", "log", "-1", "--pretty=%B"], cwd=git_repo, capture_output=True, text=True).stdout
    assert "first commit" in log

    # Nothing to commit
    resp = main.git_commit_tool(git_repo, {"message": "second commit"})
    assert resp.status == "success"
    assert resp.output["committed"] is False
    assert resp.output["message"] == "nothing to commit"

    # Explicit paths
    (git_repo / "file2.txt").write_text("2")
    (git_repo / "file3.txt").write_text("3")
    resp = main.git_commit_tool(git_repo, {"message": "commit file2", "paths": ["file2.txt"]})
    assert resp.status == "success"
    assert resp.output["committed"] is True
    status_out = subprocess.run(["git", "status", "--porcelain"], cwd=git_repo, capture_output=True, text=True).stdout
    assert "file3.txt" in status_out
    assert "file2.txt" not in status_out

    # Error path
    resp = main.git_commit_tool(git_repo, {})
    assert resp.status == "error"
    assert "requires a message" in resp.error

def test_git_diff_tool(git_repo: Path):
    # Setup initial commit
    (git_repo / "file.txt").write_text("init")
    main.git_commit_tool(git_repo, {"message": "init"})

    # Unstaged diff
    (git_repo / "file.txt").write_text("modified")
    resp = main.git_diff_tool(git_repo, {})
    assert resp.status == "success"
    assert "-init" in resp.output["diff"]
    assert "+modified" in resp.output["diff"]

    # Staged diff
    subprocess.run(["git", "add", "file.txt"], cwd=git_repo, check=True)
    resp = main.git_diff_tool(git_repo, {"cached": True})
    assert resp.status == "success"
    assert "-init" in resp.output["diff"]
    assert "+modified" in resp.output["diff"]

    resp = main.git_diff_tool(git_repo, {})
    assert resp.status == "success"
    assert resp.output["diff"] == ""

def test_exec_command_tool(tmp_path: Path):
    # Success
    resp = main.exec_command_tool(tmp_path, {"command": "echo hello"})
    assert resp.status == "success"
    assert resp.output["exitCode"] == 0
    assert "hello" in resp.output["stdout"]

    # Non-zero exit
    resp = main.exec_command_tool(tmp_path, {"command": "echo out; echo err >&2; exit 3"})
    assert resp.status == "error"
    assert resp.output["exitCode"] == 3
    assert "out" in resp.output["stdout"]
    assert "err" in resp.output["stderr"]

    # Timeout with partial output
    start_time = time.monotonic()
    resp = main.exec_command_tool(tmp_path, {"command": "echo partial; sleep 5", "timeoutSeconds": 1})
    elapsed = time.monotonic() - start_time
    assert resp.status == "error"
    assert resp.output["exitCode"] == -1
    assert "partial" in resp.output["stdout"]
    assert elapsed < 3.0, "should kill process quickly on timeout"

    # Process-group kill regression
    marker_path = tmp_path / "marker.txt"
    resp = main.exec_command_tool(tmp_path, {"command": f"sleep 5 & echo $! > {marker_path}; sleep 5", "timeoutSeconds": 1})
    assert resp.status == "error"
    pid_str = marker_path.read_text(encoding="utf-8").strip()
    assert pid_str, "marker pid not found"
    pid = int(pid_str)
    with pytest.raises(ProcessLookupError):
        os.kill(pid, 0)

    # cwd argument
    subdir = tmp_path / "subdir"
    subdir.mkdir()
    resp = main.exec_command_tool(tmp_path, {"command": "pwd", "cwd": "subdir"})
    assert resp.status == "success"
    assert "subdir" in resp.output["stdout"]
    
def test_run_pytest_tool_and_pip(tmp_path: Path):
    # Pip missing args
    resp = main.run_pip_tool(tmp_path, {})
    assert resp.status == "error"
    assert "requires args" in resp.error
    
    # Optional real pip test
    resp = main.run_pip_tool(tmp_path, {"args": ["--version"]})
    assert resp.status == "success"
    assert "pip" in resp.output["stdout"]

    # Pytest pass
    test_file = tmp_path / "test_ok.py"
    test_file.write_text("def test_ok(): assert True", encoding="utf-8")
    resp = main.run_pytest_tool(tmp_path, {"cwd": "."})
    assert resp.status == "success"
    assert resp.output["exitCode"] == 0

    # Pytest fail
    test_file.write_text("def test_fail(): assert False", encoding="utf-8")
    resp = main.run_pytest_tool(tmp_path, {"cwd": "."})
    assert resp.status == "error"
    assert resp.output["exitCode"] != 0

def test_execute_tool_dispatch():
    # Empty tool
    resp = main.execute_tool(Path("."), {})
    assert resp.status == "error"
    assert "tool name is required" in resp.error

    # Unknown tool
    resp = main.execute_tool(Path("."), {"tool": "doesNotExist"})
    assert resp.status == "error"
    assert 'unsupported tool "doesNotExist"' in resp.error

    # listAvailableTools
    resp = main.execute_tool(Path("."), {"tool": "listAvailableTools"})
    assert resp.status == "success"
    assert isinstance(resp.output["tools"], list)
    assert len(resp.output["tools"]) > 0

    # Catalog parity check
    tools = main.available_tools()
    for t in tools:
        name = t["name"]
        resp = main.execute_tool(Path("."), {"tool": name})
        # It may return an error for missing args, but must not be unsupported
        if resp.status == "error":
            assert f'unsupported tool "{name}"' not in resp.error

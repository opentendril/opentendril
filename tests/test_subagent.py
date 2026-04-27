"""
tests/test_subagent.py — Unit tests for the ephemeral sub-agent system.

All tests use mock LLMs — no live API calls required.
"""

import pytest
from unittest.mock import MagicMock, patch
from langchain_core.messages import AIMessage

from src.subagent import (
    SubAgentRunner, SubAgentProfile, spawn, list_profiles, _PROFILES
)


def _mock_llm(content: str, tool_calls: list = None) -> MagicMock:
    llm = MagicMock()
    resp = MagicMock()
    resp.content = content
    resp.tool_calls = tool_calls or []
    llm.invoke.return_value = resp
    llm.bind_tools.return_value = llm
    return llm


def _mock_router(content: str = "Analysis complete.") -> MagicMock:
    router = MagicMock()
    router.get.return_value = _mock_llm(content)
    return router


def _make_tool(name: str, result: str = "tool result") -> MagicMock:
    t = MagicMock()
    t.name = name
    t.invoke.return_value = result
    return t


class TestListProfiles:
    def test_returns_all_builtin_profiles(self):
        profiles = list_profiles()
        assert "security_auditor" in profiles
        assert "code_reviewer" in profiles
        assert "test_writer" in profiles
        assert "documenter" in profiles
        assert "linter" in profiles

    def test_returns_descriptions(self):
        profiles = list_profiles()
        for name, desc in profiles.items():
            assert isinstance(desc, str) and len(desc) > 0


class TestSubAgentRunner:
    def _make_runner(self, allowed_tools, response="Done."):
        profile = SubAgentProfile(
            name="test_worker",
            description="Test worker",
            system_prompt="You are a test worker.",
            allowed_tools=allowed_tools,
            tier="fast",
        )
        parent_tools = [
            _make_tool("read_file"),
            _make_tool("write_file"),
            _make_tool("search_project"),
        ]
        router = _mock_router(response)
        return SubAgentRunner(profile=profile, parent_tools=parent_tools, router=router)

    def test_restricts_tools_to_whitelist(self):
        runner = self._make_runner(allowed_tools=["read_file"])
        assert len(runner.tools) == 1
        assert runner.tools[0].name == "read_file"

    def test_allows_all_whitelisted_tools(self):
        runner = self._make_runner(allowed_tools=["read_file", "write_file"])
        assert {t.name for t in runner.tools} == {"read_file", "write_file"}

    def test_excludes_non_whitelisted_tools(self):
        runner = self._make_runner(allowed_tools=["read_file"])
        tool_names = {t.name for t in runner.tools}
        assert "write_file" not in tool_names

    def test_run_returns_final_response(self):
        runner = self._make_runner(allowed_tools=["read_file"], response="Security: all clear.")
        result = runner.run("Audit the auth module.")
        assert "Security: all clear." in result

    def test_run_handles_llm_error_gracefully(self):
        profile = SubAgentProfile(
            name="failing_worker", description="", system_prompt="",
            allowed_tools=[], tier="fast",
        )
        router = MagicMock()
        router.get.side_effect = RuntimeError("LLM unavailable")
        runner = SubAgentRunner(profile=profile, parent_tools=[], router=router)
        result = runner.run("task")
        assert "❌" in result

    def test_run_refuses_disallowed_tool_call(self):
        """Sub-agent should block attempts to use tools outside its whitelist."""
        profile = SubAgentProfile(
            name="readonly_worker", description="", system_prompt="",
            allowed_tools=["read_file"], tier="fast",
        )
        # Simulate LLM trying to call write_file (not in whitelist), then returning
        llm = MagicMock()
        call_resp = MagicMock()
        call_resp.content = ""
        call_resp.tool_calls = [{"name": "write_file", "args": {"filepath": "x", "content": "y"}, "id": "tc1"}]
        final_resp = MagicMock()
        final_resp.content = "I tried to write."
        final_resp.tool_calls = []
        llm.invoke.side_effect = [call_resp, final_resp]
        llm.bind_tools.return_value = llm

        router = MagicMock()
        router.get.return_value = llm

        runner = SubAgentRunner(
            profile=profile,
            parent_tools=[_make_tool("read_file"), _make_tool("write_file")],
            router=router,
        )
        result = runner.run("Try to modify a file.")
        # Should have received error about disallowed tool, but continued to final response
        assert "I tried to write." in result


class TestSpawnFunction:
    def test_valid_profile_runs_successfully(self):
        with patch("src.subagent.SubAgentRunner") as MockRunner:
            instance = MagicMock()
            instance.run.return_value = "Report done."
            MockRunner.return_value = instance
            result = spawn("security_auditor", "Audit src/", [], _mock_router())
        assert "Report done." in result

    def test_invalid_profile_returns_error(self):
        result = spawn("nonexistent_profile", "Do something", [], _mock_router())
        assert "❌" in result
        assert "nonexistent_profile" in result

    def test_error_message_lists_available_profiles(self):
        result = spawn("fake", "task", [], _mock_router())
        for name in _PROFILES.keys():
            assert name in result


class TestBuiltinProfiles:
    @pytest.mark.parametrize("profile_name,should_allow,should_deny", [
        ("security_auditor", ["read_file", "search_project"], ["write_file", "run_bash_command"]),
        ("code_reviewer",    ["read_file", "search_memory"],  ["write_file", "run_bash_command"]),
        ("test_writer",      ["read_file", "write_file"],     ["run_bash_command"]),
        ("documenter",       ["read_file", "write_file"],     ["run_bash_command"]),
        ("linter",           ["read_file", "run_bash_command"], ["write_file"]),
    ])
    def test_profile_tool_access(self, profile_name, should_allow, should_deny):
        profile = _PROFILES[profile_name]
        for tool_name in should_allow:
            assert tool_name in profile.allowed_tools, \
                f"'{profile_name}' should allow '{tool_name}'"
        for tool_name in should_deny:
            assert tool_name not in profile.allowed_tools, \
                f"'{profile_name}' should NOT allow '{tool_name}'"

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

SKIP_DIRS = {".git", "node_modules", "vendor", ".venv", "venv", "dist", "build", "__pycache__"}
AGENT_MAX_ITERATIONS = 20
MARKDOWN_CODE_BLOCK_RE = re.compile(r"```([^\n`]*)\n(.*?)```", re.DOTALL)
PATH_LIKE_RE = re.compile(r"^[\w./-]+\.[\w]+$")
PATH_COMMENT_RES = [
    re.compile(r"^\s*//\s*(?:file(?:path)?|path)\s*:\s*(?P<path>[^\s*]+)\s*$", re.IGNORECASE),
    re.compile(r"^\s*//\s*(?P<path>[\w./-]+\.[\w]+)\s*$"),
    re.compile(r"^\s*#\s*(?:file(?:path)?|path)\s*:\s*(?P<path>[^\s#]+)\s*$", re.IGNORECASE),
    re.compile(r"^\s*#\s*(?P<path>[\w./-]+\.[\w]+)\s*$"),
    re.compile(r"^\s*/\*\s*(?:file(?:path)?|path)\s*:\s*(?P<path>[^\s*]+)\s*\*/\s*$", re.IGNORECASE),
    re.compile(r"^\s*/\*\s*(?P<path>[\w./-]+\.[\w]+)\s*\*/\s*$"),
    re.compile(r"^\s*<!--\s*(?:file(?:path)?|path)\s*:\s*(?P<path>[^\s]+)\s*-->\s*$", re.IGNORECASE),
]
LANGUAGE_ALIASES = {
    "golang": "go",
    "py": "python",
    "ts": "typescript",
    "js": "javascript",
    "sh": "bash",
    "yml": "yaml",
}
LANGUAGE_EXTENSIONS = {
    "go": ".go",
    "python": ".py",
    "typescript": ".ts",
    "javascript": ".js",
    "java": ".java",
    "rust": ".rs",
    "ruby": ".rb",
    "php": ".php",
    "bash": ".sh",
    "shell": ".sh",
    "yaml": ".yaml",
    "json": ".json",
    "toml": ".toml",
    "markdown": ".md",
    "html": ".html",
    "css": ".css",
    "sql": ".sql",
    "c": ".c",
    "cpp": ".cc",
    "csharp": ".cs",
}
FILEPATH_WARNING_COMMAND = (
    "echo 'WARNING: Could not infer filepath for a markdown code block. "
    "Please specify the target file path using a comment on the first line "
    "(e.g. // path/to/file.go).'"
)


@dataclass
class ToolResponse:
    status: str
    output: Any | None = None
    error: str | None = None


@dataclass
class MarkdownCodeBlock:
    info: str
    content: str


def main() -> None:
    workspace_root = Path.cwd().resolve()

    if should_run_agent_loop(sys.argv):
        task_prompt = resolve_agent_task_prompt(sys.argv)
        if not task_prompt:
            print(
                json.dumps(
                    {
                        "status": "error",
                        "error": "agent mode requires a task prompt via --task, argv, or TENDRIL_TASK",
                    },
                    ensure_ascii=False,
                ),
                flush=True,
            )
            sys.exit(1)
        run_agent_loop(workspace_root, task_prompt)
        return

    for raw_line in sys.stdin:
        trimmed = raw_line.strip()
        if not trimmed:
            continue

        try:
            call = json.loads(trimmed)
            response = execute_tool(workspace_root, call)
        except Exception as exc:  # noqa: BLE001
            response = ToolResponse(status="error", error=str(exc))

        print(json.dumps(response.__dict__, ensure_ascii=False), flush=True)


def should_run_agent_loop(argv: list[str]) -> bool:
    if os.environ.get("TENDRIL_AGENT_LOOP", "").strip().lower() in {"1", "true", "yes"}:
        return True
    return "--agent" in argv


def resolve_agent_task_prompt(argv: list[str]) -> str:
    env_task = os.environ.get("TENDRIL_TASK", "").strip()
    if env_task:
        return env_task

    if "--task" in argv:
        task_index = argv.index("--task")
        if task_index + 1 < len(argv):
            return argv[task_index + 1].strip()

    if "--agent" in argv:
        agent_index = argv.index("--agent")
        trailing = [part.strip() for part in argv[agent_index + 1 :] if part.strip() and part != "--task"]
        if trailing:
            return " ".join(trailing)

    return ""


def has_significant_markdown_code_blocks(content: str) -> bool:
    if not content or not content.strip():
        return False
    return bool(MARKDOWN_CODE_BLOCK_RE.search(content))


def extract_markdown_code_blocks(content: str) -> list[MarkdownCodeBlock]:
    blocks: list[MarkdownCodeBlock] = []
    for match in MARKDOWN_CODE_BLOCK_RE.finditer(content):
        info = match.group(1).strip()
        code = match.group(2)
        if not code.strip():
            continue
        blocks.append(MarkdownCodeBlock(info=info, content=code))
    return blocks


def normalize_inferred_path(raw_path: str) -> str | None:
    cleaned = raw_path.strip().strip("\"'`")
    cleaned = cleaned.replace("\\", "/")
    cleaned = re.sub(r"^\.?/+", "", cleaned)
    if not cleaned or cleaned.startswith(".."):
        return None
    if not PATH_LIKE_RE.match(cleaned):
        return None
    return cleaned


def infer_filepath_from_comment(content: str) -> str | None:
    first_line = content.splitlines()[0] if content else ""
    for pattern in PATH_COMMENT_RES:
        match = pattern.match(first_line)
        if not match:
            continue
        inferred = normalize_inferred_path(match.group("path"))
        if inferred:
            return inferred
    return None


def infer_filepath_from_fence_info(info: str) -> str | None:
    candidate = info.strip()
    if not candidate:
        return None

    for token in candidate.split():
        normalized = normalize_inferred_path(token)
        if normalized:
            return normalized

    lowered = candidate.lower()
    if lowered in LANGUAGE_ALIASES:
        return None
    if lowered in LANGUAGE_EXTENSIONS:
        return None
    if "/" in candidate or "." in candidate:
        return normalize_inferred_path(candidate)
    return None


def infer_filepath(block: MarkdownCodeBlock) -> str | None:
    from_info = infer_filepath_from_fence_info(block.info)
    if from_info:
        return from_info
    return infer_filepath_from_comment(block.content)


def build_synthetic_write_file_call(path: str, content: str) -> dict[str, Any]:
    return {
        "tool": "writeFile",
        "arguments": {
            "path": path,
            "content": content,
            "append": False,
        },
    }


def build_filepath_warning_call() -> dict[str, Any]:
    return {
        "tool": "execCommand",
        "arguments": {
            "command": FILEPATH_WARNING_COMMAND,
        },
    }


def parse_markdown_to_synthetic_tool_calls(content: str) -> list[dict[str, Any]]:
    synthetic_calls: list[dict[str, Any]] = []
    for block in extract_markdown_code_blocks(content):
        inferred_path = infer_filepath(block)
        if inferred_path:
            synthetic_calls.append(build_synthetic_write_file_call(inferred_path, block.content))
        else:
            synthetic_calls.append(build_filepath_warning_call())
    return synthetic_calls


def should_use_markdown_fallback(tool_calls: list[Any], content: str) -> bool:
    if not tool_calls:
        return True
    return has_significant_markdown_code_blocks(content)


def render_tool_observation(tool_name: str, response: ToolResponse) -> str:
    payload = json.dumps(response.__dict__, ensure_ascii=False, indent=2)
    return f"Tool result for {tool_name}:\n{payload}"


def render_fallback_observation(results: list[tuple[str, ToolResponse]]) -> str:
    lines = [
        "Markdown tool-call fallback applied. The following synthetic tool calls were executed:",
    ]
    for tool_name, response in results:
        lines.append(render_tool_observation(tool_name, response))
    lines.append(
        "Files were written successfully where paths could be inferred. "
        "Continue the task or respond with structured tool calls when finished."
    )
    return "\n\n".join(lines)


def execute_synthetic_tool_calls(
    workspace_root: Path,
    synthetic_calls: list[dict[str, Any]],
) -> list[tuple[str, ToolResponse]]:
    results: list[tuple[str, ToolResponse]] = []
    for call in synthetic_calls:
        tool_name = str(call.get("tool", "")).strip()
        response = execute_tool(workspace_root, call)
        results.append((tool_name, response))
    return results


def get_llm_model_name() -> str:
    for key in (
        "TENDRIL_MODEL",
        "LOCAL_MODEL_NAME",
        "OPENAI_MODEL",
        "OPENTENDRIL_MODEL",
    ):
        value = os.environ.get(key, "").strip()
        if value:
            return value
    return "qwen2.5-coder"


def get_llm_client() -> Any:
    try:
        from openai import OpenAI
    except ImportError as exc:  # pragma: no cover - dependency should exist in sprout image
        raise RuntimeError("openai package is required for agent loop mode") from exc

    api_key = (
        os.environ.get("OPENAI_API_KEY")
        or os.environ.get("OPENTENDRIL_API_KEY")
        or os.environ.get("LOCAL_INFERENCE_API_KEY")
        or "not-needed"
    )
    base_url = (
        os.environ.get("OPENAI_BASE_URL")
        or os.environ.get("LOCAL_INFERENCE_URL")
        or os.environ.get("OPENTENDRIL_BASE_URL")
        or "http://localhost:11434/v1"
    )
    return OpenAI(api_key=api_key, base_url=base_url)


def build_agent_system_prompt(workspace_root: Path) -> str:
    tool_lines = []
    for tool in available_tools():
        tool_lines.append(f"- {tool['name']}: {tool.get('description', '').strip()}")
    catalog = "\n".join(tool_lines)
    return (
        "You are the OpenTendril Python Sprout agent.\n"
        "Use JSON function calling via the provided tools to read and write workspace files.\n"
        "When writing code, prefer structured writeFile tool calls.\n"
        "If you must emit markdown code blocks, put the target path in the fence header "
        "or as the first-line comment (e.g. // path/to/file.go).\n\n"
        f"Workspace root: {workspace_root}\n\n"
        f"Available tools:\n{catalog}"
    )


def openai_tools_schema() -> list[dict[str, Any]]:
    schemas: list[dict[str, Any]] = []
    for tool in available_tools():
        if tool["name"] == "listAvailableTools":
            continue
        properties: dict[str, Any] = {}
        required: list[str] = []
        for argument in tool.get("arguments", []):
            arg_type = argument.get("type", "string")
            json_type = "string"
            if arg_type == "boolean":
                json_type = "boolean"
            elif arg_type == "number":
                json_type = "number"
            elif arg_type == "string[]":
                json_type = "array"
            properties[argument["name"]] = {
                "type": json_type,
                "description": argument.get("description", ""),
            }
            if argument.get("required"):
                required.append(argument["name"])
        schemas.append(
            {
                "type": "function",
                "function": {
                    "name": tool["name"],
                    "description": tool.get("description", ""),
                    "parameters": {
                        "type": "object",
                        "properties": properties,
                        "required": required,
                    },
                },
            }
        )
    return schemas


def openai_tool_call_to_executor_call(tool_call: Any) -> dict[str, Any]:
    arguments_raw = tool_call.function.arguments
    if isinstance(arguments_raw, dict):
        arguments = arguments_raw
    else:
        arguments = json.loads(arguments_raw or "{}")
    return {
        "tool": tool_call.function.name,
        "arguments": arguments,
    }


def serialize_assistant_message(message: Any) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "role": "assistant",
        "content": message.content,
    }
    if message.tool_calls:
        payload["tool_calls"] = [
            {
                "id": tool_call.id,
                "type": tool_call.type,
                "function": {
                    "name": tool_call.function.name,
                    "arguments": tool_call.function.arguments,
                },
            }
            for tool_call in message.tool_calls
        ]
    return payload


def run_agent_loop(workspace_root: Path, task_prompt: str) -> None:
    client = get_llm_client()
    model_name = get_llm_model_name()
    messages: list[dict[str, Any]] = [
        {"role": "system", "content": build_agent_system_prompt(workspace_root)},
        {"role": "user", "content": task_prompt},
    ]

    for _ in range(AGENT_MAX_ITERATIONS):
        completion = client.chat.completions.create(
            model=model_name,
            messages=messages,
            tools=openai_tools_schema(),
            tool_choice="auto",
        )
        message = completion.choices[0].message
        tool_calls = list(message.tool_calls or [])
        content = message.content or ""

        if should_use_markdown_fallback(tool_calls, content):
            synthetic_calls = parse_markdown_to_synthetic_tool_calls(content)
            if synthetic_calls:
                results = execute_synthetic_tool_calls(workspace_root, synthetic_calls)
                messages.append({"role": "assistant", "content": content})
                messages.append({"role": "user", "content": render_fallback_observation(results)})
                continue

        if not tool_calls:
            print(
                json.dumps(
                    {"status": "success", "output": {"final": content.strip()}},
                    ensure_ascii=False,
                ),
                flush=True,
            )
            return

        messages.append(serialize_assistant_message(message))
        for tool_call in tool_calls:
            executor_call = openai_tool_call_to_executor_call(tool_call)
            response = execute_tool(workspace_root, executor_call)
            messages.append(
                {
                    "role": "tool",
                    "tool_call_id": tool_call.id,
                    "content": json.dumps(response.__dict__, ensure_ascii=False),
                }
            )

    print(
        json.dumps(
            {
                "status": "error",
                "error": f"agent reached max iterations ({AGENT_MAX_ITERATIONS})",
            },
            ensure_ascii=False,
        ),
        flush=True,
    )
    sys.exit(1)


def execute_tool(workspace_root: Path, call: dict[str, Any]) -> ToolResponse:
    tool_name = str(call.get("tool", "")).strip()
    arguments = call.get("arguments") or {}
    if not tool_name:
        return ToolResponse(status="error", error="tool name is required")

    if tool_name == "readFile":
        return read_file_tool(workspace_root, arguments)
    if tool_name == "writeFile":
        return write_file_tool(workspace_root, arguments)
    if tool_name == "listFiles":
        return list_files_tool(workspace_root, arguments)
    if tool_name == "gitCommit":
        return git_commit_tool(workspace_root, arguments)
    if tool_name == "gitDiff":
        return git_diff_tool(workspace_root, arguments)
    if tool_name == "execCommand":
        return exec_command_tool(workspace_root, arguments)
    if tool_name == "runPytest":
        return run_pytest_tool(workspace_root, arguments)
    if tool_name == "runPip":
        return run_pip_tool(workspace_root, arguments)
    if tool_name == "listAvailableTools":
        return ToolResponse(status="success", output={"tools": available_tools()})
    return ToolResponse(status="error", error=f'unsupported tool "{tool_name}"')


def available_tools() -> list[dict[str, Any]]:
    return [
        {
            "name": "readFile",
            "description": "Read a text file from the workspace.",
            "arguments": [
                {
                    "name": "path",
                    "type": "string",
                    "description": "Path to the file, relative to the workspace root.",
                    "required": True,
                }
            ],
        },
        {
            "name": "writeFile",
            "description": "Write text content to a file, creating parent directories when needed.",
            "arguments": [
                {
                    "name": "path",
                    "type": "string",
                    "description": "Path to the file, relative to the workspace root.",
                    "required": True,
                },
                {
                    "name": "content",
                    "type": "string",
                    "description": "The full file contents to write.",
                    "required": True,
                },
                {
                    "name": "append",
                    "type": "boolean",
                    "description": "Append instead of overwriting the file.",
                },
            ],
        },
        {
            "name": "listFiles",
            "description": "List files and directories under a workspace path.",
            "arguments": [
                {
                    "name": "path",
                    "type": "string",
                    "description": "Directory to list, relative to the workspace root.",
                },
                {
                    "name": "maxDepth",
                    "type": "number",
                    "description": "Maximum recursion depth to traverse.",
                },
                {
                    "name": "maxEntries",
                    "type": "number",
                    "description": "Maximum number of entries to return.",
                },
            ],
        },
        {
            "name": "gitCommit",
            "description": "Stage files and create a git commit.",
            "arguments": [
                {
                    "name": "message",
                    "type": "string",
                    "description": "Commit message.",
                    "required": True,
                },
                {
                    "name": "paths",
                    "type": "string[]",
                    "description": "Optional list of paths to stage instead of all changes.",
                },
            ],
        },
        {
            "name": "gitDiff",
            "description": "Show the current git diff.",
            "arguments": [
                {
                    "name": "cached",
                    "type": "boolean",
                    "description": "Show the staged diff instead of the working tree diff.",
                },
                {
                    "name": "paths",
                    "type": "string[]",
                    "description": "Optional list of paths to limit the diff.",
                },
            ],
        },
        {
            "name": "execCommand",
            "description": "Run a shell command inside the workspace.",
            "arguments": [
                {
                    "name": "command",
                    "type": "string",
                    "description": "Shell command to execute.",
                    "required": True,
                },
                {
                    "name": "cwd",
                    "type": "string",
                    "description": "Optional working directory, relative to the workspace root.",
                },
                {
                    "name": "timeoutSeconds",
                    "type": "number",
                    "description": "Optional timeout in seconds.",
                },
            ],
        },
        {
            "name": "runPytest",
            "description": "Run pytest inside the workspace.",
            "arguments": [
                {
                    "name": "args",
                    "type": "string[]",
                    "description": "Additional arguments to pass to pytest.",
                },
                {
                    "name": "cwd",
                    "type": "string",
                    "description": "Optional working directory, relative to the workspace root.",
                },
                {
                    "name": "timeoutSeconds",
                    "type": "number",
                    "description": "Optional timeout in seconds.",
                },
            ],
        },
        {
            "name": "runPip",
            "description": "Run pip inside the workspace.",
            "arguments": [
                {
                    "name": "args",
                    "type": "string[]",
                    "description": "Arguments to pass to pip.",
                    "required": True,
                },
                {
                    "name": "cwd",
                    "type": "string",
                    "description": "Optional working directory, relative to the workspace root.",
                },
                {
                    "name": "timeoutSeconds",
                    "type": "number",
                    "description": "Optional timeout in seconds.",
                },
            ],
        },
        {
            "name": "listAvailableTools",
            "description": "Return the executor tool catalog.",
        },
    ]


def read_file_tool(workspace_root: Path, args: dict[str, Any]) -> ToolResponse:
    raw_path = string_arg(args, "path")
    if not raw_path:
        return ToolResponse(status="error", error="readFile requires a path")

    resolved = resolve_workspace_path(workspace_root, raw_path)
    if resolved.error:
        return ToolResponse(status="error", error=resolved.error)

    try:
        content = resolved.abs_path.read_text(encoding="utf-8")
        return ToolResponse(status="success", output={"path": resolved.rel_path, "content": content})
    except Exception as exc:  # noqa: BLE001
        return ToolResponse(status="error", error=str(exc))


def write_file_tool(workspace_root: Path, args: dict[str, Any]) -> ToolResponse:
    raw_path = string_arg(args, "path")
    content = string_arg(args, "content")
    if not raw_path:
        return ToolResponse(status="error", error="writeFile requires a path")
    if content is None:
        return ToolResponse(status="error", error="writeFile requires content")

    resolved = resolve_workspace_path(workspace_root, raw_path)
    if resolved.error:
        return ToolResponse(status="error", error=resolved.error)

    append = bool_arg(args, "append") or False
    try:
        resolved.abs_path.parent.mkdir(parents=True, exist_ok=True)
        mode = "a" if append else "w"
        with resolved.abs_path.open(mode, encoding="utf-8") as handle:
            handle.write(content)
        return ToolResponse(
            status="success",
            output={
                "path": resolved.rel_path,
                "bytesWritten": len(content.encode("utf-8")),
                "mode": "append" if append else "overwrite",
            },
        )
    except Exception as exc:  # noqa: BLE001
        return ToolResponse(status="error", error=str(exc))


def list_files_tool(workspace_root: Path, args: dict[str, Any]) -> ToolResponse:
    raw_path = string_arg(args, "path") or "."
    resolved = resolve_workspace_path(workspace_root, raw_path)
    if resolved.error:
        return ToolResponse(status="error", error=resolved.error)

    max_depth = max(0, int(number_arg(args, "maxDepth") or 3))
    max_entries = max(1, int(number_arg(args, "maxEntries") or 500))

    try:
        entries: list[dict[str, Any]] = []
        truncated = False
        if resolved.abs_path.is_dir():
            truncated = walk_directory(resolved.abs_path, resolved.rel_path, 0, max_depth, max_entries, entries)
        else:
            entries.append(entry_for_path(resolved.rel_path, resolved.abs_path))

        return ToolResponse(
            status="success",
            output={"root": resolved.rel_path, "entries": entries, "truncated": truncated},
        )
    except Exception as exc:  # noqa: BLE001
        return ToolResponse(status="error", error=str(exc))


def git_commit_tool(workspace_root: Path, args: dict[str, Any]) -> ToolResponse:
    message = string_arg(args, "message")
    if not message:
        return ToolResponse(status="error", error="gitCommit requires a message")

    paths = string_array_arg(args, "paths") or []

    try:
        if paths:
            resolved_paths: list[str] = []
            for raw_path in paths:
                resolved = resolve_workspace_path(workspace_root, raw_path)
                if resolved.error:
                    return ToolResponse(status="error", error=resolved.error)
                resolved_paths.append(resolved.rel_path)
            run_git(workspace_root, "add", "--", *resolved_paths)
        else:
            run_git(workspace_root, "add", "-A")

        status_output = run_git(workspace_root, "status", "--porcelain")
        if not status_output.strip():
            return ToolResponse(
                status="success",
                output={"committed": False, "message": "nothing to commit", "paths": paths},
            )

        run_git(
            workspace_root,
            "-c",
            "user.name=OpenTendril",
            "-c",
            "user.email=opentendril@localhost",
            "commit",
            "-m",
            message,
        )
        hash_output = run_git(workspace_root, "rev-parse", "HEAD").strip()

        return ToolResponse(
            status="success",
            output={"committed": True, "hash": hash_output, "message": message, "paths": paths},
        )
    except subprocess.CalledProcessError as exc:
        error_output = exc.stderr.strip() if exc.stderr else str(exc)
        return ToolResponse(status="error", error=error_output)


def git_diff_tool(workspace_root: Path, args: dict[str, Any]) -> ToolResponse:
    cached = bool_arg(args, "cached") or False
    paths = string_array_arg(args, "paths") or []
    try:
        diff_args = ["diff", "--no-color", "--binary"]
        if cached:
            diff_args.append("--cached")
        if paths:
            diff_args.append("--")
            diff_args.extend(paths)
        diff_output = run_git(workspace_root, *diff_args)
        return ToolResponse(
            status="success",
            output={"diff": diff_output, "cached": cached, "paths": paths},
        )
    except subprocess.CalledProcessError as exc:
        error_output = exc.stderr.strip() if exc.stderr else str(exc)
        return ToolResponse(status="error", error=error_output)


def exec_command_tool(workspace_root: Path, args: dict[str, Any]) -> ToolResponse:
    command = string_arg(args, "command")
    if not command:
        return ToolResponse(status="error", error="execCommand requires a command")

    cwd_raw = string_arg(args, "cwd") or "."
    resolved_cwd = resolve_workspace_path(workspace_root, cwd_raw)
    if resolved_cwd.error:
        return ToolResponse(status="error", error=resolved_cwd.error)

    timeout_seconds = number_arg(args, "timeoutSeconds") or 120
    timeout = max(1, int(timeout_seconds))

    try:
        result = subprocess.run(
            command,
            shell=True,
            cwd=resolved_cwd.abs_path,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
        payload = {
            "command": command,
            "cwd": resolved_cwd.rel_path,
            "stdout": result.stdout,
            "stderr": result.stderr,
            "exitCode": result.returncode,
        }
        if result.returncode != 0:
            return ToolResponse(status="error", output=payload, error=f"command exited with code {result.returncode}")
        return ToolResponse(status="success", output=payload)
    except subprocess.TimeoutExpired as exc:
        return ToolResponse(
            status="error",
            error=f"command timed out after {timeout} seconds",
            output={
                "command": command,
                "cwd": resolved_cwd.rel_path,
                "stdout": exc.stdout or "",
                "stderr": exc.stderr or "",
                "exitCode": -1,
            },
        )


def run_pytest_tool(workspace_root: Path, args: dict[str, Any]) -> ToolResponse:
    pytest_args = string_array_arg(args, "args") or []
    cwd_raw = string_arg(args, "cwd") or "."
    resolved_cwd = resolve_workspace_path(workspace_root, cwd_raw)
    if resolved_cwd.error:
        return ToolResponse(status="error", error=resolved_cwd.error)

    timeout_seconds = number_arg(args, "timeoutSeconds") or 300
    timeout = max(1, int(timeout_seconds))

    return run_python_module(
        workspace_root,
        ["-m", "pytest", *pytest_args],
        resolved_cwd,
        timeout,
        tool_name="runPytest",
    )


def run_pip_tool(workspace_root: Path, args: dict[str, Any]) -> ToolResponse:
    pip_args = string_array_arg(args, "args") or []
    if not pip_args:
        return ToolResponse(status="error", error="runPip requires args")

    cwd_raw = string_arg(args, "cwd") or "."
    resolved_cwd = resolve_workspace_path(workspace_root, cwd_raw)
    if resolved_cwd.error:
        return ToolResponse(status="error", error=resolved_cwd.error)

    timeout_seconds = number_arg(args, "timeoutSeconds") or 300
    timeout = max(1, int(timeout_seconds))

    return run_python_module(
        workspace_root,
        ["-m", "pip", *pip_args],
        resolved_cwd,
        timeout,
        tool_name="runPip",
    )


def run_python_module(
    workspace_root: Path,
    module_args: list[str],
    resolved_cwd: "ResolvedPath",
    timeout: int,
    tool_name: str,
) -> ToolResponse:
    try:
        result = subprocess.run(
            [sys.executable, *module_args],
            cwd=resolved_cwd.abs_path,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
        payload = {
            "command": " ".join(module_args),
            "cwd": resolved_cwd.rel_path,
            "stdout": result.stdout,
            "stderr": result.stderr,
            "exitCode": result.returncode,
        }
        if result.returncode != 0:
            return ToolResponse(status="error", output=payload, error=f"{tool_name} exited with code {result.returncode}")
        return ToolResponse(status="success", output=payload)
    except subprocess.TimeoutExpired as exc:
        return ToolResponse(
            status="error",
            error=f"{tool_name} timed out after {timeout} seconds",
            output={
                "command": " ".join(module_args),
                "cwd": resolved_cwd.rel_path,
                "stdout": exc.stdout or "",
                "stderr": exc.stderr or "",
                "exitCode": -1,
            },
        )


def walk_directory(
    root_abs: Path,
    root_rel: str,
    depth: int,
    max_depth: int,
    max_entries: int,
    entries: list[dict[str, Any]],
) -> bool:
    for child in sorted(root_abs.iterdir(), key=lambda entry: entry.name.lower()):
        if len(entries) >= max_entries:
            return True
        if child.name in SKIP_DIRS:
            continue

        child_rel = "/".join(part for part in [root_rel if root_rel != "." else "", child.name] if part)
        entries.append(entry_for_path(child_rel, child))
        if len(entries) >= max_entries:
            return True

        if child.is_dir() and depth + 1 < max_depth:
            if walk_directory(child, child_rel, depth + 1, max_depth, max_entries, entries):
                return True
    return False


def entry_for_path(rel_path: str, target: Path) -> dict[str, Any]:
    if target.is_dir():
        entry_type = "dir"
    elif target.is_symlink():
        entry_type = "symlink"
    else:
        entry_type = "file"

    size = target.lstat().st_size if target.exists() or target.is_symlink() else 0
    return {"path": rel_path.replace(os.sep, "/"), "type": entry_type, "size": size}


@dataclass
class ResolvedPath:
    abs_path: Path
    rel_path: str
    error: str | None = None


def resolve_workspace_path(workspace_root: Path, raw_path: str) -> ResolvedPath:
    root_abs = workspace_root.resolve()
    cleaned = raw_path.strip() if raw_path.strip() else "."
    abs_path = Path(cleaned).resolve() if Path(cleaned).is_absolute() else (root_abs / cleaned).resolve()
    try:
        rel_path = abs_path.relative_to(root_abs)
    except ValueError:
        return ResolvedPath(abs_path=abs_path, rel_path="", error=f'path "{raw_path}" escapes the workspace root')

    return ResolvedPath(abs_path=abs_path, rel_path=str(rel_path).replace(os.sep, "/") or ".")


def run_git(workspace_root: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(workspace_root), *args],
        capture_output=True,
        text=True,
        check=True,
    )
    return result.stdout


def string_arg(args: dict[str, Any], key: str) -> str | None:
    value = args.get(key)
    return value if isinstance(value, str) else None


def bool_arg(args: dict[str, Any], key: str) -> bool | None:
    value = args.get(key)
    if isinstance(value, bool):
        return value
    if isinstance(value, (int, float)):
        return bool(value)
    if isinstance(value, str):
        lowered = value.lower()
        if lowered == "true":
            return True
        if lowered == "false":
            return False
    return None


def number_arg(args: dict[str, Any], key: str) -> float | None:
    value = args.get(key)
    if isinstance(value, (int, float)):
        return float(value)
    if isinstance(value, str):
        try:
            return float(value)
        except ValueError:
            return None
    return None


def string_array_arg(args: dict[str, Any], key: str) -> list[str] | None:
    value = args.get(key)
    if isinstance(value, list):
        return [str(item) for item in value]
    if isinstance(value, str) and value.strip():
        return [value]
    return None


if __name__ == "__main__":
    main()

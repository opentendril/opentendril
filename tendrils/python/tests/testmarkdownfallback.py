"""Tests for the in-terrarium markdown fallback parser.

When the LLM answers with markdown code blocks instead of tool calls, the
terrarium runtime converts significant blocks into synthetic write_file calls.
These tests pin that pure parsing behavior (no LLM, no filesystem).
"""

from src.main import (
    FILEPATH_WARNING_COMMAND,
    extract_markdown_code_blocks,
    has_significant_markdown_code_blocks,
    infer_filepath_from_comment,
    infer_filepath_from_fence_info,
    normalize_inferred_path,
    parse_markdown_to_synthetic_tool_calls,
)

GO_BLOCK = """Here is the file:

```go
// path: cmd/demo/main.go
package main

func main() {}
```
"""


def test_extract_markdown_code_blocks_captures_language_and_body():
    blocks = extract_markdown_code_blocks(GO_BLOCK)
    assert len(blocks) == 1
    assert blocks[0].info.strip() == "go"
    assert "package main" in blocks[0].content


def test_has_significant_markdown_code_blocks_ignores_plain_prose():
    assert not has_significant_markdown_code_blocks("no code here at all")
    assert has_significant_markdown_code_blocks(GO_BLOCK)


def test_infer_filepath_from_comment_supports_slash_and_hash_styles():
    assert (
        infer_filepath_from_comment("// path: cmd/demo/main.go\npackage main")
        == "cmd/demo/main.go"
    )
    assert (
        infer_filepath_from_comment("# path: src/app.py\nprint('hi')")
        == "src/app.py"
    )


def test_infer_filepath_from_fence_info_reads_path_like_token():
    assert infer_filepath_from_fence_info("go cmd/demo/main.go") == "cmd/demo/main.go"
    assert infer_filepath_from_fence_info("go") is None


def test_normalize_inferred_path_rejects_escapes_and_absolute_paths():
    assert normalize_inferred_path("../../etc/passwd") is None
    assert normalize_inferred_path("/etc/passwd") is None
    assert normalize_inferred_path("./pkg/util.go") == "pkg/util.go"


def test_parse_markdown_produces_synthetic_write_file_call():
    calls = parse_markdown_to_synthetic_tool_calls(GO_BLOCK)
    assert len(calls) == 1
    assert calls[0]["tool"] == "writeFile"
    assert calls[0]["arguments"]["path"] == "cmd/demo/main.go"
    assert calls[0]["arguments"]["append"] is False
    assert "package main" in calls[0]["arguments"]["content"]


def test_parse_markdown_warns_when_no_path_can_be_inferred():
    content = "```go\npackage main\n\nfunc main() {}\n```"
    calls = parse_markdown_to_synthetic_tool_calls(content)
    assert len(calls) == 1
    assert calls[0]["tool"] == "execCommand"
    assert calls[0]["arguments"]["command"] == FILEPATH_WARNING_COMMAND

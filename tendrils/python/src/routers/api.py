"""
API Router — JSON programmatic access and SDLC self-building endpoint.

Responsibilities: /edit (self-building), /v1/chat, /v1/chat/completions (OpenAI-compat).
"""

import asyncio
import json
import logging
import time
import uuid
from typing import List, Optional

from fastapi import APIRouter, HTTPException
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, Field

from ..dependencies import llm_router, memory, tendril_loop, approval
from ..editor import FileEditor
from ..credits import credit_manager
from ..eventbus import event_bus
from ..commands import intercept_slash_commands
from ..utils import safe

logger = logging.getLogger(__name__)
router = APIRouter()


# --- Self-Building /edit Endpoint ---

class EditRequest(BaseModel):
    file: str = Field(..., description="File path relative to project root")
    instruction: str = Field(..., description="What to do to the file")
    provider: str = Field(default="default", description="LLM provider to use")


@router.post("/edit")
async def edit_endpoint(req: EditRequest):
    """
    Self-building endpoint: instruct an LLM to edit a project file.
    Operates with SDLC gates (lint, test, human approval) in strict mode.
    Bypasses all gates in simple mode.
    """
    try:
        if not credit_manager.validate_request():
            raise HTTPException(status_code=402, detail="Insufficient credits.")

        # Unprotected editor: /edit is the authorised self-modification pathway
        unprotected_editor = FileEditor(enforce_protection=False)

        try:
            current_content = unprotected_editor.read(req.file)
        except FileNotFoundError:
            current_content = ""

        prov = None if req.provider == "default" else req.provider
        llm = llm_router.get(provider=prov, tier="standard")

        prompt = f"""You are editing a file in the Tendril project.

File: {req.file}
Current content:
```
{current_content}
```

Instruction: {req.instruction}

Respond with ONLY the complete new file content. No explanations, no markdown fences."""

        resp = await asyncio.to_thread(llm.invoke, prompt)
        new_content = resp.content.strip()

        # Strip any markdown fences the LLM may have added
        if new_content.startswith("```"):
            lines = new_content.split("\n")
            new_content = "\n".join(lines[1:-1]) if lines[-1].strip() == "```" else "\n".join(lines[1:])

        diff = unprotected_editor.generate_diff(req.file, new_content)
        result = unprotected_editor.write(req.file, new_content)

        from ..config import SDLC_MODE
        test_passed = True
        check_msg = "SDLC mode is simple; skipped strict syntax and CI checks."

        if SDLC_MODE == "strict":
            check_msg = "Skipped syntax & CI checks (not a .py file)"

            if req.file.endswith(".py"):
                from ..config import STRICT_LINTING
                lint_cmd = f"ruff check {req.file}" if STRICT_LINTING else f"ruff check --select E,F {req.file}"

                lint_output = await tendril_loop.tester.run_command(lint_cmd, safe=True)
                if "❌" in lint_output and "Command failed" in lint_output:
                    test_passed = False
                    check_msg = f"Linting failed:\n{lint_output}"
                else:
                    test_cmd = f"python -m py_compile {req.file} && pytest tests/"
                    test_output = await tendril_loop.tester.run_command(test_cmd, safe=True)
                    if "❌" in test_output:
                        test_passed = False
                        check_msg = f"Automated tests failed:\n{test_output}"
                    else:
                        check_msg = "✅ All linting and CI tests passed."

            if not test_passed:
                unprotected_editor.write(req.file, current_content)
                return {
                    "status": "rejected",
                    "file": req.file,
                    "diff": diff,
                    "error": check_msg,
                    "approval": "auto_reverted",
                }

        if SDLC_MODE == "simple":
            approval_status = "auto_approved"
        else:
            approval_req = await approval.request_approval(
                action="file_edit",
                description=f"Edit {req.file}: {req.instruction[:100]}\n\nCI Status: {check_msg}",
                diff=diff,
            )
            approval_status = approval_req.status.value

        if approval_status in ("approved", "auto_approved"):
            commit_msg = f"tendril(/edit): Updated {req.file} - {req.instruction[:120]}"
            
            from ..config import SDLC_STRATEGY
            
            if SDLC_STRATEGY == "pr":
                current_branch = tendril_loop.git.current_branch()
                if current_branch == "main":
                    branch_name = f"tendril-patch-{uuid.uuid4().hex[:8]}"
                    tendril_loop.git.create_branch(branch_name)
                    logger.info(f"Created new branch for PR: {branch_name}")
                
                git_result = tendril_loop.git.commit_changes(commit_msg)
                push_result = tendril_loop.git.push_branch()
                
                # Try to create PR
                pr_result = ""
                try:
                    # In a real scenario, we'd want to extract repo name from context
                    # For now we use the default opentendril/core or assume GitHub CLI / MCP will do it.
                    # We can use tendril_loop.git.create_pull_request directly if we know the repo.
                    # As a safe default, we just log the branch creation and push.
                    pr_title = f"Agent Patch: {req.instruction[:50]}"
                    pr_body = f"Automated PR from OpenTendril.\n\nFile: `{req.file}`\nInstruction: {req.instruction}"
                    
                    # Get repo name using git
                    remote_url = tendril_loop.git._run_git("config", "--get", "remote.origin.url")
                    repo_name = ""
                    if "github.com" in remote_url:
                        # Extract user/repo from git@github.com:user/repo.git or https://github.com/user/repo.git
                        import re
                        match = re.search(r'github\.com[:/](.+?)(?:\.git)?$', remote_url)
                        if match:
                            repo_name = match.group(1)
                    
                    if repo_name:
                        pr_result = tendril_loop.git.create_pull_request(repo_name, pr_title, pr_body, tendril_loop.git.current_branch())
                    else:
                        pr_result = "Pushed branch, but could not determine GitHub repo name to open PR."
                except Exception as e:
                    pr_result = f"Failed to create PR automatically: {e}"
                
                git_result = f"{git_result}\n{push_result}\n{pr_result}"
                
            else:
                git_result = tendril_loop.git.commit_changes(commit_msg)
                
            return {
                "status": "applied",
                "file": req.file,
                "action": result["action"],
                "diff": diff,
                "test": check_msg,
                "commit": git_result,
                "approval": approval_status,
            }
        else:
            unprotected_editor.write(req.file, current_content)
            return {
                "status": "rejected",
                "file": req.file,
                "diff": diff,
                "approval": approval_status,
            }

    except PermissionError as e:
        raise HTTPException(status_code=403, detail=str(e))
    except Exception as e:
        logger.error(f"Edit error: {e}")
        raise HTTPException(status_code=500, detail=f"Edit failed: {str(e)}")


# --- Simple JSON Chat API ---

class ChatRequest(BaseModel):
    session_id: str = Field(default="default", max_length=64)
    message: str = Field(..., max_length=4000)
    provider: str = Field(default="default", description="LLM provider")


@router.post("/v1/chat")
async def chat_api(req: ChatRequest):
    logger.info(f"API chat: session={req.session_id} msg='{req.message[:100]}'")
    try:
        intercepted = intercept_slash_commands(req.message)
        if intercepted:
            response = intercepted
        else:
            prov = None if req.provider == "default" else req.provider
            response = await asyncio.to_thread(
                tendril_loop.process, req.session_id, req.message, provider=prov
            )
        memory.store_convo(req.session_id, "user", req.message)
        memory.store_convo(req.session_id, "assistant", response)
        return {"response": response, "provider": req.provider}
    except Exception as e:
        logger.error(f"API error: {e}")
        raise HTTPException(status_code=500, detail="Internal error processing request.")


# --- OpenAI-Compatible Chat Completions ---

class ChatMessage(BaseModel):
    role: str
    content: str


class ChatCompletionRequest(BaseModel):
    model: str
    messages: List[ChatMessage]
    stream: Optional[bool] = False
    temperature: Optional[float] = None


@router.post("/v1/chat/completions")
async def openai_chat_completions(req: ChatCompletionRequest):
    """
    OpenAI-compatible endpoint. Allows 3rd party TUIs (Aider, Crush, OpenCode) to connect.
    """
    logger.info(f"OpenAI API chat: model={req.model} stream={req.stream}")
    try:
        model_name = req.model.lower()
        if "gpt" in model_name or "o1" in model_name:
            provider = "openai"
        elif "claude" in model_name:
            provider = "anthropic"
        elif "grok" in model_name or "xai" in model_name:
            provider = "grok"
        else:
            provider = "default"

        user_msg = next((m.content for m in reversed(req.messages) if m.role == "user"), None)
        if not user_msg:
            raise HTTPException(status_code=400, detail="No user message found")

        session_id = f"ext-{uuid.uuid4().hex[:8]}"

        if req.stream:
            async def event_generator():
                queue = asyncio.Queue()
                loop = asyncio.get_running_loop()

                def subscriber(event):
                    if event.session_id == session_id:
                        try:
                            loop.call_soon_threadsafe(queue.put_nowait, event)
                        except Exception:
                            pass

                event_bus.subscribe(subscriber)
                try:
                    task = asyncio.create_task(
                        asyncio.to_thread(tendril_loop.process, session_id, user_msg, provider=provider)
                    )
                    while True:
                        get_task = asyncio.create_task(queue.get())
                        done, _ = await asyncio.wait([get_task, task], return_when=asyncio.FIRST_COMPLETED)

                        if get_task in done:
                            event = get_task.result()
                            chunk_content = ""
                            if event.event_type == "tool.start":
                                chunk_content = f"\n\n🛠️ **Running Tool**: `{event.data.get('name', '')}` with args: `{json.dumps(event.data.get('args', {}))}`...\n"
                            elif event.event_type == "tool.end":
                                chunk_content = f"✅ **Tool `{event.data.get('name', '')}` completed.**\n\n"
                            elif event.event_type == "request.end":
                                chunk_content = f"\n{event.data.get('content', '')}"
                            elif event.event_type == "request.error":
                                chunk_content = f"\n\n❌ **Error**: {event.data.get('error', '')}\n"

                            if chunk_content:
                                chunk = {
                                    "id": f"chatcmpl-{session_id}",
                                    "object": "chat.completion.chunk",
                                    "created": int(time.time()),
                                    "model": req.model,
                                    "choices": [{"index": 0, "delta": {"content": chunk_content}, "finish_reason": None}],
                                }
                                yield f"data: {json.dumps(chunk)}\n\n"

                            if event.event_type in ["request.end", "request.error"]:
                                break

                        if task in done and queue.empty():
                            if task.exception():
                                logger.error(f"OpenAI stream task error: {task.exception()}")
                            break

                    final_chunk = {
                        "id": f"chatcmpl-{session_id}",
                        "object": "chat.completion.chunk",
                        "created": int(time.time()),
                        "model": req.model,
                        "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                    }
                    yield f"data: {json.dumps(final_chunk)}\n\n"
                    yield "data: [DONE]\n\n"
                finally:
                    event_bus.unsubscribe(subscriber)
                    memory.store_convo(session_id, "user", user_msg)

            return StreamingResponse(event_generator(), media_type="text/event-stream")
        else:
            response_text = await asyncio.to_thread(
                tendril_loop.process, session_id, user_msg, provider=provider
            )
            memory.store_convo(session_id, "user", user_msg)
            memory.store_convo(session_id, "assistant", response_text)
            return {
                "id": f"chatcmpl-{session_id}",
                "object": "chat.completion",
                "created": int(time.time()),
                "model": req.model,
                "choices": [{"index": 0, "message": {"role": "assistant", "content": response_text}, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
            }

    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"API error: {e}")
        raise HTTPException(status_code=500, detail=str(e))


def langchain_tool_to_mcp(t) -> dict:
    """Converts a LangChain tool into a standard MCP tool schema."""
    input_schema = {
        "type": "object",
        "properties": {},
        "required": []
    }
    if hasattr(t, "args_schema") and t.args_schema is not None:
        try:
            schema = t.args_schema.model_json_schema()
            input_schema = {
                "type": "object",
                "properties": schema.get("properties", {}),
                "required": schema.get("required", [])
            }
        except Exception:
            pass
    if not input_schema["properties"] and hasattr(t, "args"):
        for name, info in getattr(t, "args", {}).items():
            prop = {"type": info.get("type", "string")}
            if "description" in info:
                prop["description"] = info["description"]
            input_schema["properties"][name] = prop
            if "default" not in info:
                input_schema["required"].append(name)
    return {
        "name": t.name,
        "description": t.description,
        "inputSchema": input_schema
    }


@router.post("/v1")
async def mcp_jsonrpc_endpoint(req: dict):
    """
    Exposes an MCP JSON-RPC endpoint at /v1.
    Handles 'initialize', 'tools/list', and 'tools/call'.
    """
    req_id = req.get("id")
    method = req.get("method")
    
    if method == "initialize":
        return {
            "jsonrpc": "2.0",
            "result": {
                "protocolVersion": "2024-11-05",
                "capabilities": {
                    "tools": {}
                },
                "serverInfo": {
                    "name": "tendril-brain",
                    "version": "0.1.0"
                }
            },
            "id": req_id
        }
        
    elif method == "tools/list":
        mcp_tools = []
        for t in tendril_loop.tools:
            try:
                mcp_tools.append(langchain_tool_to_mcp(t))
            except Exception as e:
                logger.error(f"Error converting tool {t.name} to MCP: {e}")
        return {
            "jsonrpc": "2.0",
            "result": {
                "tools": mcp_tools
            },
            "id": req_id
        }
        
    elif method == "tools/call":
        params = req.get("params", {})
        tool_name = params.get("name")
        tool_args = params.get("arguments", {})
        
        # Find tool
        tool_func = next((t for t in tendril_loop.tools if t.name == tool_name), None)
        if not tool_func:
            return {
                "jsonrpc": "2.0",
                "error": {
                    "code": -32601,
                    "message": f"Tool '{tool_name}' not found"
                },
                "id": req_id
            }
            
        try:
            # Execute tool
            if getattr(tool_func, "is_async", False) or asyncio.iscoroutinefunction(tool_func._run):
                result = await tool_func.ainvoke(tool_args)
            else:
                result = await asyncio.to_thread(tool_func.invoke, tool_args)
                
            return {
                "jsonrpc": "2.0",
                "result": {
                    "content": [
                        {
                            "type": "text",
                            "text": str(result)
                        }
                    ]
                },
                "id": req_id
            }
        except Exception as e:
            logger.error(f"Error executing tool {tool_name}: {e}")
            return {
                "jsonrpc": "2.0",
                "error": {
                    "code": -32603,
                    "message": f"Tool execution failed: {str(e)}"
                },
                "id": req_id
            }
            
    elif method is None:
        return {"status": "notification received"}
        
    else:
        return {
            "jsonrpc": "2.0",
            "error": {
                "code": -32601,
                "message": f"Method '{method}' not found"
            },
            "id": req_id
        }


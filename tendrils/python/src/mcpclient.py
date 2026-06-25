import asyncio
import os
import json
import logging
from typing import List, Dict, Any, Callable
from pydantic import create_model, Field
from langchain_core.tools import StructuredTool

logger = logging.getLogger(__name__)

def get_mcp_config() -> Dict[str, Any]:
    """Load MCP server config from environment or default file."""
    config_path = os.environ.get("MCP_CONFIG_PATH", "/app/mcp-config.json")
    if not os.path.exists(config_path):
        config_path = os.path.join(os.path.dirname(__file__), "..", "..", "..", "mcp-config.json")
    
    servers = {}
    if os.path.exists(config_path):
        try:
            with open(config_path, 'r') as f:
                servers = json.load(f).get("mcpServers", {})
        except Exception as e:
            logger.error(f"Failed to load MCP config: {e}")
            
    return servers

def json_schema_to_pydantic_type(schema: Dict[str, Any]) -> Any:
    """Convert a JSON Schema property type to a Python type."""
    t = schema.get("type", "string")
    if t == "string":
        return str
    elif t == "integer":
        return int
    elif t == "number":
        return float
    elif t == "boolean":
        return bool
    elif t == "array":
        return List[Any]
    elif t == "object":
        return Dict[str, Any]
    return Any

def build_pydantic_model_from_schema(tool_name: str, schema: Dict[str, Any]) -> Any:
    """Dynamically create a Pydantic model from a JSON Schema."""
    properties = schema.get("properties", {})
    required = schema.get("required", [])
    
    fields = {}
    for prop_name, prop_schema in properties.items():
        py_type = json_schema_to_pydantic_type(prop_schema)
        description = prop_schema.get("description", "")
        
        if prop_name in required:
            fields[prop_name] = (py_type, Field(..., description=description))
        else:
            fields[prop_name] = (py_type, Field(default=None, description=description))
            
    return create_model(f"MCPToolArgs_{tool_name}", **fields)

def build_mcp_tools() -> List[StructuredTool]:
    """Discover and build all MCP tools from configured servers."""
    try:
        from mcp.client.stdio import stdio_client, StdioServerParameters
        from mcp.client.session import ClientSession
    except ImportError:
        logger.warning("mcp package not installed, skipping MCP tools.")
        return []

    servers = get_mcp_config()
    langchain_tools = []
    
    for server_name, config in servers.items():
        cmd = config.get("command")
        args = config.get("args", [])
        
        env = dict(os.environ)
        # Expand environment variables like ${GITHUB_TOKEN} in the config
        for k, v in config.get("env", {}).items():
            if isinstance(v, str) and v.startswith("${") and v.endswith("}"):
                env_key = v[2:-1]
                env[k] = os.environ.get(env_key, "")
            else:
                env[k] = v
        
        async def fetch_tools():
            params = StdioServerParameters(command=cmd, args=args, env=env)
            async with stdio_client(params) as (read, write):
                async with ClientSession(read, write) as session:
                    await session.initialize()
                    response = await session.list_tools()
                    return response.tools
                    
        try:
            mcp_tools = asyncio.run(fetch_tools())
        except Exception as e:
            logger.error(f"Failed to fetch tools from MCP server '{server_name}': {e}")
            continue
            
        for t in mcp_tools:
            tool_name = f"{server_name}_{t.name}"
            # Clean up the name for LLMs (some characters might be rejected by OpenAI/Langchain)
            tool_name = tool_name.replace("-", "_")
            
            # We capture cmd, args, env, t.name in closures
            def create_tool_func(s_cmd=cmd, s_args=args, s_env=env, s_tname=t.name):
                def _invoke(**kwargs):
                    async def execute():
                        params = StdioServerParameters(command=s_cmd, args=s_args, env=s_env)
                        async with stdio_client(params) as (read, write):
                            async with ClientSession(read, write) as session:
                                await session.initialize()
                                response = await session.call_tool(s_tname, arguments=kwargs)
                                # Combine text contents
                                return "\n".join(c.text for c in response.content if c.type == "text")
                    try:
                        return asyncio.run(execute())
                    except Exception as e:
                        return f"❌ MCP Tool Execution Failed: {str(e)}"
                return _invoke
                
            args_schema = build_pydantic_model_from_schema(tool_name, t.inputSchema)
            
            tool = StructuredTool(
                name=tool_name,
                description=t.description or f"MCP Tool from {server_name}",
                func=create_tool_func(),
                args_schema=args_schema
            )
            langchain_tools.append(tool)
            
    return langchain_tools

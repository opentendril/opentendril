import asyncio
import json
import logging
"""
=============================================================================
Tendril: GitHubTendril
Description: Manages interactions with GitHub via MCP. Spins up an ephemeral stdio MCP server for zero-latency interactions without global singletons.
Revision History:
- 2026-06-21: Initial creation (OpenTendril Bot)
=============================================================================
"""
import os
import subprocess
from typing import Dict, Any, Optional

from src.config import MCP_SERVERS

logger = logging.getLogger(__name__)

class GitHubTendril:
    """
    An ephemeral Tendril that boots the GitHub MCP server, executes a specific task,
    and then terminates to ensure zero idle cost.
    """

    def __init__(self):
        self.config = MCP_SERVERS.get("github")
        self.process: Optional[subprocess.Popen] = None

    async def emerge(self) -> bool:
        """Boot the GitHub MCP server via JSON-RPC stdio."""
        if not self.config:
            logger.error("GitHub MCP server configuration not found in mcp-config.json")
            return False

        command = self.config.get("command")
        args = self.config.get("args", [])
        env = os.environ.copy()
        
        # Inject config env vars, resolving any templates like ${GITHUB_TOKEN}
        config_env = self.config.get("env", {})
        for k, v in config_env.items():
            if v.startswith("${") and v.endswith("}"):
                env_var = v[2:-1]
                env[k] = os.getenv(env_var, "")
            else:
                env[k] = v

        try:
            logger.info(f"🌱 Sprouting GitHub Tendril: {command} {' '.join(args)}")
            self.process = subprocess.Popen(
                [command] + args,
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                env=env,
                text=True
            )
            return True
        except Exception as e:
            logger.error(f"Failed to sprout GitHub Tendril: {e}")
            return False

    async def elongate(self, task: str) -> Dict[str, Any]:
        """Execute the LLM loop with the bound MCP tools."""
        if not self.process:
            raise RuntimeError("Tendril must be emerged before elongation")
            
        logger.info(f"🌿 Elongating GitHub Tendril for task: {task}")
        
        # TODO: Hook into LLMRouter, query tools via JSON-RPC, and execute LangChain Agent loop
        # For now, we just simulate the loop
        await asyncio.sleep(1)
        
        return {"status": "success", "result": f"Executed: {task}"}

    async def retract(self):
        """Terminate the MCP server sub-process to release resources."""
        if self.process:
            logger.info("🍂 Retracting GitHub Tendril")
            self.process.terminate()
            try:
                self.process.wait(timeout=2.0)
            except subprocess.TimeoutExpired:
                self.process.kill()
            self.process = None

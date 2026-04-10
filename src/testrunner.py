"""
Tendril Test Runner — Secure Execution of Tests and Commands.

Supports two execution modes:
  - Sandbox (production): Routes commands through an isolated HTTP relay
    container with no secrets, no DB, and no internet access.
  - Local (dev): Falls back to subprocess execution when sandbox is unavailable.

The mode is controlled by SANDBOX_ENABLED in config.
"""

import asyncio
import logging
from typing import Optional

import httpx

from .approval import ApprovalGate
from .config import WORKSPACE_ROOT, SANDBOX_ENABLED, SANDBOX_URL, SANDBOX_TOKEN

logger = logging.getLogger(__name__)


class TestRunner:
    """Manages secure execution of tests and bash commands."""

    def __init__(self, approval_gate: ApprovalGate, cwd: str = WORKSPACE_ROOT):
        self.approval = approval_gate
        self.cwd = cwd
        self.sandbox_enabled = SANDBOX_ENABLED
        self.sandbox_url = SANDBOX_URL
        self.sandbox_token = SANDBOX_TOKEN

        if self.sandbox_enabled:
            if not self.sandbox_token:
                logger.warning("⚠️  SANDBOX_ENABLED=true but SANDBOX_TOKEN is empty. Falling back to local execution.")
                self.sandbox_enabled = False
            else:
                logger.info(f"🔒 TestRunner using sandbox relay at {self.sandbox_url}")
        else:
            logger.info("⚠️  TestRunner using local subprocess (sandbox disabled)")

    async def run_command(self, command: str, safe: bool = False, timeout: float = 60.0) -> str:
        """
        Run a bash command securely.

        If safe=False, requires an approval gate confirmation first.
        Routes execution through the sandbox container when enabled.
        """
        if not safe:
            approval_req = await self.approval.request_approval(
                action="run_command",
                description=f"Run arbitrary command:\n{command}",
                diff=command
            )

            status = await approval_req.wait()

            if status.value not in ("approved", "auto_approved"):
                return f"❌ Command execution rejected by user."

        if self.sandbox_enabled:
            return await self._exec_sandbox(command, timeout)
        else:
            return await self._exec_local(command, timeout)

    async def _exec_sandbox(self, command: str, timeout: float) -> str:
        """Execute a command via the secure HTTP sandbox relay."""
        try:
            logger.info(f"🔒 Sandbox exec: {command[:120]}")

            async with httpx.AsyncClient(timeout=timeout + 10) as client:
                response = await client.post(
                    f"{self.sandbox_url}/exec",
                    json={"command": command, "timeout": timeout, "cwd": "/workspace"},
                    headers={"Authorization": f"Bearer {self.sandbox_token}"},
                )

            if response.status_code == 401:
                return "❌ Sandbox authentication failed. Check SANDBOX_TOKEN."

            if response.status_code != 200:
                return f"❌ Sandbox returned HTTP {response.status_code}: {response.text}"

            data = response.json()

            if data.get("timed_out"):
                return f"❌ Command timed out after {timeout}s (sandbox)"

            # Format output consistently with local execution
            result = []
            if data.get("stdout"):
                result.append(f"STDOUT:\n{data['stdout']}")
            if data.get("stderr"):
                result.append(f"STDERR:\n{data['stderr']}")

            exit_code = data.get("exit_code", -1)
            if exit_code == 0:
                header = "✅ Command completed successfully."
            else:
                header = f"❌ Command failed with exit code {exit_code}."

            return f"{header}\n\n" + "\n\n".join(result)

        except httpx.ConnectError:
            logger.error("Sandbox container unreachable. Is it running?")
            return "❌ Sandbox container unreachable. Run `docker compose up sandbox` to start it."
        except Exception as e:
            logger.error(f"Sandbox execution error: {e}")
            return f"❌ Sandbox error: {str(e)}"

    async def _exec_local(self, command: str, timeout: float) -> str:
        """Fallback: execute a command via local subprocess (dev mode only)."""
        try:
            logger.info(f"🚀 Local exec: {command[:120]}")
            proc = await asyncio.create_subprocess_shell(
                command,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                cwd=self.cwd,
            )

            try:
                stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
            except asyncio.TimeoutError:
                proc.kill()
                return f"❌ Command timed out after {timeout}s"

            out = stdout.decode("utf-8", errors="replace").strip()
            err = stderr.decode("utf-8", errors="replace").strip()

            result = []
            if out:
                result.append(f"STDOUT:\n{out}")
            if err:
                result.append(f"STDERR:\n{err}")

            if proc.returncode == 0:
                header = "✅ Command completed successfully."
            else:
                header = f"❌ Command failed with exit code {proc.returncode}."

            final_output = f"{header}\n\n" + "\n\n".join(result)

            # Truncate to avoid blowing up LLM context limit
            if len(final_output) > 8000:
                final_output = final_output[:4000] + "\n\n... [OUTPUT TRUNCATED] ...\n\n" + final_output[-4000:]

            return final_output

        except Exception as e:
            logger.error(f"Command execution error: {e}")
            return f"❌ Failed to execute command: {str(e)}"

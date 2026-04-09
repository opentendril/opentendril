"""
Tendril Sandbox Server — Secure HTTP Relay for Code Execution.

A minimal FastAPI server that runs inside an isolated container.
Accepts commands via authenticated HTTP POST and returns stdout/stderr.

Security properties:
  - No API keys, no database access, no internet
  - Bearer token authentication
  - Per-command timeout enforcement
  - Runs as non-root user
"""

import asyncio
import logging
import os

from fastapi import FastAPI, HTTPException, Header
from pydantic import BaseModel, Field

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger(__name__)

SANDBOX_TOKEN = os.getenv("SANDBOX_TOKEN", "")

app = FastAPI(title="Tendril Sandbox", version="0.1.0")


class ExecRequest(BaseModel):
    command: str = Field(..., description="Shell command to execute")
    timeout: float = Field(default=60.0, ge=1.0, le=300.0, description="Timeout in seconds")
    cwd: str = Field(default="/workspace", description="Working directory")


class ExecResponse(BaseModel):
    stdout: str
    stderr: str
    exit_code: int
    timed_out: bool = False


def verify_token(authorization: str = Header(...)) -> None:
    """Validate the bearer token from the kernel."""
    if not SANDBOX_TOKEN:
        raise HTTPException(status_code=500, detail="SANDBOX_TOKEN not configured")
    expected = f"Bearer {SANDBOX_TOKEN}"
    if authorization != expected:
        logger.warning("Unauthorized exec attempt")
        raise HTTPException(status_code=401, detail="Invalid token")


@app.post("/exec", response_model=ExecResponse)
async def exec_command(req: ExecRequest, authorization: str = Header(...)):
    """Execute a shell command in the sandbox and return the output."""
    verify_token(authorization)

    logger.info(f"Executing: {req.command[:120]}...")

    try:
        proc = await asyncio.create_subprocess_shell(
            req.command,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            cwd=req.cwd,
        )

        timed_out = False
        try:
            stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=req.timeout)
        except asyncio.TimeoutError:
            proc.kill()
            await proc.communicate()
            timed_out = True
            stdout = b""
            stderr = f"Command timed out after {req.timeout}s".encode()

        out = stdout.decode("utf-8", errors="replace").strip()
        err = stderr.decode("utf-8", errors="replace").strip()

        # Truncate large outputs to prevent memory issues
        max_len = 8000
        if len(out) > max_len:
            out = out[:max_len // 2] + "\n\n... [TRUNCATED] ...\n\n" + out[-(max_len // 2):]
        if len(err) > max_len:
            err = err[:max_len // 2] + "\n\n... [TRUNCATED] ...\n\n" + err[-(max_len // 2):]

        return ExecResponse(
            stdout=out,
            stderr=err,
            exit_code=proc.returncode if proc.returncode is not None else -1,
            timed_out=timed_out,
        )

    except Exception as e:
        logger.error(f"Execution error: {e}")
        return ExecResponse(stdout="", stderr=str(e), exit_code=-1)


@app.get("/health")
async def health():
    return {"status": "sandbox_healthy", "token_configured": bool(SANDBOX_TOKEN)}


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=9999)

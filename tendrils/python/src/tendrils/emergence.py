"""
=============================================================================
Tendril: EmergenceTendril
Description: Bootstraps the Tendril by branching off the main Stem into an isolated container.
Revision History:
- 2026-06-21: Initial creation (OpenTendril Bot)
=============================================================================
"""
import subprocess
import logging
import json
from typing import Dict, Any
from .initiation import TendrilContext

logger = logging.getLogger(__name__)

def emerge_tendril(context: TendrilContext) -> Dict[str, Any]:
    """
    Phase 2: Emergence.
    The young Tendril visibly breaks out from the Stem. 
    This boots the isolated container/microVM and triggers Elongation.
    """
    logger.info(f"🌱 Emergence: Booting Tendril [{context.id}] as {context.genotype} via {context.runtime}")
    
    if context.runtime == "docker":
        return _emerge_via_docker(context)
    elif context.runtime == "firecracker":
        raise NotImplementedError("Firecracker microVMs are not yet supported in this environment.")
    elif context.runtime == "host":
        return _emerge_via_host(context)
    else:
        raise ValueError(f"Unknown TENDRIL_RUNTIME: {context.runtime}")

def _emerge_via_docker(context: TendrilContext) -> Dict[str, Any]:
    """Spawns an isolated Docker container that runs the Elongation phase."""
    
    # In production, this would use the official opentendril/sandbox image.
    cmd = [
        "docker", "run", "--rm",
        "-e", f"TENDRIL_ID={context.id}",
        "-e", f"TENDRIL_GENOTYPE={context.genotype}",
        "-e", f"TENDRIL_TASK={context.task}"
    ]
    
    if context.workspace_mount:
        cmd.extend(["-v", f"{context.workspace_mount}:/workspace"])
        cmd.extend(["-w", "/workspace"])
        
    # The container entrypoint executes elongation.py
    cmd.extend(["python", "-m", "src.tendrils.elongation"])
    
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        return {"status": "success", "output": result.stdout}
    except subprocess.CalledProcessError as e:
        logger.error(f"❌ Tendril [{context.id}] failed during Elongation: {e.stderr}")
        return {"status": "error", "error": e.stderr}

def _emerge_via_host(context: TendrilContext) -> Dict[str, Any]:
    """Fallback: Spawns the Elongation phase as a local subprocess (Zero isolation)."""
    import os
    env = os.environ.copy()
    env["TENDRIL_ID"] = context.id
    env["TENDRIL_GENOTYPE"] = context.genotype
    env["TENDRIL_TASK"] = context.task
    
    import sys
    cmd = [sys.executable, "-m", "src.tendrils.elongation"]
    try:
        result = subprocess.run(cmd, env=env, capture_output=True, text=True, check=True)
        return {"status": "success", "output": result.stdout}
    except subprocess.CalledProcessError as e:
        logger.error(f"❌ Tendril [{context.id}] failed during Elongation: {e.stderr}")
        return {"status": "error", "error": e.stderr}

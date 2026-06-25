"""
=============================================================================
Tendril: InitiationTendril
Description: Bootstraps the environment and initializes early connections.
Revision History:
- 2026-06-21: Initial creation (OpenTendril Bot)
=============================================================================
"""
import uuid
from pydantic import BaseModel, Field
from typing import Dict, Any, Optional

class TendrilContext(BaseModel):
    """The context required for a Tendril to emerge and elongate."""
    id: str = Field(default_factory=lambda: str(uuid.uuid4()))
    genotype: str
    transcript: str
    runtime: str = "docker"
    environment: Dict[str, str] = Field(default_factory=dict)
    workspace_mount: Optional[str] = None
    
def initiate_tendril(genotype: str, transcript: str, workspace: Optional[str] = None) -> TendrilContext:
    """
    Phase 1: Initiation.
    The Stem signals the creation of a Tendril. It builds the necessary 
    context and guardrails required before emergence.
    """
    from src.config import TENDRIL_RUNTIME
    
    # In a real scenario, this would load the Genotype from the agency/ directory
    # to enforce strict guardrails.
    
    context = TendrilContext(
        genotype=genotype,
        transcript=transcript,
        runtime=TENDRIL_RUNTIME,
        workspace_mount=workspace
    )
    
    return context

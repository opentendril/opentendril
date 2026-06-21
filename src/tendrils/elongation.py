"""
=============================================================================
Tendril: ElongationTendril
Description: Executed inside the ephemeral container. Handles the LLM interaction loop and task execution.
Revision History:
- 2026-06-21: Initial creation (OpenTendril Bot)
=============================================================================
"""
import os
import json
import sys

def elongate():
    """
    Phase 3: Elongation.
    This script is executed INSIDE the ephemeral Tendril container/process.
    It actively searches for its goal by executing the LLM inference loop, 
    returns the result via stdout, and terminates.
    """
    tendril_id = os.environ.get("TENDRIL_ID")
    persona = os.environ.get("TENDRIL_PERSONA")
    task = os.environ.get("TENDRIL_TASK")
    
    if not all([tendril_id, persona, task]):
        print(json.dumps({"error": "Missing required Tendril context in environment."}))
        sys.exit(1)
        
    # Here, the Tendril connects to the LLM Router, applies the strict Persona guardrails,
    # and executes the task. For now, we simulate the execution.
    
    result = {
        "tendril_id": tendril_id,
        "persona": persona,
        "status": "completed",
        "output": f"Simulated execution for task: {task}"
    }
    
    # We output purely to stdout so the Stem can read it when Emergence completes.
    print(json.dumps(result))

if __name__ == "__main__":
    elongate()

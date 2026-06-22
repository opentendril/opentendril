from .llmrouter import LLMRouter
from .memory import Memory

from .tendril import Orchestrator
from .editor import FileEditor
from .approval import ApprovalGate

# Core components instantiated globally
llm_router = LLMRouter()
memory = Memory()
editor = FileEditor()
approval = ApprovalGate(auto_approve=True)
orchestrator = Orchestrator(memory, llm_router, editor, approval)

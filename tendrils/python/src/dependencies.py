from .llmrouter import LLMRouter
from .memory import Memory

from .tendril_loop import TendrilLoop
from .editor import FileEditor
from .approval import ApprovalGate

# Core components instantiated globally
llm_router = LLMRouter()
memory = Memory()
editor = FileEditor()
approval = ApprovalGate(auto_approve=True)
tendril_loop = TendrilLoop(memory, llm_router, editor, approval)

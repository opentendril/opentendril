from .llmrouter import LLMRouter
from .memory import Memory
from .skillsmanager import SkillsManager
from .tendril import Orchestrator
from .editor import FileEditor
from .approval import ApprovalGate

# Core components instantiated globally
llm_router = LLMRouter()
memory = Memory()
skills_manager = SkillsManager()
editor = FileEditor()
approval = ApprovalGate(auto_approve=True)
orchestrator = Orchestrator(memory, skills_manager, llm_router, editor, approval)

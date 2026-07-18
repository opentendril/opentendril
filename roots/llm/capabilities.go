package llm

type Capabilities struct {
	RequiresReasoning bool
	RequiresVision    bool
	// RequiresToolUse restricts selection to models that reliably drive the
	// tool-calling protocol. An autonomous sprout is useless without it: a
	// model that cannot emit tool calls returns prose (or nothing) and the run
	// matures having changed no files. See ModelDefinition.DrivesTools.
	RequiresToolUse bool
	MinContextSize  int
	MaxCostTier     ModelTier
}

type ModelFamily string

const (
	ModelFamilyClaude ModelFamily = "claude"
	ModelFamilyGPT    ModelFamily = "gpt"
	ModelFamilyGemini ModelFamily = "gemini"
	ModelFamilyLlama  ModelFamily = "llama"
	ModelFamilyQwen   ModelFamily = "qwen"
)

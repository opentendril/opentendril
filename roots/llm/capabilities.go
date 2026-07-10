package llm

type Capabilities struct {
	RequiresReasoning bool
	RequiresVision    bool
	MinContextSize    int
	MaxCostTier       ModelTier
}

type ModelFamily string

const (
	ModelFamilyClaude ModelFamily = "claude"
	ModelFamilyGPT    ModelFamily = "gpt"
	ModelFamilyGemini ModelFamily = "gemini"
	ModelFamilyLlama  ModelFamily = "llama"
	ModelFamilyQwen   ModelFamily = "qwen"
)

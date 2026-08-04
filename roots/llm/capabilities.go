package llm

type Capabilities struct {
	RequiresReasoning bool
	RequiresVision    bool
	// RequiresToolUse restricts selection to models that reliably drive the
	// tool-calling protocol on the fallback (prose) path. An autonomous sprout
	// using the prose protocol is useless without it. Once a growth is carried
	// natively, the property it screens for is not a property of the model.
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

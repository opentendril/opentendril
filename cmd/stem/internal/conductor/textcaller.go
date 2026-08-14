package conductor

import (
	"context"

	"github.com/opentendril/opentendril/roots/llm"
)

// textCaller is the string-returning seam used by conductor organs that do not
// aggregate usage: Adaptation, the assessor, the chronicler, and meristem
// branching. Sprout does not use it.
type textCaller interface {
	Call(ctx context.Context, messages []llm.Message) (string, error)
	CallPrompt(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

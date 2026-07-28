package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// providerAdapter encapsulates every provider-specific wire-protocol detail
// — model discovery, request shaping, and response parsing — behind one
// small interface, so adding a new provider's dialect (e.g. an explicit
// caching mechanism a future backend needs) means implementing this
// interface once, not adding another branch to a switch scattered across
// six call sites. Only two implementations exist today (anthropicAdapter,
// openAIishAdapter) — this is not a plugin system with a registry, just a
// consolidation of what already existed.
type providerAdapter interface {
	// ModelsPath returns the path segment appended to BaseURL for the
	// list-models endpoint.
	ModelsPath() string

	// SetModelsAuthHeaders sets whatever auth headers this provider expects
	// on a models-list request. apiKey may be empty.
	SetModelsAuthHeaders(req *http.Request, apiKey string)

	// BuildChatRequest returns the JSON body for one chat call.
	BuildChatRequest(spec ProviderSpec, messages []Message, stream bool) ([]byte, error)

	// SetChatHeaders sets auth and any provider-specific headers (e.g.
	// Anthropic's caching beta flag) on a chat request.
	SetChatHeaders(req *http.Request, spec ProviderSpec)

	// ParseStreamChunk extracts a text delta from one SSE "data: ..." line's
	// JSON payload (already stripped of the "data: " prefix). ok is false
	// when the line carries no text delta for this provider's event shape.
	ParseStreamChunk(data string) (text string, ok bool)

	// ParseResponse extracts the completion text from a non-streaming
	// response body.
	ParseResponse(body []byte) (string, error)
}

// adapterForMode returns the adapter for mode. Any mode other than
// ModeAnthropic gets openAIishAdapter — this preserves the existing
// `default:` behavior in the switches being replaced, so an unrecognized
// future Mode value still gets sane OpenAI-shaped handling rather than an
// error.
func adapterForMode(mode Mode) providerAdapter {
	switch mode {
	case ModeAnthropic:
		return anthropicAdapter{}
	default:
		return openAIishAdapter{}
	}
}

type anthropicAdapter struct{}

func (anthropicAdapter) ModelsPath() string {
	return "/v1/models"
}

func (anthropicAdapter) SetModelsAuthHeaders(req *http.Request, apiKey string) {
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

func (anthropicAdapter) BuildChatRequest(spec ProviderSpec, messages []Message, stream bool) ([]byte, error) {
	systemParts := make([]string, 0, 2)
	anthropicMessages := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		content := message.Content
		trimmedContent := strings.TrimSpace(content)
		switch role {
		case "system":
			if trimmedContent != "" {
				systemParts = append(systemParts, trimmedContent)
			}
		case "assistant", "user":
			anthropicMessages = append(anthropicMessages, anthropicMessagePayload(role, content))
		default:
			anthropicMessages = append(anthropicMessages, anthropicMessagePayload("user", content))
		}
	}

	payloadBody := map[string]any{
		"model":       spec.Model,
		"max_tokens":  2048,
		"temperature": spec.Temperature,
		"messages":    anthropicMessages,
		"stream":      stream,
	}
	if len(systemParts) > 0 {
		payloadBody["system"] = []map[string]any{
			anthropicTextBlock(strings.Join(systemParts, "\n\n"), true),
		}
	}

	payload, err := json.Marshal(payloadBody)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}
	return payload, nil
}

func (anthropicAdapter) SetChatHeaders(req *http.Request, spec ProviderSpec) {
	req.Header.Set("x-api-key", spec.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
}

func (anthropicAdapter) ParseStreamChunk(dataStr string) (string, bool) {
	var event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(dataStr), &event); err == nil {
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
			return event.Delta.Text, true
		}
	}
	return "", false
}

func (anthropicAdapter) ParseResponse(body []byte) (string, error) {
	var decoded struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode anthropic response: %w", err)
	}
	for _, block := range decoded.Content {
		if strings.TrimSpace(block.Text) != "" {
			return strings.TrimSpace(block.Text), nil
		}
	}
	return "", fmt.Errorf("anthropic response contained no text")
}

type openAIishAdapter struct{}

func (openAIishAdapter) ModelsPath() string {
	return "/models"
}

func (openAIishAdapter) SetModelsAuthHeaders(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func (openAIishAdapter) BuildChatRequest(spec ProviderSpec, messages []Message, stream bool) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{
		"model":       spec.Model,
		"temperature": spec.Temperature,
		"stream":      stream,
		"messages":    messages,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal chat request: %w", err)
	}
	return payload, nil
}

func (openAIishAdapter) SetChatHeaders(req *http.Request, spec ProviderSpec) {
	if spec.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+spec.APIKey)
	}
}

func (openAIishAdapter) ParseStreamChunk(dataStr string) (string, bool) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
		if len(chunk.Choices) > 0 {
			text := chunk.Choices[0].Delta.Content
			if text != "" {
				return text, true
			}
		}
	}
	return "", false
}

func (openAIishAdapter) ParseResponse(body []byte) (string, error) {
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("chat response contained no choices")
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("chat response contained no content")
	}
	return content, nil
}

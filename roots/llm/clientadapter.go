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
	BuildChatRequest(spec ProviderSpec, messages []Message, tools []ToolDefinition, stream bool) ([]byte, error)

	// SetChatHeaders sets auth and any provider-specific headers (e.g.
	// Anthropic's caching beta flag) on a chat request.
	SetChatHeaders(req *http.Request, spec ProviderSpec)

	// NewStreamDecoder returns a stateful decoder for an SSE stream. A decoder
	// processes each line's JSON payload (already stripped of the "data: " prefix)
	// and reassembles fragments that span several lines.
	NewStreamDecoder() streamDecoder

	// ParseResponse extracts the completion — text, tool calls, or both — from
	// a non-streaming response body.
	ParseResponse(body []byte) (Result, error)
}

type streamDecoder interface {
	ParseChunk(data string) (StreamDelta, bool)
	Finalize() error
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
	if apiKey == "" {
		return
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

func (anthropicAdapter) BuildChatRequest(spec ProviderSpec, messages []Message, tools []ToolDefinition, stream bool) ([]byte, error) {
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
			anthropicMessages = append(anthropicMessages, anthropicMessagePayload(message))
		default:
			message.Role = "user"
			anthropicMessages = append(anthropicMessages, anthropicMessagePayload(message))
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

	if len(tools) > 0 {
		anthropicTools := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			anthropicTools = append(anthropicTools, map[string]any{
				"name":         tool.Function.Name,
				"description":  tool.Function.Description,
				"input_schema": tool.Function.Parameters,
			})
		}
		payloadBody["tools"] = anthropicTools
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

type anthropicStreamDecoder struct {
	accumulators map[int]*ToolCall
}

func (anthropicAdapter) NewStreamDecoder() streamDecoder {
	return &anthropicStreamDecoder{
		accumulators: make(map[int]*ToolCall),
	}
}

func (d *anthropicStreamDecoder) ParseChunk(dataStr string) (StreamDelta, bool) {
	var event struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
		return StreamDelta{}, false
	}

	switch event.Type {
	case "content_block_delta":
		if event.Delta.Type == "text_delta" {
			return StreamDelta{Text: event.Delta.Text}, true
		} else if event.Delta.Type == "input_json_delta" {
			if acc, ok := d.accumulators[event.Index]; ok {
				acc.Function.Arguments += event.Delta.PartialJSON
			}
			return StreamDelta{ToolCallFragment: event.Delta.PartialJSON}, true
		}
	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			d.accumulators[event.Index] = &ToolCall{
				ID:   event.ContentBlock.ID,
				Type: "function",
				Function: ToolCallFunction{
					Name:      event.ContentBlock.Name,
					Arguments: "",
				},
			}
			return StreamDelta{}, true // Processed, but no delta to yield
		}
	case "content_block_stop":
		if acc, ok := d.accumulators[event.Index]; ok {
			delete(d.accumulators, event.Index)
			return StreamDelta{ToolCall: acc}, true
		}
	}

	return StreamDelta{}, false
}

func (d *anthropicStreamDecoder) Finalize() error {
	if len(d.accumulators) > 0 {
		return fmt.Errorf("truncated tool call: stream ended before stop block")
	}
	return nil
}

func (anthropicAdapter) ParseResponse(body []byte) (Result, error) {
	var decoded struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Result{}, fmt.Errorf("decode anthropic response: %w", err)
	}

	var res Result
	for _, block := range decoded.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			res.Text = strings.TrimSpace(block.Text)
		} else if block.Type == "tool_use" {
			inputBytes, _ := json.Marshal(block.Input)
			res.ToolCalls = append(res.ToolCalls, ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: ToolCallFunction{
					Name:      block.Name,
					Arguments: string(inputBytes),
				},
			})
		}
	}

	if res.Text == "" && len(res.ToolCalls) == 0 {
		return Result{}, fmt.Errorf("anthropic response contained no text or tool calls")
	}
	return res, nil
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

func (openAIishAdapter) BuildChatRequest(spec ProviderSpec, messages []Message, tools []ToolDefinition, stream bool) ([]byte, error) {
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

type openAIishStreamDecoder struct{}

func (openAIishAdapter) NewStreamDecoder() streamDecoder {
	return openAIishStreamDecoder{}
}

func (openAIishStreamDecoder) ParseChunk(dataStr string) (StreamDelta, bool) {
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
				return StreamDelta{Text: text}, true
			}
		}
	}
	return StreamDelta{}, false
}

func (openAIishStreamDecoder) Finalize() error {
	return nil
}

func (openAIishAdapter) ParseResponse(body []byte) (Result, error) {
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Result{}, fmt.Errorf("decode chat response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return Result{}, fmt.Errorf("chat response contained no choices")
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return Result{}, fmt.Errorf("chat response contained no content")
	}
	return Result{Text: content}, nil
}

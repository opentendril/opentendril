package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

// anthropicOutputFallback is used when neither the model registry nor an
// operator config declares an explicit output-token limit. Anthropic's
// Messages API requires max_tokens on every request; there is no server-side
// default, so something must go on the wire. 8192 is chosen because:
//   - A realistic file-write tool call produces 2-4 KB of JSON as output tokens;
//     the previous value (2048) was too small for the native-tool path.
//   - It leaves meaningful headroom above a single large write without
//     inventing a ceiling that no model declaration asked for.
//   - It is small enough that a misconfigured model name is rejected quickly
//     rather than expensively.
const anthropicOutputFallback = 8192

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
		"model":      spec.Model,
		"max_tokens": spec.OutputLimit,
		"messages":   anthropicMessages,
		"stream":     stream,
	}
	if spec.Temperature != nil {
		payloadBody["temperature"] = *spec.Temperature
	}
	if payloadBody["max_tokens"] == 0 {
		payloadBody["max_tokens"] = anthropicOutputFallback
	}
	if len(systemParts) > 0 {
		payloadBody["system"] = []map[string]any{
			annotateCacheControl(anthropicTextBlock(strings.Join(systemParts, "\n\n"))),
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

	// Inject ephemeral cache_control markers positionally. The budget is 4 per
	// request; the system block consumed one above, leaving 3 for the message
	// sequence. The budget is fixed at 3 regardless of whether a system block is
	// present: a conditional budget adds a branch that needs its own test and
	// mutation pin in exchange for one extra marker in an uncommon case.
	//
	// Markers cluster near the end rather than spreading across the conversation.
	// A breakpoint covers every token before it, so the last one handles coverage.
	// The intermediates exist solely to stay within the provider's 20-block
	// lookback limit: a turn with many tool calls can append more than 20 blocks,
	// making the next request unable to find any prior entry. Spreading markers
	// evenly would waste budget on positions already covered by the final marker.
	//
	// Note: a written-but-never-read breakpoint above the provider's minimum costs
	// 1.25× on those tokens. Positions 1 and 2 (system block and last message) are
	// the high-confidence reads in a multi-turn loop; the intermediates earn their
	// keep only when the conversation is long enough for the chain to matter.
	const (
		cacheBreakpointBudget  = 3
		cacheBreakpointSpacing = 15
	)
	if len(anthropicMessages) > 0 {
		type blockPos struct{ msgIdx, blockIdx int }
		var positions []blockPos
		for mi, m := range anthropicMessages {
			blocks, _ := m["content"].([]map[string]any)
			for bi := range blocks {
				positions = append(positions, blockPos{mi, bi})
			}
		}
		spent := 0
		for i := len(positions) - 1; i >= 0 && spent < cacheBreakpointBudget; i -= cacheBreakpointSpacing {
			p := positions[i]
			blocks := anthropicMessages[p.msgIdx]["content"].([]map[string]any)
			annotateCacheControl(blocks[p.blockIdx])
			spent++
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
			return StreamDelta{ToolCalls: []ToolCall{*acc}}, true
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
	var texts []string
	for _, block := range decoded.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			// Every text block, joined. A response may carry prose before a tool
			// call and more after it; keeping only one of them silently discards
			// what the mind said, and which one is kept is not a choice worth
			// having.
			texts = append(texts, strings.TrimSpace(block.Text))
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

	res.Text = strings.Join(texts, "\n")

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
	payloadBody := map[string]any{
		"model":    spec.Model,
		"stream":   stream,
		"messages": messages,
	}
	if spec.Temperature != nil {
		payloadBody["temperature"] = *spec.Temperature
	}
	if len(tools) > 0 {
		payloadBody["tools"] = tools
	}
	payload, err := json.Marshal(payloadBody)
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

type openAIishStreamDecoder struct {
	accumulators map[int]*ToolCall
}

func (openAIishAdapter) NewStreamDecoder() streamDecoder {
	return &openAIishStreamDecoder{
		accumulators: make(map[int]*ToolCall),
	}
}

func (d *openAIishStreamDecoder) ParseChunk(dataStr string) (StreamDelta, bool) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(dataStr), &chunk); err == nil {
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			var delta StreamDelta
			hasDelta := false

			if choice.Delta.Content != "" {
				delta.Text = choice.Delta.Content
				hasDelta = true
			}

			if len(choice.Delta.ToolCalls) > 0 {
				for _, tcDelta := range choice.Delta.ToolCalls {
					acc, ok := d.accumulators[tcDelta.Index]
					if !ok {
						acc = &ToolCall{
							ID:   tcDelta.ID,
							Type: "function",
							Function: ToolCallFunction{
								Name:      tcDelta.Function.Name,
								Arguments: "",
							},
						}
						d.accumulators[tcDelta.Index] = acc
					}

					if tcDelta.Function.Arguments != "" {
						acc.Function.Arguments += tcDelta.Function.Arguments
						delta.ToolCallFragment += tcDelta.Function.Arguments
						hasDelta = true
					}
				}
			}

			if choice.FinishReason != "" {
				if choice.FinishReason == "length" && len(d.accumulators) > 0 {
					// Left in accumulators for Finalize to report truncation.
				} else {
					var indexes []int
					for idx := range d.accumulators {
						indexes = append(indexes, idx)
					}
					sort.Ints(indexes)
					for _, idx := range indexes {
						delta.ToolCalls = append(delta.ToolCalls, *d.accumulators[idx])
					}
					d.accumulators = make(map[int]*ToolCall)
					if len(delta.ToolCalls) > 0 {
						hasDelta = true
					}
				}
			}

			if hasDelta {
				return delta, true
			}
		}
	}
	return StreamDelta{}, false
}

func (d *openAIishStreamDecoder) Finalize() error {
	if len(d.accumulators) > 0 {
		return fmt.Errorf("truncated tool call: stream ended before stop block")
	}
	return nil
}

func (openAIishAdapter) ParseResponse(body []byte) (Result, error) {
	var decoded struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Result{}, fmt.Errorf("decode chat response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return Result{}, fmt.Errorf("chat response contained no choices")
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	toolCalls := decoded.Choices[0].Message.ToolCalls
	if content == "" && len(toolCalls) == 0 {
		return Result{}, fmt.Errorf("chat response contained no content")
	}
	return Result{Text: content, ToolCalls: toolCalls}, nil
}

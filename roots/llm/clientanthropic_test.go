package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBuildChatRequestEmitsAnthropicTools(t *testing.T) {
	adapter := anthropicAdapter{}
	spec := ProviderSpec{Model: "claude-test"}
	tools := []ToolDefinition{{
		Type: "function",
		Function: ToolFunction{
			Name:        "get_weather",
			Description: "Gets the weather",
			Parameters:  map[string]any{"type": "object"},
		},
	}}

	payload, err := adapter.BuildChatRequest(spec, nil, tools, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	toolsField, ok := parsed["tools"].([]any)
	if !ok || len(toolsField) != 1 {
		t.Fatalf("expected 1 tool in payload, got %v", parsed["tools"])
	}

	toolObj := toolsField[0].(map[string]any)
	if toolObj["name"] != "get_weather" {
		t.Errorf("tool name = %v, want get_weather", toolObj["name"])
	}
	if toolObj["description"] != "Gets the weather" {
		t.Errorf("tool description = %v", toolObj["description"])
	}
	if _, hasSchema := toolObj["input_schema"]; !hasSchema {
		t.Errorf("expected input_schema in tool")
	}
}

func TestAnthropicMessagePayloadEmitsToolUseAndResult(t *testing.T) {
	msgWithCall := Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID: "call_123",
			Function: ToolCallFunction{
				Name:      "get_weather",
				Arguments: `{"location":"London"}`,
			},
		}},
	}

	payload := anthropicMessagePayload(msgWithCall)
	if payload["role"] != "assistant" {
		t.Errorf("role = %v", payload["role"])
	}

	content, ok := payload["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected 1 content block, got %v", payload["content"])
	}

	block := content[0]
	if block["type"] != "tool_use" {
		t.Errorf("block type = %v, want tool_use", block["type"])
	}
	if block["id"] != "call_123" {
		t.Errorf("block id = %v", block["id"])
	}
	if block["name"] != "get_weather" {
		t.Errorf("block name = %v", block["name"])
	}
	input, ok := block["input"].(map[string]any)
	if !ok || input["location"] != "London" {
		t.Errorf("block input = %v", block["input"])
	}

	msgWithResult := Message{
		Role:       "user",
		ToolCallID: "call_123",
		Content:    "Sunny",
	}

	payloadResult := anthropicMessagePayload(msgWithResult)
	if payloadResult["role"] != "user" {
		t.Errorf("role = %v", payloadResult["role"])
	}
	contentResult, ok := payloadResult["content"].([]map[string]any)
	if !ok || len(contentResult) != 1 {
		t.Fatalf("expected 1 content block, got %v", payloadResult["content"])
	}
	blockResult := contentResult[0]
	if blockResult["type"] != "tool_result" {
		t.Errorf("block type = %v, want tool_result", blockResult["type"])
	}
	if blockResult["tool_use_id"] != "call_123" {
		t.Errorf("tool_use_id = %v", blockResult["tool_use_id"])
	}
	if blockResult["content"] != "Sunny" {
		t.Errorf("content = %v", blockResult["content"])
	}
}

func TestParseResponseReturnsAnthropicToolCalls(t *testing.T) {
	adapter := anthropicAdapter{}
	body := []byte(`{
		"content": [
			{"type": "text", "text": "Here is the weather:"},
			{"type": "tool_use", "id": "call_1", "name": "get_weather", "input": {"location": "Paris"}}
		]
	}`)

	res, err := adapter.ParseResponse(body)
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}

	if res.Text != "Here is the weather:" {
		t.Errorf("Text = %q", res.Text)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(res.ToolCalls))
	}

	tc := res.ToolCalls[0]
	if tc.ID != "call_1" {
		t.Errorf("ID = %q", tc.ID)
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("Name = %q", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"location":"Paris"}` {
		t.Errorf("Arguments = %q", tc.Function.Arguments)
	}
}

func TestCallStreamParsesAnthropicToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c_1","name":"get_weather"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"loc"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"ation\""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"Lon\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic})

	tokenChan := make(chan string, 10)
	res, err := client.doCall(context.Background(), server.URL, nil, nil, nil, nil, true, tokenChan)
	if err != nil {
		t.Fatalf("doCall failed: %v", err)
	}

	if res.Text != "" {
		t.Errorf("expected empty text, got %q", res.Text)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Function.Arguments != `{"location":"Lon"}` {
		t.Errorf("Arguments = %q", res.ToolCalls[0].Function.Arguments)
	}

	close(tokenChan)
	var tokens []string
	for tk := range tokenChan {
		tokens = append(tokens, tk)
	}
	fullJSON := strings.Join(tokens, "")
	if fullJSON != `{"location":"Lon"}` {
		t.Errorf("Tokens reassembled = %q", fullJSON)
	}
}

func TestCallStreamParsesAnthropicInterleavedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c_1","name":"t_1"}}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"c_2","name":"t_2"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"A"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"B"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"C"}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"content_block_stop","index":1}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic})
	res, err := client.doCall(context.Background(), server.URL, nil, nil, nil, nil, true, nil)
	if err != nil {
		t.Fatalf("doCall failed: %v", err)
	}

	if len(res.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Function.Arguments != "B" {
		t.Errorf("ToolCall 0 args = %q, want 'B'", res.ToolCalls[0].Function.Arguments)
	}
	if res.ToolCalls[1].Function.Arguments != "AC" {
		t.Errorf("ToolCall 1 args = %q, want 'AC'", res.ToolCalls[1].Function.Arguments)
	}
}

func TestCallStreamParsesAnthropicTextAndToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c_1","name":"t"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"content_block_stop","index":1}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic})
	res, err := client.doCall(context.Background(), server.URL, nil, nil, nil, nil, true, nil)
	if err != nil {
		t.Fatalf("doCall failed: %v", err)
	}

	if res.Text != "hello world" {
		t.Errorf("Text = %q", res.Text)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(res.ToolCalls))
	}
}

func TestCallStreamReturnsErrorOnTruncatedToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c_1","name":"get_weather"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"loc\""}}`,
			// No stop block!
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic})
	_, err := client.doCall(context.Background(), server.URL, nil, nil, nil, nil, true, nil)
	if err == nil || !strings.Contains(err.Error(), "truncated tool call") {
		t.Fatalf("expected truncated tool call error, got %v", err)
	}
}

// Every stream carries a DIFFERENT call, so a splice between two of them shows
// up in the content itself rather than only under -race. With identical
// payloads on every stream, two streams swapping arguments is indistinguishable
// from two streams keeping their own — measured on the OpenAI-shaped twin of
// this test, a shared-accumulator mutation passed three runs in five. Flushing
// between events is what makes the streams actually overlap in time.
func TestConcurrencyAnthropicStreamDecoder(t *testing.T) {
	var next int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&next, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			fmt.Sprintf(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"c_%d","name":"t_1"}}`, n),
			fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"arg-%d"}}`, n),
			`{"type":"content_block_stop","index":1}`,
		}
		flusher, _ := w.(http.Flusher)
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic})

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := client.doCall(context.Background(), server.URL, nil, nil, nil, nil, true, nil)
			if err != nil {
				t.Errorf("concurrent doCall failed: %v", err)
				return
			}
			if len(res.ToolCalls) != 1 {
				t.Errorf("ToolCalls = %d, want exactly 1", len(res.ToolCalls))
				return
			}
			call := res.ToolCalls[0]
			wantArgs := "arg-" + strings.TrimPrefix(call.ID, "c_")
			if call.Function.Arguments != wantArgs {
				t.Errorf("call %s carried arguments %q, want %q — a stream received another stream's fragments",
					call.ID, call.Function.Arguments, wantArgs)
			}
		}()
	}
	wg.Wait()
}

// A response may carry prose, then a tool call, then more prose. Keeping only
// one text block silently discards what the mind said — and the discard is
// invisible, because the turn still returns text and still returns the call.
// The block order is deliberate: a defect that keeps the last block and one that
// keeps the first both pass a fixture with a single text block.
func TestParseResponseKeepsEveryAnthropicTextBlock(t *testing.T) {
	body := []byte(`{"content":[
		{"type":"text","text":"before the call"},
		{"type":"tool_use","id":"t1","name":"readFile","input":{"path":"x"}},
		{"type":"text","text":"after the call"}]}`)

	res, err := anthropicAdapter{}.ParseResponse(body)
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}
	if res.Text != "before the call\nafter the call" {
		t.Errorf("Text = %q, want both blocks joined", res.Text)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %d, want 1", len(res.ToolCalls))
	}
}

func TestDoCallAnthropicDowngradesOn400(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"tools not supported"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"text","text":"downgraded answer"}]}`))
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic})
	tools := []ToolDefinition{{
		Type: "function",
		Function: ToolFunction{
			Name: "test",
		},
	}}

	observedError := false
	observer := func() { observedError = true }

	res, err := client.doCall(context.Background(), server.URL, nil, tools, observer, new(bool), false, nil)
	if err != nil {
		t.Fatalf("expected successful retry, got error: %v", err)
	}

	if requests != 2 {
		t.Errorf("expected exactly 2 requests (1 failure, 1 retry), got %d", requests)
	}
	if !observedError {
		t.Errorf("expected observer to be called on downgrade, but it wasn't")
	}
	if res.Text != "downgraded answer" {
		t.Errorf("expected downgraded answer, got %q", res.Text)
	}
}

func TestDoCallAnthropicFailsImmediatelyIfRequiresToolUse(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"tools not supported"}}`))
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic, RequiresToolUse: true})
	tools := []ToolDefinition{{
		Type: "function",
		Function: ToolFunction{
			Name: "test",
		},
	}}

	observedError := false
	observer := func() { observedError = true }

	_, err := client.doCall(context.Background(), server.URL, nil, tools, observer, new(bool), false, nil)
	if err == nil {
		t.Fatalf("expected error due to RequiresToolUse=true, got success")
	}

	if requests != 1 {
		t.Errorf("expected exactly 1 request, got %d", requests)
	}
	if observedError {
		t.Errorf("expected observer NOT to be called when RequiresToolUse=true")
	}
}

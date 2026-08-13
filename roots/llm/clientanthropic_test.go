package llm

import (
	"context"
	"encoding/json"
	"errors"
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

	res, err := adapter.ParseResponse(ProviderSpec{}, body)
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
	res, err := client.doCall(context.Background(), server.URL, nil, nil, true, tokenChan)
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
	res, err := client.doCall(context.Background(), server.URL, nil, nil, true, nil)
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
	res, err := client.doCall(context.Background(), server.URL, nil, nil, true, nil)
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
	_, err := client.doCall(context.Background(), server.URL, nil, nil, true, nil)
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
			res, err := client.doCall(context.Background(), server.URL, nil, nil, true, nil)
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

	res, err := anthropicAdapter{}.ParseResponse(ProviderSpec{}, body)
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

// The refusal contract is the client's, not one adapter's — this family must
// raise it on the same statuses and with the same single request.
func TestDoCallAnthropicReportsToolRefusalWithoutRetrying(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"tools not supported"}}`))
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic})
	tools := []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "test"}}}

	_, err := client.doCall(context.Background(), server.URL, nil, tools, false, nil)
	if !errors.Is(err, ErrRejectedWithTools) {
		t.Fatalf("error = %v, want it to satisfy errors.Is(err, ErrRejectedWithTools)", err)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want exactly 1 — the client must not re-ask on its own", requests)
	}
}

func TestDoCallAnthropicOnlyTreats400And422AsToolRefusal(t *testing.T) {
	refusing := map[int]bool{
		http.StatusBadRequest:          true,
		http.StatusUnprocessableEntity: true,
		http.StatusTooManyRequests:     false,
		http.StatusInternalServerError: false,
	}

	for status, wantRefusal := range refusing {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"message":"nope"}}`))
		}))

		client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic})
		tools := []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "test"}}}

		_, err := client.doCall(context.Background(), server.URL, nil, tools, false, nil)
		if got := errors.Is(err, ErrRejectedWithTools); got != wantRefusal {
			t.Errorf("status %d: errors.Is(err, ErrRejectedWithTools) = %v, want %v (err = %v)", status, got, wantRefusal, err)
		}
		server.Close()
	}
}

// TestTemperatureAnthropicShape asserts that the Anthropic request body carries
// temperature only when one was configured.
//
// The defect this pins: configuredTemperature used to apply a 0.1 fallback for
// every provider when nothing was configured, and anthropicAdapter put
// "temperature": spec.Temperature in the body unconditionally. Anthropic's
// extended-thinking models reject the field entirely, returning a 400, which
// removed the provider from any unattended run.
//
// Mutation plan (must be run and reported):
//   - Restore the unconditional "temperature": spec.Temperature in
//     anthropicAdapter.BuildChatRequest → the "unconfigured" sub-tests go red.
//   - Confirm the "configured" and "zero deliberate" sub-tests stay green on
//     that mutation, proving they are not trivially true.
func TestTemperatureAnthropicShape(t *testing.T) {
	adapter := anthropicAdapter{}

	t.Run("unconfigured non-streaming", func(t *testing.T) {
		// spec.Temperature is nil — no temperature was configured.
		// The field must be absent from the request body, not zero, not null.
		spec := ProviderSpec{Model: "claude-test"}
		payload, err := adapter.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
		if err != nil {
			t.Fatalf("BuildChatRequest failed: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := body["temperature"]; present {
			t.Errorf("temperature key is present in body, want absent when not configured: %s", payload)
		}
	})

	t.Run("unconfigured streaming", func(t *testing.T) {
		// Same assertion for the streaming call shape. Both shapes share one
		// implementation, but the plan requires each to be pinned separately
		// because omitting the guard from either is a half-ship.
		spec := ProviderSpec{Model: "claude-test"}
		payload, err := adapter.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, true)
		if err != nil {
			t.Fatalf("BuildChatRequest failed: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := body["temperature"]; present {
			t.Errorf("temperature key is present in streaming body, want absent when not configured: %s", payload)
		}
	})

	t.Run("configured", func(t *testing.T) {
		spec := ProviderSpec{Model: "claude-test", Temperature: ptr(0.7)}
		payload, err := adapter.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
		if err != nil {
			t.Fatalf("BuildChatRequest failed: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got, present := body["temperature"]
		if !present {
			t.Fatalf("temperature key is absent, want present when configured")
		}
		if got != 0.7 {
			t.Errorf("temperature = %v, want 0.7", got)
		}
	})

	t.Run("zero deliberate choice", func(t *testing.T) {
		// ptr(0.0) makes 0.0 expressible at the type level. The field is
		// present with value 0 when the pointer is non-nil, regardless of
		// what value it carries.
		//
		// Note: no production path can currently reach this sub-case.
		// The only caller is docker.go which guards d.Temperature > 0 before
		// calling SetTemperature, so a genotype temperature of 0 is filtered
		// out before the pointer is set. YAML cannot express it either: both
		// an absent key and an explicit 0.0 parse to float64(0), which
		// configuredTemperature reads as nil. This test proves the mechanism
		// is correct at the type level; it does not prove the mechanism is
		// reachable in production.
		spec := ProviderSpec{Model: "claude-test", Temperature: ptr(0.0)}
		payload, err := adapter.BuildChatRequest(spec, []Message{{Role: "user", Content: "hi"}}, nil, false)
		if err != nil {
			t.Fatalf("BuildChatRequest failed: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, present := body["temperature"]; !present {
			t.Errorf("temperature key is absent, want present when ptr(0.0) is set")
		}
		if body["temperature"] != float64(0) {
			t.Errorf("temperature = %v, want 0", body["temperature"])
		}
	})
}

// TestUnconfiguredTemperatureReachesProviderSpecAsNil covers the step the
// adapter tests cannot see. They build a ProviderSpec literal and assert on
// what the adapter writes; the defect lived earlier than that, in the value
// providerSpecForModel put into the spec in the first place.
//
// configuredTemperature applied a 0.1 fallback whenever the operator had set
// nothing, so every provider received a temperature nobody chose. Restoring
// that fallback leaves every adapter assertion green, because none of them
// runs this function — which is why this test exists separately.
//
// Each provider branch is checked, not just one. providerSpecForModel builds
// a ProviderSpec in eight places; a fallback reintroduced into any single
// branch would otherwise reach the wire unnoticed.
//
// Mutation: return a non-nil pointer from configuredTemperature when the
// config field is zero -> every sub-test goes red.
func TestUnconfiguredTemperatureReachesProviderSpecAsNil(t *testing.T) {
	// This repository ships its own .tendril/config.yaml; a test run from the
	// source tree would silently read it and assert nothing about the
	// unconfigured case.
	chdirWithoutTendrilConfig(t)

	for _, provider := range []string{
		"local", "anthropic", "openai", "grok", "google",
		"openrouter", "nvidia", "unknown-falls-to-default",
	} {
		t.Run(provider, func(t *testing.T) {
			spec := providerSpecForModel(provider, TierPremium, "probe-model", "")
			if spec.Temperature != nil {
				t.Errorf("provider %q: ProviderSpec.Temperature = %v, want nil so the adapter omits the field and the provider's own default applies",
					provider, *spec.Temperature)
			}
		})
	}
}

// TestConfiguredTemperatureReachesProviderSpec is the other half: an operator
// who sets a temperature must still have it sent. Without this, removing the
// field entirely would satisfy the test above.
func TestConfiguredTemperatureReachesProviderSpec(t *testing.T) {
	chdirWithTendrilConfig(t, `llm:
  providers:
    anthropic:
      temperature: 0.35
`)

	spec := providerSpecForModel("anthropic", TierPremium, "probe-model", "")
	if spec.Temperature == nil {
		t.Fatalf("ProviderSpec.Temperature = nil, want the configured 0.35 to reach the spec")
	}
	if *spec.Temperature != 0.35 {
		t.Errorf("ProviderSpec.Temperature = %v, want 0.35", *spec.Temperature)
	}
}

// countCacheMarkers walks any JSON value and returns the number of objects that
// carry a "cache_control" key with {"type":"ephemeral"}.
func countCacheMarkers(v any) int {
	switch val := v.(type) {
	case map[string]any:
		n := 0
		if cc, ok := val["cache_control"].(map[string]any); ok && cc["type"] == "ephemeral" {
			n++
		}
		for _, child := range val {
			n += countCacheMarkers(child)
		}
		return n
	case []any:
		n := 0
		for _, item := range val {
			n += countCacheMarkers(item)
		}
		return n
	}
	return 0
}

// collectMessageBlockPositions returns the flat 0-based positions (across all
// messages) that carry a cache_control marker. It does not include the system
// block; only the message sequence is walked.
func collectMessageBlockPositions(messages []any) []int {
	var marked []int
	pos := 0
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				pos++
				continue
			}
			if cc, ok := b["cache_control"].(map[string]any); ok && cc["type"] == "ephemeral" {
				marked = append(marked, pos)
			}
			pos++
		}
	}
	return marked
}

// TestAnthropicCacheControlNeverGatesOnLength verifies that cache_control is
// injected positionally regardless of content length. Both a short system block
// and a short user message must be marked.
//
// Mutation target: restore any len(content) > N gate in anthropicMessagePayload.
// A 2-character message collapses to a bare string instead of block form, so
// the positional walk finds no blocks at all and len(marked) == 0.
func TestAnthropicCacheControlNeverGatesOnLength(t *testing.T) {
	adapter := anthropicAdapter{}
	spec := ProviderSpec{Model: "claude-test"}
	messages := []Message{
		{Role: "system", Content: "hi"},
		{Role: "user", Content: "go"},
	}

	payload, err := adapter.BuildChatRequest(spec, messages, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// System block must be marked unconditionally.
	system, ok := parsed["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatalf("system block missing or empty")
	}
	sysBlock := system[0].(map[string]any)
	if cc, ok := sysBlock["cache_control"].(map[string]any); !ok || cc["type"] != "ephemeral" {
		t.Fatalf("system block cache_control = %v, want ephemeral", sysBlock["cache_control"])
	}

	// The last (and only) message block must also be marked — it is at the last
	// position in the flat index regardless of its byte length.
	msgs, ok := parsed["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("messages missing or empty")
	}
	marked := collectMessageBlockPositions(msgs)
	if len(marked) != 1 || marked[0] != 0 {
		t.Fatalf("marked message-block positions = %v, want [0]", marked)
	}
}

// TestAnthropicCacheControlBudgetLimitFull verifies that the total marker count
// is exactly 4 when the message sequence is long enough to earn all three
// message breakpoints (60 messages → positions 59, 44, 29, plus system = 4).
//
// Mutation targets:
// 1. Remove the budget counter so every block gets a marker → count > 4 → fails.
// 2. Invert the loop to walk forward from index 0 → marked positions become [0, 15, 30] → position equality assertion fails.
func TestAnthropicCacheControlBudgetLimitFull(t *testing.T) {
	adapter := anthropicAdapter{}
	spec := ProviderSpec{Model: "claude-test"}

	// 60 user messages: flat index 0–59.
	// Backward walk at step 15: positions 59, 44, 29 → 3 message markers.
	// Plus 1 system marker = 4 total.
	msgs := make([]Message, 0, 61)
	msgs = append(msgs, Message{Role: "system", Content: "sys"})
	for i := 0; i < 60; i++ {
		msgs = append(msgs, Message{Role: "user", Content: "msg"})
	}

	payload, err := adapter.BuildChatRequest(spec, msgs, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := countCacheMarkers(parsed); got != 4 {
		t.Fatalf("cache_control count = %d, want exactly 4", got)
	}

	msgsSlice, _ := parsed["messages"].([]any)
	marked := collectMessageBlockPositions(msgsSlice)
	if len(marked) != 3 || marked[0] != 29 || marked[1] != 44 || marked[2] != 59 {
		t.Fatalf("marked message-block positions = %v, want [29, 44, 59]", marked)
	}
}

// TestAnthropicCacheControlBudgetSelfLimiting verifies that the budget does not
// inflate for short conversations. With 10 messages (flat index 0–9), only
// position 9 is selected: 1 message marker + 1 system marker = 2 total.
//
// Mutation targets:
// 1. Always emit 3 message markers regardless of depth → count > 2 → fails.
// 2. Invert the loop to walk forward from index 0 → marked position becomes [0] → position equality assertion fails.
func TestAnthropicCacheControlBudgetSelfLimiting(t *testing.T) {
	adapter := anthropicAdapter{}
	spec := ProviderSpec{Model: "claude-test"}

	msgs := make([]Message, 0, 11)
	msgs = append(msgs, Message{Role: "system", Content: "sys"})
	for i := 0; i < 10; i++ {
		msgs = append(msgs, Message{Role: "user", Content: "msg"})
	}

	payload, err := adapter.BuildChatRequest(spec, msgs, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got := countCacheMarkers(parsed); got != 2 {
		t.Fatalf("cache_control count = %d, want exactly 2 (system + last message)", got)
	}

	msgsSlice, _ := parsed["messages"].([]any)
	marked := collectMessageBlockPositions(msgsSlice)
	if len(marked) != 1 || marked[0] != 9 {
		t.Fatalf("marked message-block positions = %v, want [9]", marked)
	}
}

// TestAnthropicCacheControlSpacingDefeatsLookback verifies that a single
// assistant turn with 25 tool_use blocks receives at least two message-sequence
// markers, and that no gap between consecutive marked positions exceeds 20
// content blocks.
//
// The provider's lookback scans back at most 20 blocks to find a prior cache
// entry. Without intermediate breakpoints, a 25-block turn leaves a gap of 24
// between the trailing marker and any prior entry, breaking the chain.
//
// Mutation targets:
// 1. Place only one message marker (no spacing step) → len(markedPositions) == 1 → assertion 1 fails loudly. The gap loop would pass vacuously with a single position; the explicit count assertion is what actually catches this mutation.
// 2. Invert the loop to walk forward from index 0 → marked positions become [0, 15] → position equality assertion fails.
//
// Scope note: only the message sequence is asserted. The system breakpoint is
// not included in markedPositions.
func TestAnthropicCacheControlSpacingDefeatsLookback(t *testing.T) {
	adapter := anthropicAdapter{}
	spec := ProviderSpec{Model: "claude-test"}

	// Build one assistant message with 25 tool_use blocks. Each ToolCall is
	// one block in the flat index.
	toolCalls := make([]ToolCall, 25)
	for i := range toolCalls {
		toolCalls[i] = ToolCall{
			ID: fmt.Sprintf("c%d", i),
			Function: ToolCallFunction{
				Name:      "noop",
				Arguments: "{}",
			},
		}
	}

	messages := []Message{
		{Role: "assistant", ToolCalls: toolCalls},
	}

	payload, err := adapter.BuildChatRequest(spec, messages, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	msgs, _ := parsed["messages"].([]any)
	markedPositions := collectMessageBlockPositions(msgs)

	// Assertion 1: must have at least two markers so the gap loop has pairs to
	// check. Fail loudly here — a single marker passes the gap loop vacuously.
	if len(markedPositions) < 2 {
		t.Fatalf("marked message-block positions = %v, want at least 2 (an intermediate breakpoint is required to keep the lookback chain intact on a 25-block turn)", markedPositions)
	}

	// Assertion 2: no gap between consecutive markers exceeds 20.
	for i := 1; i < len(markedPositions); i++ {
		gap := markedPositions[i] - markedPositions[i-1]
		if gap > 20 {
			t.Errorf("gap between positions %d and %d = %d blocks, exceeds the 20-block lookback limit", markedPositions[i-1], markedPositions[i], gap)
		}
	}

	// Assertion 3: exact positions pin the reverse-walk direction.
	if len(markedPositions) != 2 || markedPositions[0] != 9 || markedPositions[1] != 24 {
		t.Errorf("marked message-block positions = %v, want [9, 24]", markedPositions)
	}
}

// TestAnthropicMessagePayloadAlwaysBlockForm verifies that anthropicMessagePayload
// always returns block-form content, not a bare string, regardless of message
// length. Deterministic serialisation is required for byte-stable cache keys.
//
// Mutation target: restore the short-circuit branch (return "content": string
// when len < threshold) → short message returns a string → type assertion fails.
func TestAnthropicMessagePayloadAlwaysBlockForm(t *testing.T) {
	shortMsg := Message{Role: "user", Content: "hi"}
	payload := anthropicMessagePayload(shortMsg)
	if _, ok := payload["content"].([]map[string]any); !ok {
		t.Errorf("short message content type = %T, want []map[string]any", payload["content"])
	}

	longMsg := Message{Role: "user", Content: strings.Repeat("x", 2000)}
	payload = anthropicMessagePayload(longMsg)
	if _, ok := payload["content"].([]map[string]any); !ok {
		t.Errorf("long message content type = %T, want []map[string]any", payload["content"])
	}
}

// TestAnthropicNoBetaHeader verifies that SetChatHeaders does not emit the
// anthropic-beta header. Prompt caching is generally available; the header is
// dead and rejected by some endpoints.
//
// Mutation target: re-add req.Header.Set("anthropic-beta", ...) → header
// present → fails.
func TestAnthropicNoBetaHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.com/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	anthropicAdapter{}.SetChatHeaders(req, ProviderSpec{APIKey: "test-key"})
	if got := req.Header.Get("Anthropic-Beta"); got != "" {
		t.Fatalf("Anthropic-Beta header = %q, want absent (header is dead; prompt caching is GA)", got)
	}
}

func TestParseResponseAnthropicUsage(t *testing.T) {
	adapter := anthropicAdapter{}
	spec := ProviderSpec{Provider: "anthropic"}
	body := []byte(`{
		"content": [{"type": "text", "text": "Hi"}],
		"usage": {
			"input_tokens": 10,
			"output_tokens": 5
		}
	}`)
	res, err := adapter.ParseResponse(spec, body)
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}
	if res.Usage.PromptTokens == nil || *res.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %v, want 10", res.Usage.PromptTokens)
	}
	if res.Usage.CompletionTokens == nil || *res.Usage.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %v, want 5", res.Usage.CompletionTokens)
	}
	if res.Usage.TotalTokens != nil {
		t.Errorf("TotalTokens = %v, want nil", res.Usage.TotalTokens)
	}
}

func TestCallStreamAnthropicUsageReplacesAndDoesNotSum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":15,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`,
			`{"type":"message_delta","usage":{"output_tokens":3}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
			`{"type":"message_delta","usage":{"output_tokens":5}}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "anthropic", BaseURL: server.URL, Mode: ModeAnthropic})
	res, err := client.doCall(context.Background(), server.URL, nil, nil, true, nil)
	if err != nil {
		t.Fatalf("doCall failed: %v", err)
	}

	if res.Usage.PromptTokens == nil || *res.Usage.PromptTokens != 15 {
		t.Errorf("PromptTokens = %v, want 15", res.Usage.PromptTokens)
	}
	if res.Usage.CompletionTokens == nil || *res.Usage.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %v, want 5, cumulative replace semantics means it must not sum", res.Usage.CompletionTokens)
	}
}

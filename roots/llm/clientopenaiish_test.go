package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBuildChatRequestEmitsOpenAIishTools(t *testing.T) {
	adapter := openAIishAdapter{}
	spec := ProviderSpec{Model: "gpt-test"}
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
	if toolObj["type"] != "function" {
		t.Errorf("tool type = %v, want function", toolObj["type"])
	}
	funcObj := toolObj["function"].(map[string]any)
	if funcObj["name"] != "get_weather" {
		t.Errorf("tool name = %v, want get_weather", funcObj["name"])
	}
}

func TestBuildChatRequestWithoutToolsMatchesFixture(t *testing.T) {
	adapter := openAIishAdapter{}
	spec := ProviderSpec{Model: "gpt-4", Temperature: ptr(0.5)}
	messages := []Message{
		{Role: "system", Content: "system instruction"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "how are you?"},
	}

	payload, err := adapter.BuildChatRequest(spec, messages, nil, false)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}

	fixturePath := filepath.Join("testdata", "openai-system-user-assistant.json")
	expected, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}

	if string(payload) != strings.TrimSpace(string(expected)) {
		t.Errorf("Payload mismatch. Got:\n%s\nWant:\n%s", payload, expected)
	}
}

func TestMessageWithToolCallsAndResultsRoundTrips(t *testing.T) {
	// A tool result round-trips with no adapter involvement — an assistant message with tool calls and a following tool result must both serialise correctly with no adapter code at all.
	msgWithCall := Message{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID:   "call_123",
			Type: "function",
			Function: ToolCallFunction{
				Name:      "get_weather",
				Arguments: `{"location":"London"}`,
			},
		}},
	}
	msgWithResult := Message{
		Role:       "tool",
		ToolCallID: "call_123",
		Content:    "Sunny",
	}

	callBytes, err := json.Marshal(msgWithCall)
	if err != nil {
		t.Fatalf("marshal msgWithCall failed: %v", err)
	}
	expectedCall := `{"role":"assistant","content":"","tool_calls":[{"id":"call_123","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"London\"}"}}]}`
	if string(callBytes) != expectedCall {
		t.Errorf("msgWithCall JSON = %s, want %s", string(callBytes), expectedCall)
	}

	resBytes, err := json.Marshal(msgWithResult)
	if err != nil {
		t.Fatalf("marshal msgWithResult failed: %v", err)
	}
	expectedRes := `{"role":"tool","content":"Sunny","tool_call_id":"call_123"}`
	if string(resBytes) != expectedRes {
		t.Errorf("msgWithResult JSON = %s, want %s", string(resBytes), expectedRes)
	}
}

func TestParseResponseReturnsOpenAIishToolCalls(t *testing.T) {
	adapter := openAIishAdapter{}
	body := []byte(`{
		"choices": [{
			"message": {
				"content": "Here is the weather:",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"location\":\"Paris\"}"
						}
					}
				]
			}
		}]
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

func TestParseResponseReturnsToolOnlyResponse(t *testing.T) {
	adapter := openAIishAdapter{}
	body := []byte(`{
		"choices": [{
			"message": {
				"content": null,
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "get_weather",
							"arguments": "{\"location\":\"Paris\"}"
						}
					}
				]
			}
		}]
	}`)

	res, err := adapter.ParseResponse(ProviderSpec{}, body)
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}

	if res.Text != "" {
		t.Errorf("Text = %q", res.Text)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(res.ToolCalls))
	}
}

func TestCallStreamParsesOpenAIishToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c_1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"Lon\"}"}}]}}]}`,
			`{"choices":[{"finish_reason":"tool_calls"}]}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "openai", BaseURL: server.URL, Mode: ModeOpenAIish})

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

func TestCallStreamParsesOpenAIishInterleavedToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c_1","type":"function","function":{"name":"t_1","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c_2","type":"function","function":{"name":"t_2","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"A"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":2,"function":{"arguments":"B"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"C"}}]}}]}`,
			`{"choices":[{"finish_reason":"tool_calls"}]}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "openai", BaseURL: server.URL, Mode: ModeOpenAIish})
	res, err := client.doCall(context.Background(), server.URL, nil, nil, true, nil)
	if err != nil {
		t.Fatalf("doCall failed: %v", err)
	}

	if len(res.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Function.Arguments != "AC" {
		t.Errorf("ToolCall 0 args = %q, want 'AC'", res.ToolCalls[0].Function.Arguments)
	}
	if res.ToolCalls[1].Function.Arguments != "B" {
		t.Errorf("ToolCall 1 args = %q, want 'B'", res.ToolCalls[1].Function.Arguments)
	}
}

func TestCallStreamParsesOpenAIishTextAndToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"choices":[{"delta":{"content":"hello "}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c_1","type":"function","function":{"name":"t","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"content":"world"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
			`{"choices":[{"finish_reason":"stop"}]}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "openai", BaseURL: server.URL, Mode: ModeOpenAIish})
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

func TestCallStreamOpenAIishReturnsErrorOnTruncatedToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c_1","type":"function","function":{"name":"t","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"loc\""}}]}}]}`,
			`{"choices":[{"finish_reason":"length"}]}`, // open accumulator
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "openai", BaseURL: server.URL, Mode: ModeOpenAIish})
	_, err := client.doCall(context.Background(), server.URL, nil, nil, true, nil)
	if err == nil || !strings.Contains(err.Error(), "truncated tool call") {
		t.Fatalf("expected truncated tool call error, got %v", err)
	}
}

func TestCallStreamOpenAIishTextOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"choices":[{"delta":{"content":"text only"}}]}`,
			`{"choices":[{"finish_reason":"stop"}]}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "openai", BaseURL: server.URL, Mode: ModeOpenAIish})
	res, err := client.doCall(context.Background(), server.URL, nil, nil, true, nil)
	if err != nil {
		t.Fatalf("doCall failed: %v", err)
	}

	if res.Text != "text only" {
		t.Errorf("Text = %q", res.Text)
	}
	if len(res.ToolCalls) != 0 {
		t.Fatalf("expected 0 tool calls, got %d", len(res.ToolCalls))
	}
}

// Every stream carries a DIFFERENT call, so a splice between two of them shows
// up in the content itself. An earlier version of this test gave all five
// streams identical payloads: under a shared-accumulator mutation it passed
// three runs in five, because two streams swapping identical arguments is
// indistinguishable from two streams keeping their own. Pairing each call's id
// with its own arguments is what makes the assertion able to fail.
func TestConcurrencyOpenAIishStreamDecoder(t *testing.T) {
	var next int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&next, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c_%d","type":"function","function":{"name":"t","arguments":""}}]}}]}`, n),
			fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"arg-%d"}}]}}]}`, n),
			`{"choices":[{"finish_reason":"tool_calls"}]}`,
		}
		// Flush between events so the streams genuinely overlap in time. Without
		// this the whole response lands in one write and each stream is decoded
		// start-to-finish before the next begins, which is the arrangement least
		// likely to expose shared state.
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

	client := NewClient(ProviderSpec{Provider: "openai", BaseURL: server.URL, Mode: ModeOpenAIish})

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
			// The identifier names which stream this call came from, so its
			// arguments must be that stream's and no other's.
			wantArgs := "arg-" + strings.TrimPrefix(call.ID, "c_")
			if call.Function.Arguments != wantArgs {
				t.Errorf("call %s carried arguments %q, want %q — a stream received another stream's fragments",
					call.ID, call.Function.Arguments, wantArgs)
			}
		}()
	}
	wg.Wait()
}

// The client reports a refusal; it does not answer one. The request count is
// the assertion that matters: a client that quietly re-asks would still return
// an error here, and only counting requests can tell the two apart.
func TestDoCallOpenAIishReportsToolRefusalWithoutRetrying(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"tools not supported"}}`))
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "openai", BaseURL: server.URL, Mode: ModeOpenAIish})
	tools := []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "test"}}}

	_, err := client.doCall(context.Background(), server.URL, nil, tools, false, nil)
	if !errors.Is(err, ErrRejectedWithTools) {
		t.Fatalf("error = %v, want it to satisfy errors.Is(err, ErrRejectedWithTools)", err)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want exactly 1 — the client must not re-ask on its own", requests)
	}
	if !strings.Contains(err.Error(), "tools not supported") {
		t.Errorf("error %q drops the endpoint's own message", err)
	}
}

// A refusal is a statement about the tool definitions. Everything else the
// endpoint might say is an ordinary failure, and demoting a run to the prose
// protocol over a rate limit would lose quality while announcing a capability.
func TestDoCallOpenAIishOnlyTreats400And422AsToolRefusal(t *testing.T) {
	refusing := map[int]bool{
		http.StatusBadRequest:          true,
		http.StatusUnprocessableEntity: true,
		http.StatusUnauthorized:        false,
		http.StatusTooManyRequests:     false,
		http.StatusInternalServerError: false,
		http.StatusBadGateway:          false,
	}

	for status, wantRefusal := range refusing {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"message":"nope"}}`))
		}))

		client := NewClient(ProviderSpec{Provider: "openai", BaseURL: server.URL, Mode: ModeOpenAIish})
		tools := []ToolDefinition{{Type: "function", Function: ToolFunction{Name: "test"}}}

		_, err := client.doCall(context.Background(), server.URL, nil, tools, false, nil)
		if got := errors.Is(err, ErrRejectedWithTools); got != wantRefusal {
			t.Errorf("status %d: errors.Is(err, ErrRejectedWithTools) = %v, want %v (err = %v)", status, got, wantRefusal, err)
		}
		server.Close()
	}
}

// A request carrying no definitions cannot be refused for carrying them, so a
// 400 on a plain call stays the ordinary error every caller already matches on.
func TestDoCallOpenAIishPlain400IsNotAToolRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad model"}}`))
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "openai", BaseURL: server.URL, Mode: ModeOpenAIish})

	_, err := client.doCall(context.Background(), server.URL, nil, nil, false, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrRejectedWithTools) {
		t.Errorf("a 400 with no tool definitions must not read as a tool refusal: %v", err)
	}
}

// TestTemperatureOpenAIishShape asserts that the OpenAI-ish request body carries
// temperature only when one was configured.
//
// Mirror of TestTemperatureAnthropicShape for the other adapter implementation.
// Both adapters were changed identically; a test covering only one shape is the
// likeliest gap — two call sites, and both must be pinned.
//
// Mutation plan (must be run and reported):
//   - Restore the unconditional "temperature": spec.Temperature in
//     openAIishAdapter.BuildChatRequest → the "unconfigured" sub-tests go red.
//   - Confirm the "configured" and "zero deliberate" sub-tests stay green on
//     that mutation, proving they are not trivially true.
func TestTemperatureOpenAIishShape(t *testing.T) {
	adapter := openAIishAdapter{}

	t.Run("unconfigured non-streaming", func(t *testing.T) {
		spec := ProviderSpec{Model: "gpt-test"}
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
		spec := ProviderSpec{Model: "gpt-test"}
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
		spec := ProviderSpec{Model: "gpt-test", Temperature: ptr(0.7)}
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
		// Note: no production path can currently reach this sub-case.
		// The only caller is docker.go which guards d.Temperature > 0 before
		// calling SetTemperature, so a genotype temperature of 0 is filtered
		// out before the pointer is set. YAML cannot express it either: both
		// an absent key and an explicit 0.0 parse to float64(0), which
		// configuredTemperature reads as nil. This test proves the mechanism
		// is correct at the type level; it does not prove the mechanism is
		// reachable in production.
		spec := ProviderSpec{Model: "gpt-test", Temperature: ptr(0.0)}
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

func TestBuildChatRequestDoesNotInjectUsageFlagForOpenRouter(t *testing.T) {
	adapter := openAIishAdapter{}
	spec := ProviderSpec{Provider: "openrouter", Model: "meta-llama/llama-3.3-70b-instruct"}
	payload, err := adapter.BuildChatRequest(spec, nil, nil, true)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, ok := parsed["stream_options"]; ok {
		t.Errorf("expected stream_options to be absent for openrouter, got %v", parsed["stream_options"])
	}
}

func TestBuildChatRequestInjectsUsageFlagForOpenAI(t *testing.T) {
	adapter := openAIishAdapter{}
	spec := ProviderSpec{Provider: "openai", Model: "gpt-4o"}
	payload, err := adapter.BuildChatRequest(spec, nil, nil, true)
	if err != nil {
		t.Fatalf("BuildChatRequest failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	opts, ok := parsed["stream_options"].(map[string]any)
	if !ok || opts["include_usage"] != true {
		t.Errorf("expected stream_options.include_usage to be true for openai, got %v", parsed["stream_options"])
	}
}

func TestParseResponseOpenRouterUsage(t *testing.T) {
	adapter := openAIishAdapter{}
	spec := ProviderSpec{Provider: "openrouter"}
	body := []byte(`{
		"choices": [{"message": {"content": "Hello"}}],
		"usage": {
			"prompt_tokens": 194,
			"completion_tokens": 0,
			"cost": 0.0000052349000001
		}
	}`)
	res, err := adapter.ParseResponse(spec, body)
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}
	if res.Usage.PromptTokens == nil || *res.Usage.PromptTokens != 194 {
		t.Errorf("PromptTokens = %v, want 194", res.Usage.PromptTokens)
	}
	if res.Usage.CompletionTokens == nil || *res.Usage.CompletionTokens != 0 {
		t.Errorf("CompletionTokens = %v, want 0", res.Usage.CompletionTokens)
	}
	if res.Usage.TotalTokens != nil {
		t.Errorf("TotalTokens = %v, want nil", res.Usage.TotalTokens)
	}
	if res.Usage.CostAmount == nil || *res.Usage.CostAmount != "0.0000052349000001" {
		t.Errorf("CostAmount = %v, want exact decimal preservation '0.0000052349000001'", res.Usage.CostAmount)
	}
	if res.Usage.CostUnit == nil || *res.Usage.CostUnit != "credits" {
		t.Errorf("CostUnit = %v, want credits", res.Usage.CostUnit)
	}
	if res.Usage.CostProvenance == nil || *res.Usage.CostProvenance != "openrouter" {
		t.Errorf("CostProvenance = %v, want openrouter", res.Usage.CostProvenance)
	}
}

func TestCallStreamOpenRouterUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"choices":[{"delta":{"content":"text only"}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30,"cost":0.000015000000002}}`,
		}
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := NewClient(ProviderSpec{Provider: "openrouter", BaseURL: server.URL, Mode: ModeOpenAIish})
	res, err := client.doCall(context.Background(), server.URL, nil, nil, true, nil)
	if err != nil {
		t.Fatalf("doCall failed: %v", err)
	}

	if res.Text != "text only" {
		t.Errorf("Text = %q", res.Text)
	}
	if res.Usage.PromptTokens == nil || *res.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %v, want 10", res.Usage.PromptTokens)
	}
	if res.Usage.TotalTokens == nil || *res.Usage.TotalTokens != 30 {
		t.Errorf("TotalTokens = %v, want 30", res.Usage.TotalTokens)
	}
	if res.Usage.CostAmount == nil || *res.Usage.CostAmount != "0.000015000000002" {
		t.Errorf("CostAmount = %v, want '0.000015000000002'", res.Usage.CostAmount)
	}
	if res.Usage.CostUnit == nil || *res.Usage.CostUnit != "credits" {
		t.Errorf("CostUnit = %v, want credits", res.Usage.CostUnit)
	}
	if res.Usage.CostProvenance == nil || *res.Usage.CostProvenance != "openrouter" {
		t.Errorf("CostProvenance = %v, want openrouter", res.Usage.CostProvenance)
	}
}

func TestAbsentUsageFieldsRemainNil(t *testing.T) {
	adapter := openAIishAdapter{}
	spec := ProviderSpec{Provider: "openai"}
	body := []byte(`{
		"choices": [{"message": {"content": "Hello"}}]
	}`)
	res, err := adapter.ParseResponse(spec, body)
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}
	if res.Usage.PromptTokens != nil || res.Usage.CostAmount != nil {
		t.Errorf("Expected nil usage fields, got %v", res.Usage)
	}
}

func TestUnknownProviderIgnoresCost(t *testing.T) {
	adapter := openAIishAdapter{}
	spec := ProviderSpec{Provider: "some-unknown-provider"}
	body := []byte(`{
		"choices": [{"message": {"content": "Hello"}}],
		"usage": {
			"cost": 0.0001
		}
	}`)
	res, err := adapter.ParseResponse(spec, body)
	if err != nil {
		t.Fatalf("ParseResponse failed: %v", err)
	}
	if res.Usage.CostAmount != nil {
		t.Errorf("CostAmount = %v, want nil for unknown provider", res.Usage.CostAmount)
	}
}

package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func buildAnthropicSSE(events ...map[string]any) string {
	lines := make([]string, 0, len(events)+1)
	for _, event := range events {
		payload, _ := json.Marshal(event)
		lines = append(lines, "data: "+string(payload))
	}
	lines = append(lines, "data: [DONE]")
	return strings.Join(lines, "\n\n") + "\n\n"
}

func writeAnthropicSSEEvent(t *testing.T, w http.ResponseWriter, event map[string]any) {
	t.Helper()

	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("expected event json to marshal: %v", err)
	}
	if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
		t.Fatalf("expected event to be written: %v", err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeAnthropicSSEDone(t *testing.T, w http.ResponseWriter) {
	t.Helper()

	if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
		t.Fatalf("expected done marker to be written: %v", err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func TestCompleteSimpleKimiCodingBuildsAnthropicRequestAndParsesText(t *testing.T) {
	var requestBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("expected /v1/messages path, got %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "kimi-test-key" {
			t.Fatalf("expected kimi api key header, got %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("expected anthropic version header, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request json: %v", err)
		}

		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_123",
					"usage": map[string]any{
						"input_tokens":                120,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "hello from kimi",
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 30,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		SystemPrompt: "You are concise.",
		Messages: []Message{
			UserMessage{Content: "Say hello"},
		},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop reason stop, got %q", response.StopReason)
	}
	if len(response.Content) != 1 {
		t.Fatalf("expected one content block, got %d", len(response.Content))
	}
	text, ok := response.Content[0].(TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", response.Content[0])
	}
	if text.Text != "hello from kimi" {
		t.Fatalf("expected response text, got %q", text.Text)
	}
	if response.Usage.Input != 120 || response.Usage.Output != 30 {
		t.Fatalf("expected usage to be parsed, got %+v", response.Usage)
	}
	if requestBody.Model != "kimi-k2-thinking" {
		t.Fatalf("expected model id in request, got %q", requestBody.Model)
	}
	if len(requestBody.System) != 1 || requestBody.System[0].Text != "You are concise." {
		t.Fatalf("expected system prompt in request, got %+v", requestBody.System)
	}
	expectedMaxTokens := minInt(model.MaxTokens, 32000)
	if requestBody.MaxTokens != expectedMaxTokens {
		t.Fatalf("expected default max tokens %d, got %d", expectedMaxTokens, requestBody.MaxTokens)
	}
}

func TestCompleteSimpleKimiCodingParsesToolCallResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_124",
					"usage": map[string]any{
						"input_tokens":                50,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    "call_1",
					"name":  "edit",
					"input": map[string]any{},
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": `{"path":"README.md"}`,
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "tool_use",
				},
				"usage": map[string]any{
					"output_tokens": 10,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Edit the file"},
		},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	if response.StopReason != StopReasonToolUse {
		t.Fatalf("expected toolUse stop reason, got %q", response.StopReason)
	}
	call, ok := response.Content[0].(ToolCall)
	if !ok {
		t.Fatalf("expected tool call content, got %T", response.Content[0])
	}
	if call.ID != "call_1" || call.Name != "edit" {
		t.Fatalf("expected tool call to be parsed, got %+v", call)
	}
}

func TestCompleteSimpleKimiCodingConvertsToolResultsAndTools(t *testing.T) {
	var requestBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request json: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_125",
					"usage": map[string]any{
						"input_tokens":                10,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "done",
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 5,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Use the tool"},
			AssistantMessage{
				Content: []ContentBlock{
					ToolCall{
						ID:        "call+1",
						Name:      "double_number",
						Arguments: map[string]any{"value": 21},
					},
				},
				API:        "openai-responses",
				Provider:   "github-copilot",
				Model:      "gpt-5.4",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: "call+1",
				ToolName:   "double_number",
				Content:    []ContentBlock{TextContent{Text: "42"}},
			},
		},
		Tools: []Tool{
			{
				Name:        "double_number",
				Description: "Double a number",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"value": map[string]any{"type": "number"},
					},
					"required": []string{"value"},
				},
			},
		},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %q", response.StopReason)
	}
	if len(requestBody.Tools) != 1 || requestBody.Tools[0].Name != "double_number" {
		t.Fatalf("expected tool definition in request, got %+v", requestBody.Tools)
	}
	if len(requestBody.Messages) != 3 {
		t.Fatalf("expected three messages in request, got %d", len(requestBody.Messages))
	}
	toolResultMessage, ok := requestBody.Messages[2].Content.([]any)
	if !ok {
		t.Fatalf("expected grouped tool result blocks, got %T", requestBody.Messages[2].Content)
	}
	firstToolResult, ok := toolResultMessage[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first tool result object, got %T", toolResultMessage[0])
	}
	if firstToolResult["tool_use_id"] != "call_1" {
		t.Fatalf("expected normalized tool result id call_1, got %#v", firstToolResult["tool_use_id"])
	}
}

func TestCompleteSimpleKimiCodingReturnsProviderErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"type":    "invalid_request_error",
				"message": "prompt is too long",
			},
		})
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "hello"},
		},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	if response.StopReason != StopReasonError {
		t.Fatalf("expected error stop reason, got %q", response.StopReason)
	}
	if response.ErrorMessage != "prompt is too long" {
		t.Fatalf("expected provider error message, got %q", response.ErrorMessage)
	}
}

func TestCompleteSimpleKimiCodingSetsThinkingCacheAndToolChoice(t *testing.T) {
	var requestBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request json: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_126",
					"usage": map[string]any{
						"input_tokens":                10,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "configured",
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 3,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	_ = Complete(*model, Context{
		SystemPrompt: "Use tools when needed.",
		Tools: []Tool{
			{
				Name:        "noop",
				Description: "Do nothing",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		},
		Messages: []Message{
			UserMessage{Content: "hello"},
		},
	}, ProviderStreamOptions{
		APIKey:               "kimi-test-key",
		Reasoning:            ThinkingLevelHigh,
		ThinkingBudgetTokens: 2048,
		CacheRetention:       CacheRetentionShort,
		ToolChoice:           "any",
	})

	thinking, ok := requestBody.Thinking.(map[string]any)
	if !ok {
		t.Fatalf("expected thinking payload, got %T", requestBody.Thinking)
	}
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(2048) {
		t.Fatalf("expected budget-based thinking payload, got %#v", thinking)
	}
	if requestBody.ToolChoice != nil {
		t.Fatalf("expected unsupported kimi tool_choice to be omitted, got %#v", requestBody.ToolChoice)
	}
	if len(requestBody.System) != 1 || requestBody.System[0].CacheControl == nil || requestBody.System[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("expected cache control on system prompt, got %+v", requestBody.System)
	}
	lastMessage := requestBody.Messages[len(requestBody.Messages)-1]
	blocks, ok := lastMessage.Content.([]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("expected user content blocks with cache control, got %#v", lastMessage.Content)
	}
	lastBlock, ok := blocks[len(blocks)-1].(map[string]any)
	if !ok {
		t.Fatalf("expected last block object, got %T", blocks[len(blocks)-1])
	}
	cacheControl, ok := lastBlock["cache_control"].(map[string]any)
	if !ok || cacheControl["type"] != "ephemeral" {
		t.Fatalf("expected cache_control on last user block, got %#v", lastBlock["cache_control"])
	}
}

func TestCompleteSimpleKimiCodingOmitsCacheControlWhenDisabled(t *testing.T) {
	var requestBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request json: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_127",
					"usage": map[string]any{
						"input_tokens":                10,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "configured",
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 3,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	_ = CompleteSimple(*model, Context{
		SystemPrompt: "No cache.",
		Messages: []Message{
			UserMessage{Content: "hello"},
		},
	}, SimpleStreamOptions{
		APIKey:         "kimi-test-key",
		CacheRetention: CacheRetentionNone,
	})

	if len(requestBody.System) != 1 || requestBody.System[0].CacheControl != nil {
		t.Fatalf("expected no system cache control, got %+v", requestBody.System)
	}
	lastMessage := requestBody.Messages[len(requestBody.Messages)-1]
	if _, ok := lastMessage.Content.(string); !ok {
		t.Fatalf("expected plain string user content when cache is disabled, got %T", lastMessage.Content)
	}
}

func TestCompleteSimpleKimiCodingHostedWebSearchBuildsBuiltinFunctionAndAutoContinues(t *testing.T) {
	requestBodies := make([]map[string]any, 0, 2)
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var requestBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request json: %v", err)
		}
		requestBodies = append(requestBodies, requestBody)

		w.Header().Set("content-type", "text/event-stream")
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_web_1",
						"usage": map[string]any{
							"input_tokens":                21,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "call_web_1",
						"name":  "$web_search",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"query":"Moonshot AI Context Caching","usage":{"total_tokens":13046}}`,
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "tool_use",
					},
					"usage": map[string]any{
						"output_tokens": 11,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_web_2",
						"usage": map[string]any{
							"input_tokens":                13080,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type": "text_delta",
						"text": "Moonshot AI Context Caching is a prompt caching capability that reuses stable context across requests.",
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
					"usage": map[string]any{
						"output_tokens": 24,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := Complete(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Please search for Moonshot AI Context Caching and explain what it is."},
		},
		HostedTools: []HostedTool{{Type: HostedToolTypeWebSearch, Name: "web_search"}},
	}, ProviderStreamOptions{
		APIKey:    "kimi-test-key",
		Reasoning: ThinkingLevelHigh,
	})

	if requestCount != 2 {
		t.Fatalf("expected hosted web search to auto-continue in two requests, got %d", requestCount)
	}
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response after hosted web search continuation, got %+v", response)
	}
	text, ok := response.Content[0].(TextContent)
	if !ok || !strings.Contains(text.Text, "prompt caching") {
		t.Fatalf("expected final hosted web search answer text, got %+v", response.Content)
	}
	if len(response.HostedToolExecutions) != 1 {
		t.Fatalf("expected one hosted tool execution in response metadata, got %+v", response.HostedToolExecutions)
	}
	if response.HostedToolExecutions[0].Type != HostedToolTypeWebSearch || response.HostedToolExecutions[0].Name != "web_search" || response.HostedToolExecutions[0].ProviderToolName != "$web_search" {
		t.Fatalf("expected hosted web search execution metadata, got %+v", response.HostedToolExecutions[0])
	}
	if got := anyString(response.HostedToolExecutions[0].Arguments["query"]); got != "Moonshot AI Context Caching" {
		t.Fatalf("expected hosted web search query to be preserved, got %+v", response.HostedToolExecutions[0].Arguments)
	}
	if len(response.HostedToolExecutions[0].Result) != 0 {
		t.Fatalf("expected hosted execution result metadata to stay empty when provider does not return a real tool result payload, got %+v", response.HostedToolExecutions[0].Result)
	}

	firstTools, ok := requestBodies[0]["tools"].([]any)
	if !ok || len(firstTools) != 1 {
		t.Fatalf("expected one hosted tool declaration, got %#v", requestBodies[0]["tools"])
	}
	firstTool, ok := firstTools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected hosted tool declaration object, got %T", firstTools[0])
	}
	if firstTool["type"] != "builtin_function" {
		t.Fatalf("expected builtin_function tool declaration, got %#v", firstTool)
	}
	function, ok := firstTool["function"].(map[string]any)
	if !ok || function["name"] != "$web_search" {
		t.Fatalf("expected builtin function name $web_search, got %#v", firstTool)
	}
	thinking, ok := requestBodies[0]["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("expected hosted web search request to force thinking disabled, got %#v", requestBodies[0]["thinking"])
	}

	secondMessages, ok := requestBodies[1]["messages"].([]any)
	if !ok || len(secondMessages) != 3 {
		t.Fatalf("expected hosted continuation to append assistant + tool result messages, got %#v", requestBodies[1]["messages"])
	}
	toolMessage, ok := secondMessages[2].(map[string]any)
	if !ok || toolMessage["role"] != "user" {
		t.Fatalf("expected hosted tool result envelope as user message, got %#v", secondMessages[2])
	}
	toolBlocks, ok := toolMessage["content"].([]any)
	if !ok || len(toolBlocks) != 1 {
		t.Fatalf("expected one tool_result block in continuation request, got %#v", toolMessage["content"])
	}
	toolBlock, ok := toolBlocks[0].(map[string]any)
	if !ok || toolBlock["type"] != "tool_result" || toolBlock["tool_use_id"] != "call_web_1" {
		t.Fatalf("expected hosted tool_result echo block, got %#v", toolBlocks[0])
	}
	contentText, ok := toolBlock["content"].(string)
	if !ok || contentText != `{"query":"Moonshot AI Context Caching","usage":{"total_tokens":13046}}` {
		t.Fatalf("expected hosted tool_result continuation payload to carry serialized call arguments for provider-side execution, got %#v", toolBlock["content"])
	}
}

func TestStreamKimiCodingHostedWebSearchEmitsHostedToolLifecycle(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("content-type", "text/event-stream")
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_stream_hosted_1",
						"usage": map[string]any{
							"input_tokens":                18,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "call_stream_hosted_1",
						"name":  "$web_search",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"query":"Moonshot AI Context Caching"}`,
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "tool_use",
					},
					"usage": map[string]any{
						"output_tokens": 9,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_stream_hosted_2",
						"usage": map[string]any{
							"input_tokens":                100,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type": "text_delta",
						"text": "It reuses stable context across requests.",
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
					"usage": map[string]any{
						"output_tokens": 17,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	stream := Stream(*model, Context{
		Messages:    []Message{UserMessage{Content: "Please search for Moonshot AI Context Caching and summarize it."}},
		HostedTools: []HostedTool{{Type: HostedToolTypeWebSearch, Name: "web_search"}},
	}, ProviderStreamOptions{APIKey: "kimi-test-key"})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	result := stream.Result()

	if requestCount != 2 {
		t.Fatalf("expected hosted stream path to auto-continue in two requests, got %d", requestCount)
	}
	if len(events) < 7 {
		t.Fatalf("expected hosted stream path to emit tool lifecycle plus final text events, got %+v", events)
	}
	if events[0].Type != AssistantMessageEventStart {
		t.Fatalf("expected first hosted stream event to be start, got %+v", events[0])
	}
	if events[1].Type != AssistantMessageEventToolCallStart || events[2].Type != AssistantMessageEventToolCallDelta || events[3].Type != AssistantMessageEventToolCallEnd {
		t.Fatalf("expected hosted stream to expose tool call lifecycle before final text, got %+v", []AssistantMessageEventType{events[1].Type, events[2].Type, events[3].Type})
	}
	if events[3].ToolCall.Name != "web_search" || anyString(events[3].ToolCall.Arguments["query"]) != "Moonshot AI Context Caching" {
		t.Fatalf("expected hosted tool call event to preserve logical tool name and arguments, got %+v", events[3].ToolCall)
	}
	firstTextIndex := -1
	for index, event := range events {
		if event.Type == AssistantMessageEventTextStart {
			firstTextIndex = index
			break
		}
	}
	if firstTextIndex == -1 || firstTextIndex <= 3 {
		t.Fatalf("expected hosted tool lifecycle to be observable before final text, got %+v", events)
	}
	if events[len(events)-1].Type != AssistantMessageEventDone {
		t.Fatalf("expected final hosted stream event to be done, got %+v", events[len(events)-1])
	}
	if len(events[len(events)-1].Message.HostedToolExecutions) != 1 {
		t.Fatalf("expected done event to expose completed hosted execution metadata, got %+v", events[len(events)-1].Message)
	}
	if len(events[len(events)-1].Message.HostedToolExecutions[0].Result) != 0 {
		t.Fatalf("expected hosted stream completion metadata not to fake a tool result payload, got %+v", events[len(events)-1].Message.HostedToolExecutions[0])
	}
	if !strings.Contains(textFromContent(result.Content), "stable context") {
		t.Fatalf("expected hosted stream result text to survive, got %+v", result.Content)
	}
	if len(result.HostedToolExecutions) != 1 || anyString(result.HostedToolExecutions[0].Arguments["query"]) != "Moonshot AI Context Caching" {
		t.Fatalf("expected result to keep hosted execution metadata, got %+v", result.HostedToolExecutions)
	}
}

func TestStreamKimiCodingHostedWebSearchStreamsTextBeforeCompletion(t *testing.T) {
	requestCount := 0
	secondTextDeltaSent := make(chan struct{})
	releaseSecondResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("content-type", "text/event-stream")
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_stream_realtime_1",
						"usage": map[string]any{
							"input_tokens":                18,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "call_stream_realtime_1",
						"name":  "$web_search",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"query":"Moonshot AI Context Caching"}`,
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "tool_use",
					},
					"usage": map[string]any{
						"output_tokens": 9,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			writeAnthropicSSEEvent(t, w, map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_stream_realtime_2",
					"usage": map[string]any{
						"input_tokens":                100,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			})
			writeAnthropicSSEEvent(t, w, map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type": "text",
					"text": "",
				},
			})
			writeAnthropicSSEEvent(t, w, map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "It reuses stable context across requests.",
				},
			})
			close(secondTextDeltaSent)
			<-releaseSecondResponse
			writeAnthropicSSEEvent(t, w, map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			})
			writeAnthropicSSEEvent(t, w, map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 17,
				},
			})
			writeAnthropicSSEEvent(t, w, map[string]any{
				"type": "message_stop",
			})
			writeAnthropicSSEDone(t, w)
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	stream := Stream(*model, Context{
		Messages:    []Message{UserMessage{Content: "Please search for Moonshot AI Context Caching and summarize it."}},
		HostedTools: []HostedTool{{Type: HostedToolTypeWebSearch, Name: "web_search"}},
	}, ProviderStreamOptions{APIKey: "kimi-test-key"})

	var events []AssistantMessageEvent
	select {
	case <-secondTextDeltaSent:
	case <-time.After(2 * time.Second):
		t.Fatal("expected hosted continuation request to emit a text delta")
	}

	sawTextDelta := false
	for !sawTextDelta {
		select {
		case event, ok := <-stream.Events():
			if !ok {
				t.Fatalf("expected hosted stream to stay open until realtime text arrived, got %+v", events)
			}
			events = append(events, event)
			if event.Type == AssistantMessageEventDone || event.Type == AssistantMessageEventError {
				t.Fatalf("expected realtime hosted text before terminal event, got %+v", events)
			}
			if event.Type == AssistantMessageEventTextDelta {
				sawTextDelta = true
				if event.Delta != "It reuses stable context across requests." {
					t.Fatalf("expected realtime hosted text delta to match streamed content, got %+v", event)
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("expected realtime hosted text delta before completion, got %+v", events)
		}
	}

	close(releaseSecondResponse)
	for event := range stream.Events() {
		events = append(events, event)
	}
	result := stream.Result()

	if requestCount != 2 {
		t.Fatalf("expected hosted stream path to auto-continue in two requests, got %d", requestCount)
	}
	if result.StopReason != StopReasonStop {
		t.Fatalf("expected hosted stream result to complete successfully, got %+v", result)
	}
	if !strings.Contains(textFromContent(result.Content), "stable context") {
		t.Fatalf("expected hosted stream result text to survive, got %+v", result.Content)
	}
}

func TestCompleteSimpleKimiCodingHostedWebSearchDoesNotAffectGenericTools(t *testing.T) {
	var requestBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request json: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_generic_1",
					"usage": map[string]any{
						"input_tokens":                10,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]any{
					"type":  "tool_use",
					"id":    "call_generic_1",
					"name":  "lookup_catalog",
					"input": map[string]any{},
				},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": `{"sku":"sku-123"}`,
				},
			},
			map[string]any{
				"type":  "content_block_stop",
				"index": 0,
			},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "tool_use",
				},
				"usage": map[string]any{
					"output_tokens": 4,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Look up SKU 123 using the catalog tool."},
		},
		Tools: []Tool{{
			Name:        "lookup_catalog",
			Description: "Look up a catalog record",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sku": map[string]any{"type": "string"},
				},
			},
		}},
	}, SimpleStreamOptions{APIKey: "kimi-test-key"})

	if response.StopReason != StopReasonToolUse {
		t.Fatalf("expected generic tool path to remain unchanged, got %+v", response)
	}
	if len(requestBody.Tools) != 1 || requestBody.Tools[0].Name != "lookup_catalog" {
		t.Fatalf("expected generic tool declaration to remain anthropic-style, got %+v", requestBody.Tools)
	}
	if requestBody.Tools[0].Type != "" || requestBody.Tools[0].Function != nil {
		t.Fatalf("expected no hosted builtin declaration for generic tool path, got %+v", requestBody.Tools[0])
	}
}

func TestConvertAnthropicToolsDedupesHostedWebSearchAgainstGenericDeclaration(t *testing.T) {
	converted := convertAnthropicTools([]Tool{
		{
			Name:        "web_search",
			Description: "Fallback generic search tool",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
			},
		},
		{
			Name:        "lookup_catalog",
			Description: "Lookup catalog entries",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"sku": map[string]any{"type": "string"}},
			},
		},
	}, []HostedTool{{Type: HostedToolTypeWebSearch}}, false)

	if len(converted) != 2 {
		t.Fatalf("expected hosted declaration plus unrelated generic tool after dedupe, got %+v", converted)
	}
	if converted[0].Type != "builtin_function" || converted[0].Function == nil || converted[0].Function.Name != "$web_search" {
		t.Fatalf("expected first tool to be the hosted web_search builtin declaration, got %+v", converted[0])
	}
	if converted[1].Name != "lookup_catalog" {
		t.Fatalf("expected unrelated generic tool to remain after hosted dedupe, got %+v", converted)
	}
	for _, tool := range converted {
		if tool.Name == "web_search" {
			t.Fatalf("expected generic web_search declaration to be removed once hosted web_search is present, got %+v", converted)
		}
	}
}

func TestSupportedHostedToolsLeavesGenericFallbackWhenModelDoesNotSupportHostedTools(t *testing.T) {
	hosted := supportedHostedTools(Model{Provider: "openai-codex", ID: "gpt-5.4"}, []HostedTool{{Type: HostedToolTypeWebSearch}})
	if len(hosted) != 0 {
		t.Fatalf("expected unsupported model to filter out hosted tools, got %+v", hosted)
	}

	converted := convertAnthropicTools([]Tool{{
		Name:        "web_search",
		Description: "Fallback generic search tool",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
		},
	}}, hosted, false)

	if len(converted) != 1 || converted[0].Name != "web_search" || converted[0].Function != nil {
		t.Fatalf("expected generic web_search to remain when hosted support is unavailable, got %+v", converted)
	}
}

func TestCompleteSimpleKimiCodingHostedFetchBuildsBuiltinFunctionAndAutoContinues(t *testing.T) {
	requestBodies := make([]map[string]any, 0, 2)
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var requestBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request json: %v", err)
		}
		requestBodies = append(requestBodies, requestBody)

		w.Header().Set("content-type", "text/event-stream")
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_fetch_1",
						"usage": map[string]any{
							"input_tokens":                21,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "call_fetch_1",
						"name":  "$fetch",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"url":"https://platform.kimi.com/docs/guide/use-web-search"}`,
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "tool_use",
					},
					"usage": map[string]any{
						"output_tokens": 11,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_fetch_2",
						"usage": map[string]any{
							"input_tokens":                13080,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type": "text_delta",
						"text": "The Kimi web search documentation explains how to use the built-in $web_search function.",
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
					"usage": map[string]any{
						"output_tokens": 24,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := Complete(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Please fetch the Kimi web search documentation and summarize it."},
		},
		HostedTools: []HostedTool{{Type: HostedToolTypeFetch, Name: "fetch"}},
	}, ProviderStreamOptions{
		APIKey:    "kimi-test-key",
		Reasoning: ThinkingLevelHigh,
	})

	if requestCount != 2 {
		t.Fatalf("expected hosted fetch to auto-continue in two requests, got %d", requestCount)
	}
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response after hosted fetch continuation, got %+v", response)
	}
	text, ok := response.Content[0].(TextContent)
	if !ok || !strings.Contains(text.Text, "web search documentation") {
		t.Fatalf("expected final hosted fetch answer text, got %+v", response.Content)
	}
	if len(response.HostedToolExecutions) != 1 {
		t.Fatalf("expected one hosted tool execution in response metadata, got %+v", response.HostedToolExecutions)
	}
	if response.HostedToolExecutions[0].Type != HostedToolTypeFetch || response.HostedToolExecutions[0].Name != "fetch" || response.HostedToolExecutions[0].ProviderToolName != "$fetch" {
		t.Fatalf("expected hosted fetch execution metadata, got %+v", response.HostedToolExecutions[0])
	}
	if got := anyString(response.HostedToolExecutions[0].Arguments["url"]); got != "https://platform.kimi.com/docs/guide/use-web-search" {
		t.Fatalf("expected hosted fetch url to be preserved, got %+v", response.HostedToolExecutions[0].Arguments)
	}

	firstTools, ok := requestBodies[0]["tools"].([]any)
	if !ok || len(firstTools) != 1 {
		t.Fatalf("expected one hosted tool declaration, got %#v", requestBodies[0]["tools"])
	}
	firstTool, ok := firstTools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected hosted tool declaration object, got %T", firstTools[0])
	}
	if firstTool["type"] != "builtin_function" {
		t.Fatalf("expected builtin_function tool declaration, got %#v", firstTool)
	}
	function, ok := firstTool["function"].(map[string]any)
	if !ok || function["name"] != "$fetch" {
		t.Fatalf("expected builtin function name $fetch, got %#v", firstTool)
	}
	thinking, ok := requestBodies[0]["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("expected hosted fetch request to force thinking disabled, got %#v", requestBodies[0]["thinking"])
	}

	secondMessages, ok := requestBodies[1]["messages"].([]any)
	if !ok || len(secondMessages) != 3 {
		t.Fatalf("expected hosted continuation to append assistant + tool result messages, got %#v", requestBodies[1]["messages"])
	}
	toolMessage, ok := secondMessages[2].(map[string]any)
	if !ok || toolMessage["role"] != "user" {
		t.Fatalf("expected hosted tool result envelope as user message, got %#v", secondMessages[2])
	}
	toolBlocks, ok := toolMessage["content"].([]any)
	if !ok || len(toolBlocks) != 1 {
		t.Fatalf("expected one tool_result block in continuation request, got %#v", toolMessage["content"])
	}
	toolBlock, ok := toolBlocks[0].(map[string]any)
	if !ok || toolBlock["type"] != "tool_result" || toolBlock["tool_use_id"] != "call_fetch_1" {
		t.Fatalf("expected hosted tool_result echo block, got %#v", toolBlocks[0])
	}
	contentText, ok := toolBlock["content"].(string)
	if !ok || contentText != `{"url":"https://platform.kimi.com/docs/guide/use-web-search"}` {
		t.Fatalf("expected hosted tool_result continuation payload to carry serialized call arguments for provider-side execution, got %#v", toolBlock["content"])
	}
}

func TestCompleteSimpleKimiCodingHostedCodeRunnerBuildsBuiltinFunctionAndAutoContinues(t *testing.T) {
	requestBodies := make([]map[string]any, 0, 2)
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var requestBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request json: %v", err)
		}
		requestBodies = append(requestBodies, requestBody)

		w.Header().Set("content-type", "text/event-stream")
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_code_1",
						"usage": map[string]any{
							"input_tokens":                21,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "call_code_1",
						"name":  "$code_runner",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"code":"print(3**3)"}`,
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "tool_use",
					},
					"usage": map[string]any{
						"output_tokens": 11,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_code_2",
						"usage": map[string]any{
							"input_tokens":                13080,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type": "text_delta",
						"text": "The result of 3 raised to the power of 3 is 27.",
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
					"usage": map[string]any{
						"output_tokens": 24,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := Complete(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Please calculate 3^3 using the code runner."},
		},
		HostedTools: []HostedTool{{Type: HostedToolTypeCodeRunner, Name: "code_runner"}},
	}, ProviderStreamOptions{
		APIKey:    "kimi-test-key",
		Reasoning: ThinkingLevelHigh,
	})

	if requestCount != 2 {
		t.Fatalf("expected hosted code_runner to auto-continue in two requests, got %d", requestCount)
	}
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response after hosted code_runner continuation, got %+v", response)
	}
	text, ok := response.Content[0].(TextContent)
	if !ok || !strings.Contains(text.Text, "27") {
		t.Fatalf("expected final hosted code_runner answer text, got %+v", response.Content)
	}
	if len(response.HostedToolExecutions) != 1 {
		t.Fatalf("expected one hosted tool execution in response metadata, got %+v", response.HostedToolExecutions)
	}
	if response.HostedToolExecutions[0].Type != HostedToolTypeCodeRunner || response.HostedToolExecutions[0].Name != "code_runner" || response.HostedToolExecutions[0].ProviderToolName != "$code_runner" {
		t.Fatalf("expected hosted code_runner execution metadata, got %+v", response.HostedToolExecutions[0])
	}
	if got := anyString(response.HostedToolExecutions[0].Arguments["code"]); got != "print(3**3)" {
		t.Fatalf("expected hosted code_runner code to be preserved, got %+v", response.HostedToolExecutions[0].Arguments)
	}

	firstTools, ok := requestBodies[0]["tools"].([]any)
	if !ok || len(firstTools) != 1 {
		t.Fatalf("expected one hosted tool declaration, got %#v", requestBodies[0]["tools"])
	}
	firstTool, ok := firstTools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected hosted tool declaration object, got %T", firstTools[0])
	}
	if firstTool["type"] != "builtin_function" {
		t.Fatalf("expected builtin_function tool declaration, got %#v", firstTool)
	}
	function, ok := firstTool["function"].(map[string]any)
	if !ok || function["name"] != "$code_runner" {
		t.Fatalf("expected builtin function name $code_runner, got %#v", firstTool)
	}
}

func TestCompleteSimpleKimiCodingHostedExcelBuildsBuiltinFunctionAndAutoContinues(t *testing.T) {
	requestBodies := make([]map[string]any, 0, 2)
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var requestBody map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request json: %v", err)
		}
		requestBodies = append(requestBodies, requestBody)

		w.Header().Set("content-type", "text/event-stream")
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_excel_1",
						"usage": map[string]any{
							"input_tokens":                21,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "call_excel_1",
						"name":  "$excel",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"file_url":"https://example.com/data.csv","operation":"summarize"}`,
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "tool_use",
					},
					"usage": map[string]any{
						"output_tokens": 11,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_excel_2",
						"usage": map[string]any{
							"input_tokens":                13080,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type": "text_delta",
						"text": "The CSV contains 100 rows with sales data totaling $50000.",
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
					"usage": map[string]any{
						"output_tokens": 24,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := Complete(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Please analyze the sales CSV and summarize it."},
		},
		HostedTools: []HostedTool{{Type: HostedToolTypeExcel, Name: "excel"}},
	}, ProviderStreamOptions{
		APIKey:    "kimi-test-key",
		Reasoning: ThinkingLevelHigh,
	})

	if requestCount != 2 {
		t.Fatalf("expected hosted excel to auto-continue in two requests, got %d", requestCount)
	}
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response after hosted excel continuation, got %+v", response)
	}
	text, ok := response.Content[0].(TextContent)
	if !ok || !strings.Contains(text.Text, "sales data") {
		t.Fatalf("expected final hosted excel answer text, got %+v", response.Content)
	}
	if len(response.HostedToolExecutions) != 1 {
		t.Fatalf("expected one hosted tool execution in response metadata, got %+v", response.HostedToolExecutions)
	}
	if response.HostedToolExecutions[0].Type != HostedToolTypeExcel || response.HostedToolExecutions[0].Name != "excel" || response.HostedToolExecutions[0].ProviderToolName != "$excel" {
		t.Fatalf("expected hosted excel execution metadata, got %+v", response.HostedToolExecutions[0])
	}
	if got := anyString(response.HostedToolExecutions[0].Arguments["operation"]); got != "summarize" {
		t.Fatalf("expected hosted excel operation to be preserved, got %+v", response.HostedToolExecutions[0].Arguments)
	}

	firstTools, ok := requestBodies[0]["tools"].([]any)
	if !ok || len(firstTools) != 1 {
		t.Fatalf("expected one hosted tool declaration, got %#v", requestBodies[0]["tools"])
	}
	firstTool, ok := firstTools[0].(map[string]any)
	if !ok {
		t.Fatalf("expected hosted tool declaration object, got %T", firstTools[0])
	}
	if firstTool["type"] != "builtin_function" {
		t.Fatalf("expected builtin_function tool declaration, got %#v", firstTool)
	}
	function, ok := firstTool["function"].(map[string]any)
	if !ok || function["name"] != "$excel" {
		t.Fatalf("expected builtin function name $excel, got %#v", firstTool)
	}
}

func TestStreamKimiCodingHostedFetchEmitsHostedToolLifecycle(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("content-type", "text/event-stream")
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_stream_fetch_1",
						"usage": map[string]any{
							"input_tokens":                18,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "call_stream_fetch_1",
						"name":  "$fetch",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"url":"https://platform.kimi.com/docs"}`,
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "tool_use",
					},
					"usage": map[string]any{
						"output_tokens": 9,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_stream_fetch_2",
						"usage": map[string]any{
							"input_tokens":                100,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type": "text_delta",
						"text": "The documentation covers API usage and best practices.",
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
					"usage": map[string]any{
						"output_tokens": 17,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	stream := Stream(*model, Context{
		Messages:    []Message{UserMessage{Content: "Please fetch the Kimi documentation and summarize it."}},
		HostedTools: []HostedTool{{Type: HostedToolTypeFetch, Name: "fetch"}},
	}, ProviderStreamOptions{APIKey: "kimi-test-key"})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	result := stream.Result()

	if requestCount != 2 {
		t.Fatalf("expected hosted stream path to auto-continue in two requests, got %d", requestCount)
	}
	if len(events) < 7 {
		t.Fatalf("expected hosted stream path to emit tool lifecycle plus final text events, got %+v", events)
	}
	if events[0].Type != AssistantMessageEventStart {
		t.Fatalf("expected first hosted stream event to be start, got %+v", events[0])
	}
	if events[1].Type != AssistantMessageEventToolCallStart || events[2].Type != AssistantMessageEventToolCallDelta || events[3].Type != AssistantMessageEventToolCallEnd {
		t.Fatalf("expected hosted stream to expose tool call lifecycle before final text, got %+v", []AssistantMessageEventType{events[1].Type, events[2].Type, events[3].Type})
	}
	if events[3].ToolCall.Name != "fetch" || anyString(events[3].ToolCall.Arguments["url"]) != "https://platform.kimi.com/docs" {
		t.Fatalf("expected hosted tool call event to preserve logical tool name and arguments, got %+v", events[3].ToolCall)
	}
	if events[len(events)-1].Type != AssistantMessageEventDone {
		t.Fatalf("expected final hosted stream event to be done, got %+v", events[len(events)-1])
	}
	if len(events[len(events)-1].Message.HostedToolExecutions) != 1 {
		t.Fatalf("expected done event to expose completed hosted execution metadata, got %+v", events[len(events)-1].Message)
	}
	if !strings.Contains(textFromContent(result.Content), "API usage") {
		t.Fatalf("expected hosted stream result text to survive, got %+v", result.Content)
	}
	if len(result.HostedToolExecutions) != 1 || anyString(result.HostedToolExecutions[0].Arguments["url"]) != "https://platform.kimi.com/docs" {
		t.Fatalf("expected result to keep hosted execution metadata, got %+v", result.HostedToolExecutions)
	}
}

func TestStreamKimiCodingHostedCodeRunnerEmitsHostedToolLifecycle(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("content-type", "text/event-stream")
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_stream_code_1",
						"usage": map[string]any{
							"input_tokens":                18,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "call_stream_code_1",
						"name":  "$code_runner",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"code":"print('hello')"}`,
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "tool_use",
					},
					"usage": map[string]any{
						"output_tokens": 9,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_stream_code_2",
						"usage": map[string]any{
							"input_tokens":                100,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type": "text_delta",
						"text": "The code output is: hello",
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
					"usage": map[string]any{
						"output_tokens": 17,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	stream := Stream(*model, Context{
		Messages:    []Message{UserMessage{Content: "Please run some code."}},
		HostedTools: []HostedTool{{Type: HostedToolTypeCodeRunner, Name: "code_runner"}},
	}, ProviderStreamOptions{APIKey: "kimi-test-key"})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	result := stream.Result()

	if requestCount != 2 {
		t.Fatalf("expected hosted stream path to auto-continue in two requests, got %d", requestCount)
	}
	if len(events) < 7 {
		t.Fatalf("expected hosted stream path to emit tool lifecycle plus final text events, got %+v", events)
	}
	if events[3].ToolCall.Name != "code_runner" || anyString(events[3].ToolCall.Arguments["code"]) != "print('hello')" {
		t.Fatalf("expected hosted tool call event to preserve logical tool name and arguments, got %+v", events[3].ToolCall)
	}
	if events[len(events)-1].Type != AssistantMessageEventDone {
		t.Fatalf("expected final hosted stream event to be done, got %+v", events[len(events)-1])
	}
	if !strings.Contains(textFromContent(result.Content), "hello") {
		t.Fatalf("expected hosted stream result text to survive, got %+v", result.Content)
	}
	if len(result.HostedToolExecutions) != 1 || anyString(result.HostedToolExecutions[0].Arguments["code"]) != "print('hello')" {
		t.Fatalf("expected result to keep hosted execution metadata, got %+v", result.HostedToolExecutions)
	}
}

func TestStreamKimiCodingHostedExcelEmitsHostedToolLifecycle(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("content-type", "text/event-stream")
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_stream_excel_1",
						"usage": map[string]any{
							"input_tokens":                18,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    "call_stream_excel_1",
						"name":  "$excel",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"file_url":"https://example.com/sales.xlsx"}`,
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "tool_use",
					},
					"usage": map[string]any{
						"output_tokens": 9,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_stream_excel_2",
						"usage": map[string]any{
							"input_tokens":                100,
							"output_tokens":               0,
							"cache_read_input_tokens":     0,
							"cache_creation_input_tokens": 0,
						},
					},
				},
				map[string]any{
					"type":  "content_block_start",
					"index": 0,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type": "text_delta",
						"text": "The spreadsheet has 50 rows of product data.",
					},
				},
				map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				},
				map[string]any{
					"type": "message_delta",
					"delta": map[string]any{
						"stop_reason": "end_turn",
					},
					"usage": map[string]any{
						"output_tokens": 17,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "k2p5")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	stream := Stream(*model, Context{
		Messages:    []Message{UserMessage{Content: "Please analyze the spreadsheet."}},
		HostedTools: []HostedTool{{Type: HostedToolTypeExcel, Name: "excel"}},
	}, ProviderStreamOptions{APIKey: "kimi-test-key"})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	result := stream.Result()

	if requestCount != 2 {
		t.Fatalf("expected hosted stream path to auto-continue in two requests, got %d", requestCount)
	}
	if len(events) < 7 {
		t.Fatalf("expected hosted stream path to emit tool lifecycle plus final text events, got %+v", events)
	}
	if events[3].ToolCall.Name != "excel" || anyString(events[3].ToolCall.Arguments["file_url"]) != "https://example.com/sales.xlsx" {
		t.Fatalf("expected hosted tool call event to preserve logical tool name and arguments, got %+v", events[3].ToolCall)
	}
	if events[len(events)-1].Type != AssistantMessageEventDone {
		t.Fatalf("expected final hosted stream event to be done, got %+v", events[len(events)-1])
	}
	if !strings.Contains(textFromContent(result.Content), "product data") {
		t.Fatalf("expected hosted stream result text to survive, got %+v", result.Content)
	}
	if len(result.HostedToolExecutions) != 1 || anyString(result.HostedToolExecutions[0].Arguments["file_url"]) != "https://example.com/sales.xlsx" {
		t.Fatalf("expected result to keep hosted execution metadata, got %+v", result.HostedToolExecutions)
	}
}

func TestConvertAnthropicToolsDedupesHostedFetchAgainstGenericDeclaration(t *testing.T) {
	converted := convertAnthropicTools([]Tool{
		{
			Name:        "fetch",
			Description: "Fallback generic fetch tool",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"url": map[string]any{"type": "string"}},
			},
		},
		{
			Name:        "lookup_catalog",
			Description: "Lookup catalog entries",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"sku": map[string]any{"type": "string"}},
			},
		},
	}, []HostedTool{{Type: HostedToolTypeFetch}}, false)

	if len(converted) != 2 {
		t.Fatalf("expected hosted declaration plus unrelated generic tool after dedupe, got %+v", converted)
	}
	if converted[0].Type != "builtin_function" || converted[0].Function == nil || converted[0].Function.Name != "$fetch" {
		t.Fatalf("expected first tool to be the hosted fetch builtin declaration, got %+v", converted[0])
	}
	if converted[1].Name != "lookup_catalog" {
		t.Fatalf("expected unrelated generic tool to remain after hosted dedupe, got %+v", converted)
	}
	for _, tool := range converted {
		if tool.Name == "fetch" {
			t.Fatalf("expected generic fetch declaration to be removed once hosted fetch is present, got %+v", converted)
		}
	}
}

func TestConvertAnthropicToolsDedupesHostedCodeRunnerAgainstGenericDeclaration(t *testing.T) {
	converted := convertAnthropicTools([]Tool{
		{
			Name:        "code_runner",
			Description: "Fallback generic code runner",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"code": map[string]any{"type": "string"}},
			},
		},
		{
			Name:        "lookup_catalog",
			Description: "Lookup catalog entries",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"sku": map[string]any{"type": "string"}},
			},
		},
	}, []HostedTool{{Type: HostedToolTypeCodeRunner}}, false)

	if len(converted) != 2 {
		t.Fatalf("expected hosted declaration plus unrelated generic tool after dedupe, got %+v", converted)
	}
	if converted[0].Type != "builtin_function" || converted[0].Function == nil || converted[0].Function.Name != "$code_runner" {
		t.Fatalf("expected first tool to be the hosted code_runner builtin declaration, got %+v", converted[0])
	}
	if converted[1].Name != "lookup_catalog" {
		t.Fatalf("expected unrelated generic tool to remain after hosted dedupe, got %+v", converted)
	}
	for _, tool := range converted {
		if tool.Name == "code_runner" {
			t.Fatalf("expected generic code_runner declaration to be removed once hosted code_runner is present, got %+v", converted)
		}
	}
}

func TestConvertAnthropicToolsDedupesHostedExcelAgainstGenericDeclaration(t *testing.T) {
	converted := convertAnthropicTools([]Tool{
		{
			Name:        "excel",
			Description: "Fallback generic excel tool",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"file_url": map[string]any{"type": "string"}},
			},
		},
		{
			Name:        "lookup_catalog",
			Description: "Lookup catalog entries",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"sku": map[string]any{"type": "string"}},
			},
		},
	}, []HostedTool{{Type: HostedToolTypeExcel}}, false)

	if len(converted) != 2 {
		t.Fatalf("expected hosted declaration plus unrelated generic tool after dedupe, got %+v", converted)
	}
	if converted[0].Type != "builtin_function" || converted[0].Function == nil || converted[0].Function.Name != "$excel" {
		t.Fatalf("expected first tool to be the hosted excel builtin declaration, got %+v", converted[0])
	}
	if converted[1].Name != "lookup_catalog" {
		t.Fatalf("expected unrelated generic tool to remain after hosted dedupe, got %+v", converted)
	}
	for _, tool := range converted {
		if tool.Name == "excel" {
			t.Fatalf("expected generic excel declaration to be removed once hosted excel is present, got %+v", converted)
		}
	}
}

func textFromContent(blocks []ContentBlock) string {
	var builder strings.Builder
	for _, block := range blocks {
		text, ok := block.(TextContent)
		if !ok {
			continue
		}
		builder.WriteString(text.Text)
	}
	return builder.String()
}

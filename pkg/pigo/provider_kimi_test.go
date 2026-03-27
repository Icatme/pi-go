package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

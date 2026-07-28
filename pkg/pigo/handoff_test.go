package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCrossProviderHandoffKimiToCodexBuildsReplaySafePayload(t *testing.T) {
	var requestBody openAIResponsesRequest
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request body: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_1",
					"content": []map[string]any{
						{"type": "output_text", "text": "42"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_handoff_1",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  12,
						"output_tokens": 1,
						"total_tokens":  13,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
						},
					},
				},
			},
		)))
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.2-codex")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Answer concisely.",
		Messages: []Message{
			UserMessage{Content: "Use the tool to double 21."},
			AssistantMessage{
				Content: []ContentBlock{
					ThinkingContent{
						Thinking:          "I will use the tool.",
						ThinkingSignature: "signed-thinking",
					},
					ToolCall{
						ID:        "toolu_123",
						Name:      "double_number",
						Arguments: map[string]any{"value": 21},
					},
				},
				API:        "anthropic-messages",
				Provider:   "kimi-coding",
				Model:      "kimi-k2-thinking",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: "toolu_123",
				ToolName:   "double_number",
				Content:    []ContentBlock{TextContent{Text: "42"}},
			},
			UserMessage{Content: "What was the result? Reply with just the number."},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %q", response.StopReason)
	}
	if len(requestBody.Input) != 5 {
		t.Fatalf("expected 5 input items in codex handoff request, got %d", len(requestBody.Input))
	}

	assistantText := requestBody.Input[1]
	if assistantText["type"] != "message" {
		t.Fatalf("expected assistant message item, got %#v", assistantText)
	}
	contentParts, ok := assistantText["content"].([]any)
	if !ok || len(contentParts) != 1 {
		t.Fatalf("expected one assistant content part, got %#v", assistantText["content"])
	}
	part, ok := contentParts[0].(map[string]any)
	if !ok || part["text"] != "I will use the tool." {
		t.Fatalf("expected thinking to replay as assistant text, got %#v", assistantText["content"])
	}

	functionCall := requestBody.Input[2]
	if functionCall["type"] != "function_call" || functionCall["call_id"] != "toolu_123" {
		t.Fatalf("expected anthropic tool call to replay as function_call, got %#v", functionCall)
	}
	if _, exists := functionCall["id"]; exists {
		t.Fatalf("expected non-pipe anthropic handoff to omit item id, got %#v", functionCall["id"])
	}

	functionResult := requestBody.Input[3]
	if functionResult["type"] != "function_call_output" || functionResult["call_id"] != "toolu_123" {
		t.Fatalf("expected function_call_output for tool result, got %#v", functionResult)
	}
}

func TestCrossProviderHandoffCodexToKimiBuildsReplaySafePayload(t *testing.T) {
	var requestBody anthropicRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("expected valid request body: %v", err)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_handoff_2",
					"usage": map[string]any{
						"input_tokens":                20,
						"output_tokens":               0,
						"cache_read_input_tokens":     0,
						"cache_creation_input_tokens": 0,
					},
				},
			},
			map[string]any{
				"type":          "content_block_start",
				"index":         0,
				"content_block": map[string]any{"type": "text", "text": ""},
			},
			map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "text_delta", "text": "42"},
			},
			map[string]any{"type": "content_block_stop", "index": 0},
			map[string]any{
				"type":  "message_delta",
				"delta": map[string]any{"stop_reason": "end_turn"},
				"usage": map[string]any{"output_tokens": 1},
			},
			map[string]any{"type": "message_stop"},
		)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Answer concisely.",
		Messages: []Message{
			UserMessage{Content: "Use the tool to double 21."},
			AssistantMessage{
				Content: []ContentBlock{
					ThinkingContent{
						Thinking:          "I will use the tool.",
						ThinkingSignature: `{"type":"reasoning","summary":[{"type":"summary_text","text":"I will use the tool."}]}`,
					},
					ToolCall{
						ID:        copilotRawToolCallID,
						Name:      "double_number",
						Arguments: map[string]any{"value": 21},
					},
				},
				API:        "openai-codex-responses",
				Provider:   "openai-codex",
				Model:      "gpt-5.4",
				StopReason: StopReasonToolUse,
			},
			ToolResultMessage{
				ToolCallID: copilotRawToolCallID,
				ToolName:   "double_number",
				Content:    []ContentBlock{TextContent{Text: "42"}},
			},
			UserMessage{Content: "What was the result? Reply with just the number."},
		},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected stop response, got %q", response.StopReason)
	}
	if len(requestBody.Messages) != 4 {
		t.Fatalf("expected 4 anthropic messages in handoff request, got %d", len(requestBody.Messages))
	}

	assistantContent, ok := requestBody.Messages[1].Content.([]any)
	if !ok || len(assistantContent) != 2 {
		t.Fatalf("expected assistant handoff content blocks, got %#v", requestBody.Messages[1].Content)
	}

	textBlock, ok := assistantContent[0].(map[string]any)
	if !ok || textBlock["text"] != "I will use the tool." {
		t.Fatalf("expected codex thinking to replay as plain text, got %#v", assistantContent[0])
	}
	toolUse, ok := assistantContent[1].(map[string]any)
	if !ok {
		t.Fatalf("expected tool_use block, got %#v", assistantContent[1])
	}
	toolUseID, _ := toolUse["id"].(string)
	if strings.ContainsAny(toolUseID, "|+/= ") {
		t.Fatalf("expected normalized anthropic-safe tool id, got %q", toolUseID)
	}

	toolResultBlocks, ok := requestBody.Messages[2].Content.([]any)
	if !ok || len(toolResultBlocks) != 1 {
		t.Fatalf("expected tool_result block, got %#v", requestBody.Messages[2].Content)
	}
	toolResult, ok := toolResultBlocks[0].(map[string]any)
	if !ok || toolResult["tool_use_id"] != toolUseID {
		t.Fatalf("expected tool result to reference normalized tool id %q, got %#v", toolUseID, toolResult)
	}
}

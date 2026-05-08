package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompleteOpenAICodexDifferentModelReplayDropsRawReasoningButKeepsToolHistory(t *testing.T) {
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
					"id":   "msg_replay",
					"content": []map[string]any{
						{"type": "output_text", "text": "42"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_replay",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  6,
						"output_tokens": 1,
						"total_tokens":  7,
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
			UserMessage{Content: "Use the tool."},
			AssistantMessage{
				Content: []ContentBlock{
					ThinkingContent{
						Thinking:          "I should call the tool.",
						ThinkingSignature: `{"type":"reasoning","summary":[{"type":"summary_text","text":"I should call the tool."}]}`,
					},
					ToolCall{
						ID:        "call_test|fc_test",
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
				ToolCallID: "call_test|fc_test",
				ToolName:   "double_number",
				Content:    []ContentBlock{TextContent{Text: "42"}},
			},
			UserMessage{Content: "What was the result?"},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected successful replay response, got %+v", response)
	}

	var (
		sawAssistantText  bool
		sawFunctionCall   bool
		sawFunctionOutput bool
	)
	for _, item := range requestBody.Input {
		switch item["type"] {
		case "message":
			sawAssistantText = true
		case "function_call":
			sawFunctionCall = true
			if _, exists := item["id"]; exists {
				t.Fatalf("expected replayed function_call to omit item id, got %#v", item["id"])
			}
		case "function_call_output":
			sawFunctionOutput = true
		case "reasoning":
			t.Fatalf("expected different-model replay to avoid raw reasoning items, got %#v", item)
		}
	}

	if !sawAssistantText || !sawFunctionCall || !sawFunctionOutput {
		t.Fatalf("expected replay payload to keep text, function_call, and function_call_output, got %#v", requestBody.Input)
	}
}

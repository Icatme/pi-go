package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompleteSimpleOpenAICodexFiltersToolCallWithoutResultOnFollowup(t *testing.T) {
	var requestCount int
	token := makeOpenAICodexToken("acc_test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("content-type", "text/event-stream")

		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildOpenAICodexSSE(
				map[string]any{
					"type": "response.output_item.done",
					"item": map[string]any{
						"type":      "function_call",
						"id":        "fc_calc_1",
						"call_id":   "call_calc_1",
						"name":      "calculate",
						"arguments": `{"expression":"25 * 18"}`,
					},
				},
				map[string]any{
					"type": "response.completed",
					"response": map[string]any{
						"id":     "resp_tool_1",
						"status": "completed",
						"usage": map[string]any{
							"input_tokens":  8,
							"output_tokens": 3,
							"total_tokens":  11,
							"input_tokens_details": map[string]any{
								"cached_tokens": 0,
							},
						},
					},
				},
			)))
		case 2:
			var requestBody openAIResponsesRequest
			if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
				t.Fatalf("expected valid second request body: %v", err)
			}
			if len(requestBody.Input) != 4 {
				t.Fatalf("expected orphan tool call repair to produce 4 input items, got %#v", requestBody.Input)
			}
			if requestBody.Input[1]["type"] != "function_call" {
				t.Fatalf("expected function_call history item, got %#v", requestBody.Input[1])
			}
			if requestBody.Input[2]["type"] != "function_call_output" {
				t.Fatalf("expected synthetic function_call_output item, got %#v", requestBody.Input[2])
			}
			if requestBody.Input[2]["call_id"] != "call_calc_1" {
				t.Fatalf("expected synthetic tool result for call_calc_1, got %#v", requestBody.Input[2])
			}

			_, _ = w.Write([]byte(buildOpenAICodexSSE(
				map[string]any{
					"type": "response.output_item.done",
					"item": map[string]any{
						"type": "message",
						"id":   "msg_answer",
						"content": []map[string]any{
							{"type": "output_text", "text": "4"},
						},
					},
				},
				map[string]any{
					"type": "response.completed",
					"response": map[string]any{
						"id":     "resp_tool_2",
						"status": "completed",
						"usage": map[string]any{
							"input_tokens":  9,
							"output_tokens": 1,
							"total_tokens":  10,
							"input_tokens_details": map[string]any{
								"cached_tokens": 0,
							},
						},
					},
				},
			)))
		default:
			t.Fatalf("unexpected request count %d", requestCount)
		}
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.2-codex")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	context := Context{
		SystemPrompt: "Use the calculate tool when asked to perform calculations.",
		Tools: []Tool{
			{
				Name:        "calculate",
				Description: "Evaluate mathematical expressions",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"expression": map[string]any{"type": "string"},
					},
					"required": []string{"expression"},
				},
			},
		},
		Messages: []Message{
			UserMessage{Content: "Please calculate 25 * 18 using the calculate tool."},
		},
	}

	first := CompleteSimple(*model, context, SimpleStreamOptions{APIKey: token})
	if first.StopReason != StopReasonToolUse {
		t.Fatalf("expected first response to request tool use, got %+v", first)
	}

	context.Messages = append(context.Messages, first)
	context.Messages = append(context.Messages, UserMessage{Content: "Never mind, just tell me what is 2+2?"})

	second := CompleteSimple(*model, context, SimpleStreamOptions{APIKey: token})
	if second.StopReason != StopReasonStop {
		t.Fatalf("expected second response to succeed after orphan tool repair, got %+v", second)
	}
	text, ok := second.Content[0].(TextContent)
	if !ok || text.Text != "4" {
		t.Fatalf("expected textual followup answer, got %+v", second.Content)
	}
}

func TestCompleteSimpleKimiCodingFiltersToolCallWithoutResultOnFollowup(t *testing.T) {
	var requestCount int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("content-type", "text/event-stream")

		switch requestCount {
		case 1:
			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_tool_1",
						"usage": map[string]any{
							"input_tokens":                12,
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
						"id":    "call_calc_1",
						"name":  "calculate",
						"input": map[string]any{},
					},
				},
				map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": `{"expression":"25 * 18"}`,
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
						"output_tokens": 5,
					},
				},
				map[string]any{
					"type": "message_stop",
				},
			)))
		case 2:
			var requestBody anthropicRequest
			if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
				t.Fatalf("expected valid second request body: %v", err)
			}
			if len(requestBody.Messages) != 4 {
				t.Fatalf("expected orphan tool call repair to produce 4 messages, got %#v", requestBody.Messages)
			}
			toolResultBlocks, ok := requestBody.Messages[2].Content.([]any)
			if !ok || len(toolResultBlocks) != 1 {
				t.Fatalf("expected synthetic tool result block message, got %#v", requestBody.Messages[2].Content)
			}
			block, ok := toolResultBlocks[0].(map[string]any)
			if !ok || block["type"] != "tool_result" {
				t.Fatalf("expected tool_result block, got %#v", requestBody.Messages[2].Content)
			}
			if block["tool_use_id"] != "call_calc_1" {
				t.Fatalf("expected synthetic tool result for call_calc_1, got %#v", block)
			}

			_, _ = w.Write([]byte(buildAnthropicSSE(
				map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id": "msg_tool_2",
						"usage": map[string]any{
							"input_tokens":                14,
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
						"text": "4",
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
						"output_tokens": 1,
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

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	context := Context{
		SystemPrompt: "Use the calculate tool when asked to perform calculations.",
		Tools: []Tool{
			{
				Name:        "calculate",
				Description: "Evaluate mathematical expressions",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"expression": map[string]any{"type": "string"},
					},
					"required": []string{"expression"},
				},
			},
		},
		Messages: []Message{
			UserMessage{Content: "Please calculate 25 * 18 using the calculate tool."},
		},
	}

	first := CompleteSimple(*model, context, SimpleStreamOptions{APIKey: "kimi-test-key"})
	if first.StopReason != StopReasonToolUse {
		t.Fatalf("expected first response to request tool use, got %+v", first)
	}

	context.Messages = append(context.Messages, first)
	context.Messages = append(context.Messages, UserMessage{Content: "Never mind, just tell me what is 2+2?"})

	second := CompleteSimple(*model, context, SimpleStreamOptions{APIKey: "kimi-test-key"})
	if second.StopReason != StopReasonStop {
		t.Fatalf("expected second response to succeed after orphan tool repair, got %+v", second)
	}
	text, ok := second.Content[0].(TextContent)
	if !ok || text.Text != "4" {
		t.Fatalf("expected textual followup answer, got %+v", second.Content)
	}
}

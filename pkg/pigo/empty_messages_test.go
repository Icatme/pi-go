package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConvertOpenAICodexMessagesSkipsEmptyStringAndWhitespaceUserMessages(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}

	input := convertOpenAICodexMessages(*model, Context{
		Messages: []Message{
			UserMessage{Content: ""},
			UserMessage{Content: "   \n\t  "},
			UserMessage{Content: "respond this time"},
		},
	})

	if len(input) != 1 {
		t.Fatalf("expected only one preserved user input item, got %#v", input)
	}
	if input[0]["role"] != "user" {
		t.Fatalf("expected preserved item to be a user message, got %#v", input[0])
	}
}

func TestConvertAnthropicMessagesSkipsEmptyStringAndWhitespaceUserMessages(t *testing.T) {
	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	messages := convertAnthropicMessages([]Message{
		UserMessage{Content: ""},
		UserMessage{Content: "   \n\t  "},
		UserMessage{Content: "respond this time"},
	}, *model)

	if len(messages) != 1 {
		t.Fatalf("expected only one preserved user message, got %#v", messages)
	}
	if messages[0].Role != "user" || messages[0].Content != "respond this time" {
		t.Fatalf("expected preserved followup user message, got %+v", messages[0])
	}
}

func TestCompleteSimpleOpenAICodexHandlesEmptyAssistantMessageInHistory(t *testing.T) {
	var requestBody openAICodexRequest
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
					"id":   "msg_empty_history",
					"content": []map[string]any{
						{"type": "output_text", "text": "fresh answer"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_empty_history",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  4,
						"output_tokens": 2,
						"total_tokens":  6,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
						},
					},
				},
			},
		)))
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "Hello"},
			AssistantMessage{
				Content:    nil,
				API:        model.API,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: StopReasonStop,
			},
			UserMessage{Content: "Please respond this time."},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected successful response, got %+v", response)
	}
	if len(requestBody.Input) != 2 {
		t.Fatalf("expected empty assistant turn to be skipped, got %#v", requestBody.Input)
	}
}

func TestCompleteSimpleKimiCodingHandlesEmptyAssistantMessageInHistory(t *testing.T) {
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
					"id": "msg_empty_history",
					"usage": map[string]any{
						"input_tokens":                5,
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
					"text": "fresh answer",
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
					"output_tokens": 2,
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
			UserMessage{Content: "Hello"},
			AssistantMessage{
				Content:    nil,
				API:        model.API,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: StopReasonStop,
			},
			UserMessage{Content: "Please respond this time."},
		},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected successful response, got %+v", response)
	}
	if len(requestBody.Messages) != 2 {
		t.Fatalf("expected empty assistant turn to be skipped, got %#v", requestBody.Messages)
	}
}


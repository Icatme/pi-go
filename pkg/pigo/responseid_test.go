package pigo

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompleteSimpleKimiCodingExposesResponseID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_resp_1",
					"usage": map[string]any{
						"input_tokens":                1,
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
					"text": "ok",
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
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	response := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	if response.ResponseID != "msg_resp_1" {
		t.Fatalf("expected response id msg_resp_1, got %q", response.ResponseID)
	}
}

func TestCompleteSimpleOpenAICodexExposesResponseID(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"content": []map[string]any{
						{"type": "output_text", "text": "ok"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_abc123",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  1,
						"output_tokens": 1,
						"total_tokens":  2,
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
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if response.ResponseID != "resp_abc123" {
		t.Fatalf("expected response id resp_abc123, got %q", response.ResponseID)
	}
}


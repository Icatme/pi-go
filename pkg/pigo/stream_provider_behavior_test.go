package pigo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStreamSimpleOpenAICodexReturnsAfterTerminalEventBeforeBodyCloses(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"content": []map[string]any{
						{"type": "output_text", "text": "hello"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_terminal",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  2,
						"output_tokens": 1,
						"total_tokens":  3,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
						},
					},
				},
			},
		)))
		flusher.Flush()
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	done := make(chan AssistantMessage, 1)
	go func() {
		done <- stream.Result()
	}()

	select {
	case result := <-done:
		if result.StopReason != StopReasonStop {
			t.Fatalf("expected stop result, got %+v", result)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("timed out waiting for codex stream result after terminal event")
	}
}

func TestStreamSimpleKimiCodingReturnsAfterTerminalEventBeforeBodyCloses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_terminal",
					"usage": map[string]any{
						"input_tokens":                2,
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
					"text": "hello",
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
		flusher.Flush()
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	done := make(chan AssistantMessage, 1)
	go func() {
		done <- stream.Result()
	}()

	select {
	case result := <-done:
		if result.StopReason != StopReasonStop {
			t.Fatalf("expected stop result, got %+v", result)
		}
	case <-time.After(750 * time.Millisecond):
		t.Fatal("timed out waiting for kimi stream result after terminal event")
	}
}

func TestCompleteSimpleOpenAICodexDoesNotDeadlockWhenEventsBackUp(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		events := make([]map[string]any, 0, 1503)
		events = append(events,
			map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{"type": "message", "id": "msg_backpressure"},
			},
			map[string]any{
				"type": "response.content_part.added",
				"part": map[string]any{"type": "output_text", "text": ""},
			},
		)
		for i := 0; i < 1500; i++ {
			events = append(events, map[string]any{
				"type":  "response.output_text.delta",
				"delta": "x",
			})
		}
		events = append(events,
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_backpressure",
					"content": []map[string]any{
						{"type": "output_text", "text": strings.Repeat("x", 1500)},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_backpressure",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  2,
						"output_tokens": 1500,
						"total_tokens":  1502,
						"input_tokens_details": map[string]any{
							"cached_tokens": 0,
						},
					},
				},
			},
		)
		_, _ = w.Write([]byte(buildOpenAICodexSSE(events...)))
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	done := make(chan AssistantMessage, 1)
	go func() {
		done <- CompleteSimple(*model, Context{
			Messages: []Message{UserMessage{Content: "hi"}},
		}, SimpleStreamOptions{APIKey: token})
	}()

	select {
	case result := <-done:
		if result.StopReason != StopReasonStop {
			t.Fatalf("expected stop result, got %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for codex complete result under event backpressure")
	}
}

func TestCompleteSimpleKimiCodingDoesNotDeadlockWhenEventsBackUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		events := make([]map[string]any, 0, 1504)
		events = append(events,
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_backpressure",
					"usage": map[string]any{
						"input_tokens":                2,
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
		)
		for i := 0; i < 1500; i++ {
			events = append(events, map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": "x",
				},
			})
		}
		events = append(events,
			map[string]any{"type": "content_block_stop", "index": 0},
			map[string]any{
				"type": "message_delta",
				"delta": map[string]any{
					"stop_reason": "end_turn",
				},
				"usage": map[string]any{
					"output_tokens": 1500,
				},
			},
			map[string]any{"type": "message_stop"},
		)
		_, _ = w.Write([]byte(buildAnthropicSSE(events...)))
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	done := make(chan AssistantMessage, 1)
	go func() {
		done <- CompleteSimple(*model, Context{
			Messages: []Message{UserMessage{Content: "hi"}},
		}, SimpleStreamOptions{APIKey: "kimi-test-key"})
	}()

	select {
	case result := <-done:
		if result.StopReason != StopReasonStop {
			t.Fatalf("expected stop result, got %+v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for kimi complete result under event backpressure")
	}
}

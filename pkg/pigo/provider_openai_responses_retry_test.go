package pigo

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestShouldRetryOpenAIResponsesRequestBufferLimit(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{
			name:    "upstream request buffer limit",
			message: "exceeded request buffer limit while retrying upstream",
			want:    true,
		},
		{
			name:    "case insensitive",
			message: "Exceeded Request Buffer Limit While Retrying Upstream",
			want:    true,
		},
		{
			name:    "different request buffer failure",
			message: "exceeded request buffer limit for client quota",
			want:    false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldRetryOpenAIResponsesRequest(400, test.message); got != test.want {
				t.Fatalf("shouldRetryOpenAIResponsesRequest(400, %q) = %t, want %t", test.message, got, test.want)
			}
		})
	}
}

func TestOpenAIResponsesRetriesRequestBufferFailureFromSSEBeforeOutput(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		if attempts.Add(1) == 1 {
			_, _ = w.Write([]byte(buildOpenAICodexSSE(map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"id":     "resp_buffer_failed",
					"status": "failed",
					"error": map[string]any{
						"type":    "server_error",
						"message": "exceeded request buffer limit while retrying upstream",
					},
				},
			})))
			return
		}
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_buffer_retried",
					"content": []map[string]any{
						{"type": "output_text", "text": "retried ok"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_buffer_retried",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens": 1, "output_tokens": 1, "total_tokens": 2,
					},
				},
			},
		)))
	}))
	defer server.Close()

	model := GetModel("openai", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai model")
	}
	model.BaseURL = server.URL
	stream := StreamSimple(*model, Context{Messages: []Message{UserMessage{Content: "retry"}}}, SimpleStreamOptions{
		APIKey:        "test-key",
		MaxRetries:    1,
		MaxRetryDelay: 1,
	})
	var startCount, errorCount int
	for event := range stream.Events() {
		switch event.Type {
		case AssistantMessageEventStart:
			startCount++
		case AssistantMessageEventError:
			errorCount++
		}
	}
	response := stream.Result()
	if attempts.Load() != 2 || startCount != 1 || errorCount != 0 {
		t.Fatalf("unexpected retry lifecycle: attempts=%d starts=%d errors=%d", attempts.Load(), startCount, errorCount)
	}
	if response.StopReason != StopReasonStop || len(response.Content) != 1 {
		t.Fatalf("expected successful retry result, got %+v", response)
	}
	if text, ok := response.Content[0].(TextContent); !ok || text.Text != "retried ok" {
		t.Fatalf("unexpected retried content: %#v", response.Content)
	}
}

func TestOpenAIResponsesDoesNotRetryRequestBufferFailureAfterOutput(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_partial",
					"content": []map[string]any{
						{"type": "output_text", "text": "partial"},
					},
				},
			},
			map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"id":     "resp_buffer_after_output",
					"status": "failed",
					"error": map[string]any{
						"type":    "server_error",
						"message": "exceeded request buffer limit while retrying upstream",
					},
				},
			},
		)))
	}))
	defer server.Close()

	model := GetModel("openai", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai model")
	}
	model.BaseURL = server.URL
	response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "do not duplicate"}}}, SimpleStreamOptions{
		APIKey:        "test-key",
		MaxRetries:    1,
		MaxRetryDelay: 1,
	})
	if attempts.Load() != 1 {
		t.Fatalf("stream with visible output must not retry, attempts=%d", attempts.Load())
	}
	if response.StopReason != StopReasonError || response.ErrorMessage != "exceeded request buffer limit while retrying upstream" {
		t.Fatalf("expected terminal stream error, got %+v", response)
	}
}

func TestOpenAICompletionsRetriesRequestBufferFailureFromSSEBeforeOutput(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		if attempts.Add(1) == 1 {
			_, _ = w.Write([]byte("data: {\"error\":{\"message\":\"exceeded request buffer limit while retrying upstream\"}}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-retried\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"retried ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := Model{
		API:           "openai-completions",
		Provider:      "openrouter",
		ID:            "test-model",
		BaseURL:       server.URL,
		ContextWindow: 4096,
		MaxTokens:     256,
	}
	stream := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "retry"}}}, SimpleStreamOptions{
		APIKey:        "test-key",
		MaxRetries:    1,
		MaxRetryDelay: 1,
	})
	var startCount, errorCount int
	for event := range stream.Events() {
		switch event.Type {
		case AssistantMessageEventStart:
			startCount++
		case AssistantMessageEventError:
			errorCount++
		}
	}
	response := stream.Result()
	if attempts.Load() != 2 || startCount != 1 || errorCount != 0 {
		t.Fatalf("unexpected retry lifecycle: attempts=%d starts=%d errors=%d", attempts.Load(), startCount, errorCount)
	}
	if response.StopReason != StopReasonStop || len(response.Content) != 1 {
		t.Fatalf("expected successful retry result, got %+v", response)
	}
	if text, ok := response.Content[0].(TextContent); !ok || text.Text != "retried ok" {
		t.Fatalf("unexpected retried content: %#v", response.Content)
	}
}

func TestOpenAICodexRetriesRequestBufferFailureFromSSEBeforeOutput(t *testing.T) {
	previousRetryCount := openAICodexRetryCount
	previousRetryDelay := openAICodexBaseRetryDelay
	openAICodexRetryCount = 1
	openAICodexBaseRetryDelay = time.Millisecond
	t.Cleanup(func() {
		openAICodexRetryCount = previousRetryCount
		openAICodexBaseRetryDelay = previousRetryDelay
	})

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		if attempts.Add(1) == 1 {
			_, _ = w.Write([]byte(buildOpenAICodexSSE(map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"id":     "resp_codex_buffer_failed",
					"status": "failed",
					"error": map[string]any{
						"type":    "server_error",
						"message": "exceeded request buffer limit while retrying upstream",
					},
				},
			})))
			return
		}
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_codex_buffer_retried",
					"content": []map[string]any{
						{"type": "output_text", "text": "retried ok"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_codex_buffer_retried",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens": 1, "output_tokens": 1, "total_tokens": 2,
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
	stream := StreamSimple(*model, Context{Messages: []Message{UserMessage{Content: "retry"}}}, SimpleStreamOptions{
		APIKey:        makeOpenAICodexToken("acc_test"),
		MaxRetryDelay: 1,
	})
	var startCount, errorCount int
	for event := range stream.Events() {
		switch event.Type {
		case AssistantMessageEventStart:
			startCount++
		case AssistantMessageEventError:
			errorCount++
		}
	}
	response := stream.Result()
	if attempts.Load() != 2 || startCount != 1 || errorCount != 0 {
		t.Fatalf("unexpected retry lifecycle: attempts=%d starts=%d errors=%d", attempts.Load(), startCount, errorCount)
	}
	if response.StopReason != StopReasonStop || response.ResponseID != "resp_codex_buffer_retried" {
		t.Fatalf("expected successful retry result, got %+v", response)
	}
}

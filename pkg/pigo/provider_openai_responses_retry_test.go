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

func TestOpenAIProviderRetryClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		message   string
		wantRetry bool
	}{
		{
			name:      "transient rate limit",
			status:    http.StatusTooManyRequests,
			body:      `{"error":{"type":"rate_limit_exceeded","message":"You have hit your usage limit."}}`,
			message:   "You have hit your usage limit.",
			wantRetry: true,
		},
		{
			name:    "OpenCode Go usage limit",
			status:  http.StatusTooManyRequests,
			body:    `{"error":{"type":"GoUsageLimitError","message":"monthly limit reached"}}`,
			message: "monthly limit reached",
		},
		{
			name:    "free usage limit",
			status:  http.StatusTooManyRequests,
			body:    `{"error":{"type":"FreeUsageLimitError","message":"free usage limit reached"}}`,
			message: "free usage limit reached",
		},
		{
			name:    "quota exhausted",
			status:  http.StatusTooManyRequests,
			body:    `{"error":{"code":"insufficient_quota","message":"quota exceeded"}}`,
			message: "quota exceeded",
		},
		{
			name:    "billing required",
			status:  http.StatusTooManyRequests,
			body:    `{"error":{"code":"billing_not_active","message":"billing is not active"}}`,
			message: "billing is not active",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := shouldRetryOpenAIProviderResponse(test.status, []byte(test.body), test.message)
			if got != test.wantRetry {
				t.Fatalf("shouldRetryOpenAIProviderResponse() = %t, want %t", got, test.wantRetry)
			}
		})
	}
}

func TestOpenAIResponsesMaxRetriesZeroDoesNotRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"temporary upstream failure"}}`))
	}))
	defer server.Close()

	model := GetModel("openai", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai model")
	}
	model.BaseURL = server.URL
	response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "do not retry"}}}, SimpleStreamOptions{
		APIKey:     "test-key",
		MaxRetries: 0,
	})
	if attempts.Load() != 1 {
		t.Fatalf("MaxRetries=0 must make exactly one attempt, got %d", attempts.Load())
	}
	if response.StopReason != StopReasonError {
		t.Fatalf("expected terminal error without retry, got %+v", response)
	}
}

func TestOpenAIResponsesDoesNotRetryUsageLimit429(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"GoUsageLimitError","message":"monthly limit reached"}}`))
	}))
	defer server.Close()

	model := GetModel("openai", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai model")
	}
	model.BaseURL = server.URL
	response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "quota"}}}, SimpleStreamOptions{
		APIKey:        "test-key",
		MaxRetries:    2,
		MaxRetryDelay: 1,
	})
	if attempts.Load() != 1 {
		t.Fatalf("usage-limit 429 must not retry, got %d attempts", attempts.Load())
	}
	if response.StopReason != StopReasonError {
		t.Fatalf("expected usage-limit error, got %+v", response)
	}
}

func TestOpenAICompletionsDoesNotRetryQuota429(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"insufficient_quota","type":"insufficient_quota","message":"quota exceeded"}}`))
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
	response := CompleteSimple(model, Context{Messages: []Message{UserMessage{Content: "quota"}}}, SimpleStreamOptions{
		APIKey:        "test-key",
		MaxRetries:    2,
		MaxRetryDelay: 1,
	})
	if attempts.Load() != 1 {
		t.Fatalf("quota 429 must not retry, got %d attempts", attempts.Load())
	}
	if response.StopReason != StopReasonError {
		t.Fatalf("expected quota error, got %+v", response)
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

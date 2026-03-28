package pigo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompleteSimpleOpenAICodexImmediateAbort(t *testing.T) {
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "hello"},
		},
	}, SimpleStreamOptions{
		APIKey:         makeOpenAICodexToken("acc_test"),
		RequestContext: requestContext,
	})

	if response.StopReason != StopReasonAborted {
		t.Fatalf("expected aborted response, got %+v", response)
	}
}

func TestCompleteSimpleKimiCodingImmediateAbort(t *testing.T) {
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	response := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "hello"},
		},
	}, SimpleStreamOptions{
		APIKey:         "kimi-test-key",
		RequestContext: requestContext,
	})

	if response.StopReason != StopReasonAborted {
		t.Fatalf("expected aborted response, got %+v", response)
	}
}

func TestStreamSimpleOpenAICodexAbortedRequestKeepsZeroUsage(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected response flusher")
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\"}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"type\":\"response.content_part.added\",\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := StreamSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "hello"},
		},
	}, SimpleStreamOptions{
		APIKey:         token,
		RequestContext: requestContext,
	})

	for event := range stream.Events() {
		if event.Type == AssistantMessageEventTextDelta {
			cancel()
		}
	}

	result := stream.Result()
	if result.StopReason != StopReasonAborted {
		t.Fatalf("expected aborted result, got %+v", result)
	}
	if result.Usage.Input != 0 || result.Usage.Output != 0 || result.Usage.TotalTokens != 0 {
		t.Fatalf("expected zero usage on codex abort, got %+v", result.Usage)
	}
}

func TestStreamSimpleKimiCodingAbortedRequestKeepsInputUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected response flusher")
		}
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_abort_1\",\"usage\":{\"input_tokens\":12,\"output_tokens\":0,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":0}}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"))
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := StreamSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "hello"},
		},
	}, SimpleStreamOptions{
		APIKey:         "kimi-test-key",
		RequestContext: requestContext,
	})

	for event := range stream.Events() {
		if event.Type == AssistantMessageEventTextDelta {
			cancel()
		}
	}

	result := stream.Result()
	if result.StopReason != StopReasonAborted {
		t.Fatalf("expected aborted result, got %+v", result)
	}
	if result.Usage.Input != 12 {
		t.Fatalf("expected kimi abort to preserve input usage, got %+v", result.Usage)
	}
	if result.Usage.Output != 0 {
		t.Fatalf("expected no output usage on kimi abort, got %+v", result.Usage)
	}
}

func TestAbortedAssistantMessageCanBeReplayedOnFollowup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected response flusher")
		}

		select {
		case <-r.Context().Done():
			return
		default:
		}

		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_followup",
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
					"output_tokens": 3,
				},
			},
			map[string]any{
				"type": "message_stop",
			},
		)))
		flusher.Flush()
	}))
	defer server.Close()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	aborted := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "start"},
		},
	}, SimpleStreamOptions{
		APIKey:         "kimi-test-key",
		RequestContext: requestContext,
	})
	if aborted.StopReason != StopReasonAborted {
		t.Fatalf("expected aborted first response, got %+v", aborted)
	}

	followup := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "start"},
			aborted,
			UserMessage{Content: "continue"},
		},
	}, SimpleStreamOptions{
		APIKey: "kimi-test-key",
	})

	if followup.StopReason != StopReasonStop {
		t.Fatalf("expected successful followup after aborted replay, got %+v", followup)
	}
}


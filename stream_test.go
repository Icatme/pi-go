package pigo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestStreamSimpleKimiCodingEmitsTextLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildAnthropicSSE(
			map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id": "msg_stream_1",
					"usage": map[string]any{
						"input_tokens":                3,
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
					"output_tokens": 4,
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

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, CompleteOptions{
		APIKey: "kimi-test-key",
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	result := stream.Result()
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}
	if events[0].Type != AssistantMessageEventStart {
		t.Fatalf("expected start event, got %q", events[0].Type)
	}
	if events[1].Type != AssistantMessageEventTextStart || events[2].Type != AssistantMessageEventTextDelta || events[3].Type != AssistantMessageEventTextEnd {
		t.Fatalf("expected text lifecycle events, got %+v", []AssistantMessageEventType{events[1].Type, events[2].Type, events[3].Type})
	}
	if events[2].Delta != "hello from kimi" || events[3].Content != "hello from kimi" {
		t.Fatalf("expected text delta/end content to match, got delta=%q content=%q", events[2].Delta, events[3].Content)
	}
	if events[4].Type != AssistantMessageEventDone || events[4].Reason != StopReasonStop {
		t.Fatalf("expected done stop event, got %+v", events[4])
	}
	if events[4].Message.ResponseID != "msg_stream_1" {
		t.Fatalf("expected response id on done event, got %q", events[4].Message.ResponseID)
	}
	if !reflect.DeepEqual(events[4].Message, result) {
		t.Fatalf("expected done event message to equal result, got done=%+v result=%+v", events[4].Message, result)
	}
}

func TestStreamSimpleOpenAICodexEmitsThinkingAndToolCallLifecycle(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type": "reasoning",
					"id":   "rs_1",
				},
			},
			map[string]any{
				"type":  "response.reasoning_summary_text.delta",
				"delta": "first think, then act",
			},
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "reasoning",
					"id":   "rs_1",
					"summary": []map[string]any{
						{"type": "summary_text", "text": "first think, then act"},
					},
				},
			},
			map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type":    "function_call",
					"id":      "fc_1",
					"call_id": "call_1",
					"name":    "edit_file",
				},
			},
			map[string]any{
				"type":  "response.function_call_arguments.delta",
				"delta": `{"path":"README.md",`,
			},
			map[string]any{
				"type":      "response.function_call_arguments.done",
				"arguments": `{"path":"README.md","content":"hi"}`,
			},
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type":      "function_call",
					"id":        "fc_1",
					"call_id":   "call_1",
					"name":      "edit_file",
					"arguments": `{"path":"README.md","content":"hi"}`,
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_stream_1",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  10,
						"output_tokens": 8,
						"total_tokens":  18,
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

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "edit readme"}},
	}, CompleteOptions{
		APIKey: token,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	result := stream.Result()
	expected := []AssistantMessageEventType{
		AssistantMessageEventStart,
		AssistantMessageEventThinkingStart,
		AssistantMessageEventThinkingDelta,
		AssistantMessageEventThinkingEnd,
		AssistantMessageEventToolCallStart,
		AssistantMessageEventToolCallDelta,
		AssistantMessageEventToolCallEnd,
		AssistantMessageEventDone,
	}
	if len(events) != len(expected) {
		t.Fatalf("expected %d events, got %d", len(expected), len(events))
	}
	for index, event := range events {
		if event.Type != expected[index] {
			t.Fatalf("expected event %d to be %q, got %q", index, expected[index], event.Type)
		}
	}
	if events[2].Delta != "first think, then act" {
		t.Fatalf("expected thinking delta, got %q", events[2].Delta)
	}
	if events[5].Delta != `{"path":"README.md",` {
		t.Fatalf("expected incremental tool call delta json, got %q", events[5].Delta)
	}
	if events[6].ToolCall.ID != "call_1|fc_1" {
		t.Fatalf("expected tool call id to include item id, got %q", events[6].ToolCall.ID)
	}
	if events[7].Reason != StopReasonToolUse {
		t.Fatalf("expected done reason toolUse, got %q", events[7].Reason)
	}
	if result.ResponseID != "resp_stream_1" {
		t.Fatalf("expected response id resp_stream_1, got %q", result.ResponseID)
	}
}

func TestStreamSimpleOpenAICodexEmitsTextLifecycleFromSSEDelta(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_1",
				},
			},
			map[string]any{
				"type": "response.content_part.added",
				"part": map[string]any{
					"type": "output_text",
					"text": "",
				},
			},
			map[string]any{
				"type":  "response.output_text.delta",
				"delta": "hello from codex",
			},
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_1",
					"content": []map[string]any{
						{"type": "output_text", "text": "hello from codex"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_text_1",
					"status": "completed",
					"usage": map[string]any{
						"input_tokens":  4,
						"output_tokens": 3,
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

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}
	model.BaseURL = server.URL

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, CompleteOptions{
		APIKey: token,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}
	if events[1].Type != AssistantMessageEventTextStart || events[2].Type != AssistantMessageEventTextDelta || events[3].Type != AssistantMessageEventTextEnd {
		t.Fatalf("expected text lifecycle events, got %+v", []AssistantMessageEventType{events[1].Type, events[2].Type, events[3].Type})
	}
	if events[2].Delta != "hello from codex" {
		t.Fatalf("expected text delta, got %q", events[2].Delta)
	}
}

func TestStreamSimpleEmitsAbortedErrorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("did not expect request to reach server after context cancellation")
	}))
	defer server.Close()

	requestContext, cancel := context.WithCancel(context.Background())
	cancel()

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}
	model.BaseURL = server.URL

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, CompleteOptions{
		APIKey:         "kimi-test-key",
		RequestContext: requestContext,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	result := stream.Result()
	if len(events) != 1 {
		t.Fatalf("expected single aborted error event, got %d", len(events))
	}
	if events[0].Type != AssistantMessageEventError || events[0].Reason != StopReasonAborted {
		t.Fatalf("expected aborted error event, got %+v", events[0])
	}
	if result.StopReason != StopReasonAborted {
		t.Fatalf("expected aborted result, got %+v", result)
	}
}

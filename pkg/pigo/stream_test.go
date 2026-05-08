package pigo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAssistantMessageEventStreamResultDoesNotDependOnEventConsumption(t *testing.T) {
	stream := newAssistantMessageEventStream()
	done := make(chan struct{})

	go func() {
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart})
		for i := 0; i < 1500; i++ {
			stream.push(AssistantMessageEvent{
				Type:  AssistantMessageEventTextDelta,
				Delta: strings.Repeat("x", 8),
			})
		}
		stream.push(AssistantMessageEvent{
			Type:    AssistantMessageEventDone,
			Reason:  StopReasonStop,
			Message: AssistantMessage{StopReason: StopReasonStop},
		})
		stream.finish(AssistantMessage{
			Provider:   "test",
			Model:      "fake",
			StopReason: StopReasonStop,
			Content: []ContentBlock{
				TextContent{Text: "ok"},
			},
		})
		close(done)
	}()

	result := stream.Result()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected producer to finish without event consumption")
	}
	if result.StopReason != StopReasonStop {
		t.Fatalf("expected stop result, got %+v", result)
	}
	text, _ := result.Content[0].(TextContent)
	if text.Text != "ok" {
		t.Fatalf("expected final content to survive backpressure, got %+v", result)
	}
}

func TestAssistantMessageEventStreamReportsDroppedDeltaEvents(t *testing.T) {
	stream := newAssistantMessageEventStream()
	done := make(chan struct{})

	go func() {
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart})
		for i := 0; i < 3000; i++ {
			stream.push(AssistantMessageEvent{
				Type:  AssistantMessageEventTextDelta,
				Delta: "x",
			})
		}
		stream.push(AssistantMessageEvent{
			Type:         AssistantMessageEventTextEnd,
			ContentIndex: 0,
			Content:      "done",
		})
		stream.push(AssistantMessageEvent{
			Type:    AssistantMessageEventDone,
			Reason:  StopReasonStop,
			Message: AssistantMessage{StopReason: StopReasonStop},
		})
		stream.finish(AssistantMessage{StopReason: StopReasonStop})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected producer to finish before events are consumed")
	}

	var (
		events         []AssistantMessageEvent
		sawDropped     bool
		textDeltaCount int
	)
	for event := range stream.Events() {
		events = append(events, event)
		if event.DroppedEvents > 0 {
			sawDropped = true
		}
		if event.Type == AssistantMessageEventTextDelta {
			textDeltaCount++
		}
	}
	if !sawDropped {
		t.Fatal("expected dropped delta count to be reported on a later event")
	}
	if textDeltaCount >= 3000 {
		t.Fatalf("expected some delta events to be dropped, got %d", textDeltaCount)
	}
	if len(events) == 0 || events[len(events)-1].Type != AssistantMessageEventDone {
		t.Fatalf("expected done event to survive backpressure, got %+v", events)
	}
}

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
	}, SimpleStreamOptions{
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
	}, SimpleStreamOptions{
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
	if events[6].Delta != `"content":"hi"}` {
		t.Fatalf("expected final tool call delta json, got %q", events[6].Delta)
	}
	if events[7].ToolCall.ID != "call_1|fc_1" {
		t.Fatalf("expected tool call id to include item id, got %q", events[7].ToolCall.ID)
	}
	if events[8].Reason != StopReasonToolUse {
		t.Fatalf("expected done reason toolUse, got %q", events[8].Reason)
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
	}, SimpleStreamOptions{
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

func TestStreamSimpleOpenAICodexEmitsLifecycleFromTerminalOnlyOutput(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.created",
				"response": map[string]any{
					"id": "resp_terminal_only",
				},
			},
			map[string]any{
				"type": "response.done",
				"response": map[string]any{
					"status": "completed",
					"output": []map[string]any{
						{
							"type": "reasoning",
							"id":   "rs_1",
							"summary": []map[string]any{
								{"type": "summary_text", "text": "plan first"},
							},
						},
						{
							"type": "message",
							"id":   "msg_1",
							"content": []map[string]any{
								{"type": "output_text", "text": "hello"},
							},
						},
						{
							"type":      "function_call",
							"id":        "fc_9",
							"call_id":   "call_9",
							"name":      "edit_file",
							"arguments": `{"path":"README.md"}`,
						},
					},
					"usage": map[string]any{
						"input_tokens":  6,
						"output_tokens": 3,
						"total_tokens":  9,
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
	}, SimpleStreamOptions{
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
		AssistantMessageEventTextStart,
		AssistantMessageEventTextDelta,
		AssistantMessageEventTextEnd,
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
	if events[2].Delta != "plan first" {
		t.Fatalf("expected terminal thinking delta, got %q", events[2].Delta)
	}
	if events[5].Delta != "hello" {
		t.Fatalf("expected terminal text delta from message output_text, got %q", events[5].Delta)
	}
	if events[6].Content != "hello" {
		t.Fatalf("expected text end content to reflect output_text block, got %q", events[6].Content)
	}
	if events[8].Delta != `{"path":"README.md"}` {
		t.Fatalf("expected terminal tool delta, got %q", events[8].Delta)
	}
	if events[9].ToolCall.ID != "call_9|fc_9" {
		t.Fatalf("expected terminal tool call id, got %q", events[9].ToolCall.ID)
	}
	if result.ResponseID != "resp_terminal_only" {
		t.Fatalf("expected response id from response.created, got %q", result.ResponseID)
	}
	if result.StopReason != StopReasonToolUse {
		t.Fatalf("expected toolUse stop reason, got %q", result.StopReason)
	}
}

func TestStreamSimpleOpenAICodexFailedEventPreservesResponseID(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.failed",
				"response": map[string]any{
					"id":     "resp_failed_1",
					"status": "failed",
					"error": map[string]any{
						"type":    "server_error",
						"message": "backend exploded",
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
	}, SimpleStreamOptions{
		APIKey: token,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	if len(events) != 2 {
		t.Fatalf("expected start + error events, got %d", len(events))
	}
	if events[1].Type != AssistantMessageEventError {
		t.Fatalf("expected error event, got %q", events[1].Type)
	}
	if events[1].Error.ResponseID != "resp_failed_1" {
		t.Fatalf("expected failed response id to be preserved, got %q", events[1].Error.ResponseID)
	}
}

func TestStreamSimpleOpenAICodexEmitsRefusalLifecycle(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_refusal",
				},
			},
			map[string]any{
				"type": "response.content_part.added",
				"part": map[string]any{
					"type":    "refusal",
					"refusal": "",
				},
			},
			map[string]any{
				"type":  "response.refusal.delta",
				"delta": "cannot comply",
			},
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_refusal",
					"content": []map[string]any{
						{"type": "refusal", "refusal": "cannot comply"},
					},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_refusal_stream",
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

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	if len(events) != 5 {
		t.Fatalf("expected 5 refusal events, got %d", len(events))
	}
	if events[1].Type != AssistantMessageEventTextStart || events[2].Type != AssistantMessageEventTextDelta || events[3].Type != AssistantMessageEventTextEnd {
		t.Fatalf("expected refusal to reuse text lifecycle, got %+v", []AssistantMessageEventType{events[1].Type, events[2].Type, events[3].Type})
	}
	if events[2].Delta != "cannot comply" || events[3].Content != "cannot comply" {
		t.Fatalf("expected refusal delta/content to match, got delta=%q content=%q", events[2].Delta, events[3].Content)
	}
}

func TestStreamSimpleOpenAICodexFinalizesOpenMessageFromTerminalOutput(t *testing.T) {
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
				"delta": "hello",
			},
			map[string]any{
				"type": "response.done",
				"response": map[string]any{
					"id":     "resp_finish_open",
					"status": "completed",
					"output": []map[string]any{
						{
							"type": "message",
							"id":   "msg_1",
							"content": []map[string]any{
								{"type": "output_text", "text": "hello"},
							},
						},
					},
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

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}
	if events[3].Type != AssistantMessageEventTextEnd {
		t.Fatalf("expected terminal to finalize open text block, got %q", events[3].Type)
	}
	if events[3].Content != "hello" {
		t.Fatalf("expected finalized text content, got %q", events[3].Content)
	}
}

func TestStreamSimpleOpenAICodexAppendsMissingTerminalItems(t *testing.T) {
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
				"delta": "hello",
			},
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_1",
					"content": []map[string]any{
						{"type": "output_text", "text": "hello"},
					},
				},
			},
			map[string]any{
				"type": "response.done",
				"response": map[string]any{
					"id":     "resp_append_terminal",
					"status": "completed",
					"output": []map[string]any{
						{
							"type": "message",
							"id":   "msg_1",
							"content": []map[string]any{
								{"type": "output_text", "text": "hello"},
							},
						},
						{
							"type":      "function_call",
							"id":        "fc_2",
							"call_id":   "call_2",
							"name":      "edit_file",
							"arguments": `{"path":"README.md"}`,
						},
					},
					"usage": map[string]any{
						"input_tokens":  5,
						"output_tokens": 3,
						"total_tokens":  8,
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
	}, SimpleStreamOptions{
		APIKey: token,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	result := stream.Result()
	if len(result.Content) != 2 {
		t.Fatalf("expected terminal output to append missing tool call, got %+v", result.Content)
	}
	call, ok := result.Content[1].(ToolCall)
	if !ok || call.ID != "call_2|fc_2" {
		t.Fatalf("expected appended terminal tool call, got %#v", result.Content[1])
	}
	if len(events) != 8 {
		t.Fatalf("expected 8 events including terminal tool lifecycle, got %d", len(events))
	}
	if events[4].Type != AssistantMessageEventToolCallStart || events[5].Type != AssistantMessageEventToolCallDelta || events[6].Type != AssistantMessageEventToolCallEnd {
		t.Fatalf("expected terminal tool lifecycle events, got %+v", []AssistantMessageEventType{events[4].Type, events[5].Type, events[6].Type})
	}
}

func TestStreamSimpleOpenAICodexCombinesTerminalMessagePartsIntoSingleLifecycle(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.done",
				"response": map[string]any{
					"id":     "resp_multi_message",
					"status": "completed",
					"output": []map[string]any{
						{
							"type": "message",
							"id":   "msg_multi",
							"content": []map[string]any{
								{"type": "output_text", "text": "hello"},
								{"type": "output_text", "text": " there"},
								{"type": "refusal", "refusal": "!"},
							},
						},
					},
					"usage": map[string]any{
						"input_tokens":  5,
						"output_tokens": 3,
						"total_tokens":  8,
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
	}, SimpleStreamOptions{
		APIKey: token,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	result := stream.Result()
	if len(events) != 5 {
		t.Fatalf("expected single message lifecycle plus done, got %d events", len(events))
	}
	if events[1].Type != AssistantMessageEventTextStart || events[2].Type != AssistantMessageEventTextDelta || events[3].Type != AssistantMessageEventTextEnd {
		t.Fatalf("expected single text lifecycle, got %+v", []AssistantMessageEventType{events[1].Type, events[2].Type, events[3].Type})
	}
	if events[2].Delta != "hello there!" || events[3].Content != "hello there!" {
		t.Fatalf("expected combined terminal message text, got delta=%q content=%q", events[2].Delta, events[3].Content)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected single text content block, got %+v", result.Content)
	}
	text, ok := result.Content[0].(TextContent)
	if !ok || text.Text != "hello there!" {
		t.Fatalf("expected combined result text, got %#v", result.Content[0])
	}
	if !reflect.DeepEqual(events[4].Message, result) {
		t.Fatalf("expected done event message to equal result, got done=%+v result=%+v", events[4].Message, result)
	}
}

func TestStreamSimpleOpenAICodexTopLevelErrorPreservesCreatedResponseID(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.created",
				"response": map[string]any{
					"id": "resp_error_created",
				},
			},
			map[string]any{
				"type":    "error",
				"message": "transport broke",
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
	}, SimpleStreamOptions{
		APIKey: token,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	if len(events) != 2 {
		t.Fatalf("expected start + error events, got %d", len(events))
	}
	if events[1].Type != AssistantMessageEventError {
		t.Fatalf("expected error event, got %q", events[1].Type)
	}
	if events[1].Error.ResponseID != "resp_error_created" {
		t.Fatalf("expected created response id to survive error, got %q", events[1].Error.ResponseID)
	}
}

func TestStreamSimpleOpenAICodexPreservesReasoningDeltaWhenDoneHasNoSummary(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type": "reasoning",
					"id":   "rs_empty_summary",
				},
			},
			map[string]any{
				"type":  "response.reasoning_summary_text.delta",
				"delta": "draft reasoning",
			},
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type":    "reasoning",
					"id":      "rs_empty_summary",
					"summary": []map[string]any{},
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_reasoning_preserve",
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

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}

	result := stream.Result()
	if events[2].Type != AssistantMessageEventThinkingDelta || events[2].Delta != "draft reasoning" {
		t.Fatalf("expected reasoning delta event, got %+v", events[2])
	}
	if events[3].Type != AssistantMessageEventThinkingEnd || events[3].Content != "draft reasoning" {
		t.Fatalf("expected reasoning end to preserve accumulated delta, got %+v", events[3])
	}
	thinking, ok := result.Content[0].(ThinkingContent)
	if !ok || thinking.Thinking != "draft reasoning" {
		t.Fatalf("expected result to preserve reasoning delta, got %#v", result.Content[0])
	}
}

func TestStreamSimpleOpenAICodexTerminalToolArgumentsOverridePartialJSON(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(buildOpenAICodexSSE(
			map[string]any{
				"type": "response.output_item.added",
				"item": map[string]any{
					"type":    "function_call",
					"id":      "fc_override",
					"call_id": "call_override",
					"name":    "edit_file",
				},
			},
			map[string]any{
				"type":  "response.function_call_arguments.delta",
				"delta": `{"path":"REA`,
			},
			map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type":      "function_call",
					"id":        "fc_override",
					"call_id":   "call_override",
					"name":      "edit_file",
					"arguments": `{"path":"README.md","content":"ok"}`,
				},
			},
			map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_tool_override",
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

	stream := StreamSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	for range stream.Events() {
	}
	result := stream.Result()
	call, ok := result.Content[0].(ToolCall)
	if !ok {
		t.Fatalf("expected tool call result, got %#v", result.Content)
	}
	if call.Arguments["path"] != "README.md" || call.Arguments["content"] != "ok" {
		t.Fatalf("expected terminal arguments to override partial json, got %+v", call.Arguments)
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
	}, SimpleStreamOptions{
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

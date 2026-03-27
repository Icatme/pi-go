package pigo

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

type openAICodexFixtureResult struct {
	Events         []AssistantMessageEvent
	StreamResult   AssistantMessage
	CompleteResult AssistantMessage
}

func runOpenAICodexFixture(t *testing.T, modelID string, sseBody string) openAICodexFixtureResult {
	t.Helper()

	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(sseBody))
	}))
	defer server.Close()

	model := GetModel("openai-codex", modelID)
	if model == nil {
		t.Fatalf("expected codex model %q", modelID)
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

	streamResult := stream.Result()
	completeResult := CompleteSimple(*model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	return openAICodexFixtureResult{
		Events:         events,
		StreamResult:   normalizeAssistantForComparison(streamResult),
		CompleteResult: normalizeAssistantForComparison(completeResult),
	}
}

func normalizeAssistantForComparison(message AssistantMessage) AssistantMessage {
	message.Timestamp = time.Time{}
	return message
}

func TestOpenAICodexFixtureKeepsStreamAndCompleteConsistentForMixedTerminalOutput(t *testing.T) {
	fixture := runOpenAICodexFixture(t, "gpt-5.4", buildOpenAICodexSSE(
		map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id": "resp_fixture_mixed",
			},
		},
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
						"type": "message",
						"id":   "msg_2",
						"content": []map[string]any{
							{"type": "output_text", "text": " world"},
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
					"input_tokens":  6,
					"output_tokens": 4,
					"total_tokens":  10,
					"input_tokens_details": map[string]any{
						"cached_tokens": 1,
					},
				},
			},
		},
	))

	if !reflect.DeepEqual(fixture.StreamResult, fixture.CompleteResult) {
		t.Fatalf("expected stream result and complete result to match, got stream=%+v complete=%+v", fixture.StreamResult, fixture.CompleteResult)
	}
	if len(fixture.StreamResult.Content) != 3 {
		t.Fatalf("expected mixed fixture to produce 3 content blocks, got %+v", fixture.StreamResult.Content)
	}
}

func TestOpenAICodexFixtureKeepsStreamAndCompleteConsistentForMalformedTerminalResponse(t *testing.T) {
	fixture := runOpenAICodexFixture(t, "gpt-5.4", buildOpenAICodexSSE(
		map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id": "resp_fixture_error",
			},
		},
		map[string]any{
			"type": "response.completed",
		},
	))

	if !reflect.DeepEqual(fixture.StreamResult, fixture.CompleteResult) {
		t.Fatalf("expected malformed fixture stream/complete parity, got stream=%+v complete=%+v", fixture.StreamResult, fixture.CompleteResult)
	}
	if fixture.StreamResult.StopReason != StopReasonError {
		t.Fatalf("expected malformed fixture to fail, got %+v", fixture.StreamResult)
	}
	if fixture.StreamResult.ResponseID != "resp_fixture_error" {
		t.Fatalf("expected created response id to survive malformed terminal response, got %q", fixture.StreamResult.ResponseID)
	}
}

func TestOpenAICodexFixtureKeepsStreamAndCompleteConsistentForIncompleteResponse(t *testing.T) {
	fixture := runOpenAICodexFixture(t, "gpt-5.4", buildOpenAICodexSSE(
		map[string]any{
			"type": "response.output_item.done",
			"item": map[string]any{
				"type": "message",
				"id":   "msg_incomplete",
				"content": []map[string]any{
					{"type": "output_text", "text": "partial answer"},
				},
			},
		},
		map[string]any{
			"type": "response.incomplete",
			"response": map[string]any{
				"id":     "resp_fixture_incomplete",
				"status": "incomplete",
				"usage": map[string]any{
					"input_tokens":  5,
					"output_tokens": 2,
					"total_tokens":  7,
					"input_tokens_details": map[string]any{
						"cached_tokens": 0,
					},
				},
			},
		},
	))

	if !reflect.DeepEqual(fixture.StreamResult, fixture.CompleteResult) {
		t.Fatalf("expected incomplete fixture stream/complete parity, got stream=%+v complete=%+v", fixture.StreamResult, fixture.CompleteResult)
	}
	if fixture.StreamResult.StopReason != StopReasonLength {
		t.Fatalf("expected incomplete fixture to map to length, got %+v", fixture.StreamResult)
	}
}

func TestOpenAICodexFixtureKeepsStreamAndCompleteConsistentForFailedResponse(t *testing.T) {
	fixture := runOpenAICodexFixture(t, "gpt-5.4", buildOpenAICodexSSE(
		map[string]any{
			"type": "response.failed",
			"response": map[string]any{
				"id":     "resp_fixture_failed",
				"status": "failed",
				"error": map[string]any{
					"type":    "server_error",
					"message": "backend exploded",
				},
			},
		},
	))

	if !reflect.DeepEqual(fixture.StreamResult, fixture.CompleteResult) {
		t.Fatalf("expected failed fixture stream/complete parity, got stream=%+v complete=%+v", fixture.StreamResult, fixture.CompleteResult)
	}
	if fixture.StreamResult.StopReason != StopReasonError || fixture.StreamResult.ErrorMessage != "backend exploded" {
		t.Fatalf("expected failed fixture to propagate error, got %+v", fixture.StreamResult)
	}
	if fixture.StreamResult.ResponseID != "resp_fixture_failed" {
		t.Fatalf("expected failed fixture response id, got %q", fixture.StreamResult.ResponseID)
	}
}

func TestStreamSimpleOpenAICodexTerminalItemOverridesPartialMessageText(t *testing.T) {
	fixture := runOpenAICodexFixture(t, "gpt-5.4", buildOpenAICodexSSE(
		map[string]any{
			"type": "response.output_item.added",
			"item": map[string]any{
				"type": "message",
				"id":   "msg_override",
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
			"delta": "hel",
		},
		map[string]any{
			"type": "response.output_item.done",
			"item": map[string]any{
				"type": "message",
				"id":   "msg_override",
				"content": []map[string]any{
					{"type": "output_text", "text": "hello there"},
				},
			},
		},
		map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id":     "resp_override",
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
	))

	if fixture.Events[3].Type != AssistantMessageEventTextEnd || fixture.Events[3].Content != "hello there" {
		t.Fatalf("expected terminal item to finalize text as hello there, got %+v", fixture.Events[3])
	}
	text, ok := fixture.StreamResult.Content[0].(TextContent)
	if !ok || text.Text != "hello there" {
		t.Fatalf("expected result text to use terminal content, got %#v", fixture.StreamResult.Content[0])
	}
}

func TestStreamSimpleOpenAICodexMalformedJSONAfterCreatedPreservesResponseID(t *testing.T) {
	token := makeOpenAICodexToken("acc_test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_bad_json\"}}\n\n"))
		_, _ = w.Write([]byte("data: {bad-json}\n\n"))
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
	if len(events) != 2 {
		t.Fatalf("expected start + error events for malformed json, got %d", len(events))
	}
	if events[1].Type != AssistantMessageEventError {
		t.Fatalf("expected error event, got %q", events[1].Type)
	}
	if events[1].Error.ResponseID != "resp_bad_json" || result.ResponseID != "resp_bad_json" {
		t.Fatalf("expected created response id to survive malformed json, got event=%q result=%q", events[1].Error.ResponseID, result.ResponseID)
	}
	if !strings.Contains(result.ErrorMessage, "invalid character") {
		t.Fatalf("expected malformed json error message, got %q", result.ErrorMessage)
	}
}


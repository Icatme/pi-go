package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
)

func responsesToolEvent(kind, id, callID, name, arguments string, index int) map[string]any {
	return map[string]any{"type": kind, "output_index": index, "item": map[string]any{
		"type": "function_call", "id": id, "call_id": callID, "name": name, "arguments": arguments,
	}}
}

func TestResponsesToolCallIntegrityAcrossTransports(t *testing.T) {
	terminal := map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp", "status": "completed"}}
	for _, test := range []struct {
		name      string
		events    []map[string]any
		wantError bool
		wantPaths []string
	}{
		{name: "unfinished call", events: []map[string]any{
			responsesToolEvent("response.output_item.added", "fc_a", "call_a", "edit", "", 0),
			{"type": "response.function_call_arguments.delta", "item_id": "fc_a", "output_index": 0, "delta": `{"path":"cut`}, terminal,
		}, wantError: true},
		{name: "arguments done does not complete item", events: []map[string]any{
			responsesToolEvent("response.output_item.added", "fc_a", "call_a", "edit", "", 0),
			{"type": "response.function_call_arguments.done", "item_id": "fc_a", "output_index": 0, "arguments": `{"path":"a"}`}, terminal,
		}, wantError: true},
		{name: "interleaved calls", events: []map[string]any{
			responsesToolEvent("response.output_item.added", "fc_a", "call_a", "edit", "", 0),
			responsesToolEvent("response.output_item.added", "fc_b", "call_b", "edit", "", 1),
			{"type": "response.function_call_arguments.delta", "item_id": "fc_a", "output_index": 0, "delta": `{"path":"a"}`},
			{"type": "response.function_call_arguments.delta", "item_id": "fc_b", "output_index": 1, "delta": `{"path":"b"}`},
			responsesToolEvent("response.output_item.done", "fc_a", "call_a", "edit", "", 0),
			responsesToolEvent("response.output_item.done", "fc_b", "call_b", "edit", "", 1), terminal,
		}, wantPaths: []string{"a", "b"}},
		{name: "ambiguous identityless delta", events: []map[string]any{
			responsesToolEvent("response.output_item.added", "fc_a", "call_a", "edit", "", 0),
			responsesToolEvent("response.output_item.added", "fc_b", "call_b", "edit", "", 1),
			{"type": "response.function_call_arguments.delta", "delta": `{"path":"wrong"}`}, terminal,
		}, wantError: true},
		{name: "mismatched identity", events: []map[string]any{
			responsesToolEvent("response.output_item.added", "fc_a", "call_a", "edit", "", 0),
			{"type": "response.function_call_arguments.delta", "item_id": "fc_unknown", "output_index": 0, "delta": `{"path":"wrong"}`}, terminal,
		}, wantError: true},
		{name: "truncated done arguments", events: []map[string]any{
			responsesToolEvent("response.output_item.added", "fc_a", "call_a", "edit", "", 0),
			responsesToolEvent("response.output_item.done", "fc_a", "call_a", "edit", `{"path":"cut`, 0), terminal,
		}, wantError: true},
		{name: "terminal output completes call", events: []map[string]any{
			responsesToolEvent("response.output_item.added", "fc_a", "call_a", "edit", "", 0),
			{"type": "response.function_call_arguments.delta", "item_id": "fc_a", "output_index": 0, "delta": `{"path":"cut`},
			{"type": "response.completed", "response": map[string]any{"id": "resp", "status": "completed", "output": []map[string]any{{"type": "function_call", "id": "fc_a", "call_id": "call_a", "name": "edit", "arguments": `{"path":"a"}`, "status": "completed"}}}},
		}, wantPaths: []string{"a"}},
		{name: "terminal output cannot complete a different call", events: []map[string]any{
			responsesToolEvent("response.output_item.added", "fc_a", "call_a", "edit", "", 0),
			{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []map[string]any{{"type": "function_call", "id": "fc_b", "call_id": "call_b", "name": "edit", "arguments": `{"path":"b"}`, "status": "completed"}}}},
		}, wantError: true},
	} {
		for _, transport := range []string{"responses-sse", "codex-sse", "codex-websocket"} {
			t.Run(test.name+"/"+transport, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if transport == "codex-websocket" {
						conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.Close()
						if _, _, err := conn.ReadMessage(); err != nil {
							t.Error(err)
							return
						}
						for _, event := range test.events {
							if err := conn.WriteJSON(event); err != nil {
								return
							}
						}
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, buildOpenAICodexSSE(test.events...))
				}))
				defer server.Close()
				model := Model{ID: "fixture", API: "openai-responses", Provider: "openai", BaseURL: server.URL, MaxTokens: 256}
				options := SimpleStreamOptions{APIKey: "fixture", MaxRetries: 2, MaxRetryDelay: 1}
				if strings.HasPrefix(transport, "codex") {
					model.API, model.Provider = "openai-codex-responses", "openai-codex"
					options.APIKey = makeOpenAICodexToken("fixture-account")
					options.Transport = TransportSSE
					if transport == "codex-websocket" {
						options.Transport = TransportWebSocket
					}
				}
				stream := StreamSimple(model, Context{}, options)
				var errors, ends int
				for event := range stream.Events() {
					if event.Type == AssistantMessageEventError {
						errors++
					}
					if event.Type == AssistantMessageEventToolCallEnd {
						ends++
					}
				}
				response := stream.Result()
				if requests.Load() != 1 {
					t.Fatalf("retried after partial output: requests=%d", requests.Load())
				}
				if test.wantError {
					if response.StopReason != StopReasonError || errors != 1 || response.ErrorMessage == "" {
						t.Fatalf("expected single error, got errors=%d %+v", errors, response)
					}
					if len(response.Content) == 0 {
						t.Fatal("partial content lost on integrity error")
					}
					return
				}
				if response.StopReason != StopReasonToolUse || errors != 0 || ends != len(test.wantPaths) || len(response.Content) != len(test.wantPaths) {
					t.Fatalf("invalid tool lifecycle: ends=%d errors=%d %+v", ends, errors, response)
				}
				for i, path := range test.wantPaths {
					call := response.Content[i].(ToolCall)
					if call.Arguments["path"] != path || call.ID != "call_"+path+"|fc_"+path {
						t.Fatalf("call %d mismatched: %+v", i, call)
					}
				}
			})
		}
	}
}

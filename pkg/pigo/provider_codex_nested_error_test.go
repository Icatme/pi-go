package pigo

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestResponsesNestedErrorEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name            string
		payload         map[string]any
		msg, code, kind string
	}{
		{"nested", map[string]any{"type": "error", "status": 400, "error": map[string]any{"type": "invalid_request_error", "code": "previous_response_not_found", "message": "fixture missing response", "raw": "SECRET"}}, "fixture missing response", "previous_response_not_found", "invalid_request_error"},
		{"nested-busy", map[string]any{"type": "error", "status": 503, "error": map[string]any{"message": "service unavailable", "code": "server_error", "type": "server_error"}}, "service unavailable", "server_error", "server_error"},
		{"nested-partial", map[string]any{"type": "error", "error": map[string]any{"message": "after partial", "code": "bad_request", "type": "invalid_request_error"}}, "after partial", "bad_request", "invalid_request_error"},
		{"top", map[string]any{"type": "error", "message": "old message", "code": "old_code"}, "old message", "old_code", ""},
		{"both-top-wins", map[string]any{"type": "error", "message": "top", "code": "top_code", "error": map[string]any{"message": "nested", "code": "nested_code", "type": "nested_type"}}, "top", "top_code", ""},
		{"nonstring-top-falls-back", map[string]any{"type": "error", "message": 12, "code": "ignore_top", "error": map[string]any{"message": "nested", "code": "nested_code", "type": "nested_type"}}, "nested", "nested_code", "nested_type"},
		{"nonstring-nested", map[string]any{"type": "error", "error": map[string]any{"message": 12, "code": false, "type": []any{1}}}, "error", "", ""},
		{"malformed-nested", map[string]any{"type": "error", "code": "legacy", "error": "bad"}, "error", "legacy", ""},
	} {
		for _, transport := range []string{"websocket", "sse"} {
			t.Run(tc.name+transport, func(t *testing.T) {
				wantMessage, wantCode, wantType := tc.msg, tc.code, tc.kind
				if transport == "sse" {
					topMessage, _ := tc.payload["message"].(string)
					wantMessage = topMessage
					if wantMessage == "" {
						wantMessage = "error"
					}
					wantCode, _ = tc.payload["code"].(string)
					wantType = ""
				}
				var requests atomic.Int32
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if transport == "websocket" {
						c, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if e != nil {
							t.Error(e)
							return
						}
						defer c.Close()
						if _, _, e = c.ReadMessage(); e != nil {
							t.Error(e)
							return
						}
						if tc.name == "nested-partial" {
							c.WriteJSON(map[string]any{"type": "response.output_text.delta", "delta": "partial"})
						}
						c.WriteJSON(tc.payload)
					} else {
						w.Header().Set("Content-Type", "text/event-stream")
						if tc.name == "nested-partial" {
							fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
						}
						b, _ := json.Marshal(tc.payload)
						fmt.Fprintf(w, "data: %s\n\n", b)
					}
				}))
				defer s.Close()
				m := Model{ID: "fixture", Provider: "openai-codex", API: "openai-codex-responses", BaseURL: s.URL, MaxTokens: 64}
				opts := SimpleStreamOptions{APIKey: makeOpenAICodexToken("fixture"), Transport: TransportAuto, MaxRetries: 2, RequestContext: t.Context()}
				if transport == "sse" {
					opts.Transport = TransportSSE
				}
				st := StreamSimple(m, Context{}, opts)
				events := collectAssistantMessageEvents(st.Events())
				r := st.Result()
				if tc.name == "nested-partial" && (len(r.Content) != 1 || r.Content[0].(TextContent).Text != "partial") {
					t.Fatalf("lost partial %+v", r)
				}
				f, _ := ProviderFailureOf(r)
				if r.StopReason != StopReasonError || r.ErrorMessage != wantMessage || f.Code != wantCode || f.Type != wantType || f.Reason != "error_event" || requests.Load() != 1 {
					t.Fatalf("result=%+v failure=%+v requests=%d", r, f, requests.Load())
				}
				errors, dones := 0, 0
				for _, e := range events {
					if e.Type == AssistantMessageEventError {
						errors++
					}
					if e.Type == AssistantMessageEventDone {
						dones++
					}
				}
				if errors != 1 || dones != 0 {
					t.Fatalf("error%d done%d", errors, dones)
				}
				for _, d := range r.Diagnostics {
					if _, exists := d.Details["http_status"]; exists {
						t.Fatal("invented HTTP status")
					}
					if _, exists := d.Details["raw"]; exists {
						t.Fatal("copied rawpayload")
					}
				}
			})
		}
	}
}

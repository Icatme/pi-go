package pigo

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenCodeGoSessionHeaderAcrossProtocols(t *testing.T) {
	for _, modelID := range []string{"kimi-k3", "gpt-5.6-luna", "minimax-m3"} {
		for _, simple := range []bool{false, true} {
			for _, tc := range []struct {
				name, session, expected string
				headers                 map[string]string
			}{
				{name: "session", session: "conversation-123", expected: "conversation-123"},
				{name: "override", session: "conversation-123", expected: "custom", headers: map[string]string{"X-OPENCODE-SESSION": "custom"}},
				{name: "absent"},
			} {
				t.Run(modelID+"/"+map[bool]string{false: "stream", true: "simple"}[simple]+"/"+tc.name, func(t *testing.T) {
					var captured string
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						captured = r.Header.Get("x-opencode-session")
						w.Header().Set("Content-Type", "text/event-stream")
						switch modelID {
						case "minimax-m3":
							_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"minimax-m3\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"))
						case "gpt-5.6-luna":
							_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"))
						default:
							_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
						}
					}))
					defer server.Close()
					model := *GetModel("opencode-go", modelID)
					model.BaseURL = server.URL
					ctx := Context{Messages: []Message{UserMessage{Content: "hello"}}}
					var result AssistantMessage
					if simple {
						result = CompleteSimple(model, ctx, SimpleStreamOptions{APIKey: "test", SessionID: tc.session, Headers: tc.headers})
					} else {
						result = Complete(model, ctx, ProviderStreamOptions{APIKey: "test", SessionID: tc.session, Headers: tc.headers})
					}
					if result.StopReason != StopReasonStop {
						t.Fatalf("request failed: %s", result.ErrorMessage)
					}
					if captured != tc.expected {
						t.Fatalf("session header = %q, want %q", captured, tc.expected)
					}
					if len(tc.headers) > 0 && tc.headers["X-OPENCODE-SESSION"] != "custom" {
						t.Fatal("mutated caller headers")
					}
				})
			}
		}
	}
}

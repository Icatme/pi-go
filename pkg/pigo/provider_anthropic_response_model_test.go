package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicResponseModelDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name, echo, want string
		include          bool
	}{
		{"different", "upstream-actual", "upstream-actual", true},
		{"same", "requested-alias", "", true},
		{"empty", "", "", true},
		{"missing", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requested string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				requested = req.Model
				w.Header().Set("Content-Type", "text/event-stream")
				msg := map[string]any{"id": "msg_diag", "usage": map[string]any{"input_tokens": 3, "output_tokens": 0}}
				if tc.include {
					msg["model"] = tc.echo
				}
				events := []map[string]any{
					{"type": "message_start", "message": msg},
					{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}},
					{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "answer"}},
					{"type": "content_block_stop", "index": 0},
					{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}},
					{"type": "message_stop"},
				}
				for _, e := range events {
					b, _ := json.Marshal(e)
					fmt.Fprintf(w, "data: %s\n\n", b)
					w.(http.Flusher).Flush()
				}
			}))
			defer server.Close()
			m := Model{ID: "requested-alias", Provider: "anthropic", API: "anthropic-messages", BaseURL: server.URL, MaxTokens: 64}
			stream := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture"})
			events := collectAssistantMessageEvents(stream.Events())
			result := stream.Result()
			if result.Model != m.ID || result.Provider != m.Provider || result.API != m.API || result.ResponseModel != tc.want || result.ResponseID != "msg_diag" || requested != m.ID || result.StopReason != StopReasonStop {
				t.Fatalf("requested=%q result=%+v want echo=%q", requested, result, tc.want)
			}
			if len(result.Content) != 1 || result.Content[0].(TextContent).Text != "answer" {
				t.Fatalf("content=%+v", result.Content)
			}
			if result.Usage.Input != 3 || result.Usage.Output != 2 {
				t.Fatal(result.Usage)
			}
			seen := false
			for _, e := range events {
				if e.Type == AssistantMessageEventTextDelta {
					seen = true
					if e.Partial.Model != m.ID || e.Partial.ResponseModel != tc.want {
						t.Fatalf("partial=%+v", e.Partial)
					}
				}
			}
			if !seen {
				t.Fatal("missing delta")
			}
		})
	}
}

func TestAnthropicResponseModelEmptyEchoDoesNotClearDiagnostic(t *testing.T) {
	m := Model{ID: "requested", Provider: "kimi-coding", API: "anthropic-messages"}
	response := AssistantMessage{Model: m.ID, Provider: m.Provider, API: m.API, ResponseModel: "observed"}
	states := map[int]*anthropicStreamingBlockState{}
	for _, data := range []string{
		`{"type":"message_start","message":{"id":"msg","model":""}}`,
		`{"type":"message_start","message":{"id":"msg"}}`,
		`{"type":"message_start","message":{"id":"msg","model":"requested"}}`,
	} {
		_, err := processAnthropicStreamEvent(data, m, &response, nil, states, false, nil, nil, false)
		if err != nil || response.ResponseModel != "observed" || response.Model != m.ID || response.Provider != m.Provider || response.API != m.API {
			t.Fatalf("err=%v response=%+v", err, response)
		}
	}
}

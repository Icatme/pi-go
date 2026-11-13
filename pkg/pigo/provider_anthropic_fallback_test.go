package pigo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The native content model cannot yet preserve a fallback boundary. Never
// return a successful mixed-model transcript with that marker silently dropped.
func TestAnthropicFallbackFailsWithoutReplayingPartialHistory(t *testing.T) {
	for _, prefix := range []string{"", "thinking", "tool_use"} {
		t.Run(prefix, func(t *testing.T) {
			events := []map[string]any{map[string]any{"type": "message_start", "message": map[string]any{"id": "fallback-test", "model": "primary"}}}
			if prefix != "" {
				block := map[string]any{"type": prefix, "id": "call_before", "name": "write", "input": map[string]any{}}
				events = append(events, map[string]any{"type": "content_block_start", "index": 0, "content_block": block}, map[string]any{"type": "content_block_stop", "index": 0})
			}
			events = append(events, map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "fallback", "from": map[string]any{"model": "primary"}, "to": map[string]any{"model": "secondary"}}}, map[string]any{"type": "content_block_stop", "index": 1}, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}}, map[string]any{"type": "message_stop"})
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(buildAnthropicSSE(events...)))
			}))
			defer server.Close()
			model := *GetModel("anthropic", "claude-sonnet-4-5")
			model.BaseURL = server.URL
			response := CompleteSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{APIKey: "test"})
			if response.StopReason != StopReasonError || !strings.Contains(response.ErrorMessage, "fallback") {
				t.Fatalf("expected explicit fallback error, got %+v", response)
			}
			if calls != 1 {
				t.Fatalf("unexpected retry count %d", calls)
			}
			request := buildAnthropicRequest(model, Context{Messages: []Message{UserMessage{Content: "hello"}, response}}, AnthropicMessagesProviderOptions{}, false, true)
			if len(request.Messages) != 1 {
				t.Fatalf("failed partial response replayed: %+v", request.Messages)
			}
		})
	}
}

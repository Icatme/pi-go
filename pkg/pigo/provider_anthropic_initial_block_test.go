package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicInitialBlockContent(t *testing.T) {
	for _, provider := range []Provider{"anthropic", "kimi-coding"} {
		for _, tc := range []struct{ name, text, thought, sig, delta string }{
			{"initial only", "初始 text", "初始 thought", "sig-start", ""},
			{"initial and deltas", "prefix ", "reason ", "sig-", "suffix"},
			{"empty initial", "", "", "", "suffix"},
		} {
			t.Run(string(provider)+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					emit := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b); w.(http.Flusher).Flush() }
					emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "initial-block", "usage": map[string]int{"input_tokens": 3}}})
					// Interleaved blocks prove that the initial content stays with its own index.
					emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "thinking", "thinking": tc.thought, "signature": tc.sig}})
					emit(map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]string{"type": "text", "text": tc.text}})
					if tc.delta != "" {
						emit(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]string{"type": "text_delta", "text": tc.delta}})
						emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "thinking_delta", "thinking": tc.delta}})
						emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "signature_delta", "signature": tc.delta}})
					}
					emit(map[string]any{"type": "content_block_stop", "index": 1})
					emit(map[string]any{"type": "content_block_stop", "index": 0})
					emit(map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "end_turn"}, "usage": map[string]int{"output_tokens": 2}})
					emit(map[string]any{"type": "message_stop"})
				}))
				defer server.Close()
				model := Model{ID: "fixture", Provider: provider, API: "anthropic-messages", BaseURL: server.URL, MaxTokens: 64}
				stream := Stream(model, Context{}, ProviderStreamOptions{APIKey: "fixture"})
				events := collectAssistantMessageEvents(stream.Events())
				result := stream.Result()
				if result.StopReason != StopReasonStop || result.Model != model.ID || result.Provider != provider || len(result.Content) != 2 {
					t.Fatalf("%+v", result)
				}
				thought := result.Content[0].(ThinkingContent)
				text := result.Content[1].(TextContent)
				if text.Text != tc.text+tc.delta || thought.Thinking != tc.thought+tc.delta || thought.ThinkingSignature != tc.sig+tc.delta {
					t.Fatalf("text=%+v thought=%+v", text, thought)
				}
				if result.Usage.Input != 3 || result.Usage.Output != 2 {
					t.Fatalf("usage=%+v", result.Usage)
				}
				counts := map[AssistantMessageEventType]int{}
				for _, e := range events {
					counts[e.Type]++
					if e.Type == AssistantMessageEventTextStart && e.Partial.Content[e.ContentIndex].(TextContent).Text != tc.text {
						t.Fatal("text start snapshot changed")
					}
					if e.Type == AssistantMessageEventThinkingStart {
						b := e.Partial.Content[e.ContentIndex].(ThinkingContent)
						if b.Thinking != tc.thought || b.ThinkingSignature != tc.sig {
							t.Fatal("thinking start snapshot changed")
						}
					}
					if e.Type == AssistantMessageEventTextEnd && e.Content != tc.text+tc.delta {
						t.Fatalf("text end=%+v", e)
					}
					if e.Type == AssistantMessageEventThinkingEnd && e.Content != tc.thought+tc.delta {
						t.Fatalf("thinking end=%+v", e)
					}
				}
				for _, typ := range []AssistantMessageEventType{AssistantMessageEventTextStart, AssistantMessageEventTextEnd, AssistantMessageEventThinkingStart, AssistantMessageEventThinkingEnd, AssistantMessageEventDone} {
					if counts[typ] != 1 {
						t.Fatalf("%s count=%d", typ, counts[typ])
					}
				}
				wantDelta := 0
				if tc.delta != "" {
					wantDelta = 1
				}
				if counts[AssistantMessageEventTextDelta] != wantDelta || counts[AssistantMessageEventThinkingDelta] != wantDelta {
					t.Fatalf("extra deltas %v", counts)
				}
			})
		}
	}
}

package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIResponsesPreservesTerminalReasoningReplayFields(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		t.Run(fmt.Sprintf("incremental=%v", incremental), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if incremental {
					fmt.Fprintln(w, `data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`)
					fmt.Fprintln(w)
					fmt.Fprintln(w, `data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"private plan"}],"encrypted_content":"ciphertext"}}`)
					fmt.Fprintln(w)
				}
				_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"content":[{"type":"reasoning_text","text":"private plan"}],"encrypted_content":"ciphertext"},{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5,"input_tokens_details":{"cached_tokens":0}}}}`)
				_, _ = fmt.Fprintln(w)
			}))
			defer server.Close()

			model := Model{
				ID: "gpt-test", Provider: "openai", API: "openai-responses", BaseURL: server.URL,
				Input: []InputType{InputText}, Reasoning: true,
			}
			response := CompleteSimple(model, Context{
				Messages: []Message{UserMessage{Content: "hello"}},
			}, SimpleStreamOptions{APIKey: "secret", Reasoning: ThinkingLevelHigh})
			if response.StopReason != StopReasonStop || len(response.Content) != 2 {
				t.Fatalf("response=%#v", response)
			}
			reasoning, ok := response.Content[0].(ThinkingContent)
			if !ok || reasoning.Thinking != "private plan" {
				t.Fatalf("reasoning=%#v", response.Content[0])
			}
			var signature map[string]any
			if err := json.Unmarshal([]byte(reasoning.ThinkingSignature), &signature); err != nil {
				t.Fatal(err)
			}
			summary, ok := signature["summary"].([]any)
			if !ok || len(summary) != 0 || signature["id"] != "rs_1" || signature["status"] != "completed" || signature["encrypted_content"] != "ciphertext" {
				t.Fatalf("signature=%#v", signature)
			}
			text, ok := response.Content[1].(TextContent)
			if !ok || text.Text != "done" {
				t.Fatalf("text=%#v", response.Content[1])
			}
		})
	}
}

package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestResponsesTerminalCipherBackfillAndReplay(t *testing.T) {
	for _, api := range []API{"openai-responses", "openai-codex-responses"} {
		t.Run(string(api), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","summary":[{"type":"summary_text","text":"summary"}]}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"ciphertext"},{"type":"message","id":"msg_1","status":"completed","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}

`)
			}))
			defer server.Close()
			model := Model{ID: "fixture", Provider: "openai", API: api, BaseURL: server.URL, Reasoning: true, Input: []InputType{InputText}}
			stream := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{APIKey: makeOpenAICodexToken("fixture"), Transport: TransportSSE})
			ends := 0
			var endPartial []ContentBlock
			for event := range stream.Events() {
				if event.Type == AssistantMessageEventThinkingEnd {
					ends++
					endPartial = event.Partial.Content
				}
			}
			result := stream.Result()
			if result.StopReason != StopReasonStop || len(result.Content) != 2 || ends != 1 {
				t.Fatalf("result=%+v ends=%d", result, ends)
			}
			thinking := result.Content[0].(ThinkingContent)
			if thinking.Thinking != "summary" || !strings.Contains(thinking.ThinkingSignature, `"encrypted_content":"ciphertext"`) {
				t.Fatalf("thinking=%+v", thinking)
			}
			if strings.Contains(endPartial[0].(ThinkingContent).ThinkingSignature, "ciphertext") {
				t.Fatal("emitted partial was mutated")
			}
			ctx := Context{Messages: []Message{UserMessage{Content: "hello"}, result, UserMessage{Content: "next"}}}
			var req openAIResponsesRequest
			if api == "openai-responses" {
				var err error
				req, err = buildOpenAIResponsesRequest(nil, model, ctx, ProviderStreamOptions{})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				req = buildOpenAICodexRequest(nil, model, ctx, ProviderStreamOptions{})
			}
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `"encrypted_content":"ciphertext"`) {
				t.Fatalf("missing replay cipher: %s", raw)
			}
		})
	}
}

func TestResponsesTerminalCipherBackfillBoundaries(t *testing.T) {
	original := `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"kept"}],"opaque":9007199254740993}`
	for _, tc := range []struct {
		name, signature, id, cipher string
		duplicate, want             bool
	}{
		{name: "missing", signature: original, id: "rs_1", cipher: "new", want: true},
		{name: "empty", signature: `{"type":"reasoning","id":"rs_1","encrypted_content":""}`, id: "rs_1", cipher: "new", want: true},
		{name: "null", signature: `{"type":"reasoning","id":"rs_1","encrypted_content":null}`, id: "rs_1", cipher: "new", want: true},
		{name: "invalid cipher type", signature: `{"type":"reasoning","id":"rs_1","encrypted_content":42}`, id: "rs_1", cipher: "new"},
		{name: "existing", signature: `{"type":"reasoning","id":"rs_1","encrypted_content":"old"}`, id: "rs_1", cipher: "new"},
		{name: "no item id", signature: original, cipher: "new"},
		{name: "no cipher", signature: original, id: "rs_1"},
		{name: "wrong id", signature: original, id: "rs_2", cipher: "new"},
		{name: "ambiguous", signature: original, id: "rs_1", cipher: "new", duplicate: true},
		{name: "malformed signature", signature: `{`, id: "rs_1", cipher: "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := AssistantMessage{Content: []ContentBlock{ThinkingContent{Thinking: "kept text", ThinkingSignature: tc.signature}, TextContent{Text: "unchanged"}}}
			if tc.duplicate {
				response.Content = append(response.Content, response.Content[0])
			}
			previous := append([]ContentBlock(nil), response.Content...)
			aliased := response.Content
			backfillOpenAIResponsesReasoningCipher(&response, openAIResponsesResponseItem{Type: "reasoning", ID: tc.id, EncryptedContent: tc.cipher})
			if !reflect.DeepEqual(aliased, previous) {
				t.Fatal("previous content mutated")
			}
			if !tc.want {
				if !reflect.DeepEqual(response.Content, previous) {
					t.Fatal("unexpected change")
				}
				return
			}
			block := response.Content[0].(ThinkingContent)
			if block.Thinking != "kept text" || response.Content[1] != (TextContent{Text: "unchanged"}) {
				t.Fatal("non-signature changed")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(block.ThinkingSignature), &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["encrypted_content"]) != `"new"` {
				t.Fatal(block)
			}
			if tc.name == "missing" && string(fields["opaque"]) != "9007199254740993" {
				t.Fatal("large integer changed")
			}
		})
	}
}

func TestResponsesTerminalCipherDuplicateIdentity(t *testing.T) {
	for _, api := range []API{"openai-responses", "openai-codex-responses"} {
		t.Run(string(api), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"completed","summary":[{"type":"summary_text","text":"summary"}]}}

data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"first"},{"type":"reasoning","id":"rs_1","encrypted_content":"second"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}

`)
			}))
			defer server.Close()
			model := Model{ID: "fixture", Provider: "openai", API: api, BaseURL: server.URL, Reasoning: true, Input: []InputType{InputText}}
			stream := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{APIKey: makeOpenAICodexToken("fixture"), Transport: TransportSSE})
			ends := 0
			var completed ThinkingContent
			for event := range stream.Events() {
				if event.Type == AssistantMessageEventThinkingEnd {
					ends++
					completed = event.Partial.Content[0].(ThinkingContent)
				}
			}
			result := stream.Result()
			if result.StopReason != StopReasonStop || len(result.Content) != 1 || ends != 1 {
				t.Fatalf("result=%+v ends=%d", result, ends)
			}
			thinking := result.Content[0].(ThinkingContent)
			if thinking != completed || thinking.Thinking != "summary" || strings.Contains(thinking.ThinkingSignature, "encrypted_content") {
				t.Fatalf("ambiguous terminal output changed completed reasoning: %+v", thinking)
			}
		})
	}
}

package pigo

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestResponsesMultipleAssistantTextIDs(t *testing.T) {
	model := Model{ID: "test", Provider: "openai", API: API("openai-responses")}
	transcript := TranscriptContext{Messages: []Message{UserMessage{Content: "hello"}, AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID, StopReason: StopReasonStop, Content: []ContentBlock{TextContent{Text: "one"}, TextContent{Text: "two"}}}}}
	input := convertOpenAIResponsesMessages(nil, nil, model, transcript, true)
	seen := map[string]bool{}
	for _, item := range input {
		id, _ := item["id"].(string)
		if id == "" {
			continue
		}
		if seen[id] {
			t.Fatalf("duplicate item ID %q: %#v", id, input)
		}
		seen[id] = true
	}
}

func TestResponsesMessageIDsPreserveBlockOrder(t *testing.T) {
	for _, api := range []API{"openai-responses", "openai-codex-responses"} {
		t.Run(string(api), func(t *testing.T) {
			model := Model{ID: "test", Provider: "openai", API: api}
			history := []Message{UserMessage{Content: "hello"}, AssistantMessage{Provider: model.Provider, API: api, Model: model.ID, StopReason: StopReasonToolUse, Content: []ContentBlock{
				TextContent{Text: "  "}, TextContent{Text: "one", TextSignature: `{"v":1,"id":"original"}`}, ThinkingContent{Thinking: "thought", ThinkingSignature: `{"type":"reasoning","id":"rs_original","encrypted_content":"opaque"}`}, TextContent{Text: "two"}, ToolCall{ID: "call_a", Name: "tool", Arguments: map[string]any{}}, TextContent{Text: "three"}}}, ToolResultMessage{ToolCallID: "call_a", ToolName: "tool", Content: []ContentBlock{TextContent{Text: "result"}}}, AssistantMessage{Provider: model.Provider, API: api, Model: model.ID, StopReason: StopReasonStop, Content: []ContentBlock{TextContent{Text: "last"}}}}
			before, _ := json.Marshal(history)
			ctx := Context{Messages: history}
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
			// Check actual serialized request, not only intermediate maps.
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Input []map[string]any `json:"input"`
			}
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			var ids, types, texts []string
			for _, item := range wire.Input {
				typ, _ := item["type"].(string)
				types = append(types, typ)
				if typ == "message" {
					ids = append(ids, item["id"].(string))
					parts := item["content"].([]any)
					texts = append(texts, parts[0].(map[string]any)["text"].(string))
				}
			}
			if !reflect.DeepEqual(ids, []string{"msg_1", "msg_1_3", "msg_1_5", "msg_3"}) {
				t.Fatalf("IDs: %v", ids)
			}
			if !reflect.DeepEqual(types, []string{"", "message", "reasoning", "message", "function_call", "message", "function_call_output", "message"}) {
				t.Fatalf("order: %v", types)
			}
			if !reflect.DeepEqual(texts, []string{"one", "two", "three", "last"}) {
				t.Fatalf("texts: %v", texts)
			}
			if wire.Input[2]["id"] != "rs_original" || wire.Input[2]["encrypted_content"] != "opaque" {
				t.Fatalf("opaque item changed: %v", wire.Input[2])
			}
			after, _ := json.Marshal(history)
			if string(before) != string(after) {
				t.Fatal("source mutated")
			}
		})
	}
}

package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestMistralThinkingReplayWire(t *testing.T) {
	m := Model{ID: "fixture", Provider: "mistral", API: "mistral-conversations", MaxTokens: 64}
	history := AssistantMessage{Model: m.ID, Provider: m.Provider, API: m.API, StopReason: StopReasonStop, Content: []ContentBlock{ThinkingContent{Thinking: "first", ThinkingSignature: "opaque"}, TextContent{Text: "answer"}, ThinkingContent{Thinking: "second"}}}
	ctx := Context{Messages: []Message{UserMessage{Content: "question"}, history, UserMessage{Content: "continue"}}}
	before, _ := json.Marshal(ctx)
	var got map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	m.BaseURL = s.URL
	st := Stream(m, ctx, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context()})
	collectAssistantMessageEvents(st.Events())
	if r := st.Result(); r.StopReason != StopReasonStop {
		t.Fatalf("%+v", r)
	}
	messages := got["messages"].([]any)
	parts := messages[1].(map[string]any)["content"]
	want := []any{map[string]any{"type": "thinking", "thinking": []any{map[string]any{"type": "text", "text": "first"}}}, map[string]any{"type": "text", "text": "answer"}, map[string]any{"type": "thinking", "thinking": []any{map[string]any{"type": "text", "text": "second"}}}}
	if !reflect.DeepEqual(parts, want) {
		t.Fatalf("parts=%#v want=%#v", parts, want)
	}
	after, _ := json.Marshal(ctx)
	if string(before) != string(after) {
		t.Fatal("mutated history")
	}
}

func TestMistralThinkingWireControls(t *testing.T) {
	parts, calls := convertMistralAssistantContent([]ContentBlock{ThinkingContent{Thinking: "  "}, TextContent{Text: "text"}, ToolCall{ID: "call", Name: "echo", Arguments: map[string]any{"value": "ok"}}})
	b, _ := json.Marshal(parts)
	if string(b) != `[{"type":"text","text":"text"}]` || len(calls) != 1 || calls[0].Function.Name != "echo" {
		t.Fatalf("parts=%s calls=%+v", b, calls)
	}
	parts, _ = convertMistralAssistantContent([]ContentBlock{ThinkingContent{Thinking: " only\n"}})
	b, _ = json.Marshal(parts)
	if string(b) != `[{"type":"thinking","thinking":[{"type":"text","text":" only\n"}]}]` {
		t.Fatalf("%s", b)
	}
}

func TestMistralThinkingChangePreservesUserImageWire(t *testing.T) {
	content := convertMistralUserContent([]ContentBlock{TextContent{Text: "question"}, ImageContent{Data: "YWJj", MIMEType: "image/png"}}, true)
	b, _ := json.Marshal(content)
	if string(b) != `[{"type":"text","text":"question"},{"type":"image_url","image_url":"data:image/png;base64,YWJj"}]` {
		t.Fatalf("%s", b)
	}
	if plain := convertMistralUserContent("plain", false); plain != "plain" {
		t.Fatalf("%#v", plain)
	}
}

package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekDedicatedReasoningReplayHTTP(t *testing.T) {
	m := GetModel("deepseek", "deepseek-v4-pro")
	if m == nil {
		t.Fatal("missing model")
	}
	ctx := Context{Messages: []Message{UserMessage{Content: "run"}, AssistantMessage{API: m.API, Provider: m.Provider, Model: m.ID, StopReason: StopReasonToolUse, Content: []ContentBlock{ThinkingContent{Thinking: " exact reasoning \n", ThinkingSignature: "reasoning_content"}, ToolCall{ID: "call1", Name: "echo", Arguments: map[string]any{}}}}, ToolResultMessage{ToolCallID: "call1", ToolName: "echo", Content: []ContentBlock{TextContent{Text: "ok"}}}}, Tools: []Tool{{Name: "echo", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}}
	var b []byte
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Error(err)
		}
		b = append([]byte(nil), raw...)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	m.BaseURL = s.URL
	result := Complete(*m, ctx, ProviderStreamOptions{APIKey: "fixture"})
	if result.StopReason != StopReasonStop {
		t.Fatalf("fixture failed %+v", result)
	}
	var wire map[string]any
	json.Unmarshal(b, &wire)
	assistants := 0
	for _, v := range wire["messages"].([]any) {
		x := v.(map[string]any)
		if x["role"] == "assistant" {
			assistants++
			calls, ok := x["tool_calls"].([]any)
			if !ok || len(calls) != 1 || calls[0].(map[string]any)["id"] != "call1" {
				t.Fatalf("lost tool calls: %#v", x)
			}
			t.Logf("wire=%s", b)
			if x["reasoning_content"] != " exact reasoning \n" {
				t.Fatalf("reasoning missing/mutated: %#v", x)
			}
			if x["content"] != "" {
				t.Fatalf("reasoning leaked to content: %#v", x)
			}
		}
	}

	if assistants != 1 {
		t.Fatalf("assistants=%d", assistants)
	}
}

func TestDeepSeekReasoningReplayIdentityAndPresence(t *testing.T) {
	m := *GetModel("deepseek", "deepseek-v4-pro")
	for _, tc := range []struct {
		name, foreign string
		blocks        []ContentBlock
		reasoning     *string
		content       string
	}{
		{"ordered-blocks", "", []ContentBlock{ThinkingContent{Thinking: " first\n"}, TextContent{Text: "answer"}, ThinkingContent{Thinking: "second "}}, strPtr3636(" first\nsecond "), "answer"},
		{"missing", "", []ContentBlock{TextContent{Text: "answer"}}, nil, "answer"},
		{"explicit-empty", "", []ContentBlock{ThinkingContent{ThinkingSignature: "reasoning_content"}, TextContent{Text: "answer"}}, strPtr3636(""), "answer"},
		{"unsigned-empty-existing-transform", "", []ContentBlock{ThinkingContent{}, TextContent{Text: "answer"}}, nil, "answer"},
		{"redacted", "", []ContentBlock{ThinkingContent{Redacted: true, Thinking: "opaque"}, TextContent{Text: "answer"}}, nil, "answer"},
		{"other-model", "model", []ContentBlock{ThinkingContent{Thinking: "foreign"}, TextContent{Text: "answer"}}, nil, "foreign\nanswer"},
		{"other-api", "api", []ContentBlock{ThinkingContent{Thinking: "foreign"}, TextContent{Text: "answer"}}, nil, "foreign\nanswer"},
		{"other-provider", "provider", []ContentBlock{ThinkingContent{Thinking: "foreign"}, TextContent{Text: "answer"}}, nil, "foreign\nanswer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := AssistantMessage{API: m.API, Provider: m.Provider, Model: m.ID, ResponseModel: "diagnostic-only", StopReason: StopReasonStop, Content: tc.blocks}
			switch tc.foreign {
			case "model":
				a.Model = "other"
			case "api":
				a.API = "other"
			case "provider":
				a.Provider = "other"
			}
			ctx := Context{Messages: []Message{a}}
			before, _ := json.Marshal(ctx)
			got := convertDeepSeekChatMessages(m, ctx)
			after, _ := json.Marshal(ctx)
			if string(before) != string(after) {
				t.Fatal("input mutated")
			}
			if len(got) != 1 {
				t.Fatalf("messages=%+v", got)
			}
			if got[0].Content != tc.content {
				t.Fatalf("content=%#v", got[0].Content)
			}
			if tc.reasoning == nil {
				if got[0].ReasoningContent != nil {
					t.Fatalf("fabricated reasoning %q", *got[0].ReasoningContent)
				}
			} else if got[0].ReasoningContent == nil || *got[0].ReasoningContent != *tc.reasoning {
				t.Fatalf("reasoning=%v want=%q", got[0].ReasoningContent, *tc.reasoning)
			}
		})
	}
}
func strPtr3636(s string) *string { return &s }

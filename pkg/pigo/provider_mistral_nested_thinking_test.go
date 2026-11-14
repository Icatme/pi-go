package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMistralNestedThinkingDelta(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"reason"}]},{"type":"text","text":"answer"}]},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	m := Model{ID: "fixture", Provider: "mistral", API: "mistral-conversations", BaseURL: s.URL}
	st := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context()})
	collectAssistantMessageEvents(st.Events())
	r := st.Result()
	for _, b := range r.Content {
		if q, ok := b.(ThinkingContent); ok && q.Thinking == "reason" {
			return
		}
	}
	t.Fatalf("dropped nested thinking: %+v", r)
}

func TestMistralNestedThinkingLifecycle(t *testing.T) {
	for _, textFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(textFirst), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if textFirst {
					fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"before"}}]}`+"\n\n")
				}
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":""},{"type":"reference","text":"NOT_TEXT"},{"type":"text","text":"a"}]},{"type":"text","text":"answer"}]}}]}`+"\n\n")
				w.(http.Flusher).Flush()
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":" b"},{"type":"tool_reference","text":"NOT_TEXT"}]}],"tool_calls":[{"index":0,"id":"call_1","function":{"name":"echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\ndata: [DONE]\n\n")
			}))
			defer s.Close()
			m := Model{ID: "fixture", Provider: "mistral", API: "mistral-conversations", BaseURL: s.URL}
			st := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context()})
			events := collectAssistantMessageEvents(st.Events())
			r := st.Result()
			ti, xi := 0, 1
			if textFirst {
				ti, xi = 1, 0
			}
			if len(r.Content) != 3 || r.Content[ti].(ThinkingContent).Thinking != "a b" || r.StopReason != StopReasonToolUse || r.Usage.TotalTokens != 5 {
				t.Fatalf("%+v", r)
			}
			want := "answer"
			if textFirst {
				want = "beforeanswer"
			}
			if r.Content[xi].(TextContent).Text != want || r.Content[2].(ToolCall).Name != "echo" {
				t.Fatalf("%+v", r)
			}
			starts, deltas, ends, done := 0, 0, 0, 0
			for _, e := range events {
				switch e.Type {
				case AssistantMessageEventThinkingStart:
					starts++
					if e.Partial.Content[ti].(ThinkingContent).Thinking != "" {
						t.Fatal("nonempty start snapshot")
					}
				case AssistantMessageEventThinkingDelta:
					deltas++
					if deltas == 1 && e.Partial.Content[ti].(ThinkingContent).Thinking != "a" {
						t.Fatal("mutated first delta snapshot")
					}
				case AssistantMessageEventThinkingEnd:
					ends++
				case AssistantMessageEventDone:
					done++
				}
			}
			if starts != 1 || deltas != 2 || ends != 1 || done != 1 {
				t.Fatalf("events start=%d delta=%d end=%d done=%d", starts, deltas, ends, done)
			}
		})
	}
}

func TestMistralEmptyNestedThinkingDoesNotCreateBlock(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":""},{"type":"text","text":12},{"type":"text","text":null},{"type":"reference","text":"hidden"}]}]},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	m := Model{ID: "fixture", Provider: "mistral", API: "mistral-conversations", BaseURL: s.URL}
	st := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context()})
	events := collectAssistantMessageEvents(st.Events())
	r := st.Result()
	if len(r.Content) != 0 || r.StopReason != StopReasonStop {
		t.Fatalf("%+v", r)
	}
	for _, e := range events {
		if e.Type == AssistantMessageEventThinkingStart || e.Type == AssistantMessageEventThinkingDelta || e.Type == AssistantMessageEventThinkingEnd {
			t.Fatal("empty thinking event")
		}
	}
}

func TestMistralLegacyThinkingTextFallback(t *testing.T) {
	for _, shape := range []string{"", `,"thinking":null`, `,"thinking":"wrong"`} {
		t.Run(shape, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":[{"type":"thinking","text":"legacy"%s}]},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n", shape)
			}))
			defer s.Close()
			m := Model{ID: "fixture", Provider: "mistral", API: "mistral-conversations", BaseURL: s.URL}
			st := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context()})
			collectAssistantMessageEvents(st.Events())
			r := st.Result()
			if len(r.Content) != 1 || r.Content[0].(TextContent).Text != "legacy" || r.StopReason != StopReasonStop {
				t.Fatalf("%+v", r)
			}
		})
	}
}

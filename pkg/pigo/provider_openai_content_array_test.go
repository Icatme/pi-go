package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompletionsTypedContentWire(t *testing.T) {
	for _, field := range []string{"delta", "message"} {
		t.Run(field, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"%s\":{\"content\":[{\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"plan\"}]},{\"type\":\"thinking\",\"thinking\":[{\"type\":\"text\",\"text\":\" more\"}]},{\"type\":\"text\",\"text\":\"Hello\"},{\"type\":\"text\",\"text\":\"!\"}]},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", field)
			}))
			defer srv.Close()
			stream := Stream(Model{Provider: "fixture", API: "openai-completions", ID: "test", BaseURL: srv.URL}, Context{}, ProviderStreamOptions{APIKey: "test"})
			var text, thinking string
			for e := range stream.Events() {
				if e.Type == AssistantMessageEventTextDelta {
					text += e.Delta
				}
				if e.Type == AssistantMessageEventThinkingDelta {
					thinking += e.Delta
				}
			}
			result := stream.Result()
			if result.StopReason != StopReasonStop || text != "Hello!" || thinking != "plan more" {
				t.Fatalf("result=%#v text=%q thinking=%q", result, text, thinking)
			}
		})
	}
}

func TestCompletionsTypedContentRejectsUnknown(t *testing.T) {
	for _, content := range []string{`[{"type":"image","url":"secret"}]`, `[{"type":"text","text":12}]`, `[{"type":"thinking","thinking":[{"type":"image"}]}]`, `[{"type":"reasoning","summary":[{"type":"other","text":"lost"}]}]`, `{}`, `42`} {
		response := AssistantMessage{}
		stream := newAssistantMessageEventStream()
		state := &openAICompletionsStreamState{ToolCalls: map[int]*openAICompletionsToolCallState{}}
		_, err := processOpenAICompletionsStreamEvent(`{"choices":[{"delta":{"content":`+content+`},"finish_reason":"stop"}]}`, Model{}, &response, stream, state)
		if err == nil || state.FinishSeen {
			t.Fatalf("accepted unsupported content %s: %v", content, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("raw payload leaked")
		}
	}
}

func TestCompletionsContentShapes(t *testing.T) {
	for _, tc := range []struct {
		raw, text string
		thinking  bool
	}{
		{`" plain "`, " plain ", false},
		{`[{"type":"thinking","thinking":" plan "}]`, " plan ", true},
		{`[{"type":"reasoning","summary":[{"type":"summary_text","text":" a"},{"type":"summary_text","text":"b "}]}]`, " ab ", true},
	} {
		parts, err := decodeOpenAICompletionsContent([]byte(tc.raw))
		if err != nil || len(parts) != 1 || parts[0].text != tc.text || parts[0].thinking != tc.thinking {
			t.Fatalf("%s => %#v, %v", tc.raw, parts, err)
		}
	}
	for _, raw := range []string{"", `null`, `[]`} {
		parts, err := decodeOpenAICompletionsContent([]byte(raw))
		if err != nil || len(parts) != 0 {
			t.Fatalf("%s => %#v, %v", raw, parts, err)
		}
	}
	for _, raw := range []string{`[{"type":"text","text":"ok"},{"type":"unknown"}]`, `[null]`, `["x"]`, `[{"type":"thinking","thinking":null}]`, `[{"type":"reasoning","summary":"lost"}]`} {
		parts, err := decodeOpenAICompletionsContent([]byte(raw))
		if err == nil || len(parts) != 0 {
			t.Fatalf("partially accepted %s => %#v, %v", raw, parts, err)
		}
	}
}

func TestCompletionsTypedContentRequiresFinish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"partial\"}]}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	stream := Stream(Model{Provider: "fixture", API: "openai-completions", ID: "test", BaseURL: srv.URL}, Context{}, ProviderStreamOptions{APIKey: "test"})
	for range stream.Events() {
	}
	if result := stream.Result(); result.StopReason != StopReasonError {
		t.Fatalf("missing finish accepted: %#v", result)
	}
}

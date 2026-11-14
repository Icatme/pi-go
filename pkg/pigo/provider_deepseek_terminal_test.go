package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDeepSeekRequiresTerminalFinishReason(t *testing.T) {
	for _, tc := range []struct {
		name, finish, tail string
		want               StopReason
	}{
		{"eof", "", "", StopReasonError}, {"done-only", "", "data: [DONE]\n\n", StopReasonError}, {"whitespace", " ", "", StopReasonError}, {"stop", "stop", "", StopReasonStop}, {"length", "length", "data: [DONE]\n\n", StopReasonLength}, {"filter", "content_filter", "", StopReasonError}, {"upstream-error", "", `data: {"error":{"message":"fixture upstream failure"}}` + "\n\n", StopReasonError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var count atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n%s", tc.finish, tc.tail)
			}))
			defer s.Close()
			m := *GetModel("deepseek", "deepseek-v4-pro")
			m.BaseURL = s.URL
			stream := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context(), MaxRetries: 2})
			events := collectAssistantMessageEvents(stream.Events())
			r := stream.Result()
			if r.StopReason != tc.want || count.Load() != 1 || r.Usage.Input != 3 || r.Usage.Output != 2 || len(r.Content) != 1 || r.Content[0].(TextContent).Text != "partial" {
				t.Fatalf("result=%+v count=%d", r, count.Load())
			}
			if tc.name == "eof" || tc.name == "done-only" || tc.name == "whitespace" {
				f, ok := ProviderFailureOf(r)
				if !ok || f.Kind != ProviderFailureStream || f.Reason != "missing_terminal" {
					t.Fatalf("failure=%+v", f)
				}
				errors, dones := 0, 0
				for _, e := range events {
					if e.Type == AssistantMessageEventError {
						errors++
					}
					if e.Type == AssistantMessageEventDone {
						dones++
					}
				}
				if errors != 1 || dones != 0 {
					t.Fatalf("errors%d dones%d", errors, dones)
				}
			}
			if tc.name == "upstream-error" && r.ErrorMessage != "fixture upstream failure" {
				t.Fatalf("replaced upstream error: %+v", r)
			}
		})
	}
}

func TestDeepSeekUnfinishedToolStreamIsFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"echo","arguments":"{\"value\":"}}]}}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	m := *GetModel("deepseek", "deepseek-v4-pro")
	m.BaseURL = s.URL
	stream := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context()})
	events := collectAssistantMessageEvents(stream.Events())
	r := stream.Result()
	if r.StopReason != StopReasonError || len(r.Content) != 1 || r.Content[0].(ToolCall).Name != "echo" {
		t.Fatalf("result=%+v", r)
	}
	for _, e := range events {
		if e.Type == AssistantMessageEventDone || e.Type == AssistantMessageEventToolCallEnd {
			t.Fatalf("false completion event: %+v", e)
		}
	}
}

package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestChatEmptyToolUseIsFailure(t *testing.T) {
	for _, tc := range []struct {
		name, delta, reason string
		want                StopReason
	}{
		{"empty-tool-calls", `{}`, "tool_calls", StopReasonError},
		{"empty-function-call", `{}`, "function_call", StopReasonError},
		{"text-without-call", `{"content":"prior text"}`, "tool_calls", StopReasonError},
		{"valid-tool", `{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"echo","arguments":"{}"}}]}`, "tool_calls", StopReasonToolUse},
		{"valid-tool-legacy-reason", `{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"echo","arguments":"{}"}}]}`, "function_call", StopReasonToolUse},
		{"stop-empty", `{}`, "stop", StopReasonStop},
		{"length-empty", `{}`, "length", StopReasonLength},
		{"unknown", `{}`, "future_reason", StopReasonError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s,\"finish_reason\":%q}]}\n\n", tc.delta, tc.reason)
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
			}))
			defer s.Close()
			m := Model{ID: "requested", Provider: "openai", API: "openai-completions", BaseURL: s.URL, MaxTokens: 64}
			stream := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture", MaxRetries: 2})
			events := collectAssistantMessageEvents(stream.Events())
			r := stream.Result()
			if r.StopReason != tc.want || requests.Load() != 1 || (tc.name != "unknown" && (r.Usage.Input != 3 || r.Usage.Output != 2)) || r.Model != m.ID {
				t.Fatalf("result=%+v requests=%d", r, requests.Load())
			}
			if (tc.reason == "tool_calls" || tc.reason == "function_call") && tc.want == StopReasonError {
				f, ok := ProviderFailureOf(r)
				if !ok || f.Kind != ProviderFailureParse || f.Reason != "missing_tool_call" {
					t.Fatalf("failure=%+v", f)
				}
				errs, dones := 0, 0
				for _, e := range events {
					if e.Type == AssistantMessageEventError {
						errs++
					}
					if e.Type == AssistantMessageEventDone {
						dones++
					}
				}
				if errs != 1 || dones != 0 {
					t.Fatalf("errors=%d done=%d", errs, dones)
				}
			}
			if tc.name == "text-without-call" && (len(r.Content) != 1 || r.Content[0].(TextContent).Text != "prior text") {
				t.Fatalf("lost partial: %+v", r.Content)
			}
		})
	}
}

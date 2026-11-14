package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestMistralUsageRejectsInvalidBeforeMutation(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name string
		u    mistralChatUsage
	}{
		{"negative-prompt", mistralChatUsage{PromptTokens: -1}},
		{"negative-output", mistralChatUsage{CompletionTokens: -1}},
		{"negative-total", mistralChatUsage{TotalTokens: -1}},
		{"prompt-output-overflow", mistralChatUsage{PromptTokens: max, CompletionTokens: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := AssistantMessage{Usage: Usage{Input: 3, Output: 2, TotalTokens: 5}, UsageReported: true}
			before := r
			err := applyMistralUsage(&r, Model{}, tc.u)
			if err == nil || !reflect.DeepEqual(r, before) {
				t.Fatalf("err=%v before=%+v after=%+v", err, before, r)
			}
			applyRequestError(&r, err)
			f, ok := ProviderFailureOf(r)
			if !ok || f.Kind != ProviderFailureParse || f.Reason != "invalid_usage" {
				t.Fatalf("%+v", f)
			}
		})
	}
	for _, u := range []mistralChatUsage{{}, {PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7}, {PromptTokens: 5, CompletionTokens: 2, TotalTokens: 99}, {PromptTokens: max, TotalTokens: max}} {
		r := AssistantMessage{}
		if err := applyMistralUsage(&r, Model{}, u); err != nil {
			t.Fatalf("valid %+v: %v", u, err)
		}
		if r.Usage.Input != u.PromptTokens || r.Usage.Output != u.CompletionTokens || r.Usage.TotalTokens != u.TotalTokens || r.Usage.CacheRead != 0 || r.Usage.CacheWrite != 0 {
			t.Fatalf("changed legal usage: %+v", r.Usage)
		}
	}
}
func TestMistralInvalidUsagePreservesEarlierSnapshot(t *testing.T) {
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n"+`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":-4,"total_tokens":-1}}`+"\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	m := Model{ID: "fixture", Provider: "mistral", API: "mistral-conversations", MaxTokens: 64}
	m.BaseURL = s.URL
	st := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context(), MaxRetries: 2})
	events := collectAssistantMessageEvents(st.Events())
	r := st.Result()
	if r.StopReason != StopReasonError || r.Usage.Input != 3 || r.Usage.Output != 2 || r.Usage.TotalTokens != 5 || !r.UsageReported || len(r.Content) != 1 || r.Content[0].(TextContent).Text != "partial" || count.Load() != 1 {
		t.Fatalf("%+v requests=%d", r, count.Load())
	}
	failure, ok := ProviderFailureOf(r)
	if !ok || failure.Kind != ProviderFailureParse || failure.Reason != "invalid_usage" {
		t.Fatalf("failure=%+v", failure)
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

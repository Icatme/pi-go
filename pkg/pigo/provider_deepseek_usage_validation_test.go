package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestDeepSeekUsageRejectsInvalidBeforeMutation(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name string
		u    deepSeekChatUsage
	}{
		{"negative-prompt", deepSeekChatUsage{PromptTokens: -1}},
		{"negative-output", deepSeekChatUsage{CompletionTokens: -1}},
		{"negative-total", deepSeekChatUsage{TotalTokens: -1}},
		{"negative-hit", deepSeekChatUsage{PromptCacheHitTokens: -1}},
		{"negative-miss", deepSeekChatUsage{PromptCacheMissTokens: -1}},
		{"negative-reasoning", deepSeekChatUsage{CompletionTokensDetails: &deepSeekCompletionTokenDetails{ReasoningTokens: -1}}},
		{"hit-exceeds-prompt", deepSeekChatUsage{PromptTokens: 3, PromptCacheHitTokens: 4}},
		{"miss-exceeds-prompt", deepSeekChatUsage{PromptTokens: 3, PromptCacheMissTokens: 4}},
		{"subtraction-wrap", deepSeekChatUsage{PromptTokens: -max - 1, PromptCacheHitTokens: 1}},
		{"prompt-output-overflow", deepSeekChatUsage{PromptTokens: max, CompletionTokens: 1}},
		{"selected-input-cache-overflow", deepSeekChatUsage{PromptTokens: max, PromptCacheHitTokens: max, PromptCacheMissTokens: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := AssistantMessage{Usage: Usage{Input: 3, Output: 2, TotalTokens: 5}, UsageReported: true}
			before := r
			err := applyDeepSeekUsage(&r, Model{}, tc.u)
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
	for _, u := range []deepSeekChatUsage{{}, {PromptTokens: 5, PromptCacheHitTokens: 5, CompletionTokens: 2, TotalTokens: 7}, {PromptTokens: 5, PromptCacheHitTokens: 2, PromptCacheMissTokens: 1, TotalTokens: 99}, {PromptTokens: max, PromptCacheHitTokens: max, TotalTokens: max}} {
		r := AssistantMessage{}
		if err := applyDeepSeekUsage(&r, Model{}, u); err != nil {
			t.Fatalf("valid boundary %+v: %v", u, err)
		}
		wantInput := u.PromptTokens - u.PromptCacheHitTokens
		if u.PromptCacheMissTokens > 0 {
			wantInput = u.PromptCacheMissTokens
		}
		if r.Usage.Input != wantInput || r.Usage.CacheRead != u.PromptCacheHitTokens || r.Usage.Output != u.CompletionTokens {
			t.Fatalf("changed legal usage: %+v", r.Usage)
		}
		if r.Usage.TotalTokens != u.TotalTokens {
			t.Fatal("rewrote total")
		}
	}
}
func TestDeepSeekInvalidUsagePreservesEarlierSnapshot(t *testing.T) {
	var count atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n"+`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":-4,"total_tokens":-1}}`+"\n\ndata: [DONE]\n\n")
	}))
	defer s.Close()
	m := *GetModel("deepseek", "deepseek-v4-pro")
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

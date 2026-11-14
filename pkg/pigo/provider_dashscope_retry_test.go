package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDashScopeThrottleRetry(t *testing.T) {
	for _, tc := range []struct {
		name, provider, code, message string
		retries                       int
		want                          int32
	}{
		{"dashscope throttle", "dashscope", "insufficient_quota", "You exceeded your current quota, please check your plan and billing details.", 1, 2},
		{"bailian throttle", "bailian", "Throttling.AllocationQuota", "Allocated quota exceeded, please increase your quota limit.", 1, 2},
		{"openai quota", "openai", "insufficient_quota", "You exceeded your current quota, please check your plan and billing details.", 1, 1},
		{"free quota", "dashscope", "insufficient_quota", "Free allocated quota exceeded.", 1, 1},
		{"prepaid overdue", "dashscope", "PrepaidBillOverdue", "The prepaid bill is overdue.", 1, 1},
		{"postpaid overdue", "dashscope", "PostpaidBillOverdue", "The postpaid bill is overdue.", 1, 1},
		{"zero retries", "dashscope", "insufficient_quota", "You exceeded your current quota, please check your plan and billing details.", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(429)
					fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, tc.code, tc.message)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			model := Model{ID: "fixture", API: "openai-completions", Provider: Provider(tc.provider), BaseURL: server.URL, MaxTokens: 256}
			result := CompleteSimple(model, Context{Messages: []Message{UserMessage{Content: "hi"}}}, SimpleStreamOptions{APIKey: "fixture", MaxRetries: tc.retries, MaxRetryDelay: 1})
			if calls.Load() != tc.want {
				t.Fatalf("calls=%d want=%d error=%s", calls.Load(), tc.want, result.ErrorMessage)
			}
		})
	}
}

func TestDashScopeThrottleClassifierIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name, provider, code, kind, message string
		status                              int
		want                                bool
	}{
		{"valid", "alibaba", "insufficient_quota", "", "Allocated quota exceeded, please increase your quota limit.", 429, true},
		{"unknown provider", "other", "insufficient_quota", "", "Allocated quota exceeded, please increase your quota limit.", 429, false},
		{"wrong status", "dashscope", "insufficient_quota", "", "Allocated quota exceeded, please increase your quota limit.", 400, false},
		{"unknown message", "dashscope", "insufficient_quota", "", "unrecognized quota exhaustion", 429, false},
		{"free exhaustion", "dashscope", "insufficient_quota", "", "Free allocated quota exceeded.", 429, false},
		{"overdue type", "dashscope", "insufficient_quota", "PrepaidBillOverdue", "Allocated quota exceeded, please increase your quota limit.", 429, false},
		{"monthly limit", "dashscope", "insufficient_quota", "", "month allocated quota exceeded", 429, false},
		{"overdue code", "dashscope", "PostpaidBillOverdue", "", "Allocated quota exceeded, please increase your quota limit.", 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"error":{"code":%q,"type":%q,"message":%q}}`, tc.code, tc.kind, tc.message))
			if got := isDashScopeTokenThrottle(Model{Provider: Provider(tc.provider)}, tc.status, body); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestDashScopeRetryBudgetAndBillingTypes(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int32
	}{
		{"persistent throttle", `{"error":{"code":"insufficient_quota","message":"Allocated quota exceeded, please increase your quota limit."}}`, 3},
		{"budget", `{"error":{"code":"BudgetLimitExceeded","message":"exhausted"}}`, 1},
		{"not purchased", `{"error":{"code":"CommodityNotPurchased","message":"not purchased"}}`, 1},
		{"type only overdue", `{"error":{"type":"PrepaidBillOverdue","message":"overdue"}}`, 1},
		{"throttle code billing type", `{"error":{"code":"insufficient_quota","type":"PostpaidBillOverdue","message":"Allocated quota exceeded, please increase your quota limit."}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			model := Model{ID: "fixture", API: "openai-completions", Provider: "dashscope", BaseURL: server.URL, MaxTokens: 256}
			result := CompleteSimple(model, Context{Messages: []Message{UserMessage{Content: "hi"}}}, SimpleStreamOptions{APIKey: "fixture", MaxRetries: 2, MaxRetryDelay: 1})
			if calls.Load() != tc.want || result.StopReason != StopReasonError {
				t.Fatalf("calls=%d want=%d stop=%s", calls.Load(), tc.want, result.StopReason)
			}
		})
	}
}

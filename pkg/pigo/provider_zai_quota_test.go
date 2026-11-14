package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAudit11537GLMHardQuota(t *testing.T) {
	for _, api := range []API{"openai-completions", "anthropic-messages"} {
		t.Run(string(api), func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				fmt.Fprint(w, `{"error":{"code":"1308","message":"已达到 5 小时的使用上限。您的限额将在 2026-10-29 02:54:39 重置。"}}`)
			}))
			defer s.Close()
			result := CompleteSimple(Model{ID: "glm-fixture", API: api, Provider: "zai", BaseURL: s.URL, MaxTokens: 256}, Context{}, SimpleStreamOptions{APIKey: "fixture", MaxRetries: 2, MaxRetryDelay: 1})
			if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "2026-10-29 02:54:39") {
				t.Fatalf("quota diagnostic lost: %+v", result)
			}
			if status, ok := ProviderHTTPStatusOf(result); !ok || status != 429 {
				t.Fatalf("HTTP status lost: %d %v", status, ok)
			}
			if calls.Load() != 1 {
				t.Fatalf("hard quota retried: calls=%d result=%+v", calls.Load(), result)
			}
		})
	}
}

func TestZaiQuotaProviderAndCodeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, provider, body string
		status               int
		terminal             bool
	}{
		{"nested string", "zai", `{"error":{"code":"1308","message":"quota reset later"}}`, 429, true},
		{"nested numeric", "zhipu", `{"error":{"code":1308}}`, 429, true},
		{"top level", "bigmodel", `{"code":"1308"}`, 429, true},
		{"other provider", "other", `{"error":{"code":"1308"}}`, 429, false},
		{"model name not opt in", "openai", `{"error":{"code":"1308"}}`, 429, false},
		{"wrong status", "zai", `{"error":{"code":"1308"}}`, 503, false},
		{"rate limit", "zai", `{"error":{"code":"1302"}}`, 429, false},
		{"overloaded", "zai", `{"error":{"code":"1305"}}`, 429, false},
		{"prose only", "zai", `{"error":{"message":"1308"}}`, 429, false},
		{"nested wins", "zai", `{"code":"1308","error":{"code":"1302"}}`, 429, false},
		{"malformed", "zai", `{"error":{"code":"1308"}`, 429, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{Provider: Provider(tc.provider), ID: "glm-5", BaseURL: "https://open.bigmodel.cn/api/paas/v4"}
			if got := isZaiPeriodicQuota(m, tc.status, []byte(tc.body)); got != tc.terminal {
				t.Fatalf("terminal=%v", got)
			}
			if got := shouldRetryCompletionsModelResponse(m, tc.status, []byte(tc.body), ""); got == tc.terminal {
				t.Fatalf("retry=%v terminal=%v", got, tc.terminal)
			}
		})
	}
}

package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDeepSeekURLQueryHTTP(t *testing.T) {
	for _, base := range []string{"/deploy?api-version=2025&x=a%2Fb&x=c/", "/deploy/chat/completions?api-version=2025&x=a%2Fb&x=c/"} {
		t.Run(base, func(t *testing.T) {
			var uri string
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Errorf("authorization changed")
				}
				uri = r.RequestURI
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer s.Close()
			m := *GetModel("deepseek", "deepseek-v4-pro")
			m.BaseURL = s.URL + base
			stream := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture"})
			for range stream.Events() {
			}
			r := stream.Result()
			if requests.Load() != 1 {
				t.Fatalf("requests=%d", requests.Load())
			}
			if r.StopReason != StopReasonStop {
				t.Fatalf("result=%+v", r)
			}
			if uri != "/deploy/chat/completions?api-version=2025&x=a%2Fb&x=c/" {
				t.Fatalf("wire URI=%q", uri)
			}
		})
	}
}
func TestDeepSeekURLComponents(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "https://api.deepseek.com/chat/completions"},
		{"https://example.test/", "https://example.test/chat/completions"},
		{"https://example.test/v1///", "https://example.test/v1/chat/completions"},
		{"https://example.test/deploy/chat/completions/", "https://example.test/deploy/chat/completions"},
		{"https://user:pass@example.test/a%2fb/../x?x=%2f&x=/+#part%2f/", "https://user:pass@example.test/a%2fb/../x/chat/completions?x=%2f&x=/+#part%2f/"},
		{"https://example.test?", "https://example.test/chat/completions?"},
		{"https://example.test/deploy#note/chat/completions", "https://example.test/deploy/chat/completions#note/chat/completions"},
		{"https://example.test/deploy%2F?x=1", "https://example.test/deploy%2F/chat/completions?x=1"},
		{"https://example.test/%zz", "https://example.test/%zz/chat/completions"},
		{"https://example.test/%zz?x=1", "https://example.test/%zz?x=1/chat/completions"},
		{"opaque:value?x=1", "opaque:value?x=1/chat/completions"},
		{"https://example.test/chat%2Fcompletions?x=1", "https://example.test/chat%2Fcompletions/chat/completions?x=1"},
	} {
		if got := resolveDeepSeekChatURL(tc.in); got != tc.want {
			t.Errorf("%q got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestDeepSeekURLWithoutQueryKeepsLegacy(t *testing.T) {
	for _, base := range []string{"", " / ", "https://example.test", "https://example.test/", "https://example.test/v1", "https://example.test/chat/completions", "https://example.test/custom/", "https://example.test/%2F", "https://example.test/a%2fb/", "relative", "opaque:value"} {
		if got, want := resolveDeepSeekChatURL(base), resolveLegacyDeepSeekChatURL(base); got != want {
			t.Errorf("%q got %q want legacy %q", base, got, want)
		}
	}
}

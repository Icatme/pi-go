package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCommandCodeGenerateURLQueryHTTP(t *testing.T) {
	for _, base := range []string{"/provider/v1?x=a%2Fb&x=c/", "/alpha/generate?x=a%2Fb&x=c/"} {
		t.Run(base, func(t *testing.T) {
			var uri string
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				uri = r.RequestURI
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("auth changed")
				}
				fmt.Fprintln(w, `{"type":"text-delta","text":"ok"}`)
				fmt.Fprintln(w, `{"type":"finish","finishReason":"stop"}`)
			}))
			defer s.Close()
			stream := commandCodeGenerateSimpleForTest(commandCodeTestModel(s.URL+base), Context{}, SimpleStreamOptions{APIKey: "fixture"})
			for range stream.Events() {
			}
			result := stream.Result()
			if result.StopReason != StopReasonStop || calls.Load() != 1 {
				t.Fatalf("result%+v calls%d", result, calls.Load())
			}
			if uri != "/alpha/generate?x=a%2Fb&x=c/" {
				t.Errorf("URI=%q", uri)
			}
		})
	}
}
func TestCommandCodeGenerateURLComponents(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://example.test?", "https://example.test/alpha/generate?"},
		{"https://example.test/provider/v1/?x=1", "https://example.test/alpha/generate?x=1"},
		{"https://example.test/base/provider/v1?x=1", "https://example.test/base/alpha/generate?x=1"},
		{"https://example.test/custom#note/alpha/generate", "https://example.test/custom/alpha/generate#note/alpha/generate"},
		{"https://user:pass@example.test/a%2fb/../provider/v1?x=%2f&x=/+#frag%2f/", "https://user:pass@example.test/a%2fb/../alpha/generate?x=%2f&x=/+#frag%2f/"},
		{"https://example.test/provider%2Fv1?x=1", "https://example.test/provider%2Fv1/alpha/generate?x=1"},
		{"https://example.test/%zz?x=1", "https://example.test/%zz?x=1/alpha/generate"},
		{"opaque:value?x=1", "opaque:value?x=1/alpha/generate"},
	} {
		if got := resolveCommandCodeGenerateURL(tc.in); got != tc.want {
			t.Errorf("%q got%q want%q", tc.in, got, tc.want)
		}
	}
	for _, s := range []string{"https://example.test", "https://example.test/", "https://example.test/provider/v1", "https://example.test/alpha/generate", "https://example.test/custom/", "https://example.test/a%2fb/", "relative", "opaque:value"} {
		if got, want := resolveCommandCodeGenerateURL(s), resolveLegacyCommandCodeGenerateURL(s); got != want {
			t.Errorf("%q got%q legacy%q", s, got, want)
		}
	}
}

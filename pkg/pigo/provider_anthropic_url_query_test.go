package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicQueryWire(t *testing.T) {
	for _, provider := range []Provider{"anthropic", "kimi-coding"} {
		for _, tc := range []struct{ base, want string }{
			{"/proxy?key=a%2Fb&x=1&x=2#private", "/proxy/v1/messages?key=a%2Fb&x=1&x=2"},
			{"/proxy?key=a+b%2Bc", "/proxy/v1/messages?key=a+b%2Bc"},
			{"/proxy#private", "/proxy/v1/messages"},
			{"/proxy%2F?x=1", "/proxy%2F/v1/messages?x=1"},
			{"/proxy%2F/?x=1", "/proxy%2F/v1/messages?x=1"},
			{"/coding", "/coding/v1/messages"},
		} {
			t.Run(string(provider)+tc.base, func(t *testing.T) {
				var uri string
				count := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					count++
					if provider == "anthropic" && r.Header.Get("x-api-key") != "fixture" {
						t.Error("auth changed")
					}
					if provider == "kimi-coding" && r.Header.Get("x-api-key") != "fixture" {
						t.Error("auth changed")
					}
					uri = r.RequestURI
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				}))
				defer srv.Close()
				m := Model{ID: "fixture", Provider: provider, API: "anthropic-messages", BaseURL: srv.URL + tc.base, MaxTokens: 128}
				s := Stream(m, Context{Messages: []Message{UserMessage{Content: "hi"}}}, ProviderStreamOptions{APIKey: "fixture"})
				defer s.Close()
				r := s.Result()
				if r.StopReason != StopReasonStop || count != 1 || uri != tc.want {
					t.Fatalf("uri=%q count=%d stop=%s err=%s", uri, count, r.StopReason, r.ErrorMessage)
				}
			})
		}
	}
}

func TestAnthropicMessagesURLComponents(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"https://example.test?x=a%2Fb&x=2", "https://example.test/v1/messages?x=a%2Fb&x=2"},
		{"https://example.test/root#private", "https://example.test/root/v1/messages#private"},
		{"https://example.test/a%2Fb?sig=a+b%2Bc", "https://example.test/a%2Fb/v1/messages?sig=a+b%2Bc"},
		{"https://example.test/proxy%2F?x=1", "https://example.test/proxy%2F/v1/messages?x=1"},
		{"https://example.test?", "https://example.test/v1/messages?"},
		{"https://example.test/coding", "https://example.test/coding/v1/messages"},
		{"https://example.test/v1?x=1", "https://example.test/v1/v1/messages?x=1"},
		{"https://example.test/v1/", "https://example.test/v1/v1/messages"},
		{"relative?x=1", "relative?x=1/v1/messages"},
		{"https:opaque?x=1", "https:opaque?x=1/v1/messages"},
		{"https://%zz?x=1", "https://%zz?x=1/v1/messages"},
	} {
		if got := resolveAnthropicMessagesURL(tc.base); got != tc.want {
			t.Errorf("%q got %q want %q", tc.base, got, tc.want)
		}
	}
}

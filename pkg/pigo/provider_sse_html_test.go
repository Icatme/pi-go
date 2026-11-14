package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChatHTMLResponseDiagnosticPreservesSSECompatibility(t *testing.T) {
	complete := "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n"
	partial := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	usage := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n"
	for _, tc := range []struct {
		name, mime, body, reason string
		status                   int
		stop                     StopReason
	}{
		{"html", "text/html; charset=utf-8", "<!doctype html><title>private console</title>", "unexpected_content_type", 200, StopReasonError},
		{"case_parameters", "Text/HTML; charset=UTF-8", "private console", "unexpected_content_type", 200, StopReasonError},
		{"empty_html", "text/html", "", "unexpected_content_type", 200, StopReasonError},
		{"valid_wrong_mime", "text/html", complete, "", 200, StopReasonStop},
		{"partial_wrong_mime", "text/html", partial, "missing_terminal", 200, StopReasonError},
		{"usage_wrong_mime", "text/html", usage, "missing_terminal", 200, StopReasonError},
		{"done_only", "text/html", "data: [DONE]\n\n", "missing_terminal", 200, StopReasonError},
		{"normal_sse", "text/event-stream", complete, "", 200, StopReasonStop},
		{"plain_sse", "text/plain", complete, "", 200, StopReasonStop},
		{"missing_mime", "", "private console", "missing_terminal", 200, StopReasonError},
		{"malformed_mime", "text/html; charset=\"", "private console", "missing_terminal", 200, StopReasonError},
		{"http_error", "text/html", "private console", "http_error", 400, StopReasonError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header()["Content-Type"] = []string{tc.mime}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer s.Close()
			m := Model{ID: "fixture", API: "openai-completions", Provider: "openai", BaseURL: s.URL, MaxTokens: 256}
			r := Complete(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context(), MaxRetries: 2})
			if r.StopReason != tc.stop || requests.Load() != 1 {
				t.Fatalf("stop=%s requests=%d error=%s", r.StopReason, requests.Load(), r.ErrorMessage)
			}
			if tc.reason != "" {
				f, ok := ProviderFailureOf(r)
				if !ok || f.Reason != tc.reason {
					t.Fatalf("failure=%+v error=%s", f, r.ErrorMessage)
				}
			}
			if tc.reason == "unexpected_content_type" {
				f, _ := ProviderFailureOf(r)
				if f.Kind != ProviderFailureParse || !strings.Contains(r.ErrorMessage, "HTTP 200") || !strings.Contains(r.ErrorMessage, "text/html") || strings.Contains(r.ErrorMessage, "private") {
					t.Fatal(r.ErrorMessage)
				}
				if _, ok := ProviderHTTPStatusOf(r); ok {
					t.Fatal("changed non-2xx status contract")
				}
			}
			if tc.name == "partial_wrong_mime" && (len(r.Content) != 1 || r.Content[0].(TextContent).Text != "partial") {
				t.Fatal("lost partial")
			}
			if tc.name == "usage_wrong_mime" && r.Usage.TotalTokens != 3 {
				t.Fatal("lost usage")
			}
		})
	}
}

func TestChatHTMLDiagnosticUsesFinalHTTPAttempt(t *testing.T) {
	for _, html := range []bool{false, true} {
		t.Run(fmt.Sprint(html), func(t *testing.T) {
			var requests, observations atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(503)
					fmt.Fprint(w, "temporarily unavailable")
					return
				}
				if html {
					w.Header().Set("Content-Type", "text/html")
				} else {
					w.Header().Set("Content-Type", "text/plain")
				}
				w.WriteHeader(200)
			}))
			defer s.Close()
			m := Model{ID: "fixture", API: "openai-completions", Provider: "openai", BaseURL: s.URL, MaxTokens: 256}
			r := Complete(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context(), MaxRetries: 1, MaxRetryDelay: 1, OnResponse: func(ProviderResponse, Model) { observations.Add(1) }})
			f, ok := ProviderFailureOf(r)
			want := "missing_terminal"
			if html {
				want = "unexpected_content_type"
			}
			if !ok || f.Reason != want || requests.Load() != 2 || observations.Load() != 2 {
				t.Fatalf("failure=%+v requests=%d observations=%d", f, requests.Load(), observations.Load())
			}
		})
	}
}

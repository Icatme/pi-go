package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type responsesQueryTransportFunc func(*http.Request) (*http.Response, error)

func (f responsesQueryTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResponsesQueryWire(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"/deploy?api-version=2025&x=a%2Fb&x=c/", "/deploy/responses?api-version=2025&x=a%2Fb&x=c/"},
		{"/deploy/responses?api-version=2025&x=a%2Fb&x=c/", "/deploy/responses?api-version=2025&x=a%2Fb&x=c/"},
		{"/a%2fb/responses/?z=2&a=1&a=%2f&plus=+&space=%20#fragment/", "/a%2fb/responses?z=2&a=1&a=%2f&plus=+&space=%20"},
		{"/deploy%2F?x=1", "/deploy%2F/responses?x=1"},
		{"/deploy?", "/deploy/responses?"},
		{"/deploy#fragment/responses", "/deploy/responses"},
		{"?x=1", "/v1/responses?x=1"},
		{"/deploy", "/deploy/responses"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			var requests, transports, observations atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.RequestURI != tc.want {
					t.Errorf("wire URI=%q want=%q", r.RequestURI, tc.want)
				}
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("X-Custom") != "kept" {
					t.Errorf("method/header changed: %s %v", r.Method, r.Header)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-Trace", "fixture")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer s.Close()
			client := &http.Client{Transport: responsesQueryTransportFunc(func(r *http.Request) (*http.Response, error) {
				transports.Add(1)
				return http.DefaultTransport.RoundTrip(r)
			})}
			m := Model{ID: "fixture", Provider: "openai", API: "openai-responses", BaseURL: s.URL + tc.base, MaxTokens: 64}
			st := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context(), HTTPClient: client, Headers: map[string]string{"X-Custom": "kept"}, OnResponse: func(r ProviderResponse, model Model) {
				observations.Add(1)
				if r.Status != 200 || r.Headers["X-Request-Trace"] != "fixture" || model.ID != m.ID {
					t.Errorf("observer=%+v model=%+v", r, model)
				}
			}})
			for range st.Events() {
			}
			r := st.Result()
			if r.StopReason != StopReasonStop || requests.Load() != 1 || transports.Load() != 1 || observations.Load() != 1 {
				t.Fatalf("response=%+v counts=%d/%d/%d", r, requests.Load(), transports.Load(), observations.Load())
			}
		})
	}
}

func TestResponsesAbsoluteQueryURLComponents(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://u:p@example.test/a%2fb/../x?z=1&z=/+#f%2f/", "https://u:p@example.test/a%2fb/../x/responses?z=1&z=/+#f%2f/"},
		{"https://example.test/responses?", "https://example.test/responses?"},
		{"https://example.test?x=1", "https://example.test/v1/responses?x=1"},
		{"https://example.test/responses%2F?x=1", "https://example.test/responses%2F/responses?x=1"},
		{"https://example.test/v1#f", "https://example.test/v1/responses#f"},
	} {
		if got := resolveOpenAIResponsesAbsoluteRequestURL(tc.in); got != tc.want {
			t.Errorf("%q got=%q want=%q", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{"", "https://example.test/v1", "https://example.test/responses", "https://example.test/%zz?x=1", "opaque:value?x=1", "/relative?x=1", "//example.test/path?x=1", "ftp://example.test/a?x=1", "https:?x=1", " https://example.test/v1?x=1"} {
		if got := resolveOpenAIResponsesAbsoluteRequestURL(in); got != "" {
			t.Errorf("expected legacy fallback for %q got=%q", in, got)
		}
	}
}

func TestResponsesQueryRetryPolicyUnchanged(t *testing.T) {
	for _, maxRetries := range []int{0, 1} {
		t.Run(fmt.Sprint(maxRetries), func(t *testing.T) {
			var count atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.RequestURI != "/deploy/responses?z=%2f&z=/" {
					t.Errorf("retry URI=%q", r.RequestURI)
				}
				if count.Add(1) == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(503)
					fmt.Fprint(w, `{"error":{"message":"fixture unavailable"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			}))
			defer s.Close()
			r := Complete(Model{ID: "fixture", API: "openai-responses", Provider: "openai", BaseURL: s.URL + "/deploy/responses?z=%2f&z=/"}, Context{}, ProviderStreamOptions{APIKey: "fixture", RequestContext: t.Context(), MaxRetries: maxRetries, MaxRetryDelay: 1})
			want := StopReasonError
			if maxRetries == 1 {
				want = StopReasonStop
			}
			if count.Load() != int32(maxRetries+1) || r.StopReason != want {
				t.Fatalf("attempts=%d stop=%s want=%s", count.Load(), r.StopReason, want)
			}
		})
	}
}

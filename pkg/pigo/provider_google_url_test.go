package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleURLHTTPRouting(t *testing.T) {
	for _, suffix := range []string{"/v1beta", "/v1beta?tenant=a%2Fb&tenant=c", "/v1beta#fragment", "/proxy%2F?%61lt=sse&alt=sse&x=%2f"} {
		t.Run(suffix, func(t *testing.T) {
			var path, query, key string
			count := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				path = r.URL.EscapedPath()
				query = r.URL.RawQuery
				key = r.Header.Get("x-goog-api-key")
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
			}))
			defer srv.Close()
			result := Complete(Model{ID: "fixture", Provider: "google", API: "google-generative-ai", BaseURL: srv.URL + suffix}, Context{}, ProviderStreamOptions{APIKey: "fixture-key"})
			wantQuery := "alt=sse"
			if suffix == "/v1beta?tenant=a%2Fb&tenant=c" {
				wantQuery = "tenant=a%2Fb&tenant=c&alt=sse"
			}
			wantPath := "/v1beta/models/fixture:streamGenerateContent"
			if strings.HasPrefix(suffix, "/proxy") {
				wantPath = "/proxy%2F/models/fixture:streamGenerateContent"
				wantQuery = "%61lt=sse&alt=sse&x=%2f"
			}
			if path != wantPath || query != wantQuery || count != 1 || key != "fixture-key" || result.StopReason != StopReasonStop {
				t.Fatalf("path=%q query=%q requests=%d key=%q stop=%v", path, query, count, key, result.StopReason)
			}
		})
	}
}

func TestGoogleURLComponents(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"https://example.test/v1beta?x=a%2Fb&x=c/", "https://example.test/v1beta/models/m:streamGenerateContent?x=a%2Fb&x=c/&alt=sse"},
		{"https://example.test/proxy%2F?%61lt=sse&alt=sse&x=%2f#frag", "https://example.test/proxy%2F/models/m:streamGenerateContent?%61lt=sse&alt=sse&x=%2f#frag"},
		{"https://example.test/custom/../prefix?#/models/m:streamGenerateContent", "https://example.test/custom/../prefix/models/m:streamGenerateContent?alt=sse#/models/m:streamGenerateContent"},
	} {
		got, err := resolveGoogleStreamURL(tc.base, "m")
		if err != nil || got != tc.want {
			t.Fatalf("got %q err %v want %q", got, err, tc.want)
		}
	}
	for _, base := range []string{"https://example.test", "https://example.test/v1beta/", "https://example.test/proxy%2F", "relative", "https://example.test/%zz?x=y", "opaque:thing?x=y"} {
		got, err := resolveGoogleStreamURL(base, "m")
		want := strings.TrimRight(strings.TrimSpace(base), "/") + "/models/m:streamGenerateContent?alt=sse"
		if err != nil || got != want {
			t.Fatalf("legacy mismatch %q %v", got, err)
		}
	}
}
func TestGoogleURLInvalidQueryZeroHTTP(t *testing.T) {
	for _, q := range []string{"alt=json", "alt", "alt=", "alt=sse&alt=json", "%61lt=json", "secret=%zz", "secret=x;y"} {
		t.Run(q, func(t *testing.T) {
			count := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count++ }))
			defer srv.Close()
			result := Complete(Model{ID: "fixture", Provider: "google", API: "google-generative-ai", BaseURL: srv.URL + "/v1beta?" + q}, Context{}, ProviderStreamOptions{APIKey: "fixture-key"})
			failure, ok := ProviderFailureOf(result)
			if !ok || failure.Kind != ProviderFailureSDK || failure.Reason != "invalid_stream_query" {
				t.Fatalf("typed failure missing: %+v", result)
			}
			if count != 0 || result.StopReason != StopReasonError || strings.Contains(result.ErrorMessage, q) || !strings.Contains(result.ErrorMessage, "Google streaming URL") {
				t.Fatalf("count=%d result=%+v", count, result)
			}
		})
	}
}

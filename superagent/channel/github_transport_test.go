package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/superagent"
)

func githubHTTPRequest() Request {
	return Request{Protocol: GitHub, Source: "github:11", Organization: "11", Thread: "22/pull/3", Delivery: superagent.ReplyDelivery{ReplyID: "reply-1", Attempt: 1}, IdempotencyKey: "reply-1", Body: json.RawMessage(`{"body":"approved <text>"}`)}
}
func githubHTTPTransport(t *testing.T, s *httptest.Server) *GitHubTransport {
	t.Helper()
	tr, err := NewGitHubTransport(GitHubTransportConfig{Source: "github:11", OwnerID: "11", APIBaseURL: s.URL, Client: s.Client(), Destinations: map[string]GitHubDestination{"22/pull/3": {Owner: "owner", Repository: "repo"}}})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}
func githubHTTPResponse(base string) string {
	return fmt.Sprintf(`{"id":45,"body":"approved <text>","issue_url":%q,"url":%q}`, base+"/repos/owner/repo/issues/3", base+"/repos/owner/repo/issues/comments/45")
}

func TestGitHubTransportSuccess(t *testing.T) {
	var calls atomic.Int32
	var s *httptest.Server
	s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.RequestURI() != "/repos/owner/repo/issues/3/comments" {
			t.Errorf("destination: %s %s", r.Method, r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != string(githubHTTPRequest().Body) {
			t.Errorf("changed body %q", b)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("headers: %v", r.Header)
		}
		if r.Header.Get("Idempotency-Key") != "" || r.Header.Get("X-Idempotency-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected credentials or idempotency: %v", r.Header)
		}
		w.WriteHeader(201)
		io.WriteString(w, githubHTTPResponse(s.URL))
	}))
	defer s.Close()
	tr := githubHTTPTransport(t, s)
	req := githubHTTPRequest()
	got, err := tr.Send(context.Background(), req)
	if err != nil || got.Status != "delivered" || got.MessageID != "45" || got.Delivery != req.Delivery || got.Source != req.Source || got.Thread != req.Thread || got.Organization != req.Organization {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = tr.Lookup(context.Background(), req)
	if got.Status != "unknown" || calls.Load() != 1 {
		t.Fatalf("lookup: %+v %v calls=%d", got, err, calls.Load())
	}
}

func TestGitHubTransportAmbiguousResponses(t *testing.T) {
	for _, kind := range []string{"500", "404", "403", "redirect", "lost_ack", "malformed", "duplicate", "oversized", "foreign_issue", "foreign_comment", "wrong_body", "wrong_id", "trailing", "surrogate"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			var s *httptest.Server
			s = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body := githubHTTPResponse(s.URL)
				switch kind {
				case "500":
					w.WriteHeader(500)
				case "404":
					w.WriteHeader(404)
				case "403":
					w.WriteHeader(403)
				case "redirect":
					w.Header().Set("Location", s.URL+"/redirected")
					w.WriteHeader(307)
				case "lost_ack":
					c, _, _ := w.(http.Hijacker).Hijack()
					c.Close()
					return
				default:
					w.WriteHeader(201)
				}
				switch kind {
				case "malformed":
					body = `{"id":`
				case "duplicate":
					body = strings.Replace(body, `"id":45`, `"id":45,"id":45`, 1)
				case "oversized":
					body = strings.Repeat(" ", 1<<20) + body
				case "foreign_issue":
					body = strings.Replace(body, "/issues/3\"", "/issues/4\"", 1)
				case "foreign_comment":
					body = strings.Replace(body, "/issues/comments/45", "/pulls/comments/45", 1)
				case "wrong_body":
					body = strings.Replace(body, "approved", "changed", 1)
				case "wrong_id":
					body = strings.Replace(body, `"id":45`, `"id":46`, 1)
				case "trailing":
					body += ` {}`
				case "surrogate":
					body = strings.Replace(body, "approved", `\ud800`, 1)
				}
				io.WriteString(w, body)
			}))
			defer s.Close()
			got, err := githubHTTPTransport(t, s).Send(context.Background(), githubHTTPRequest())
			if got.Status != "unknown" || err == nil || calls.Load() != 1 {
				t.Fatalf("%+v %v calls=%d", got, err, calls.Load())
			}
		})
	}
}

func TestGitHubTransportRateLimits(t *testing.T) {
	for _, tc := range []struct {
		code                    int
		remaining, reset, retry string
		want                    string
	}{
		{403, "0", fmt.Sprint(time.Now().Add(time.Hour).Unix()), "", "rate_limited"},
		{429, "", "", "60", "rate_limited"},
		{429, "", "", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), "rate_limited"},
		{403, "1", "", "60", "rate_limited"}, {403, "0", "invalid", "", "unknown"},
		{429, "", "", "", "unknown"}, {429, "", "", "-1", "unknown"},
	} {
		t.Run(fmt.Sprintf("%d/%s/%s", tc.code, tc.remaining, tc.retry), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", tc.remaining)
				w.Header().Set("X-RateLimit-Reset", tc.reset)
				w.Header().Set("Retry-After", tc.retry)
				w.WriteHeader(tc.code)
				io.WriteString(w, `{"message":"rate limit"}`)
			}))
			defer s.Close()
			before := time.Now().UnixMilli()
			got, err := githubHTTPTransport(t, s).Send(context.Background(), githubHTTPRequest())
			if got.Status != tc.want || (tc.want == "rate_limited" && (err != nil || got.RetryAt <= before)) {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestGitHubTransportRejectsRequestsBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer s.Close()
	tr := githubHTTPTransport(t, s)
	for _, edit := range []func(*Request){func(r *Request) { r.Source = "github:12" }, func(r *Request) { r.Organization = "12" }, func(r *Request) { r.Thread = "22/pull/4" }, func(r *Request) { r.Protocol = IM }, func(r *Request) { r.Delivery.Attempt = 0 }, func(r *Request) { r.IdempotencyKey = "other" }, func(r *Request) { r.Body = []byte(`{"body":"x","extra":1}`) }, func(r *Request) { r.Body = []byte(`{"body":null}`) }, func(r *Request) { r.Body = []byte(`{"body":" "}`) }} {
		r := githubHTTPRequest()
		edit(&r)
		got, err := tr.Send(context.Background(), r)
		if err == nil || got.Status != "unknown" {
			t.Fatalf("%+v %v", got, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request sent")
	}
}

func TestGitHubTransportConfiguration(t *testing.T) {
	base := func() GitHubTransportConfig {
		return GitHubTransportConfig{Source: "github:11", OwnerID: "11", APIBaseURL: "https://api.github.com", Client: &http.Client{Transport: http.DefaultTransport}, Destinations: map[string]GitHubDestination{"22/pull/3": {Owner: "owner", Repository: "repo"}}}
	}
	for _, raw := range []string{"http://api.github.com", "http://localhost:123", "https://user:pass@api.github.com", "https://api.github.com/", "https://api.github.com/a/../api/v3", "https://api.github.com/api%2fv3", "https://api.github.com?", "https://api.github.com?q=x", "https://api.github.com#x", "/relative"} {
		c := base()
		c.APIBaseURL = raw
		if _, err := NewGitHubTransport(c); err == nil {
			t.Errorf("accepted URL %q", raw)
		}
	}
	for _, raw := range []string{"https://api.github.com", "https://github.example/api/v3", "http://127.0.0.1:123", "http://[::1]:123"} {
		c := base()
		c.APIBaseURL = raw
		if _, err := NewGitHubTransport(c); err != nil {
			t.Errorf("rejected URL %q: %v", raw, err)
		}
	}
	for _, path := range []string{"", ".", "..", "a/b", "a%2fb", "a?b", "a#b", "a\\b", "a b", "é"} {
		c := base()
		c.Destinations["22/pull/3"] = GitHubDestination{Owner: "owner", Repository: path}
		if _, err := NewGitHubTransport(c); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
	for _, edit := range []func(*GitHubTransportConfig){
		func(c *GitHubTransportConfig) { c.Source = "github:12" },
		func(c *GitHubTransportConfig) { c.OwnerID = "011" },
		func(c *GitHubTransportConfig) { c.Client = nil },
		func(c *GitHubTransportConfig) { c.Client = &http.Client{} },
		func(c *GitHubTransportConfig) { c.Destinations = nil },
		func(c *GitHubTransportConfig) {
			c.Destinations["22/pull/03"] = GitHubDestination{Owner: "owner", Repository: "repo"}
		},
	} {
		c := base()
		edit(&c)
		if _, err := NewGitHubTransport(c); err == nil {
			t.Error("invalid config accepted")
		}
	}
	c := base()
	c.Client.Timeout = time.Hour
	tr, err := NewGitHubTransport(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Destinations["22/pull/3"] = GitHubDestination{Owner: "foreign", Repository: "repo"}
	c.Client.Timeout = time.Nanosecond
	if tr.destinations["22/pull/3"].Owner != "owner" || tr.client.Timeout != 30*time.Second {
		t.Fatal("config not snapshotted or timeout not bounded")
	}
}

func TestGitHubTransportTimeoutAndCancellation(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer s.Close()
	tr := githubHTTPTransport(t, s)
	tr.client.Timeout = 20 * time.Millisecond
	got, err := tr.Send(context.Background(), githubHTTPRequest())
	if err == nil || got.Status != "unknown" || calls.Load() != 1 {
		t.Fatalf("%+v %v calls=%d", got, err, calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = tr.Send(ctx, githubHTTPRequest())
	if err == nil || got.Status != "unknown" || calls.Load() != 1 {
		t.Fatalf("%+v %v calls=%d", got, err, calls.Load())
	}
}

func TestGitHubTransportConservativeRateHints(t *testing.T) {
	reset := time.Now().Add(time.Hour).Unix()
	for _, tc := range []struct {
		name    string
		code    int
		headers http.Header
		valid   bool
		minimum int64
	}{
		{"secondary", 403, http.Header{"Retry-After": {"60"}}, true, time.Now().Add(60 * time.Second).UnixMilli()},
		{"dual403", 403, http.Header{"Retry-After": {"60"}, "X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(reset)}}, true, reset*1000 + 1000},
		{"dual429", 429, http.Header{"Retry-After": {"60"}, "X-Ratelimit-Reset": {fmt.Sprint(reset)}}, true, reset*1000 + 1000},
		{"laterRetry", 429, http.Header{"Retry-After": {"7200"}, "X-Ratelimit-Reset": {fmt.Sprint(reset)}}, true, time.Now().Add(7200 * time.Second).UnixMilli()},
		{"duplicateRetry", 429, http.Header{"Retry-After": {"60", "120"}}, false, 0},
		{"duplicateReset", 403, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(reset), fmt.Sprint(reset)}}, false, 0},
		{"badReset", 429, http.Header{"Retry-After": {"60"}, "X-Ratelimit-Reset": {"bad"}}, false, 0},
		{"overflowRetry", 429, http.Header{"Retry-After": {"18446744073709551615"}}, false, 0},
		{"overflowReset", 403, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"9223372036854775807"}}, false, 0},
		{"badRemaining", 403, http.Header{"Retry-After": {"60"}, "X-Ratelimit-Remaining": {"bad"}}, false, 0},
		{"pastReset", 403, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1"}}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for name, values := range tc.headers {
					for _, value := range values {
						w.Header().Add(name, value)
					}
				}
				w.WriteHeader(tc.code)
				io.WriteString(w, `{"message":"rate limit"}`)
			}))
			defer s.Close()
			got, err := githubHTTPTransport(t, s).Send(context.Background(), githubHTTPRequest())
			if tc.valid {
				if err != nil || got.Status != "rate_limited" || got.RetryAt < tc.minimum {
					t.Fatalf("%+v %v min=%d", got, err, tc.minimum)
				}
			} else if err == nil || got.Status != "unknown" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

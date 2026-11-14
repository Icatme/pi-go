package pigo

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOwnedProviderRedirectBlocksCrossOrigin(t *testing.T) {
	for _, status := range []int{307, 308} {
		for _, changeHost := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/host=%v", status, changeHost), func(t *testing.T) {
				var reached, sources atomic.Int32
				dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); io.Copy(io.Discard, r.Body) }))
				defer dst.Close()
				destination := dst.URL
				if changeHost {
					destination = strings.Replace(destination, "127.0.0.1", "localhost", 1)
				}
				src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sources.Add(1); http.Redirect(w, r, destination, status) }))
				defer src.Close()
				req, _ := http.NewRequest("POST", src.URL, strings.NewReader("private-fixture-prompt"))
				req.Header.Set("X-Api-Key", "fake-key")
				req.Header.Set("X-Custom-Secret", "fake-secret")
				c := NewProviderHTTPClient(nil, ProviderHTTPPolicy{})
				defer c.CloseIdleConnections()
				resp, err := c.Do(req)
				if resp != nil {
					resp.Body.Close()
				}
				if err != nil || resp == nil || resp.StatusCode != status || sources.Load() != 1 || reached.Load() != 0 {
					t.Fatalf("cross-origin request received: %d; err=%v", reached.Load(), err)
				}
			})
		}
	}
}

func TestOwnedProviderRedirectSameOriginAndExplicitClient(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			var target atomic.Int32
			dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				target.Add(1)
				b, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || string(b) != "fixture" || r.Header.Get("X-Api-Key") != "fake-key" {
					t.Error("body/header changed")
				}
			}))
			defer dst.Close()
			src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/end" {
					b, _ := io.ReadAll(r.Body)
					if r.Method != "POST" || string(b) != "fixture" || r.Header.Get("X-Api-Key") != "fake-key" {
						t.Error("same-origin body/header changed")
					}
					target.Add(1)
					return
				}
				location := "/end"
				if explicit {
					location = dst.URL
				}
				http.Redirect(w, r, location, 307)
			}))
			defer src.Close()
			var base *http.Client
			if explicit {
				base = &http.Client{}
			}
			c := NewProviderHTTPClient(base, ProviderHTTPPolicy{})
			defer c.CloseIdleConnections()
			req, _ := http.NewRequest("POST", src.URL, strings.NewReader("fixture"))
			req.Header.Set("X-Api-Key", "fake-key")
			r, e := c.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			r.Body.Close()
			if target.Load() != 1 {
				t.Fatalf("target %d", target.Load())
			}
		})
	}
}

func TestOwnedProviderRedirectOriginBoundaries(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		want     bool
	}{
		{"https://api.example/path", "https://API.example:443/other?x=1", true},
		{"http://api.example", "http://api.example:80", true},
		{"https://api.example", "http://api.example", false},
		{"http://api.example", "https://api.example", false},
		{"https://api.example", "https://sub.api.example", false},
		{"https://api.example", "https://api.example:444", false},
		{"https://api.example", "https://other.example", false},
		{"https://api.example", "https://user:fake@api.example", false},
		{"http://[fe80::1%25ethA]", "http://[fe80::1%25etha]", false},
		{"http://[fe80::1%25ethA]", "http://[fe80::1%25ethA]:80", true},
	} {
		a, _ := url.Parse(tc.from)
		b, _ := url.Parse(tc.to)
		if got := sameProviderHTTPOrigin(a, b); got != tc.want {
			t.Fatalf("%s -> %s: %v", tc.from, tc.to, got)
		}
	}
}

func TestOwnedProviderRedirectTenHopLimit(t *testing.T) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1); http.Redirect(w, r, "/again", 307) }))
	defer s.Close()
	c := NewProviderHTTPClient(nil, ProviderHTTPPolicy{})
	defer c.CloseIdleConnections()
	r, e := c.Get(s.URL)
	if r != nil {
		r.Body.Close()
	}
	if e == nil || !strings.Contains(e.Error(), "stopped after 10 redirects") || n.Load() != 10 {
		t.Fatalf("requests=%d error=%v", n.Load(), e)
	}
}

func TestOwnedProviderRedirectDowngradeNoDispatch(t *testing.T) {
	c := NewProviderHTTPClient(nil, ProviderHTTPPolicy{})
	var n atomic.Int32
	c.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		n.Add(1)
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"http://fixture.invalid/leak?secret=fake"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	req, _ := http.NewRequest("POST", "https://fixture.invalid/start", strings.NewReader("fake-private-body"))
	req.Header.Set("X-Api-Key", "fake-key")
	r, e := c.Do(req)
	if r != nil {
		r.Body.Close()
	}
	if e != nil || n.Load() != 1 || r.StatusCode != 307 {
		t.Fatalf("requests=%d resp=%v err=%v", n.Load(), r, e)
	}
}

func TestOwnedProviderRedirectProviderFailure(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-responses", "anthropic-messages", "google-generative-ai", "mistral-conversations", "deepseek-chat-completions"} {
		t.Run(string(api), func(t *testing.T) {
			var hits, origins atomic.Int32
			dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
			defer dst.Close()
			src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				origins.Add(1)
				w.Header().Set("Location", dst.URL+"/private?key=fake")
				w.WriteHeader(307)
				io.WriteString(w, `{"error":{"message":"redirect unavailable"}}`)
			}))
			defer src.Close()
			provider := Provider("fixture")
			if api == "anthropic-messages" {
				provider = "anthropic"
			}
			m := Model{ID: "fixture", Provider: provider, API: api, BaseURL: src.URL, MaxTokens: 64}
			result := Complete(m, Context{Messages: []Message{UserMessage{Content: "fake-prompt"}}}, ProviderStreamOptions{APIKey: "fake-key", MaxRetries: 0})
			if origins.Load() == 0 || hits.Load() != 0 || result.StopReason != StopReasonError {
				t.Fatalf("hits=%d result=%+v", hits.Load(), result)
			}
			if status, ok := ProviderHTTPStatusOf(result); api != "mistral-conversations" && api != "deepseek-chat-completions" && (!ok || status != 307) {
				t.Fatalf("HTTP status=%d present=%v", status, ok)
			}
			if strings.Contains(result.ErrorMessage, "key=fake") {
				t.Fatal("Location copied into failure")
			}
		})
	}
}

func TestOwnedProviderRedirectErrorBodies(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-responses"} {
		for _, body := range []string{"", "<html>fixture</html>", `{"error":{"message":"service unavailable"}}`} {
			t.Run(string(api)+body, func(t *testing.T) {
				var hits, origins atomic.Int32
				dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
				defer dst.Close()
				src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					origins.Add(1)
					w.Header().Set("Location", dst.URL+"/?key=fake-location")
					w.WriteHeader(308)
					io.WriteString(w, body)
				}))
				defer src.Close()
				result := Complete(Model{ID: "fixture", Provider: "openai", API: api, BaseURL: src.URL}, Context{}, ProviderStreamOptions{APIKey: "fake-key", MaxRetries: 1, MaxRetryDelay: 1})
				if hits.Load() != 0 || origins.Load() < 1 || origins.Load() > 2 || result.StopReason != StopReasonError {
					t.Fatalf("target=%d origin=%d result=%+v", hits.Load(), origins.Load(), result)
				}
				if strings.Contains(result.ErrorMessage, "fake-location") {
					t.Fatal("blocked Location copied into diagnostic")
				}
			})
		}
	}
}

func TestOwnedProviderRedirectExplicitCallback(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			var callbacks, hits atomic.Int32
			dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				b, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || string(b) != "fixture" || r.Header.Get("X-Api-Key") != "fake" {
					t.Error("replay changed")
				}
			}))
			defer dst.Close()
			src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dst.URL, 308) }))
			defer src.Close()
			base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				callbacks.Add(1)
				if allow {
					return nil
				}
				return http.ErrUseLastResponse
			}}
			c := NewProviderHTTPClient(base, ProviderHTTPPolicy{})
			defer c.CloseIdleConnections()
			req, _ := http.NewRequest("POST", src.URL, strings.NewReader("fixture"))
			req.Header.Set("X-Api-Key", "fake")
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			want := int32(0)
			if allow {
				want = 1
			}
			if hits.Load() != want || callbacks.Load() != 1 || base.Transport != nil {
				t.Fatalf("hits=%d callbacks=%d base=%v", hits.Load(), callbacks.Load(), base)
			}
		})
	}
}

package channel_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/superagent/channel"
)

var webhookSecret = []byte("synthetic-offline-secret-32-bytes-only")

const webhookDelivery = "12345678-1234-1234-1234-123456789abc"

func webhookRequest(t *testing.T, url string, body []byte, edit func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", webhookDelivery)
	mac := hmac.New(sha256.New, webhookSecret)
	mac.Write(body)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if edit != nil {
		edit(req)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func TestGitHubWebhookHTTPBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	a := host(t, path, nil)
	sim := newSimulator("")
	ad := adapter(t, a, channel.GitHub, sim)
	var authorizations atomic.Int64
	var current atomic.Pointer[channel.GitHubWebhook]
	results := make(chan channel.GitHubWebhookResult, 1)
	var observed channel.GitHubWebhookResult
	cfg := channel.GitHubWebhookConfig{Secret: func(context.Context) ([]byte, error) { return webhookSecret, nil }, AllowedThreads: []string{"100/pull/27"}, Authorize: func(_ context.Context, event channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
		authorizations.Add(1)
		if event.DeliveryID != webhookDelivery || event.Principal != "7" || event.MessageID != "81" || event.Thread != "100/pull/27" || event.EventType != "issue_comment" {
			t.Errorf("event: %+v", event)
		}
		// A host callback cannot rewrite signed data before normalization.
		for i := range event.RawBody {
			event.RawBody[i] = 'x'
		}
		return channel.GitHubAuthorization{Allowed: true, ExplicitRequest: true}, nil
	}}
	hook, err := channel.NewGitHubWebhook(ad, cfg)
	if err != nil {
		t.Fatal(err)
	}
	current.Store(hook)
	cfg.AllowedThreads[0] = "200/pull/27"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := current.Load().Admit(r)
		results <- result
		if errors.Is(err, channel.ErrGitHubReplay) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	send := func(body []byte, edit func(*http.Request)) *http.Response {
		response := webhookRequest(t, server.URL, body, edit)
		observed = <-results
		return response
	}
	_, body := inbound(channel.GitHub, "irrelevant", "hello")
	if r := send(body, nil); r.StatusCode != http.StatusAccepted {
		t.Fatal(r.StatusCode)
	}
	if string(observed.Event.RawBody) != string(body) || observed.Event.DeliveryID != webhookDelivery || observed.Event.BodySHA256 == "" {
		t.Fatalf("raw evidence lost: %+v", observed)
	}
	if err := a.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(a.Admissions()) != 1 || len(a.Replies()) != 1 {
		t.Fatal("missing admission")
	}
	if calls, _ := sim.counts(); calls != 0 {
		t.Fatal("webhook automatically replied")
	}
	if r := send(body, nil); r.StatusCode != http.StatusConflict || !observed.Admission.Duplicate {
		t.Fatal("replay not surfaced")
	}
	// Delivery headers are unsigned. A fresh header cannot replay the signed body.
	cfg2 := cfg
	cfg2.AllowedThreads = []string{"100/pull/27"}
	cfg2.Authorize = func(context.Context, channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
		return channel.GitHubAuthorization{Allowed: true, ExplicitRequest: true}, nil
	}
	hook, err = channel.NewGitHubWebhook(ad, cfg2)
	if err != nil {
		t.Fatal(err)
	}
	current.Store(hook)
	if r := send(body, func(r *http.Request) { r.Header.Set("X-GitHub-Delivery", "abcdefab-1234-1234-1234-123456789abc") }); r.StatusCode != http.StatusConflict {
		t.Fatal("changed header replay accepted", r.StatusCode)
	}
	if len(a.Admissions()) != 1 {
		t.Fatal("duplicate routed")
	}
	if observed.Event.DeliveryID == webhookDelivery {
		t.Fatal("new unsigned delivery header lost")
	}
	// The same raw-body replay fence survives replacement of both wrapper and coordinator.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = host(t, path, nil)
	ad = adapter(t, a, channel.GitHub, sim)
	hook, err = channel.NewGitHubWebhook(ad, cfg2)
	if err != nil {
		t.Fatal(err)
	}
	current.Store(hook)
	if r := send(body, nil); r.StatusCode != http.StatusConflict {
		t.Fatal("restart replay accepted")
	}
	if authorizations.Load() != 2 {
		t.Fatal("authorization callbacks missing")
	}
}

func TestGitHubWebhookRejectsForgedAndUnauthorized(t *testing.T) {
	cases := []struct {
		name     string
		body     func([]byte) []byte
		edit     func(*http.Request)
		allow    bool
		wantAuth int64
	}{
		{name: "bad_signature", edit: func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64)) }},
		{name: "unsigned_event_switch", edit: func(r *http.Request) { r.Header.Set("X-GitHub-Event", "pull_request_review_comment") }},
		{name: "duplicate_signature", edit: func(r *http.Request) { r.Header.Add("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64)) }},
		{name: "missing_delivery", edit: func(r *http.Request) { r.Header.Del("X-GitHub-Delivery") }},
		{name: "bad_method", edit: func(r *http.Request) { r.Method = http.MethodPut }},
		{name: "gzip", edit: func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
		{name: "cross_repo", body: func(b []byte) []byte { return bytes.Replace(b, []byte(`"id":100`), []byte(`"id":200`), 1) }},
		{name: "cross_pr", body: func(b []byte) []byte { return bytes.Replace(b, []byte(`"number":27`), []byte(`"number":28`), 1) }},
		{name: "duplicate_key", body: func(b []byte) []byte { return append([]byte(`{"action":"created",`), b[1:]...) }},
		{name: "huge", body: func(b []byte) []byte { return append(b, bytes.Repeat([]byte(" "), 1<<20)...) }},
		{name: "deny_actor", wantAuth: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := host(t, filepath.Join(t.TempDir(), "journal"), nil)
			sim := newSimulator("")
			ad := adapter(t, a, channel.GitHub, sim)
			var authorized atomic.Int64
			hook, err := channel.NewGitHubWebhook(ad, channel.GitHubWebhookConfig{Secret: func(context.Context) ([]byte, error) { return webhookSecret, nil }, AllowedThreads: []string{"100/pull/27"}, Authorize: func(context.Context, channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
				authorized.Add(1)
				return channel.GitHubAuthorization{Allowed: tc.allow, ExplicitRequest: true}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, err := hook.Admit(r); err == nil {
					t.Error("accepted invalid request")
				}
				w.WriteHeader(403)
			}))
			defer server.Close()
			_, body := inbound(channel.GitHub, "irrelevant", "hello")
			if tc.body != nil {
				body = tc.body(body)
			}
			webhookRequest(t, server.URL, body, tc.edit)
			if authorized.Load() != tc.wantAuth || len(a.Admissions()) != 0 {
				t.Fatalf("auth/admission: %d %d", authorized.Load(), len(a.Admissions()))
			}
		})
	}
}

func TestGitHubWebhookReplayPreservesOriginalGrant(t *testing.T) {
	a := host(t, filepath.Join(t.TempDir(), "journal"), nil)
	ad := adapter(t, a, channel.GitHub, newSimulator(""))
	var calls atomic.Int64
	hook, err := channel.NewGitHubWebhook(ad, channel.GitHubWebhookConfig{Secret: func(context.Context) ([]byte, error) { return webhookSecret, nil }, AllowedThreads: []string{"100/pull/27"}, Authorize: func(context.Context, channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
		return channel.GitHubAuthorization{Allowed: true, ExplicitRequest: calls.Add(1) == 1}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan channel.GitHubWebhookResult, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, e := hook.Admit(r)
		results <- v
		if e != nil {
			if !errors.Is(e, channel.ErrGitHubReplay) {
				t.Errorf("lost original replay receipt: %v", e)
			}
			w.WriteHeader(409)
			return
		}
		w.WriteHeader(202)
	}))
	defer server.Close()
	_, body := inbound(channel.GitHub, "ignore", "hello")
	webhookRequest(t, server.URL, body, nil)
	first := <-results
	webhookRequest(t, server.URL, body, nil)
	second := <-results
	if !second.Admission.Duplicate || !second.Admission.Record.Envelope.ExplicitRequest || first.Admission.Record.Envelope != second.Admission.Record.Envelope || len(a.Admissions()) != 1 {
		t.Fatal("replay changed original grant")
	}
}

func TestGitHubWebhookHTTPApprovedSendAndEcho(t *testing.T) {
	for _, lostAck := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivered_echo", true: "lost_receipt"}[lostAck], func(t *testing.T) {
			var posts atomic.Int64
			var apiURL string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/repos/owner/repo/issues/27/comments" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				body, e := io.ReadAll(r.Body)
				if e != nil {
					t.Error(e)
				}
				var content map[string]string
				if e := json.Unmarshal(body, &content); e != nil {
					t.Error(e)
				}
				if len(content) != 1 || content["body"] != "Approved only: hello <world>\n第二行" {
					t.Errorf("wrong outbound %s", body)
				}
				posts.Add(1)
				if lostAck {
					conn, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(201)
				json.NewEncoder(w).Encode(map[string]any{"id": 901, "body": content["body"], "issue_url": apiURL + "/repos/owner/repo/issues/27", "url": apiURL + "/repos/owner/repo/issues/comments/901"})
			}))
			defer api.Close()
			apiURL = api.URL
			transport, e := channel.NewGitHubTransport(channel.GitHubTransportConfig{Source: "github:42", OwnerID: "42", APIBaseURL: api.URL, Destinations: map[string]channel.GitHubDestination{"100/pull/27": {Owner: "owner", Repository: "repo"}}, Client: api.Client()})
			if e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(t.TempDir(), "journal")
			a := host(t, path, nil)
			ad := adapter(t, a, channel.GitHub, transport)
			cfg := channel.GitHubWebhookConfig{Secret: func(context.Context) ([]byte, error) { return webhookSecret, nil }, AllowedThreads: []string{"100/pull/27"}, Authorize: func(context.Context, channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
				return channel.GitHubAuthorization{Allowed: true, ExplicitRequest: true}, nil
			}}
			hook, e := channel.NewGitHubWebhook(ad, cfg)
			if e != nil {
				t.Fatal(e)
			}
			webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, e := hook.Admit(r)
				if e != nil {
					if !errors.Is(e, channel.ErrGitHubReplay) {
						t.Error(e)
					}
					w.WriteHeader(409)
					return
				}
				w.WriteHeader(202)
			}))
			defer webhook.Close()
			_, body := inbound(channel.GitHub, "ignored", "please reply")
			if response := webhookRequest(t, webhook.URL, body, nil); response.StatusCode != 202 {
				t.Fatal(response.StatusCode)
			}
			if e := a.WaitIdle(t.Context()); e != nil {
				t.Fatal(e)
			}
			if posts.Load() != 0 {
				t.Fatal("automatic HTTP reply")
			}
			reply := a.Replies()[0]
			attempt, e := ad.SendReply(t.Context(), reply.ID)
			if lostAck {
				if !errors.Is(e, channel.ErrUncertain) || a.Replies()[0].Status != "needs_review" {
					t.Fatalf("lost receipt retriable: %v %+v", e, a.Replies())
				}
				if e := a.Close(); e != nil {
					t.Fatal(e)
				}
				a = host(t, path, nil)
				ad = adapter(t, a, channel.GitHub, transport)
				if e := ad.Reconcile(t.Context(), attempt); !errors.Is(e, channel.ErrUncertain) {
					t.Fatal("lookup invented absence", e)
				}
				if _, e := ad.SendReply(t.Context(), reply.ID); e == nil {
					t.Fatal("retried uncertain POST")
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				_, echo := inbound(channel.GitHub, "irrelevant", reply.Text)
				echo = bytes.Replace(echo, []byte(`"id":81`), []byte(`"id":901`), 1)
				if response := webhookRequest(t, webhook.URL, echo, nil); response.StatusCode != 409 {
					t.Fatal("own echo admitted", response.StatusCode)
				}
				if len(a.Admissions()) != 1 {
					t.Fatal("echo became request")
				}
			}
			if posts.Load() != 1 {
				t.Fatal("POST repeated", posts.Load())
			}
		})
	}
}

func TestGitHubWebhookSignedBytesAndDistinctComments(t *testing.T) {
	a := host(t, filepath.Join(t.TempDir(), "journal"), nil)
	ad := adapter(t, a, channel.GitHub, newSimulator(""))
	var auth atomic.Int64
	hook, e := channel.NewGitHubWebhook(ad, channel.GitHubWebhookConfig{Secret: func(context.Context) ([]byte, error) { return webhookSecret, nil }, AllowedThreads: []string{"100/pull/27"}, Authorize: func(context.Context, channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
		auth.Add(1)
		return channel.GitHubAuthorization{Allowed: true, ExplicitRequest: true}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, e := hook.Admit(r)
		if e != nil {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(202)
	}))
	defer server.Close()
	_, body := inbound(channel.GitHub, "ignore", "same text")
	// The signature is made before this body replacement; even semantic JSON
	// equivalence is not permission to change the original bytes.
	if response := webhookRequest(t, server.URL, body, func(r *http.Request) {
		changed := append([]byte(" "), body...)
		r.Body = io.NopCloser(bytes.NewReader(changed))
		r.ContentLength = int64(len(changed))
	}); response.StatusCode != 403 {
		t.Fatal("re-encoded body passed HMAC")
	}
	if auth.Load() != 0 {
		t.Fatal("forged bytes reached authorization")
	}
	if response := webhookRequest(t, server.URL, body, nil); response.StatusCode != 202 {
		t.Fatal(response.StatusCode)
	}
	second := bytes.Replace(body, []byte(`"id":81`), []byte(`"id":82`), 1)
	if response := webhookRequest(t, server.URL, second, func(r *http.Request) { r.Header.Set("X-GitHub-Delivery", "abcdefab-1234-1234-1234-123456789abc") }); response.StatusCode != 202 {
		t.Fatal("distinct comment collapsed", response.StatusCode)
	}
	if len(a.Admissions()) != 2 {
		t.Fatal("same text distinct object was lost")
	}
}

func TestGitHubWebhookEditorRemainsIndependentActor(t *testing.T) {
	for _, allowedActor := range []string{"7", "8"} {
		t.Run("policy_actor_"+allowedActor, func(t *testing.T) {
			a := host(t, filepath.Join(t.TempDir(), "journal"), nil)
			c := config(channel.GitHub)
			c.Policy.Rules[0].Principal = allowedActor
			ad, e := channel.New(a, c, newSimulator(""))
			if e != nil {
				t.Fatal(e)
			}
			hook, e := channel.NewGitHubWebhook(ad, channel.GitHubWebhookConfig{Secret: func(context.Context) ([]byte, error) { return webhookSecret, nil }, AllowedThreads: []string{"100/pull/27"}, Authorize: func(_ context.Context, event channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
				if event.Principal != "8" || event.Action != "edited" {
					t.Errorf("wrong edit actor: %+v", event)
				}
				return channel.GitHubAuthorization{Allowed: true, ExplicitRequest: true}, nil
			}})
			if e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, e := hook.Admit(r); e != nil {
					t.Error(e)
				}
				w.WriteHeader(202)
			}))
			defer server.Close()
			_, body := inbound(channel.GitHub, "ignore", "please do this")
			body = bytes.Replace(body, []byte(`"created"`), []byte(`"edited"`), 1)
			body = bytes.Replace(body, []byte(`"sender":{"id":7}`), []byte(`"sender":{"id":8}`), 1)
			webhookRequest(t, server.URL, body, nil)
			if e := a.WaitIdle(t.Context()); e != nil {
				t.Fatal(e)
			}
			want := "accepted"
			if allowedActor == "7" {
				want = "ignored"
			}
			records := a.Admissions()
			if len(records) != 1 || records[0].Decision != want || records[0].Envelope.Principal != "8" {
				t.Fatalf("actor policy bypassed: %+v", records)
			}
			if allowedActor == "7" && len(a.Replies()) != 0 {
				t.Fatal("original author granted editor permission")
			}
		})
	}
}

func TestGitHubWebhookConfigurationAndCallbackFailures(t *testing.T) {
	a := host(t, filepath.Join(t.TempDir(), "journal"), nil)
	ad := adapter(t, a, channel.GitHub, newSimulator(""))
	good := channel.GitHubWebhookConfig{Secret: func(context.Context) ([]byte, error) { return webhookSecret, nil }, AllowedThreads: []string{"100/pull/27"}, Authorize: func(context.Context, channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
		return channel.GitHubAuthorization{Allowed: true, ExplicitRequest: true}, nil
	}}
	if _, e := channel.NewGitHubWebhook(nil, good); e == nil {
		t.Fatal("nil adapter accepted")
	}
	for _, edit := range []func(*channel.GitHubWebhookConfig){func(c *channel.GitHubWebhookConfig) { c.Secret = nil }, func(c *channel.GitHubWebhookConfig) { c.Authorize = nil }, func(c *channel.GitHubWebhookConfig) { c.AllowedThreads = []string{"100/pull/../27"} }} {
		c := good
		edit(&c)
		if _, e := channel.NewGitHubWebhook(ad, c); e == nil {
			t.Fatal("bad configuration accepted")
		}
	}
	for _, mode := range []string{"secret_error", "secret_panic", "short_secret", "authorize_error", "authorize_panic", "default_deny", "empty_targets"} {
		t.Run(mode, func(t *testing.T) {
			c := good
			switch mode {
			case "secret_error":
				c.Secret = func(context.Context) ([]byte, error) { return nil, errors.New("PRIVATE secret") }
			case "secret_panic":
				c.Secret = func(context.Context) ([]byte, error) { panic("PRIVATE secret") }
			case "short_secret":
				c.Secret = func(context.Context) ([]byte, error) { return []byte("short"), nil }
			case "authorize_error":
				c.Authorize = func(context.Context, channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
					return channel.GitHubAuthorization{Allowed: true}, errors.New("PRIVATE grant")
				}
			case "authorize_panic":
				c.Authorize = func(context.Context, channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
					panic("PRIVATE grant")
				}
			case "default_deny":
				c.Authorize = func(context.Context, channel.GitHubWebhookEvent) (channel.GitHubAuthorization, error) {
					return channel.GitHubAuthorization{}, nil
				}
			case "empty_targets":
				c.AllowedThreads = nil
			}
			hook, e := channel.NewGitHubWebhook(ad, c)
			if e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, e := hook.Admit(r)
				if e == nil || strings.Contains(e.Error(), "PRIVATE") {
					t.Errorf("private/absent error: %v", e)
				}
				w.WriteHeader(403)
			}))
			defer server.Close()
			_, body := inbound(channel.GitHub, "ignore", "hello")
			webhookRequest(t, server.URL, body, nil)
			if len(a.Admissions()) != 0 {
				t.Fatal("failed callback admitted request")
			}
		})
	}
}

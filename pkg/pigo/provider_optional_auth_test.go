package pigo

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompletionsExplicitOptionalAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		compat                      *OpenAICompletionsCompat
		key, customHeader, wantAuth string
		wantCalls                   int
	}{
		{name: "default rejects"},
		{name: "false rejects", compat: &OpenAICompletionsCompat{}},
		{name: "blank credential rejected", compat: &OpenAICompletionsCompat{AllowUnauthenticated: true}, key: "   "},
		{name: "explicit no auth", compat: &OpenAICompletionsCompat{AllowUnauthenticated: true}, wantCalls: 1},
		{name: "key still used", compat: &OpenAICompletionsCompat{AllowUnauthenticated: true}, key: "configured", wantAuth: "Bearer configured", wantCalls: 1},
		{name: "caller header remains explicit", compat: &OpenAICompletionsCompat{AllowUnauthenticated: true}, customHeader: "Custom explicit", wantAuth: "Custom explicit", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if got := r.Header.Get("Authorization"); got != tc.wantAuth {
					t.Errorf("authorization %q want %q", got, tc.wantAuth)
				}
				if tc.wantAuth == "" {
					if _, exists := r.Header["Authorization"]; exists {
						t.Error("empty auth header present")
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			}))
			defer server.Close()
			model := Model{API: "openai-completions", Provider: "audit-optional-auth", ID: "custom", BaseURL: server.URL, Compat: tc.compat}
			options := ProviderStreamOptions{APIKey: tc.key, RequestContext: t.Context()}
			if tc.customHeader != "" {
				options.Headers = map[string]string{"Authorization": tc.customHeader}
			}
			got := Complete(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, options)
			if int(calls.Load()) != tc.wantCalls {
				t.Fatalf("calls %d want %d: %+v", calls.Load(), tc.wantCalls, got)
			}
			if tc.wantCalls == 0 {
				if got.StopReason != StopReasonError || got.ErrorMessage != "missing api key" {
					t.Fatalf("missing default guard %+v", got)
				}
			} else if got.StopReason != StopReasonStop {
				t.Fatalf("request failed %+v", got)
			}
		})
	}
}
func TestOptionalAuthenticationCompatCloneIsIndependent(t *testing.T) {
	source := &OpenAICompletionsCompat{AllowUnauthenticated: true}
	cloned := cloneCompat(source).(*OpenAICompletionsCompat)
	source.AllowUnauthenticated = false
	if !cloned.AllowUnauthenticated {
		t.Fatal("compat setting aliases caller")
	}
}

func TestOptionalAuthenticationUsesOnlyMatchingStoredCredential(t *testing.T) {
	for _, matching := range []bool{false, true} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			want := ""
			if matching {
				want = "Bearer scoped-key"
			}
			if got := r.Header.Get("Authorization"); got != want {
				t.Errorf("header %q want %q", got, want)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		}))
		provider := Provider("audit-optional-auth")
		authProvider := Provider("unrelated-provider")
		if matching {
			authProvider = provider
		}
		model := Model{API: "openai-completions", Provider: provider, ID: "custom", BaseURL: server.URL, Compat: &OpenAICompletionsCompat{AllowUnauthenticated: true}}
		got := Complete(model, Context{}, ProviderStreamOptions{RequestContext: t.Context(), Auth: map[Provider]AuthConfig{authProvider: {Type: AuthTypeAPIKey, APIKey: "scoped-key"}}})
		server.Close()
		if calls.Load() != 1 || got.StopReason != StopReasonStop {
			t.Fatalf("calls %d response %+v", calls.Load(), got)
		}
	}
}

func TestOptionalAuthenticationDoesNotDowngradeOAuthFailure(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: observationRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host == "inference.invalid" {
			t.Error("inference dispatched despite OAuth failure")
		}
		return nil, errors.New("synthetic auth resolver failure")
	})}
	model := Model{API: "openai-completions", Provider: "openai-codex", ID: "custom", BaseURL: "https://inference.invalid/v1", Compat: &OpenAICompletionsCompat{AllowUnauthenticated: true}}
	got := Complete(model, Context{}, ProviderStreamOptions{HTTPClient: client, RequestContext: t.Context(), Auth: map[Provider]AuthConfig{"openai-codex": {Type: AuthTypeOAuth, OAuth: &OAuthCredentials{RefreshToken: "optional-auth-failure-fixture"}}}})
	if got.StopReason != StopReasonError || !strings.Contains(got.ErrorMessage, "synthetic auth resolver failure") || calls.Load() != 1 {
		t.Fatalf("calls%d got%+v", calls.Load(), got)
	}
}

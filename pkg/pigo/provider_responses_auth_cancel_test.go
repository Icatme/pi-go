package pigo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var responsesAuthFailureTestSequence atomic.Uint64

func TestResponsesAuthFailureClassification(t *testing.T) {
	for _, kind := range []string{"cancel", "deadline", "ordinary", "typed"} {
		t.Run(kind, func(t *testing.T) {
			provider := Provider(fmt.Sprintf("test-responses-auth-%s-%d", kind, responsesAuthFailureTestSequence.Add(1)))
			entered := make(chan struct{})
			var resolves, requests atomic.Int32
			sentinel := errors.New("fixture authorization failed")
			RegisterProviderModule(ProviderModule{Provider: provider, Models: map[string]Model{"requested": {API: "openai-responses"}}, Auth: ProviderAuth{ResolveAuthorization: func(_ Provider, _ AuthConfig, _ *http.Client, ctx context.Context) (string, error) {
				resolves.Add(1)
				close(entered)
				if kind == "ordinary" {
					return "", sentinel
				}
				if kind == "typed" {
					return "", withProviderFailure(sentinel, ProviderFailureParse, "fixture_auth")
				}
				<-ctx.Done()
				return "", fmt.Errorf("authorization: %w", ctx.Err())
			}}})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 50*time.Millisecond)
				defer stop()
			}
			client := &http.Client{Transport: samplingRoundTripper(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, errors.New("unexpected provider HTTP")
			})}
			model := Model{Provider: provider, ID: "requested", API: "openai-responses", BaseURL: "http://127.0.0.1:1"}
			s := Stream(model, Context{}, ProviderStreamOptions{HTTPClient: client, RequestContext: ctx, MaxRetries: 2, Auth: map[Provider]AuthConfig{provider: {Type: AuthTypeOAuth, OAuth: &OAuthCredentials{}}}})
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("auth never entered")
			}
			if kind == "cancel" {
				cancel()
			}
			events := collectAssistantMessageEvents(s.Events())
			r := s.Result()
			want := StopReasonError
			if kind == "cancel" || kind == "deadline" {
				want = StopReasonAborted
			}
			if r.StopReason != want || r.Model != model.ID || r.Provider != provider || r.API != model.API || resolves.Load() != 1 || requests.Load() != 0 {
				t.Fatalf("result=%+v resolves=%d requests=%d", r, resolves.Load(), requests.Load())
			}
			if (kind == "ordinary" || kind == "typed") && r.ErrorMessage != sentinel.Error() {
				t.Fatalf("auth diagnostic lost: %q", r.ErrorMessage)
			}
			if (kind == "cancel" || kind == "deadline") && !strings.Contains(r.ErrorMessage, "authorization: context") {
				t.Fatalf("cancel diagnostic lost: %q", r.ErrorMessage)
			}
			var terminals int
			for _, e := range events {
				if e.Type == AssistantMessageEventDone {
					t.Fatal("unexpected success")
				}
				if e.Type == AssistantMessageEventError {
					terminals++
					if e.Reason != want || e.Error.StopReason != want {
						t.Fatalf("terminal=%+v", e)
					}
				}
			}
			if terminals != 1 {
				t.Fatalf("terminal errors=%d", terminals)
			}
			if kind == "typed" {
				f, ok := ProviderFailureOf(r)
				if !ok || f.Kind != ProviderFailureParse || f.Reason != "fixture_auth" {
					t.Fatalf("lost typed metadata: %+v", f)
				}
			}
		})
	}
}

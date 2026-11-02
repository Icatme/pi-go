package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestKiroCredentialSourceScopeKindAndOwnership(t *testing.T) {
	for _, kind := range []kiroCredentialKind{kiroCredentialBearer, kiroCredentialAPIKey, kiroCredentialExternalIDP} {
		options := kiroHTTPRequestFixture()
		conversation := Context{Messages: []Message{UserMessage{Content: "original"}}}
		calls := 0
		token := "LOCAL_ONLY_SOURCE_FAKE"
		if kind == kiroCredentialAPIKey {
			token = "ksk_LOCAL_ONLY_SOURCE_FAKE"
		}
		request, err := newKiroOfflineRequestWithCredentialSource(context.Background(), conversation, nil, options, func(_ context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
			calls++
			if scope.ProfileARN != options.Identity.ProfileARN || scope.Region != "us-east-1" {
				t.Fatal("scope changed")
			}
			conversation.Messages[0] = UserMessage{Content: "source changed input"}
			return kiroScopedCredential{Scope: scope, Kind: kind, Token: token}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer request.Body.Close()
		raw, _ := io.ReadAll(request.Body)
		if calls != 1 || !strings.Contains(string(raw), "original") || strings.Contains(string(raw), "source changed input") || strings.Contains(string(raw), token) || strings.Contains(request.URL.String(), token) || request.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("scope/body/auth ownership")
		}
		want := ""
		if kind == kiroCredentialAPIKey {
			want = "API_KEY"
		}
		if kind == kiroCredentialExternalIDP {
			want = "EXTERNAL_IDP"
		}
		if request.Header.Get("tokentype") != want {
			t.Fatal("explicit credential kind lost")
		}
	}
}

func TestKiroCredentialSourceInvalidPreflightNeverResolves(t *testing.T) {
	for _, which := range []string{"nil-context", "cancelled", "region", "profile", "system", "source"} {
		ctx := context.Background()
		options := kiroHTTPRequestFixture()
		conversation := Context{Messages: []Message{UserMessage{Content: "fixture"}}}
		switch which {
		case "nil-context":
			ctx = nil
		case "cancelled":
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			ctx = cancelled
		case "region":
			options.Region = "https://other.invalid"
		case "profile":
			options.Identity.ProfileARN = ""
		case "system":
			conversation.SystemPrompt = "unsupported"
		}
		calls := 0
		source := kiroCredentialSource(func(_ context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
			calls++
			return kiroScopedCredential{Scope: scope, Kind: kiroCredentialBearer, Token: "LOCAL_ONLY_SOURCE_FAKE"}, nil
		})
		if which == "source" {
			source = nil
		}
		request, err := newKiroOfflineRequestWithCredentialSource(ctx, conversation, nil, options, source)
		if err == nil || request != nil || calls != 0 {
			t.Fatal("invalid preflight resolved", which)
		}
	}
}

func TestKiroCredentialSourceRejectsMismatchErrorAndCancellation(t *testing.T) {
	for _, which := range []string{"profile", "region", "kind", "token", "error", "cancel"} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		request, err := newKiroOfflineRequestWithCredentialSource(ctx, Context{Messages: []Message{UserMessage{Content: "fixture"}}}, nil, kiroHTTPRequestFixture(), func(_ context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
			calls++
			c := kiroScopedCredential{Scope: scope, Kind: kiroCredentialBearer, Token: "LOCAL_ONLY_SOURCE_FAKE"}
			switch which {
			case "profile":
				c.Scope.ProfileARN = "other-profile"
			case "region":
				c.Scope.Region = "eu-west-1"
			case "kind":
				c.Kind = "future-kind"
			case "token":
				c.Token = "bad\r\nsecret"
			case "error":
				return c, errors.New("secret error: LOCAL_ONLY_SOURCE_FAKE")
			case "cancel":
				cancel()
			}
			return c, nil
		})
		cancel()
		if err == nil || request != nil || calls != 1 || err.Error() != "kiro: explicit credential source failed" {
			t.Fatal("source failure escaped", which)
		}
	}
}

func TestKiroCredentialSourceNoCacheAndDebugRedaction(t *testing.T) {
	var calls atomic.Int32
	source := func(_ context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
		n := calls.Add(1)
		return kiroScopedCredential{Scope: scope, Kind: kiroCredentialBearer, Token: fmt.Sprintf("LOCAL_ONLY_ROTATION_%d", n)}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			request, err := newKiroOfflineRequestWithCredentialSource(context.Background(), Context{Messages: []Message{UserMessage{Content: "fixture"}}}, nil, kiroHTTPRequestFixture(), source)
			if err != nil {
				t.Error(err)
				return
			}
			request.Body.Close()
			if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer LOCAL_ONLY_ROTATION_") {
				t.Error("bad auth")
			}
		})
	}
	wg.Wait()
	if calls.Load() != 4 {
		t.Fatal("source cached/retried")
	}
	credential := kiroScopedCredential{Token: "LOCAL_ONLY_PRIVATE_SECRET"}
	encoded, err := json.Marshal(credential)
	if err != nil || strings.Contains(string(encoded), credential.Token) {
		t.Fatal("credential JSON leaked token")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, credential), credential.Token) {
			t.Fatal("credential debug leaked")
		}
	}
}

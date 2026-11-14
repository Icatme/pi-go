package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type oauthRedirectRoundTrip func(*http.Request) (*http.Response, error)

func (f oauthRedirectRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOAuthTokenExchangeRejectsRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{"https://other.invalid/token?secret-location", "http://auth.openai.com/token?secret-location", "https://auth.openai.com/elsewhere?secret-location"} {
			for _, useDefault := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/default=%v", status, destination, useDefault), func(t *testing.T) {
					requests, redirects := 0, 0
					client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { redirects++; return nil }, Transport: oauthRedirectRoundTrip(func(r *http.Request) (*http.Response, error) {
						requests++
						if requests > 1 {
							t.Fatal("redirect dispatched")
						}
						b, _ := io.ReadAll(r.Body)
						if !strings.Contains(string(b), "code=fixture-code") || !strings.Contains(string(b), "code_verifier=fixture-verifier") {
							t.Fatal("wrong initial form")
						}
						return &http.Response{StatusCode: status, Status: "sensitive-status", Header: http.Header{"Location": {destination}}, Body: io.NopCloser(strings.NewReader("fixture-code fixture-verifier secret-location")), Request: r}, nil
					})}
					old := http.DefaultClient
					defer func() { http.DefaultClient = old }()
					argument := client
					if useDefault {
						http.DefaultClient = client
						argument = nil
					}
					_, err := exchangeOpenAICodexAuthorizationCode(context.Background(), argument, "fixture-code", "fixture-verifier")
					if err == nil || err.Error() != fmt.Sprintf("openai codex oauth token exchange refused redirect: HTTP %d", status) {
						t.Fatalf("unexpected diagnostic %v", err)
					}
					if requests != 1 || redirects != 0 || http.DefaultClient != (func() *http.Client {
						if useDefault {
							return client
						}
						return old
					})() {
						t.Fatal("client behavior changed")
					}
					// The original caller policy is still installed, including the global default case.
					if client.CheckRedirect(nil, nil) != nil || redirects != 1 {
						t.Fatal("caller redirect policy mutated")
					}
				})
			}
		}
	}
}
func TestOAuthTokenExchangeNormalAndCancellation(t *testing.T) {
	calls := 0
	c := &http.Client{Transport: oauthRedirectRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":60}`)), Request: r}, nil
	})}
	token, err := exchangeOpenAICodexAuthorizationCode(context.Background(), c, "fixture-code", "fixture-verifier")
	if err != nil || token.AccessToken != "fixture-access" || calls != 1 {
		t.Fatal("normal exchange failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = exchangeOpenAICodexAuthorizationCode(ctx, c, "fixture-code", "fixture-verifier")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestOAuthTokenExchangeMalformedLocationIsPrivate(t *testing.T) {
	calls := 0
	c := &http.Client{Transport: oauthRedirectRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://other.invalid/%bad%escape?fixture-code&fixture-verifier"}}, Body: io.NopCloser(strings.NewReader("private-body")), Request: r}, nil
	})}
	_, err := exchangeOpenAICodexAuthorizationCode(context.Background(), c, "fixture-code", "fixture-verifier")
	if err == nil || err.Error() != "openai codex oauth token exchange request failed" || calls != 1 {
		t.Fatalf("safe wrapper missing: %v, calls %d", err, calls)
	}
	if errors.Unwrap(err) == nil {
		t.Fatal("host error chain lost")
	}
}
func TestOAuthTokenExchangeDeadlineIdentity(t *testing.T) {
	c := &http.Client{Transport: oauthRedirectRoundTrip(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	_, err := exchangeOpenAICodexAuthorizationCode(context.Background(), c, "fixture-code", "fixture-verifier")
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "openai codex oauth token exchange request failed" {
		t.Fatal("deadline identity lost")
	}
}

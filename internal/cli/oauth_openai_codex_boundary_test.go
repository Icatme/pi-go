package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAICodexLoginOccupiedCallbackStopsBeforePresentation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	previous := openAICodexCallbackAddress
	openAICodexCallbackAddress = listener.Addr().String()
	t.Cleanup(func() { openAICodexCallbackAddress = previous })
	var opened, presented, prompted bool
	provider := newOpenAICodexOAuthProvider()
	provider.openBrowser = func(string) error { opened = true; return nil }
	_, err = provider.Login(t.Context(), oauthLoginCallbacks{
		OnAuth: func(oauthAuthInfo) { presented = true },
		OnPrompt: func(oauthPrompt) (string, error) {
			prompted = true
			return "", errors.New("unexpected prompt")
		},
	})
	var networkError *net.OpError
	if !errors.As(err, &networkError) || opened || presented || prompted {
		t.Fatalf("bind cause lost or login continued: err=%v opened=%v presented=%v prompted=%v", err, opened, presented, prompted)
	}
}

func TestOpenAICodexLoginCancellationDoesNotPrompt(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before login", false: "while waiting"}[before], func(t *testing.T) {
			previous := openAICodexCallbackAddress
			openAICodexCallbackAddress = "127.0.0.1:0"
			t.Cleanup(func() { openAICodexCallbackAddress = previous })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if before {
				cancel()
			}
			var opened, presented, prompted bool
			provider := newOpenAICodexOAuthProvider()
			provider.openBrowser = func(string) error { opened = true; cancel(); return nil }
			_, err := provider.Login(ctx, oauthLoginCallbacks{
				OnAuth: func(oauthAuthInfo) { presented = true },
				OnPrompt: func(oauthPrompt) (string, error) {
					prompted = true
					return "", errors.New("unexpected prompt")
				},
			})
			if !errors.Is(err, context.Canceled) || prompted || before && (opened || presented) {
				t.Fatalf("cancellation lost or login continued: err=%v opened=%v presented=%v prompted=%v", err, opened, presented, prompted)
			}
		})
	}
}

func TestOpenAICodexLoginCallbackAndManualStateBoundary(t *testing.T) {
	for _, mode := range []string{"callback", "bad-manual-state", "cancel-auth", "cancel-prompt", "cancel-fallback"} {
		t.Run(mode, func(t *testing.T) {
			oldAddress, oldURL, oldTimeout := openAICodexCallbackAddress, openAICodexTokenURL, openAICodexCallbackTimeout
			t.Cleanup(func() {
				openAICodexCallbackAddress, openAICodexTokenURL, openAICodexCallbackTimeout = oldAddress, oldURL, oldTimeout
			})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			openAICodexCallbackAddress = listener.Addr().String()
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			var tokens atomic.Int32
			access := buildJWTWithAccountID(t, "callback-test")
			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tokens.Add(1)
				if err := r.ParseForm(); err != nil || r.Form.Get("code") != "callback-code" {
					t.Errorf("unexpected token form: %v", err)
				}
				_ = json.NewEncoder(w).Encode(openAICodexTokenResponse{AccessToken: access, RefreshToken: "refresh", ExpiresIn: 3600})
			}))
			defer tokenServer.Close()
			openAICodexTokenURL = tokenServer.URL
			openAICodexCallbackTimeout = 5 * time.Second
			if mode != "callback" {
				openAICodexCallbackTimeout = time.Nanosecond
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			provider := newOpenAICodexOAuthProvider()
			opened, prompted := false, false
			provider.openBrowser = func(target string) error {
				opened = true
				if mode != "callback" {
					return nil
				}
				parsed, err := url.Parse(target)
				if err != nil {
					return err
				}
				client := &http.Client{Timeout: 5 * time.Second}
				response, err := client.Get("http://" + openAICodexCallbackAddress + "/auth/callback?code=callback-code&state=" + url.QueryEscape(parsed.Query().Get("state")))
				if err != nil {
					return err
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Errorf("callback status=%d", response.StatusCode)
				}
				return nil
			}
			credentials, err := provider.Login(ctx, oauthLoginCallbacks{
				OnOutput: func(message string) {
					if mode == "cancel-fallback" && strings.Contains(message, "Falling back") {
						cancel()
					}
				},
				OnAuth: func(oauthAuthInfo) {
					if mode == "cancel-auth" {
						cancel()
					}
				},
				OnPrompt: func(oauthPrompt) (string, error) {
					prompted = true
					if mode == "cancel-prompt" {
						cancel()
						return "callback-code", nil
					}
					return "callback-code#wrong-state", nil
				},
			})
			if mode == "callback" {
				if err != nil || credentials.AccountID != "callback-test" || tokens.Load() != 1 || prompted {
					t.Fatalf("callback credentials=%+v tokens=%d prompt=%v err=%v", credentials, tokens.Load(), prompted, err)
				}
			} else {
				if err == nil || tokens.Load() != 0 {
					t.Fatalf("invalid/canceled flow exchanged code: %d %v", tokens.Load(), err)
				}
				if mode == "cancel-auth" && (opened || prompted) {
					t.Fatal("authorization callback cancellation continued")
				}
				if mode == "cancel-fallback" && prompted {
					t.Fatal("fallback presentation cancellation continued to manual prompt")
				}
				if mode != "bad-manual-state" && !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation: %v", err)
				}
			}
		})
	}
}

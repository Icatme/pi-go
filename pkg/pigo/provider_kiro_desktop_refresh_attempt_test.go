package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func kiroDesktopRefreshAttemptFixture() kiroDesktopRefreshAttemptOptions {
	_, now := kiroRefreshFixture()
	return kiroDesktopRefreshAttemptOptions{Request: kiroDesktopRefreshRequestFixture(), MaxResponseBytes: 4096, MaxLifetime: 24 * time.Hour, RefreshSkew: 5 * time.Minute, Clock: func(context.Context) (time.Time, error) { return now, nil }}
}

func TestKiroDesktopRefreshAttemptScopeRotationAndConservativeExpiry(t *testing.T) {
	for _, mode := range []string{"omitted", "rotated", "matching-profile"} {
		t.Run(mode, func(t *testing.T) {
			options := kiroDesktopRefreshAttemptFixture()
			scope := options.Request.Scope
			oldToken := options.Request.RefreshToken
			raw := `{"accessToken":"NEW_ACCESS","expiresIn":3600}`
			if mode == "rotated" {
				raw = `{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":3600}`
			}
			if mode == "matching-profile" {
				raw = `{"accessToken":"NEW_ACCESS","expiresIn":3600,"profileArn":"explicit-profile"}`
			}
			_, start := kiroRefreshFixture()
			observed := start
			body := &kiroAttemptBody{reader: strings.NewReader(raw), onClose: func() { observed = start.Add(10 * time.Minute) }}
			options.Clock = func(context.Context) (time.Time, error) { return observed, nil }
			calls := 0
			result, invoked, err := runKiroInjectedDesktopRefreshAttempt(context.Background(), options, func(request *http.Request) (*http.Response, error) {
				calls++
				wire, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				var fields map[string]string
				if json.Unmarshal(wire, &fields) != nil || len(fields) != 1 || fields["refreshToken"] != oldToken || request.URL.Host != "prod.us-east-1.auth.desktop.kiro.dev" {
					t.Error("request shape")
				}
				options.Request.Scope.ProfileARN = "CHANGED"
				options.Request.RefreshToken = "CHANGED"
				return kiroCatalogHTTPFixture(body), nil
			})
			expected := oldToken
			if mode == "rotated" {
				expected = "NEW_REFRESH"
			}
			if err != nil || !invoked || calls != 1 || body.closes != 1 || result.Credential.Scope != scope || result.Credential.Token != "NEW_ACCESS" || result.RefreshToken != expected || result.retained != (mode != "rotated") || !result.ExpiresAt.Equal(start.Add(time.Hour)) {
				t.Fatal("desktop result", err)
			}
		})
	}
}

func TestKiroDesktopRefreshAttemptFailuresUnknownNotRetained(t *testing.T) {
	for _, mode := range []string{"profile-mismatch", "profile-null", "refresh-null", "refresh-empty", "transport", "redirect", "mime", "length", "budget", "close", "cancel", "expired", "backwards", "clock"} {
		t.Run(mode, func(t *testing.T) {
			options := kiroDesktopRefreshAttemptFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw := `{"accessToken":"NEW_ACCESS","expiresIn":3600}`
			switch mode {
			case "profile-mismatch":
				raw = `{"accessToken":"NEW_ACCESS","expiresIn":3600,"profileArn":"OTHER"}`
			case "profile-null":
				raw = `{"accessToken":"NEW_ACCESS","expiresIn":3600,"profileArn":null}`
			case "refresh-null":
				raw = `{"accessToken":"NEW_ACCESS","expiresIn":3600,"refreshToken":null}`
			case "refresh-empty":
				raw = `{"accessToken":"NEW_ACCESS","expiresIn":3600,"refreshToken":""}`
			}
			body := &kiroAttemptBody{reader: strings.NewReader(raw)}
			if mode == "close" {
				body.closeErr = errors.New("PRIVATE_CLOSE")
			}
			if mode == "cancel" {
				body.onClose = cancel
			}
			if mode == "budget" {
				options.MaxResponseBytes = len(raw) - 1
			}
			clocks := 0
			_, start := kiroRefreshFixture()
			options.Clock = func(context.Context) (time.Time, error) {
				clocks++
				if clocks == 2 {
					switch mode {
					case "expired":
						return start.Add(time.Hour), nil
					case "backwards":
						return start.Add(-time.Second), nil
					case "clock":
						return time.Time{}, errors.New("PRIVATE_CLOCK")
					}
				}
				return start, nil
			}
			calls := 0
			result, invoked, err := runKiroInjectedDesktopRefreshAttempt(ctx, options, func(*http.Request) (*http.Response, error) {
				calls++
				response := kiroCatalogHTTPFixture(body)
				switch mode {
				case "transport":
					return response, errors.New("PRIVATE_TRANSPORT")
				case "redirect":
					response.StatusCode = 307
					response.Header.Set("Location", "https://example.invalid/")
				case "mime":
					response.Header.Set("Content-Type", "text/plain")
				case "length":
					response.ContentLength = int64(len(raw) + 1)
				}
				return response, nil
			})
			if err != errKiroDesktopRefreshAttempt || !invoked || !reflect.DeepEqual(result, kiroRefreshResult{}) || calls != 1 || body.closes != 1 {
				t.Fatal("failure retained/retried", err)
			}
			if (mode == "transport" || mode == "redirect" || mode == "mime") && body.reads != 0 {
				t.Fatal("rejected body read")
			}
		})
	}
}

func TestKiroDesktopRefreshAttemptPreflightNoInvocation(t *testing.T) {
	for _, mode := range []string{"scope", "budget", "lifetime", "skew", "nil-clock", "clock-error", "zero-clock", "nil-executor", "cancel-clock"} {
		t.Run(mode, func(t *testing.T) {
			options := kiroDesktopRefreshAttemptFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			executor := func(*http.Request) (*http.Response, error) { calls++; return nil, nil }
			switch mode {
			case "scope":
				options.Request.Scope.ProfileARN = ""
			case "budget":
				options.MaxResponseBytes = 0
			case "lifetime":
				options.MaxLifetime = 0
			case "skew":
				options.RefreshSkew = -1
			case "nil-clock":
				options.Clock = nil
			case "clock-error":
				options.Clock = func(context.Context) (time.Time, error) { return time.Time{}, errors.New("PRIVATE_CLOCK") }
			case "zero-clock":
				options.Clock = func(context.Context) (time.Time, error) { return time.Time{}, nil }
			case "nil-executor":
				executor = nil
			case "cancel-clock":
				original := options.Clock
				options.Clock = func(ctx context.Context) (time.Time, error) { cancel(); return original(ctx) }
			}
			result, invoked, err := runKiroInjectedDesktopRefreshAttempt(ctx, options, executor)
			if err == nil || invoked || calls != 0 || !reflect.DeepEqual(result, kiroRefreshResult{}) {
				t.Fatal("preflight invoked")
			}
		})
	}
}

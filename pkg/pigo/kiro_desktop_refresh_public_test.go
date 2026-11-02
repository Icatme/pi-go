package pigo_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func kiroPublicDesktopConfig() pigo.KiroDesktopRefreshConfig {
	return pigo.KiroDesktopRefreshConfig{Scope: pigo.KiroCredentialScope{Region: "us-east-1", ProfileARN: "PRIVATE_PROFILE"}, UserAgent: "local-test/1", RefreshToken: "PRIVATE_OLD", MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxLifetime: 24 * time.Hour, RefreshSkew: 5 * time.Minute, Clock: func(context.Context) (time.Time, error) { return time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC), nil }}
}

func TestKiroPublicDesktopRefreshResultScopeAndRetention(t *testing.T) {
	for _, mode := range []string{"omitted", "rotated", "same-returned"} {
		t.Run(mode, func(t *testing.T) {
			rotated := mode != "omitted"
			config := kiroPublicDesktopConfig()
			original := config.RefreshToken
			raw := `{"accessToken":"PRIVATE_ACCESS","expiresIn":3600,"profileArn":"PRIVATE_PROFILE"}`
			if rotated {
				raw = `{"accessToken":"PRIVATE_ACCESS","refreshToken":"PRIVATE_NEW","expiresIn":3600,"profileArn":"PRIVATE_PROFILE"}`
			}
			if mode == "same-returned" {
				raw = strings.ReplaceAll(raw, "PRIVATE_NEW", "PRIVATE_OLD")
			}
			body := &kiroPublicCatalogBody{Reader: strings.NewReader(raw)}
			calls, clocks := 0, 0
			stamp, _ := config.Clock(context.Background())
			config.Clock = func(context.Context) (time.Time, error) {
				clocks++
				if clocks == 2 && body.closes != 1 {
					t.Error("ending clock before Close")
				}
				return stamp, nil
			}
			config.RoundTrip = func(request *http.Request) (*http.Response, error) {
				calls++
				raw, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				var fields map[string]string
				if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || fields["refreshToken"] != original || request.URL.String() != "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken" {
					t.Error("desktop wire")
				}
				return kiroPublicIDCResponse(body), nil
			}
			result, err := pigo.RefreshKiroDesktop(context.Background(), config)
			expected := original
			if mode == "rotated" {
				expected = "PRIVATE_NEW"
			}
			if err != nil || result == nil || calls != 1 || clocks != 2 || body.closes != 1 || config.RefreshToken != original || result.Credential().Scope != config.Scope || result.Credential().Token != "PRIVATE_ACCESS" || result.RefreshToken() != expected || result.RetainedRefreshToken() == rotated || !result.ExpiresAt().Equal(stamp.Add(time.Hour)) || !result.RefreshAfter().Equal(stamp.Add(55*time.Minute)) {
				t.Fatal("public result", err)
			}
			copy := result.Credential()
			copy.Token = "CHANGED"
			if result.Credential().Token != "PRIVATE_ACCESS" {
				t.Fatal("result alias")
			}
			for _, value := range []any{config, result, *result} {
				for _, format := range []string{"%v", "%+v", "%#v"} {
					if strings.Contains(fmt.Sprintf(format, value), "PRIVATE_") {
						t.Fatal("fmt leak")
					}
				}
			}
			if raw, err := json.Marshal(result); err != nil || string(raw) != "{}" {
				t.Fatal("persistable result")
			}
			if raw, err := json.Marshal(config); err == nil || len(raw) != 0 {
				t.Fatal("persistable callback config")
			}
		})
	}
}

func TestKiroPublicDesktopRefreshFailureMarkerAndNoFallback(t *testing.T) {
	for _, mode := range []string{"zero", "nil-context", "cancelled", "pre-clock", "before-read", "lost-response", "profile", "null-token", "close", "end-clock", "expired", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			config := kiroPublicDesktopConfig()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw := `{"accessToken":"PRIVATE_ACCESS","expiresIn":3600}`
			if mode == "profile" {
				raw = `{"accessToken":"PRIVATE_ACCESS","expiresIn":3600,"profileArn":"OTHER"}`
			}
			if mode == "null-token" {
				raw = `{"accessToken":"PRIVATE_ACCESS","expiresIn":3600,"refreshToken":null}`
			}
			body := &kiroPublicCatalogBody{Reader: strings.NewReader(raw)}
			if mode == "close" {
				body.failure = errors.New("PRIVATE_CLOSE")
			}
			calls, clocks := 0, 0
			oldClock := config.Clock
			config.Clock = func(ctx context.Context) (time.Time, error) {
				clocks++
				if mode == "pre-clock" || (mode == "end-clock" && clocks == 2) {
					return time.Time{}, errors.New("PRIVATE_CLOCK")
				}
				stamp, err := oldClock(ctx)
				if mode == "expired" && clocks == 2 {
					stamp = stamp.Add(time.Hour)
				}
				return stamp, err
			}
			config.RoundTrip = func(request *http.Request) (*http.Response, error) {
				calls++
				if mode == "before-read" {
					return nil, errors.New("PRIVATE_SEND")
				}
				if mode == "lost-response" {
					_, _ = io.Copy(io.Discard, request.Body)
					return nil, errors.New("PRIVATE_LOST")
				}
				response := kiroPublicIDCResponse(body)
				if mode == "redirect" {
					response.StatusCode = 302
					response.Header.Set("Location", "https://example.invalid/")
				}
				return response, nil
			}
			switch mode {
			case "zero":
				config = pigo.KiroDesktopRefreshConfig{}
			case "nil-context":
				ctx = nil
			case "cancelled":
				cancel()
			}
			result, err := pigo.RefreshKiroDesktop(ctx, config)
			var failure *pigo.KiroDesktopRefreshError
			pre := mode == "zero" || mode == "nil-context" || mode == "cancelled" || mode == "pre-clock"
			if result != nil || !errors.As(err, &failure) || failure.ExecutorInvoked() == pre || (pre && calls != 0) || (!pre && calls != 1) || err.Error() != "kiro: explicit desktop refresh failed" {
				t.Fatal("failure outcome", err, calls)
			}
			if !pre && mode != "before-read" && mode != "lost-response" && body.closes != 1 {
				t.Fatal("returned response not closed")
			}
			for _, format := range []string{"%v", "%+v", "%#v"} {
				if strings.Contains(fmt.Sprintf(format, err), "PRIVATE_") {
					t.Fatal("error secret")
				}
			}
			if raw, err := json.Marshal(failure); err != nil || string(raw) != "{}" {
				t.Fatal("error JSON")
			}
		})
	}
}

func TestKiroPublicDesktopRefreshZeroValues(t *testing.T) {
	var result pigo.KiroDesktopRefreshResult
	if result.Credential().Token != "" || result.RefreshToken() != "" || result.RetainedRefreshToken() || !result.ExpiresAt().IsZero() || !result.RefreshAfter().IsZero() {
		t.Fatal("zero result")
	}
	var failure *pigo.KiroDesktopRefreshError
	if failure.ExecutorInvoked() {
		t.Fatal("nil failure")
	}
}

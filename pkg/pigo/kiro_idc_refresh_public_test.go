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

func kiroPublicIDCConfig() pigo.KiroIDCRefreshConfig {
	return pigo.KiroIDCRefreshConfig{Scope: pigo.KiroCredentialScope{Region: "us-east-1", ProfileARN: "PRIVATE_PROFILE"}, UserAgent: "local-test/1", ClientID: "PRIVATE_CLIENT", ClientSecret: "PRIVATE_SECRET", RefreshToken: "PRIVATE_OLD", MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxLifetime: 24 * time.Hour, RefreshSkew: 5 * time.Minute, Clock: func(context.Context) (time.Time, error) { return time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC), nil }}
}
func kiroPublicIDCResponse(body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: 200, ContentLength: -1, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}
}

const kiroPublicIDCResponseJSON = `{"accessToken":"PRIVATE_ACCESS","refreshToken":"PRIVATE_ROTATED","expiresIn":3600}`

func TestKiroPublicIDCRefreshExplicitResultNoPersistence(t *testing.T) {
	config := kiroPublicIDCConfig()
	original := config.RefreshToken
	calls := 0
	body := &kiroPublicCatalogBody{Reader: strings.NewReader(kiroPublicIDCResponseJSON)}
	config.RoundTrip = func(r *http.Request) (*http.Response, error) {
		calls++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var fields map[string]string
		if json.Unmarshal(raw, &fields) != nil || fields["refreshToken"] != original || r.URL.String() != "https://oidc.us-east-1.amazonaws.com/token" {
			t.Error("explicit request")
		}
		return kiroPublicIDCResponse(body), nil
	}
	result, err := pigo.RefreshKiroIDC(context.Background(), config)
	if err != nil || result == nil || calls != 1 || body.closes != 1 || config.RefreshToken != original {
		t.Fatal("result/lifecycle", err)
	}
	stamp, _ := config.Clock(context.Background())
	if result.Credential().Token != "PRIVATE_ACCESS" || result.Credential().Scope != config.Scope || result.RefreshToken() != "PRIVATE_ROTATED" || !result.ExpiresAt().Equal(stamp.Add(time.Hour)) || !result.RefreshAfter().Equal(stamp.Add(55*time.Minute)) {
		t.Fatal("explicit result getters")
	}
	credential := result.Credential()
	credential.Token = "CHANGED"
	if result.Credential().Token != "PRIVATE_ACCESS" {
		t.Fatal("mutated result")
	}
	for _, value := range []any{config, result, *result} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, value), "PRIVATE_") {
				t.Fatal("fmt leak")
			}
		}
	}
	if encoded, err := json.Marshal(config); err == nil || len(encoded) != 0 {
		t.Fatal("callback config unexpectedly serializable")
	}
	encoded, err := json.Marshal(result)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("result persistence exposure")
	}
	var zero pigo.KiroIDCRefreshResult
	if zero.RefreshToken() != "" || zero.Credential().Token != "" || !zero.ExpiresAt().IsZero() || !zero.RefreshAfter().IsZero() {
		t.Fatal("zero result")
	}
}

func TestKiroPublicIDCRefreshUnknownRotationAfterExecutor(t *testing.T) {
	for _, mode := range []string{"before-read", "partial-read", "full-read", "response-close", "parse", "clock", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			config := kiroPublicIDCConfig()
			calls := 0
			body := &kiroPublicCatalogBody{Reader: strings.NewReader(kiroPublicIDCResponseJSON)}
			if mode == "response-close" {
				body.failure = errors.New("PRIVATE_CLOSE")
			}
			if mode == "parse" {
				body.Reader = strings.NewReader(`{"accessToken":"PRIVATE_ACCESS","expiresIn":3600}`)
			}
			if mode == "clock" {
				clockCalls := 0
				originalClock := config.Clock
				config.Clock = func(ctx context.Context) (time.Time, error) {
					clockCalls++
					if clockCalls == 2 {
						return time.Time{}, errors.New("PRIVATE_CLOCK")
					}
					return originalClock(ctx)
				}
			}
			config.RoundTrip = func(r *http.Request) (*http.Response, error) {
				calls++
				switch mode {
				case "before-read":
					return nil, errors.New("PRIVATE_BEFORE_SEND")
				case "partial-read":
					_, _ = io.ReadFull(r.Body, make([]byte, 3))
					return nil, errors.New("PRIVATE_PARTIAL_SEND")
				case "full-read":
					_, _ = io.Copy(io.Discard, r.Body)
					return nil, errors.New("PRIVATE_LOST_RESPONSE")
				}
				response := kiroPublicIDCResponse(body)
				if mode == "redirect" {
					response.StatusCode = 307
					response.Header.Set("Location", "https://example.invalid/")
				}
				return response, nil
			}
			result, err := pigo.RefreshKiroIDC(context.Background(), config)
			var failure *pigo.KiroIDCRefreshError
			if result != nil || !errors.As(err, &failure) || !failure.ExecutorInvoked() || calls != 1 || err.Error() != "kiro: explicit IDC refresh failed" {
				t.Fatal("indeterminate outcome/retry", err, calls)
			}
			if encoded, marshalErr := json.Marshal(failure); marshalErr != nil || string(encoded) != "{}" {
				t.Fatal("error JSON exposure")
			}
			for _, format := range []string{"%v", "%+v", "%#v"} {
				if strings.Contains(fmt.Sprintf(format, err), "PRIVATE_") {
					t.Fatal("error leaked")
				}
			}
			if (mode == "response-close" || mode == "parse" || mode == "clock" || mode == "redirect") && body.closes != 1 {
				t.Fatal("body not closed")
			}
		})
	}
}

func TestKiroPublicIDCRefreshNotDispatchedAndZeroConfig(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, mode := range []string{"zero", "nil-executor", "nil-context", "cancelled", "invalid-secret", "invalid-policy"} {
		t.Run(mode, func(t *testing.T) {
			config := kiroPublicIDCConfig()
			calls := 0
			config.RoundTrip = func(*http.Request) (*http.Response, error) { calls++; return nil, nil }
			ctx := context.Background()
			switch mode {
			case "zero":
				config = pigo.KiroIDCRefreshConfig{}
			case "nil-executor":
				config.RoundTrip = nil
			case "nil-context":
				ctx = nil
			case "cancelled":
				ctx = cancelled
			case "invalid-secret":
				config.ClientSecret = ""
			case "invalid-policy":
				config.MaxLifetime = 0
			}
			result, err := pigo.RefreshKiroIDC(ctx, config)
			var failure *pigo.KiroIDCRefreshError
			if result != nil || !errors.As(err, &failure) || failure.ExecutorInvoked() || calls != 0 {
				t.Fatal("preflight dispatch", err, calls)
			}
		})
	}
	var failure *pigo.KiroIDCRefreshError
	if failure.ExecutorInvoked() {
		t.Fatal("nil error")
	}
}

type kiroPublicRefreshCloseClockBody struct {
	io.Reader
	closed *bool
}

func (b *kiroPublicRefreshCloseClockBody) Close() error { *b.closed = true; return nil }

func TestKiroPublicIDCRefreshClockBoundaryAndUnknownExpiry(t *testing.T) {
	for _, mode := range []string{"pre-zero", "pre-error", "pre-year", "pre-cancel", "backwards", "exact-expired", "beyond-expired", "within-lifetime"} {
		t.Run(mode, func(t *testing.T) {
			config := kiroPublicIDCConfig()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start, _ := config.Clock(context.Background())
			calls, clocks := 0, 0
			closed := false
			config.Clock = func(context.Context) (time.Time, error) {
				clocks++
				if clocks == 1 {
					if calls != 0 || closed {
						t.Error("start clock after dispatch")
					}
					switch mode {
					case "pre-zero":
						return time.Time{}, nil
					case "pre-error":
						return time.Time{}, errors.New("PRIVATE_CLOCK")
					case "pre-cancel":
						cancel()
						return start, nil
					case "pre-year":
						return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), nil
					}
					return start, nil
				}
				if !closed || calls != 1 {
					t.Error("end clock before Close")
				}
				switch mode {
				case "backwards":
					return start.Add(-time.Second), nil
				case "exact-expired":
					return start.Add(time.Hour), nil
				case "beyond-expired":
					return start.Add(2 * time.Hour), nil
				default:
					return start.Add(59 * time.Minute), nil
				}
			}
			config.RoundTrip = func(*http.Request) (*http.Response, error) {
				calls++
				return kiroPublicIDCResponse(&kiroPublicRefreshCloseClockBody{Reader: strings.NewReader(kiroPublicIDCResponseJSON), closed: &closed}), nil
			}
			result, err := pigo.RefreshKiroIDC(ctx, config)
			if mode == "within-lifetime" {
				if err != nil || result == nil || !result.ExpiresAt().Equal(start.Add(time.Hour)) || !result.RefreshAfter().Equal(start.Add(55*time.Minute)) || clocks != 2 || calls != 1 || !closed {
					t.Fatal("deadline extended by delay", err)
				}
				return
			}
			var failure *pigo.KiroIDCRefreshError
			if result != nil || !errors.As(err, &failure) {
				t.Fatal("invalid clock released credentials")
			}
			pre := strings.HasPrefix(mode, "pre-")
			if failure.ExecutorInvoked() == pre || (pre && (calls != 0 || clocks != 1 || closed)) || (!pre && (calls != 1 || clocks != 2 || !closed)) {
				t.Fatal("wrong rotation outcome", calls, clocks, closed)
			}
		})
	}
}

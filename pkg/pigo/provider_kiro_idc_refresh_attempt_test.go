package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const kiroIDCFakeResponse = `{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":3600}`

func kiroIDCRefreshAttemptFixture() kiroIDCRefreshAttemptOptions {
	scope, now := kiroRefreshFixture()
	return kiroIDCRefreshAttemptOptions{Request: kiroIDCRefreshRequestFixture(), Scope: scope, MaxResponseBytes: 4096, MaxLifetime: 24 * time.Hour, RefreshSkew: 5 * time.Minute, Clock: func(context.Context) (time.Time, error) { return now, nil }}
}
func TestKiroInjectedIDCRefreshAttemptScopeRotationAndClose(t *testing.T) {
	options := kiroIDCRefreshAttemptFixture()
	original := options.Request
	body := &kiroAttemptBody{reader: strings.NewReader(kiroIDCFakeResponse)}
	calls, clocks := 0, 0
	_, now := kiroRefreshFixture()
	options.Clock = func(context.Context) (time.Time, error) {
		clocks++
		if clocks == 2 && body.closes != 1 {
			t.Error("end clock before Close")
		}
		return now, nil
	}
	result, err := runKiroInjectedIDCRefreshAttempt(context.Background(), options, func(r *http.Request) (*http.Response, error) {
		calls++
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var fields map[string]string
		if json.Unmarshal(raw, &fields) != nil || fields["refreshToken"] != original.RefreshToken || fields["clientSecret"] != original.ClientSecret || r.GetBody != nil {
			t.Error("request changed")
		}
		// Callback mutates the caller's variable, not the attempt's value snapshot.
		options.Request.RefreshToken = "CHANGED"
		options.Scope.ProfileARN = "CHANGED"
		return kiroCatalogHTTPFixture(body), nil
	})
	expectedScope, _ := kiroRefreshFixture()
	if err != nil || calls != 1 || clocks != 2 || body.closes != 1 || result.Credential.Scope != expectedScope || result.Credential.Token != "NEW_ACCESS" || result.RefreshToken != "NEW_REFRESH" || !result.ExpiresAt.Equal(now.Add(time.Hour)) || !result.RefreshAfter.Equal(now.Add(55*time.Minute)) {
		t.Fatal("result/lifecycle", err)
	}
	for _, value := range []any{result, kiroIDCRefreshAttemptFixture()} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			s := fmt.Sprintf(format, value)
			if strings.Contains(s, "PRIVATE_") || strings.Contains(s, "NEW_ACCESS") || strings.Contains(s, "NEW_REFRESH") {
				t.Fatal("fmt secrets")
			}
		}
	}
}

func TestKiroInjectedIDCRefreshAttemptFailuresAreZeroAndSingle(t *testing.T) {
	for _, mode := range []string{"transport", "redirect", "unauthorized", "mime", "encoding", "length", "budget", "read", "close", "cancel", "missing-rotation", "malformed", "clock", "zero-clock"} {
		t.Run(mode, func(t *testing.T) {
			options := kiroIDCRefreshAttemptFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw := kiroIDCFakeResponse
			if mode == "missing-rotation" {
				raw = `{"accessToken":"NEW_ACCESS","expiresIn":3600}`
			}
			if mode == "malformed" {
				raw = `{"accessToken":"PRIVATE_BROKEN"`
			}
			body := &kiroAttemptBody{reader: strings.NewReader(raw)}
			if mode == "read" {
				body.reader = io.MultiReader(strings.NewReader(raw), kiroTestErrorReader{})
			}
			if mode == "close" {
				body.closeErr = errors.New("PRIVATE_CLOSE")
			}
			if mode == "cancel" {
				body.onClose = cancel
			}
			clocks, calls := 0, 0
			oldClock := options.Clock
			options.Clock = func(ctx context.Context) (time.Time, error) {
				clocks++
				if mode == "clock" && clocks == 2 {
					return time.Time{}, errors.New("PRIVATE_CLOCK")
				}
				if mode == "zero-clock" && clocks == 2 {
					return time.Time{}, nil
				}
				return oldClock(ctx)
			}
			if mode == "budget" {
				options.MaxResponseBytes = len(raw) - 1
			}
			result, err := runKiroInjectedIDCRefreshAttempt(ctx, options, func(*http.Request) (*http.Response, error) {
				calls++
				response := kiroCatalogHTTPFixture(body)
				switch mode {
				case "transport":
					return response, errors.New("PRIVATE_TRANSPORT")
				case "redirect":
					response.StatusCode = 302
					response.Header.Set("Location", "https://example.invalid/secret")
				case "unauthorized":
					response.StatusCode = 401
				case "mime":
					response.Header.Set("Content-Type", "text/plain")
				case "encoding":
					response.Header.Set("Content-Encoding", "gzip")
				case "length":
					response.ContentLength = int64(len(raw) + 1)
				}
				return response, nil
			})
			if err != errKiroIDCRefreshAttempt || !reflect.DeepEqual(result, kiroRefreshResult{}) || calls != 1 || body.closes != 1 {
				t.Fatal("failure escaped or retried", err, calls, body.closes)
			}
			if (mode == "transport" || mode == "redirect" || mode == "unauthorized" || mode == "mime" || mode == "encoding") && body.reads != 0 {
				t.Fatal("rejected response body read")
			}
			parseFailure := mode == "missing-rotation" || mode == "malformed" || mode == "clock" || mode == "zero-clock"
			if (parseFailure && clocks != 2) || (!parseFailure && clocks != 1) {
				t.Fatal("clock ordering", clocks)
			}
		})
	}
}

type kiroTestErrorReader struct{}

func (kiroTestErrorReader) Read([]byte) (int, error) { return 0, errors.New("PRIVATE_READ") }

func TestKiroInjectedIDCRefreshAttemptPreflightNoExecutor(t *testing.T) {
	for _, change := range []func(*kiroIDCRefreshAttemptOptions){func(o *kiroIDCRefreshAttemptOptions) { o.Scope.Region = "eu-west-1" }, func(o *kiroIDCRefreshAttemptOptions) { o.Scope.ProfileARN = "" }, func(o *kiroIDCRefreshAttemptOptions) { o.MaxResponseBytes = 0 }, func(o *kiroIDCRefreshAttemptOptions) { o.MaxLifetime = time.Millisecond }, func(o *kiroIDCRefreshAttemptOptions) { o.RefreshSkew = -1 }, func(o *kiroIDCRefreshAttemptOptions) { o.Clock = nil }, func(o *kiroIDCRefreshAttemptOptions) { o.Request.ClientSecret = "" }} {
		options := kiroIDCRefreshAttemptFixture()
		change(&options)
		calls := 0
		result, err := runKiroInjectedIDCRefreshAttempt(context.Background(), options, func(*http.Request) (*http.Response, error) {
			calls++
			return kiroCatalogHTTPFixture(io.NopCloser(bytes.NewReader(nil))), nil
		})
		if err == nil || calls != 0 || !reflect.DeepEqual(result, kiroRefreshResult{}) {
			t.Fatal("preflight side effect")
		}
	}
	options := kiroIDCRefreshAttemptFixture()
	if _, err := runKiroInjectedIDCRefreshAttempt(context.Background(), options, nil); err == nil {
		t.Fatal("default executor")
	}
}

func TestKiroInjectedIDCRefreshAttemptWaitsForClose(t *testing.T) {
	options := kiroIDCRefreshAttemptFixture()
	closing, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	body := &kiroAttemptBody{reader: strings.NewReader(kiroIDCFakeResponse), onClose: func() { close(closing); <-release }}
	type outcome struct {
		result kiroRefreshResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runKiroInjectedIDCRefreshAttempt(context.Background(), options, func(*http.Request) (*http.Response, error) { return kiroCatalogHTTPFixture(body), nil })
		done <- outcome{result, err}
	}()
	select {
	case <-closing:
	case <-time.After(5 * time.Second):
		t.Fatal("Close not reached")
	}
	select {
	case <-done:
		t.Fatal("credentials released before Close")
	default:
	}
	unblock()
	select {
	case result := <-done:
		if result.err != nil || result.result.RefreshToken != "NEW_REFRESH" {
			t.Fatal("closed result")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not release")
	}
}

func TestKiroIDCRefreshExpiryCannotExtendDuringClose(t *testing.T) {
	options := kiroIDCRefreshAttemptFixture()
	_, start := kiroRefreshFixture()
	observed := start
	options.Clock = func(context.Context) (time.Time, error) { return observed, nil }
	body := &kiroAttemptBody{reader: strings.NewReader(kiroIDCFakeResponse), onClose: func() { observed = start.Add(10 * time.Minute) }}
	result, err := runKiroInjectedIDCRefreshAttempt(context.Background(), options, func(*http.Request) (*http.Response, error) { return kiroCatalogHTTPFixture(body), nil })
	if err != nil || !result.ExpiresAt.Equal(start.Add(time.Hour)) {
		t.Fatal("Close latency extended expiry", err, result.ExpiresAt)
	}
}

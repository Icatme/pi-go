package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

func kiroCatalogAttemptFixture() kiroCatalogAttemptOptions {
	scope, stamp := kiroRefreshFixture()
	return kiroCatalogAttemptOptions{Request: kiroCatalogRequestOptions{Scope: scope, Origin: "LOCAL_TEST", UserAgent: "local/1"}, MaxBytes: 4096, MaxModels: 8, Clock: func(context.Context) (time.Time, error) { return stamp, nil }}
}
func kiroCatalogHTTPFixture(body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: 200, Body: body, ContentLength: -1, Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}}
}

func TestKiroInjectedCatalogAttemptSingleScopedObservationAfterClose(t *testing.T) {
	options := kiroCatalogAttemptFixture()
	raw := []byte(`{"models":[{"modelId":"sample-2.3","tokenLimits":{"maxInputTokens":9007199254740993}}]}`)
	closing, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	body := &kiroAttemptBody{reader: bytes.NewReader(raw), onClose: func() { close(closing); <-release }}
	clockCalls := 0
	options.Clock = func(context.Context) (time.Time, error) {
		clockCalls++
		if body.closes != 1 {
			t.Error("clock before Close")
		}
		_, now := kiroRefreshFixture()
		return now, nil
	}
	type outcome struct {
		catalog *kiroScopedModelCatalog
		err     error
	}
	done := make(chan outcome, 1)
	calls := 0
	go func() {
		c, err := runKiroInjectedCatalogAttempt(context.Background(), options, kiroMixedSource, func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Path != "/List-Available-Models" {
				t.Error("wrong path")
			}
			response := kiroCatalogHTTPFixture(body)
			response.ContentLength = int64(len(raw))
			return response, nil
		})
		done <- outcome{c, err}
	}()
	select {
	case <-closing:
	case <-time.After(5 * time.Second):
		t.Fatal("Close not reached")
	}
	select {
	case <-done:
		t.Fatal("observation before Close")
	default:
	}
	unblock()
	var result outcome
	select {
	case result = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not finish")
	}
	if result.err != nil || clockCalls != 1 || calls != 1 || body.closes != 1 {
		t.Fatal("single observation", result.err)
	}
	_, now := kiroRefreshFixture()
	entry, err := result.catalog.selectModel(options.Request.Scope, kiroCatalogAlias, "sample-2-3", now, time.Hour)
	if err != nil || entry.ServiceID != "sample-2.3" || *entry.MaxInputTokens != 9007199254740993 {
		t.Fatal("bound selection", err)
	}
}

func TestKiroInjectedCatalogAttemptFailuresCloseAndNeverRetry(t *testing.T) {
	for _, mode := range []string{"transport", "redirect", "unauthorized", "encoding", "charset", "duplicate-type", "length", "oversize", "read-error", "no-progress", "close", "cancel", "invalid-json", "empty-catalog"} {
		t.Run(mode, func(t *testing.T) {
			options := kiroCatalogAttemptFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw := []byte(`{"models":[{"modelId":"sample"}]}`)
			if mode == "invalid-json" {
				raw = raw[:len(raw)-1]
			}
			if mode == "empty-catalog" {
				raw = []byte(`{"models":[]}`)
			}
			body := &kiroAttemptBody{reader: bytes.NewReader(raw)}
			response := kiroCatalogHTTPFixture(body)
			var transportErr error
			switch mode {
			case "transport":
				transportErr = errors.New("PRIVATE_TRANSPORT")
			case "redirect":
				response.StatusCode = 302
			case "unauthorized":
				response.StatusCode = 401
			case "encoding":
				response.Header.Set("Content-Encoding", "gzip")
			case "charset":
				response.Header.Set("Content-Type", "application/json; charset=latin1")
			case "duplicate-type":
				response.Header["content-type"] = []string{"application/json"}
			case "length":
				response.ContentLength = int64(len(raw) + 1)
			case "oversize":
				options.MaxBytes = len(raw) - 1
			case "read-error":
				body.reader = kiroOfflineFailReader{}
			case "no-progress":
				body.reader = &kiroOfflineEmptyReader{}
			case "close":
				body.closeErr = errors.New("PRIVATE_CLOSE")
			case "cancel":
				body.onClose = cancel
			}
			calls, clockCalls := 0, 0
			options.Clock = func(context.Context) (time.Time, error) {
				clockCalls++
				_, now := kiroRefreshFixture()
				return now, nil
			}
			catalog, err := runKiroInjectedCatalogAttempt(ctx, options, kiroMixedSource, func(*http.Request) (*http.Response, error) { calls++; return response, transportErr })
			if catalog != nil || err != errKiroCatalogAttempt || calls != 1 || body.closes != 1 {
				t.Fatal("failure retained observation/retried", mode, err, calls, body.closes)
			}
			if mode != "invalid-json" && mode != "empty-catalog" && clockCalls != 0 {
				t.Fatal("clock before HTTP validation")
			}
		})
	}
}

func TestKiroInjectedCatalogAttemptPreflightAndClockFailure(t *testing.T) {
	for _, mode := range []string{"nil-executor", "nil-clock", "budget", "model-limit", "clock-error", "clock-zero"} {
		t.Run(mode, func(t *testing.T) {
			options := kiroCatalogAttemptFixture()
			sources, calls := 0, 0
			source := func(ctx context.Context, s kiroCredentialScope) (kiroScopedCredential, error) {
				sources++
				return kiroMixedSource(ctx, s)
			}
			executor := func(*http.Request) (*http.Response, error) {
				calls++
				return kiroCatalogHTTPFixture(io.NopCloser(bytes.NewReader([]byte(`{"models":[{"modelId":"sample"}]}`)))), nil
			}
			switch mode {
			case "nil-executor":
				executor = nil
			case "nil-clock":
				options.Clock = nil
			case "budget":
				options.MaxBytes = 1
			case "model-limit":
				options.MaxModels = 0
			case "clock-error":
				options.Clock = func(context.Context) (time.Time, error) { return time.Time{}, errors.New("PRIVATE_CLOCK") }
			case "clock-zero":
				options.Clock = func(context.Context) (time.Time, error) { return time.Time{}, nil }
			}
			catalog, err := runKiroInjectedCatalogAttempt(context.Background(), options, source, executor)
			if catalog != nil || err != errKiroCatalogAttempt {
				t.Fatal("invalid attempt accepted")
			}
			expected := 0
			if mode == "clock-error" || mode == "clock-zero" {
				expected = 1
			}
			if sources != expected || calls != expected {
				t.Fatal("preflight/source ordering")
			}
		})
	}
}

type kiroCatalogBytesWithError struct {
	payload []byte
	failure error
}

func (r *kiroCatalogBytesWithError) Read(p []byte) (int, error) {
	n := copy(p, r.payload)
	r.payload = r.payload[n:]
	if len(r.payload) == 0 {
		return n, r.failure
	}
	return n, nil
}

func TestKiroInjectedCatalogAttemptExactBudgetAndFinalReadError(t *testing.T) {
	raw := []byte(`{"models":[{"modelId":"sample"}]}`)
	for _, mode := range []string{"exact", "full-error", "wrapped-eof", "clock-cancel", "nil-response", "nil-body", "uncompressed"} {
		t.Run(mode, func(t *testing.T) {
			options := kiroCatalogAttemptFixture()
			options.MaxBytes = len(raw)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := &kiroAttemptBody{reader: bytes.NewReader(raw)}
			response := kiroCatalogHTTPFixture(body)
			response.ContentLength = int64(len(raw))
			switch mode {
			case "full-error":
				body.reader = &kiroCatalogBytesWithError{payload: append([]byte(nil), raw...), failure: errors.New("PRIVATE_READ")}
			case "wrapped-eof":
				body.reader = &kiroCatalogBytesWithError{payload: append([]byte(nil), raw...), failure: errors.Join(io.EOF, errors.New("PRIVATE_WRAPPED"))}
			case "clock-cancel":
				options.Clock = func(context.Context) (time.Time, error) { cancel(); _, now := kiroRefreshFixture(); return now, nil }
			case "nil-response":
				response = nil
			case "nil-body":
				response.Body = nil
			case "uncompressed":
				response.Uncompressed = true
			}
			catalog, err := runKiroInjectedCatalogAttempt(ctx, options, kiroMixedSource, func(*http.Request) (*http.Response, error) { return response, nil })
			if mode == "exact" {
				if err != nil || catalog == nil {
					t.Fatal("exact EOF budget rejected", err)
				}
			} else if err != errKiroCatalogAttempt || catalog != nil {
				t.Fatal("invalid response produced catalog")
			}
			expected := 1
			if mode == "nil-response" || mode == "nil-body" {
				expected = 0
			}
			if body.closes != expected {
				t.Fatal("body close count")
			}
		})
	}
}

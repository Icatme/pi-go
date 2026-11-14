package pigo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPRetryAfterParser(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		values []string
		want   time.Duration
		ok     bool
	}{
		{"seconds", []string{"2"}, 2 * time.Second, true}, {"zero", []string{"0"}, 0, true}, {"whitespace", []string{" \t2 "}, 2 * time.Second, true},
		{"date", []string{now.Add(2 * time.Second).Format(http.TimeFormat)}, 2 * time.Second, true}, {"past", []string{now.Add(-time.Hour).Format(http.TimeFormat)}, 0, true},
		{"over", []string{"31"}, 0, false}, {"overflow", []string{"184467440737095516160"}, 0, false}, {"far-date", []string{"Fri, 31 Dec 9999 23:59:59 GMT"}, 0, false},
		{"negative", []string{"-1"}, 0, false}, {"fraction", []string{"0.5"}, 0, false}, {"empty", []string{""}, 0, false}, {"missing", nil, 0, false}, {"repeat", []string{"1", "1"}, 0, false}, {"comma", []string{"1,2"}, 0, false}, {"oversized", []string{string(make([]byte, 129))}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseHTTPRetryAfter(tc.values, now, 30*time.Second)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %v %v", got, ok)
			}
		})
	}
}

func TestHTTPRetryAfterExistingEligibilityAndOriginalError(t *testing.T) {
	for _, tc := range []struct {
		name        string
		enabled     bool
		values      []string
		status, max int
		want        int32
	}{
		{"default-ignores", false, []string{"30"}, 429, 1, 2}, {"opt-over-budget", true, []string{"30"}, 429, 1, 1}, {"duplicate", true, []string{"0", "0"}, 429, 1, 1}, {"malformed", true, []string{"bad"}, 429, 1, 1}, {"empty", true, []string{""}, 429, 1, 1}, {"missing", true, nil, 429, 1, 2}, {"zero", true, []string{"0"}, 429, 1, 2}, {"not-eligible", true, []string{"0"}, 400, 1, 1}, {"no-retries", true, []string{"0"}, 429, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) > 1 {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: done\n\n")
					return
				}
				for _, v := range tc.values {
					w.Header().Add("Retry-After", v)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, "fixture-original-error")
			}))
			defer s.Close()
			c := HTTPStreamClient{MaxRetries: tc.max, BaseRetryDelay: time.Millisecond, RespectRetryAfter: tc.enabled, RetryAfterMaxDelay: 5 * time.Millisecond}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := c.PostStream(ctx, s.URL, nil, nil, func(string, string) (bool, error) { return true, nil })
			if requests.Load() != tc.want {
				t.Fatalf("requests %d err=%v", requests.Load(), err)
			}
			if tc.want == 1 {
				if err == nil || err.Error() != "fixture-original-error" {
					t.Fatalf("original lost: %v", err)
				}
				var pe *providerFailureError
				if !errors.As(err, &pe) {
					t.Fatal("typed failure lost")
				}
				if recordedProviderHTTPStatus(err) != tc.status {
					t.Fatalf("status lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHTTPRetryAfterCancellationAndFloor(t *testing.T) {
	for _, header := range []string{"1", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)} {
		t.Run(header, func(t *testing.T) {
			var n atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				w.Header().Set("Retry-After", header)
				w.WriteHeader(429)
				fmt.Fprint(w, "rate limit")
			}))
			defer s.Close()
			c := HTTPStreamClient{MaxRetries: 1, BaseRetryDelay: time.Millisecond, RespectRetryAfter: true, RetryAfterMaxDelay: 2 * time.Hour}
			time.AfterFunc(30*time.Millisecond, cancel)
			err := c.PostStream(ctx, s.URL, nil, nil, func(string, string) (bool, error) { return true, nil })
			if !errors.Is(err, context.Canceled) || n.Load() != 1 {
				t.Fatalf("count=%d err=%v", n.Load(), err)
			}
		})
	}
}

func TestHTTPRetryAfterFiniteDefaultAndBackoffBudget(t *testing.T) {
	c := HTTPStreamClient{RespectRetryAfter: true}
	for _, budget := range []time.Duration{0, -1} {
		c.RetryAfterMaxDelay = budget
		ok, e := c.waitHTTPRetryDelay(context.Background(), http.Header{"Retry-After": []string{"31"}}, 0, time.Millisecond)
		if ok || e != nil {
			t.Fatalf("default budget: %v %v", ok, e)
		}
	}
	c.RetryAfterMaxDelay = time.Millisecond
	ok, e := c.waitHTTPRetryDelay(context.Background(), http.Header{"Retry-After": []string{"0"}}, 100, time.Duration(1<<62))
	if ok || e != nil {
		t.Fatalf("overflow: %v %v", ok, e)
	}
	c.MaxRetryDelay = time.Nanosecond
	ok, e = c.waitHTTPRetryDelay(context.Background(), http.Header{"Retry-After": []string{"0"}}, 100, time.Duration(1<<62))
	if !ok || e != nil {
		t.Fatalf("existing cap: %v %v", ok, e)
	}
}

func TestHTTPRetryAfterSecondsWaitsBeforeNextRequest(t *testing.T) {
	var n atomic.Int32
	first := make(chan time.Time, 1)
	second := make(chan time.Time, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			first <- time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		second <- time.Now()
		fmt.Fprint(w, "data: done\n\n")
	}))
	defer s.Close()
	c := HTTPStreamClient{MaxRetries: 1, BaseRetryDelay: time.Millisecond, MaxRetryDelay: time.Millisecond, RespectRetryAfter: true, RetryAfterMaxDelay: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err := c.PostStream(ctx, s.URL, nil, nil, func(string, string) (bool, error) { return true, nil })
	if err != nil || n.Load() != 2 {
		t.Fatalf("count=%d err=%v", n.Load(), err)
	}
	if gap := (<-second).Sub(<-first); gap < time.Second {
		t.Fatalf("early retry %v", gap)
	}
}

func TestHTTPRetryAfterMissingHeaderKeepsOldDelay(t *testing.T) {
	c := HTTPStreamClient{RespectRetryAfter: true, RetryAfterMaxDelay: time.Nanosecond}
	start := time.Now()
	retry, err := c.waitHTTPRetryDelay(context.Background(), http.Header{}, 0, 5*time.Millisecond)
	if !retry || err != nil || time.Since(start) < 5*time.Millisecond {
		t.Fatalf("missing hint changed backoff: %v %v", retry, err)
	}
}

func TestHTTPRetryAfterCancellationNoSecondSend(t *testing.T) {
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}))
	defer s.Close()
	c := HTTPStreamClient{RespectRetryAfter: true, MaxRetries: 1, BaseRetryDelay: time.Nanosecond}
	err := c.postStream(ctx, s.URL, httpStreamRequest{OnResponse: func(*http.Response) { cancel() }, ShouldRetry: func(int, string) bool { return true }})
	if !errors.Is(err, context.Canceled) || n.Load() != 1 {
		t.Fatalf("requests=%d err=%v", n.Load(), err)
	}
}

func TestHTTPRetryAfterRejectedHintDoesNotReplaceOriginalDecision(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := HTTPStreamClient{RespectRetryAfter: true}
	for _, value := range []string{"bad", "31"} {
		retry, err := c.waitHTTPRetryDelay(ctx, http.Header{"Retry-After": []string{value}}, 0, time.Millisecond)
		if retry || err != nil {
			t.Fatalf("rejected header must fall through to originalHTTPerror: %v %v", retry, err)
		}
	}
}

func TestHTTPRetryAfterDoesNotGrantCustomRetryEligibility(t *testing.T) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(503)
		fmt.Fprint(w, "original")
	}))
	defer s.Close()
	c := HTTPStreamClient{RespectRetryAfter: true, MaxRetries: 3, BaseRetryDelay: time.Millisecond}
	err := c.postStream(context.Background(), s.URL, httpStreamRequest{ShouldRetry: func(int, string) bool { return false }})
	if err == nil || n.Load() != 1 || recordedProviderHTTPStatus(err) != 503 {
		t.Fatalf("requests=%d err=%v", n.Load(), err)
	}
}

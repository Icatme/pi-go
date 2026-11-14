package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type bodyLimitFunc func(*http.Request) (*http.Response, error)

func (f bodyLimitFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type limitObservedBody struct {
	io.Reader
	closes atomic.Int32
}

func (b *limitObservedBody) Close() error { b.closes.Add(1); return nil }
func TestHTTPRequestBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		name, body      string
		declared, limit int64
		reason          string
	}{
		{"exact", "abcd", 4, 4, ""}, {"below", "abc", 3, 4, ""}, {"known over", "abcde", 5, 4, "limit_exceeded"}, {"lying over", "abcde", 3, 4, "limit_exceeded"}, {"short", "ab", 3, 4, "length_mismatch"}, {"long", "abc", 2, 4, "length_mismatch"}, {"unknown", "abc", -1, 4, "unknown_length"}, {"zero unknown", "abc", 0, 4, "unknown_length"}} {
		t.Run(tc.name, func(t *testing.T) {
			b := &limitObservedBody{Reader: strings.NewReader(tc.body)}
			r, _ := http.NewRequest("POST", "https://example.invalid", b)
			r.ContentLength = tc.declared
			r.Header.Set("X-Check", "same")
			origGetBody := r.GetBody
			calls := 0
			tr, err := NewHTTPRequestBodyLimitTransport(bodyLimitFunc(func(out *http.Request) (*http.Response, error) {
				calls++
				if out == r || out.Header.Get("X-Check") != "same" || out.Context() != r.Context() {
					t.Fatal("clone metadata")
				}
				if b.closes.Load() != 1 {
					t.Fatal("original not closed before send")
				}
				got, _ := io.ReadAll(out.Body)
				out.Body.Close()
				replay, _ := out.GetBody()
				again, _ := io.ReadAll(replay)
				replay.Close()
				if string(got) != tc.body || !bytes.Equal(got, again) {
					t.Fatal("payload changed")
				}
				return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
			}), tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tr.RoundTrip(r)
			if tc.reason != "" {
				var e *HTTPRequestBodyLimitError
				if !errors.As(err, &e) || e.Reason != tc.reason || calls != 0 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("%d %v", calls, err)
			}
			if b.closes.Load() != 1 || r.Body != b || r.ContentLength != tc.declared || (origGetBody == nil) != (r.GetBody == nil) {
				t.Fatal("ownership/metadata changed")
			}
		})
	}
}
func TestHTTPRequestBodyLimitEmptyAndConfig(t *testing.T) {
	for _, n := range []int64{0, -1, math.MaxInt64} {
		if tr, e := NewHTTPRequestBodyLimitTransport(nil, n); e == nil || tr != nil {
			t.Fatal(n)
		}
	}
	tr, _ := NewHTTPRequestBodyLimitTransport(bodyLimitFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}), 1)
	for _, b := range []io.ReadCloser{nil, http.NoBody} {
		r, _ := http.NewRequest("GET", "https://example.invalid", nil)
		r.Body = b
		if _, e := tr.RoundTrip(r); e != nil {
			t.Fatal(e)
		}
		r.ContentLength = 2
		if _, e := tr.RoundTrip(r); e == nil {
			t.Fatal("missing mismatch")
		}
	}
	if _, e := tr.RoundTrip(nil); e == nil {
		t.Fatal("nil")
	}
}

type limitErrorReader struct{ err error }

func (b limitErrorReader) Read([]byte) (int, error) { return 0, b.err }
func TestHTTPRequestBodyLimitReadFailure(t *testing.T) {
	sentinel := errors.New("PRIVATE request content")
	b := &limitObservedBody{Reader: limitErrorReader{sentinel}}
	r, _ := http.NewRequest("POST", "https://example.invalid", b)
	r.ContentLength = 1
	tr, _ := NewHTTPRequestBodyLimitTransport(bodyLimitFunc(func(*http.Request) (*http.Response, error) { t.Fatal("sent"); return nil, nil }), 4)
	_, e := tr.RoundTrip(r)
	if !errors.Is(e, sentinel) || strings.Contains(e.Error(), "PRIVATE") || b.closes.Load() != 1 {
		t.Fatal(e)
	}
}

type limitReadStarted struct {
	io.ReadCloser
	started chan struct{}
}

func (b *limitReadStarted) Read(p []byte) (int, error) {
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	return b.ReadCloser.Read(p)
}
func TestHTTPRequestBodyLimitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	defer pw.Close()
	started := make(chan struct{})
	r, _ := http.NewRequestWithContext(ctx, "POST", "https://example.invalid", &limitReadStarted{ReadCloser: pr, started: started})
	r.ContentLength = 1
	tr, _ := NewHTTPRequestBodyLimitTransport(bodyLimitFunc(func(*http.Request) (*http.Response, error) { t.Fatal("sent"); return nil, nil }), 4)
	done := make(chan error, 1)
	go func() { _, e := tr.RoundTrip(r); done <- e }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read not started")
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked")
	}
}
func TestHTTPRequestBodyLimitProviderRejectsBeforeSend(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(413) }))
	defer s.Close()
	tr, _ := NewHTTPRequestBodyLimitTransport(s.Client().Transport, 1024)
	model := GetModel("openai-codex", "gpt-5.5")
	model.BaseURL = s.URL
	var preflights atomic.Int32
	counted := bodyLimitFunc(func(r *http.Request) (*http.Response, error) { preflights.Add(1); return tr.RoundTrip(r) })
	out := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: strings.Repeat("x", 10000)}}}, SimpleStreamOptions{APIKey: makeOpenAICodexToken("fixture"), Transport: TransportSSE, HTTPClient: &http.Client{Transport: counted}})
	if preflights.Load() != 1 {
		t.Fatalf("preflight attempts %d", preflights.Load())
	}
	if calls.Load() != 0 || out.StopReason != StopReasonError || !strings.Contains(out.ErrorMessage, "limit_exceeded") {
		t.Fatalf("calls=%d out=%+v", calls.Load(), out)
	}
}
func TestHTTPRequestBodyLimitBoundedRead(t *testing.T) {
	src := strings.NewReader(strings.Repeat("x", 100))
	b := &limitObservedBody{Reader: src}
	r, _ := http.NewRequest("POST", "https://example.invalid", b)
	r.ContentLength = 1
	tr, _ := NewHTTPRequestBodyLimitTransport(bodyLimitFunc(func(*http.Request) (*http.Response, error) { t.Fatal("sent"); return nil, nil }), 4)
	_, e := tr.RoundTrip(r)
	if e == nil || src.Len() != 95 || b.closes.Load() != 1 {
		t.Fatalf("remaining=%d error=%v", src.Len(), e)
	}
}
func TestHTTPRequestBodyLimitRedirect(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		if string(b) != "data" {
			t.Errorf("body %q", b)
		}
		if r.URL.Path == "/first" {
			w.Header().Set("Location", "/second")
			w.WriteHeader(307)
			return
		}
		w.WriteHeader(200)
	}))
	defer s.Close()
	tr, _ := NewHTTPRequestBodyLimitTransport(s.Client().Transport, 4)
	c := &http.Client{Transport: tr}
	req, _ := http.NewRequest("POST", s.URL+"/first", strings.NewReader("data"))
	res, e := c.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}
func TestHTTPRequestBodyLimitAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := &limitObservedBody{Reader: strings.NewReader("x")}
	r, _ := http.NewRequestWithContext(ctx, "POST", "https://example.invalid", b)
	r.ContentLength = 1
	tr, _ := NewHTTPRequestBodyLimitTransport(bodyLimitFunc(func(*http.Request) (*http.Response, error) { t.Fatal("sent"); return nil, nil }), 4)
	_, e := tr.RoundTrip(r)
	if !errors.Is(e, context.Canceled) || b.closes.Load() != 1 {
		t.Fatal(e)
	}
}

type limitCountingReader struct{ reads int }

func (r *limitCountingReader) Read(p []byte) (int, error) { r.reads++; return 0, io.EOF }
func TestHTTPRequestBodyLimitRejectsWithoutRead(t *testing.T) {
	for _, n := range []int64{-1, 0, 5} {
		src := &limitCountingReader{}
		b := &limitObservedBody{Reader: src}
		r, _ := http.NewRequest("POST", "https://example.invalid", b)
		r.ContentLength = n
		tr, _ := NewHTTPRequestBodyLimitTransport(nil, 4)
		_, e := tr.RoundTrip(r)
		if e == nil || src.reads != 0 || b.closes.Load() != 1 {
			t.Fatalf("n=%d reads=%d err=%v", n, src.reads, e)
		}
	}
}

type limitTrailerReader struct {
	request *http.Request
	reader  io.Reader
}

func (b *limitTrailerReader) Read(p []byte) (int, error) {
	n, e := b.reader.Read(p)
	b.request.Trailer.Set("X-Final", "ready")
	return n, e
}
func (b *limitTrailerReader) Close() error { return nil }
func TestHTTPRequestBodyLimitMetadata(t *testing.T) {
	r, _ := http.NewRequest("POST", "https://example.invalid", strings.NewReader("abc"))
	r.Trailer = http.Header{"X-Final": nil}
	r.Host = "host.invalid"
	r.TransferEncoding = []string{"chunked"}
	r.Body = &limitTrailerReader{request: r, reader: strings.NewReader("abc")}
	tr, _ := NewHTTPRequestBodyLimitTransport(bodyLimitFunc(func(out *http.Request) (*http.Response, error) {
		defer out.Body.Close()
		if out.Trailer.Get("X-Final") != "ready" || out.Host != r.Host || len(out.TransferEncoding) != 1 || out.TransferEncoding[0] != "chunked" {
			t.Fatal("metadata lost")
		}
		out.Header.Set("X-Clone", "only")
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}), 4)
	if _, e := tr.RoundTrip(r); e != nil {
		t.Fatal(e)
	}
	original, e := r.GetBody()
	if e != nil {
		t.Fatal(e)
	}
	got, _ := io.ReadAll(original)
	original.Close()
	if string(got) != "abc" || r.Header.Get("X-Clone") != "" {
		t.Fatal("original changed")
	}
}

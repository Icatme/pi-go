package pigo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"sync"
)

// HTTPRequestBodyLimitError is a local preflight rejection. It is not an HTTP
// response and does not authorize a retry. Reason is unknown_length,
// limit_exceeded, length_mismatch, or body_read. No payload is included.
type HTTPRequestBodyLimitError struct {
	Reason   string
	Limit    int64
	Declared int64
	cause    error
}

func (e *HTTPRequestBodyLimitError) Error() string {
	return "HTTP request body preflight rejected: " + e.Reason
}
func (e *HTTPRequestBodyLimitError) Unwrap() error { return e.cause }

type httpRequestBodyLimitTransport struct {
	next  http.RoundTripper
	limit int64
}

// NewHTTPRequestBodyLimitTransport returns an opt-in HTTP body preflight wrapper.
// limit must be positive and less than MaxInt64. A nil next uses DefaultTransport.
// Nothing installs it automatically. Set it on a caller-owned http.Client.
//
// Bodies other than nil/http.NoBody require a positive ContentLength (even an
// empty reader is rejected when its length is unknown). Before invoking
// next, the wrapper buffers at most limit+1 bytes, checks actual length, and closes
// the original body. It rejects unknown lengths rather than consuming streams.
// Accepted requests use a cloned request and a replayable copy of the same bytes;
// headers and context are retained. The original request's metadata is unchanged.
// Memory is O(limit) per concurrent call. Request bodies must obey net/http's
// concurrent Read/Close contract so cancellation can unblock a pending read.
//
// This checks body bytes, not headers, image counts, decompressed sizes, WebSocket
// messages, or transports not using this client. It does not resize, trim, retry,
// infer a proxy limit, or change redirect policy. The next transport may have its
// own retries; each HTTP request/redirect entering this wrapper is checked.
func NewHTTPRequestBodyLimitTransport(next http.RoundTripper, limit int64) (http.RoundTripper, error) {
	if limit <= 0 || limit == math.MaxInt64 {
		return nil, fmt.Errorf("HTTP request body limit must be positive and less than MaxInt64")
	}
	if next == nil {
		next = http.DefaultTransport
	}
	return &httpRequestBodyLimitTransport{next: next, limit: limit}, nil
}

func (t *httpRequestBodyLimitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil {
		return nil, fmt.Errorf("nil HTTP request")
	}
	fail := func(reason string, cause error) (*http.Response, error) {
		return nil, &HTTPRequestBodyLimitError{Reason: reason, Limit: t.limit, Declared: r.ContentLength, cause: cause}
	}
	if r.Body == nil || r.Body == http.NoBody {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		if r.ContentLength > 0 {
			return fail("length_mismatch", nil)
		}
		return t.next.RoundTrip(r)
	}
	var once sync.Once
	closeBody := func() { once.Do(func() { _ = r.Body.Close() }) }
	defer closeBody()
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	if r.ContentLength <= 0 {
		return fail("unknown_length", nil)
	}
	if r.ContentLength > t.limit {
		return fail("limit_exceeded", nil)
	}
	stop := context.AfterFunc(r.Context(), closeBody)
	body, err := io.ReadAll(io.LimitReader(r.Body, t.limit+1))
	stop()
	closeBody()
	if ctxErr := r.Context().Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return fail("body_read", err)
	}
	if int64(len(body)) > t.limit {
		return fail("limit_exceeded", nil)
	}
	if int64(len(body)) != r.ContentLength {
		return fail("length_mismatch", nil)
	}
	out := r.Clone(r.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return t.next.RoundTrip(out)
}

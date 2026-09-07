package pigo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderHTTPRedirectDoesNotWaitForUnusedBody(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var finalCalls atomic.Int32
			stop := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/final" {
					finalCalls.Add(1)
					io.WriteString(w, "ok")
					return
				}
				w.Header().Set("Location", "/final")
				w.Header().Set("Content-Length", "100000")
				w.WriteHeader(status)
				io.WriteString(w, strings.Repeat("x", 4096))
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-stop:
				}
			}))
			defer server.Close()
			defer close(stop)
			observations := make(chan HTTPObservation, 3)
			base := server.Client()
			base.Timeout = time.Second
			client := NewProviderHTTPClient(base, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { observations <- o }})
			response, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK || finalCalls.Load() != 1 {
				t.Fatalf("status=%d final calls=%d", response.StatusCode, finalCalls.Load())
			}
			if len(observations) != 2 {
				t.Fatalf("observations=%d, want one for each round trip", len(observations))
			}
			first, second := <-observations, <-observations
			if first.StatusCode != status || first.ErrorBodyTruncated || first.Err != nil || second.StatusCode != 200 {
				t.Fatalf("redirect=%+v final=%+v", first, second)
			}
		})
	}
}

type trackedRedirectBody struct {
	io.ReadCloser
	read   atomic.Int64
	closed atomic.Int32
}

func (b *trackedRedirectBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read.Add(int64(n))
	return n, err
}

func (b *trackedRedirectBody) Close() error {
	b.closed.Add(1)
	return b.ReadCloser.Close()
}

func TestProviderHTTPRedirectLastResponseHasBoundedBody(t *testing.T) {
	const payload = `{"error":{"type":"redirect_error","code":"blocked"}}`
	for _, tc := range []struct {
		name       string
		body       string
		limit      int64
		incomplete bool
		truncated  bool
	}{
		{name: "oversized", body: strings.Repeat("x", 256), limit: 32, truncated: true},
		{name: "exact limit", body: payload, limit: int64(len(payload))},
		{name: "complete metadata", body: payload, limit: 128},
		{name: "incomplete body", body: payload, limit: 128, incomplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				length := len(tc.body)
				if tc.incomplete {
					length += 20
				}
				w.Header().Set("Content-Length", strconv.Itoa(length))
				w.Header().Set("Location", "/unused")
				w.WriteHeader(http.StatusTemporaryRedirect)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			base := server.Client()
			transport := base.Transport
			var source *trackedRedirectBody
			base.Transport = observationRoundTripper(func(r *http.Request) (*http.Response, error) {
				response, err := transport.RoundTrip(r)
				if err == nil {
					source = &trackedRedirectBody{ReadCloser: response.Body}
					response.Body = source
				}
				return response, err
			})
			base.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			observations := make(chan HTTPObservation, 3)
			client := NewProviderHTTPClient(base, ProviderHTTPPolicy{MaxErrorBodyBytes: tc.limit, Observe: func(o HTTPObservation) { observations <- o }})
			response, err := client.Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if source.read.Load() != 0 || source.closed.Load() != 0 || len(observations) != 0 {
				t.Fatal("redirect response consumed before its caller read the body")
			}
			body, readErr := io.ReadAll(response.Body)
			want := tc.body[:min(int64(len(tc.body)), tc.limit)]
			if string(body) != want || (tc.incomplete && !errors.Is(readErr, io.ErrUnexpectedEOF)) || (!tc.incomplete && readErr != nil) {
				t.Fatalf("body=%q read error=%v", body, readErr)
			}
			if source.read.Load() > tc.limit+1 || source.closed.Load() != 1 || calls.Load() != 1 {
				t.Fatalf("read=%d closes=%d server calls=%d", source.read.Load(), source.closed.Load(), calls.Load())
			}
			response.Body.Close()
			response.Body.Close()
			if len(observations) != 1 || source.closed.Load() != 1 {
				t.Fatalf("observations=%d closes=%d", len(observations), source.closed.Load())
			}
			got := <-observations
			if got.ErrorBodyTruncated != tc.truncated {
				t.Fatalf("observation=%+v", got)
			}
			if tc.truncated {
				if !errors.Is(got.Err, ErrProviderErrorBodyTooLarge) || got.ErrorCode != "" {
					t.Fatalf("observation=%+v", got)
				}
			} else if tc.incomplete {
				if !errors.Is(got.Err, io.ErrUnexpectedEOF) || got.ErrorCode != "" {
					t.Fatalf("observation=%+v", got)
				}
			} else if got.Err != nil || got.ErrorCode != "blocked" || got.ErrorType != "redirect_error" {
				t.Fatalf("observation=%+v", got)
			}
		})
	}
}

type blockingRedirectBody struct {
	*io.PipeReader
	started chan struct{}
}

func (b *blockingRedirectBody) Read(p []byte) (int, error) {
	close(b.started)
	return b.PipeReader.Read(p)
}

func TestProviderRedirectBodyCloseInterruptsRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	source := &blockingRedirectBody{PipeReader: reader, started: make(chan struct{})}
	observations := make(chan HTTPObservation, 3)
	body := newProviderRedirectBody(source, ProviderHTTPPolicy{MaxErrorBodyBytes: 32, Observe: func(o HTTPObservation) { observations <- o }}, HTTPObservation{Delivery: HTTPDeliveryResponseReceived, StatusCode: 307})
	readDone := make(chan error, 1)
	go func() {
		_, err := body.Read(make([]byte, 32))
		readDone <- err
	}()
	<-source.started
	closeDone := make(chan error, 1)
	go func() { closeDone <- body.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt the blocked Read")
	}
	select {
	case err := <-readDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("read error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read remained blocked after Close")
	}
	if len(observations) != 1 {
		t.Fatalf("observations=%d", len(observations))
	}
	if got := <-observations; !errors.Is(got.Err, io.ErrClosedPipe) || got.ErrorCode != "" {
		t.Fatalf("observation=%+v", got)
	}
}

type closeErrorRedirectBody struct {
	io.Reader
	err error
}

func (b closeErrorRedirectBody) Close() error { return b.err }

func TestProviderRedirectBodyObservesEarlyCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	observations := make(chan HTTPObservation, 3)
	body := newProviderRedirectBody(closeErrorRedirectBody{Reader: strings.NewReader(`{"error":{"code":"unread"}}`), err: closeErr}, ProviderHTTPPolicy{MaxErrorBodyBytes: 64, Observe: func(o HTTPObservation) { observations <- o }}, HTTPObservation{Delivery: HTTPDeliveryResponseReceived, StatusCode: 307})
	for range 2 {
		if err := body.Close(); !errors.Is(err, closeErr) {
			t.Fatalf("close error=%v", err)
		}
	}
	if len(observations) != 1 {
		t.Fatalf("observations=%d", len(observations))
	}
	if got := <-observations; !errors.Is(got.Err, closeErr) || got.ErrorCode != "" {
		t.Fatalf("observation=%+v", got)
	}
}

func TestProviderRedirectBodyEarlyCloseDoesNotIdentifyPartialJSON(t *testing.T) {
	const prefix = `{"error":{"code":"partial"}}`
	observations := make(chan HTTPObservation, 3)
	body := newProviderRedirectBody(io.NopCloser(strings.NewReader(prefix+" trailing bytes")), ProviderHTTPPolicy{MaxErrorBodyBytes: 64, Observe: func(o HTTPObservation) { observations <- o }}, HTTPObservation{Delivery: HTTPDeliveryResponseReceived, StatusCode: 307})
	defer body.Close()
	buf := make([]byte, len(prefix))
	if _, err := io.ReadFull(body, buf); err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations=%d", len(observations))
	}
	if got := <-observations; got.Err != nil || got.ErrorCode != "" || got.ErrorType != "" || got.ErrorBodyTruncated {
		t.Fatalf("observation=%+v", got)
	}
}

func TestProviderHTTPDefaultClientRedirectAndConnectionReuse(t *testing.T) {
	addresses := make(chan string, 2)
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/redirect" {
			addresses <- r.RemoteAddr
			io.WriteString(w, "ok")
			return
		}
		w.Header().Set("Location", "/final")
		w.Header().Set("Content-Length", "100000")
		w.WriteHeader(http.StatusTemporaryRedirect)
		io.WriteString(w, strings.Repeat("x", 4096))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer server.Close()
	defer close(stop)
	client := NewProviderHTTPClient(nil, ProviderHTTPPolicy{})
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, path := range []string{"/redirect", "/again"} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(body) != "ok" {
			t.Fatalf("status=%d body=%q error=%v", response.StatusCode, body, err)
		}
	}
	if len(addresses) != 2 {
		t.Fatalf("successful requests=%d", len(addresses))
	}
	if first, second := <-addresses, <-addresses; first != second {
		t.Fatalf("connection was not reused: first=%s second=%s", first, second)
	}
}

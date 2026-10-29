package pigo

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type failingObservedBody struct {
	cause         error
	closeErr      error
	reads, closes int
}

func (b *failingObservedBody) Read(p []byte) (int, error) {
	b.reads++
	return copy(p, "partial"), b.cause
}
func (b *failingObservedBody) Close() error { b.closes++; return b.closeErr }

func TestProviderHTTPBodyTypedReadObservation(t *testing.T) {
	for _, cause := range []error{&net.DNSError{IsTimeout: true}, context.DeadlineExceeded, context.Canceled, io.ErrUnexpectedEOF, errors.Join(io.EOF, context.DeadlineExceeded)} {
		t.Run(cause.Error(), func(t *testing.T) {
			body := &failingObservedBody{cause: cause, closeErr: errors.New("close failure")}
			var observations []HTTPObservation
			calls := 0
			client := NewProviderHTTPClient(&http.Client{Transport: observationRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: r}, nil
			})}, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { observations = append(observations, o) }})
			req, _ := http.NewRequest(http.MethodGet, "https://synthetic.invalid", nil)
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if body.reads != 0 || body.closes != 0 || len(observations) != 1 || observations[0].Err != nil {
				t.Fatal("eager body access or header error")
			}
			p := make([]byte, 32)
			n, err := res.Body.Read(p)
			if n != 7 || string(p[:n]) != "partial" || err != cause {
				t.Fatalf("read changed: %q %v", p[:n], err)
			}
			if len(observations) != 2 || observations[1].Err != cause || observations[1].StatusCode != 200 || observations[1].Delivery != HTTPDeliveryResponseReceived {
				t.Fatalf("observations=%+v", observations)
			}
			res.Body.Read(p)
			if err := res.Body.Close(); err != body.closeErr {
				t.Fatal("close error changed")
			}
			if len(observations) != 2 || calls != 1 || body.closes != 1 {
				t.Fatal("duplicate observation, request or close")
			}
		})
	}
}

func TestProviderHTTPBodyEOFAndNoObserver(t *testing.T) {
	for _, observed := range []bool{false, true} {
		body := io.NopCloser(strings.NewReader("ok"))
		count := 0
		policy := ProviderHTTPPolicy{}
		if observed {
			policy.Observe = func(HTTPObservation) { count++ }
		}
		client := NewProviderHTTPClient(&http.Client{Transport: observationRoundTripper(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: r}, nil
		})}, policy)
		req, _ := http.NewRequest(http.MethodGet, "https://synthetic.invalid", nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if !observed && res.Body != body {
			t.Fatal("no observer changed body")
		}
		data, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || string(data) != "ok" || (observed && count != 1) || (!observed && count != 0) {
			t.Fatalf("data=%q err=%v count=%d", data, err, count)
		}
	}
}

type closeUnblocksBody struct {
	started              chan struct{}
	closed               chan struct{}
	startOnce, closeOnce sync.Once
}

func (b *closeUnblocksBody) Read([]byte) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-b.closed
	return 0, context.Canceled
}
func (b *closeUnblocksBody) Close() error { b.closeOnce.Do(func() { close(b.closed) }); return nil }
func TestProviderHTTPBodyConcurrentClose(t *testing.T) {
	source := &closeUnblocksBody{started: make(chan struct{}), closed: make(chan struct{})}
	observations := make(chan HTTPObservation, 4)
	body := &providerObservedBody{ReadCloser: source, observe: func(o HTTPObservation) { observations <- o }, observation: HTTPObservation{StatusCode: 200, Delivery: HTTPDeliveryResponseReceived}}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := body.Read(make([]byte, 1))
			if !errors.Is(err, context.Canceled) {
				t.Errorf("read=%v", err)
			}
		}()
	}
	select {
	case <-source.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not start")
	}
	done := make(chan struct{})
	go func() { body.Close(); wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not unblock Read")
	}
	if len(observations) != 1 {
		t.Fatalf("observations=%d", len(observations))
	}
}

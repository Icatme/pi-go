package pigo

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
)

type observationRoundTripper func(*http.Request) (*http.Response, error)

func (f observationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type observedReadCloser struct {
	io.Reader
	read   int
	closed bool
}

func (b *observedReadCloser) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *observedReadCloser) Close() error { b.closed = true; return nil }

func TestProviderHTTPDeliveryEvidence(t *testing.T) {
	boom := errors.New("dial failed")
	for _, tc := range []struct {
		name  string
		trace func(*httptrace.ClientTrace)
		want  HTTPDelivery
	}{
		{"no trace is not proof", func(*httptrace.ClientTrace) {}, HTTPDeliveryUnknown},
		{"dns before connection", func(tr *httptrace.ClientTrace) { tr.DNSDone(httptrace.DNSDoneInfo{Err: boom}) }, HTTPDeliveryNotSent},
		{"connect before write", func(tr *httptrace.ClientTrace) { tr.ConnectDone("tcp", "unused", boom) }, HTTPDeliveryNotSent},
		{"earlier connection remains ambiguous", func(tr *httptrace.ClientTrace) {
			tr.GotConn(httptrace.GotConnInfo{})
			tr.ConnectDone("tcp", "unused", boom)
		}, HTTPDeliveryUnknown},
		{"write remains ambiguous", func(tr *httptrace.ClientTrace) { tr.WroteHeaders(); tr.ConnectDone("tcp", "unused", boom) }, HTTPDeliveryUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got HTTPObservation
			calls := 0
			base := &http.Client{Transport: observationRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				tc.trace(httptrace.ContextClientTrace(r.Context()))
				return nil, boom
			})}
			client := NewProviderHTTPClient(base, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
			req, _ := http.NewRequest(http.MethodPost, "https://example.invalid", strings.NewReader("{}"))
			_, err := client.Do(req)
			if err == nil || calls != 1 || got.Delivery != tc.want {
				t.Fatalf("calls=%d observation=%+v err=%v", calls, got, err)
			}
		})
	}
}

func TestProviderHTTPBoundsSDKErrorBody(t *testing.T) {
	body := &observedReadCloser{Reader: strings.NewReader(strings.Repeat("x", 1024))}
	var got HTTPObservation
	base := &http.Client{Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 502, Header: make(http.Header), Body: body}, nil
	})}
	client := NewProviderHTTPClient(base, ProviderHTTPPolicy{MaxErrorBodyBytes: 32, Observe: func(o HTTPObservation) { got = o }})
	req, _ := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil || len(data) != 32 || body.read != 33 || !body.closed || !got.ErrorBodyTruncated || !errors.Is(got.Err, ErrProviderErrorBodyTooLarge) {
		t.Fatalf("len=%d read=%d closed=%v observation=%+v", len(data), body.read, body.closed, got)
	}
}

func TestProviderHTTPMetadataAndSuccessfulStreams(t *testing.T) {
	for _, status := range []int{200, 429} {
		body := &observedReadCloser{Reader: strings.NewReader(`{"error":{"type":"rate_limit_error","code":"insufficient_quota"}}`)}
		var got HTTPObservation
		redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		base := &http.Client{CheckRedirect: redirect, Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: body}, nil
		})}
		client := NewProviderHTTPClient(base, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
		req, _ := http.NewRequest(http.MethodPost, "https://example.invalid", nil)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if got.Delivery != HTTPDeliveryResponseReceived || got.StatusCode != status {
			t.Fatalf("observation=%+v", got)
		}
		if status == 200 && (body.read != 0 || body.closed || response.Body != body) {
			t.Fatal("successful stream consumed eagerly")
		}
		if status == 429 && (got.ErrorCode != "insufficient_quota" || got.ErrorType != "rate_limit_error") {
			t.Fatalf("metadata=%+v", got)
		}
		response.Body.Close()
		if client.CheckRedirect(req, nil) != http.ErrUseLastResponse || base.Transport == client.Transport {
			t.Fatal("caller policy changed")
		}
		if providerHTTPClient(client) != client {
			t.Fatal("already bounded client wrapped again")
		}
	}
}

func TestProviderHTTPPreservesExistingTrace(t *testing.T) {
	var observed, existing bool
	base := &http.Client{Transport: observationRoundTripper(func(r *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(r.Context()).GotConn(httptrace.GotConnInfo{})
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}
	client := NewProviderHTTPClient(base, ProviderHTTPPolicy{Observe: func(HTTPObservation) { observed = true }})
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { existing = true }}))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !observed || !existing {
		t.Fatal("existing trace or policy callback was lost")
	}
}

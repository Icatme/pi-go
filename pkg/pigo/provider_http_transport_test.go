package pigo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderHTTPCustomDialTraceDoesNotProveNonDelivery(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var got HTTPObservation
	base := &http.Client{Transport: observationRoundTripper(func(r *http.Request) (*http.Response, error) {
		// net.Dialer emits ConnectDone but a custom HTTP implementation need not
		// emit GotConn or WroteRequest. A later dial failure cannot prove no send.
		var dialer net.Dialer
		conn, err := dialer.DialContext(r.Context(), "tcp", server.Listener.Addr().String())
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		if _, err := fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 0\r\n\r\n"); err != nil {
			return nil, err
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), r)
		if err != nil {
			return nil, err
		}
		response.Body.Close()
		_, err = dialer.DialContext(r.Context(), "tcp", "127.0.0.1:0")
		return nil, err
	})}
	for _, nested := range []bool{false, true} {
		transport := base.Transport
		if nested {
			outer := &http.Transport{}
			outer.RegisterProtocol("http", transport)
			defer outer.CloseIdleConnections()
			transport = outer
		}
		calls.Store(0)
		client := NewProviderHTTPClient(&http.Client{Transport: transport}, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
		req, _ := http.NewRequest(http.MethodPost, server.URL, nil)
		_, err := client.Do(req)
		if err == nil || calls.Load() != 1 || got.Delivery != HTTPDeliveryUnknown {
			t.Fatalf("nested=%v server calls=%d observation=%+v err=%v", nested, calls.Load(), got, err)
		}
	}
}

func TestProviderHTTPStandardConnectFailureRemainsUnknown(t *testing.T) {
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	var got HTTPObservation
	client := NewProviderHTTPClient(&http.Client{Transport: transport}, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:0", nil)
	_, err := client.Do(req)
	if err == nil || got.Delivery != HTTPDeliveryUnknown {
		t.Fatalf("observation=%+v err=%v", got, err)
	}
}

type failedProviderBody struct {
	io.Reader
	err error
}

func (b failedProviderBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}

func TestProviderHTTPPreservesErrorBodyReadFailures(t *testing.T) {
	const payload = `{"error":{"type":"server_error","code":"failed"}}`
	for _, readErr := range []error{io.ErrUnexpectedEOF, context.Canceled, context.DeadlineExceeded} {
		t.Run(readErr.Error(), func(t *testing.T) {
			body := &observedReadCloser{Reader: failedProviderBody{Reader: strings.NewReader(payload), err: readErr}}
			var got HTTPObservation
			client := NewProviderHTTPClient(&http.Client{Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 502, Header: make(http.Header), Body: body}, nil
			})}, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
			req, _ := http.NewRequest(http.MethodPost, "http://example.invalid", nil)
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if string(data) != payload || !errors.Is(err, readErr) || !body.closed || !errors.Is(got.Err, readErr) || got.ErrorCode != "" {
				t.Fatalf("body=%q read err=%v closed=%v observation=%+v", data, err, body.closed, got)
			}
		})
	}
}

func TestCompleteEntryPointsPreserveOpenAISDKErrorBodyFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"partial error","type":"invalid_request_error"}}`)
	}))
	defer server.Close()
	model := Model{ID: "test", Provider: "openai", API: "openai-responses", BaseURL: server.URL, Input: []InputType{InputText}, MaxTokens: 16}
	for _, simple := range []bool{false, true} {
		input := Context{Messages: []Message{UserMessage{Content: "test"}}}
		var response AssistantMessage
		if simple {
			response = CompleteSimple(model, input, SimpleStreamOptions{APIKey: "test-key", HTTPClient: server.Client(), MaxRetries: 0})
		} else {
			response = Complete(model, input, ProviderStreamOptions{APIKey: "test-key", HTTPClient: server.Client(), MaxRetries: 0})
		}
		if response.StopReason != StopReasonError || !strings.Contains(response.ErrorMessage, io.ErrUnexpectedEOF.Error()) {
			t.Fatalf("simple=%v response=%+v", simple, response)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("server calls=%d, want one per Complete entry point", calls.Load())
	}
}

func TestProviderHTTPReusedConnectionFailureRemainsUnknown(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			io.WriteString(w, "ok")
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer server.Close()
	var got HTTPObservation
	client := NewProviderHTTPClient(server.Client(), ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	var reused atomic.Bool
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader("request"))
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}))
	_, err = client.Do(req)
	if err == nil || !reused.Load() || calls.Load() != 2 || got.Delivery != HTTPDeliveryUnknown {
		t.Fatalf("reused=%v calls=%d observation=%+v err=%v", reused.Load(), calls.Load(), got, err)
	}
}

func TestProviderHTTPCancellationAfterSendRemainsUnknown(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint("timeout=", timeout), func(t *testing.T) {
			var calls atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				calls.Add(1)
				if !timeout {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			base := server.Client()
			if timeout {
				base.Timeout = 100 * time.Millisecond
			}
			var got HTTPObservation
			client := NewProviderHTTPClient(base, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("request"))
			_, err := client.Do(req)
			want := context.Canceled
			if timeout {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) || calls.Load() != 1 || got.Delivery != HTTPDeliveryUnknown {
				t.Fatalf("calls=%d observation=%+v err=%v", calls.Load(), got, err)
			}
		})
	}
}

func TestProviderHTTPPreservesProxy(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Host != "provider.invalid" {
			t.Errorf("proxy URL=%s", r.URL)
		}
		io.WriteString(w, "data: proxy\n\n")
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	var got HTTPObservation
	client := NewProviderHTTPClient(&http.Client{Transport: transport}, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
	response, err := client.Post("http://provider.invalid", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "data: proxy\n\n" || calls.Load() != 1 || got.Delivery != HTTPDeliveryResponseReceived {
		t.Fatalf("calls=%d body=%q observation=%+v err=%v", calls.Load(), body, got, err)
	}
}

func TestProviderHTTPTLSFailureRemainsConservative(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	var got HTTPObservation
	client := NewProviderHTTPClient(&http.Client{Transport: transport}, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
	_, err := client.Get(server.URL)
	if err == nil || calls.Load() != 0 || got.Delivery != HTTPDeliveryUnknown {
		t.Fatalf("calls=%d observation=%+v err=%v", calls.Load(), got, err)
	}
}

type partialProviderWriteConn struct {
	net.Conn
	written *atomic.Int32
}

func (c *partialProviderWriteConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p[:min(len(p), 8)])
	c.written.Add(int32(n))
	if err != nil {
		return n, err
	}
	return n, io.ErrUnexpectedEOF
}

func TestProviderHTTPPartialWriteRemainsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a partial request must not reach the handler")
	}))
	defer server.Close()
	var written, dials atomic.Int32
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		var dialer net.Dialer
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &partialProviderWriteConn{Conn: conn, written: &written}, nil
	}}
	defer transport.CloseIdleConnections()
	var got HTTPObservation
	client := NewProviderHTTPClient(&http.Client{Transport: transport}, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
	_, err := client.Post(server.URL, "application/json", strings.NewReader(`{"message":"test"}`))
	if err == nil || written.Load() == 0 || dials.Load() != 1 || got.Delivery != HTTPDeliveryUnknown {
		t.Fatalf("dials=%d bytes=%d observation=%+v err=%v", dials.Load(), written.Load(), got, err)
	}
}

func TestProviderHTTPConnectProxyFailureRemainsUnknown(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodConnect {
			t.Errorf("proxy method=%s", r.Method)
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	var got HTTPObservation
	client := NewProviderHTTPClient(&http.Client{Transport: transport}, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
	_, err := client.Get("https://provider.invalid")
	if err == nil || calls.Load() != 1 || got.Delivery != HTTPDeliveryUnknown {
		t.Fatalf("calls=%d observation=%+v err=%v", calls.Load(), got, err)
	}
}

func TestProviderHTTPErrorBodyCopyPreservesReadError(t *testing.T) {
	body := &providerErrorBody{reader: bytes.NewReader([]byte("partial")), err: io.ErrUnexpectedEOF}
	n, err := io.Copy(io.Discard, io.NopCloser(body))
	if n != 7 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("copied=%d err=%v", n, err)
	}
}

func TestProviderHTTPPrivateTransportConnectFailureIsNotSent(t *testing.T) {
	var got HTTPObservation
	client := NewProviderHTTPClient(nil, ProviderHTTPPolicy{Observe: func(o HTTPObservation) { got = o }})
	defer client.CloseIdleConnections()
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:0", nil)
	_, err := client.Do(req)
	if err == nil || got.Delivery != HTTPDeliveryNotSent {
		t.Fatalf("observation=%+v err=%v", got, err)
	}
}

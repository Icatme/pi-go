package pigo

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"
)

// HTTPDelivery describes transport evidence, not whether a model executed or billed.
// Unknown is intentional: a generic RoundTripper cannot prove non-delivery.
// Only the private standard transport used by a nil base client can prove not_sent.
type HTTPDelivery string

const (
	HTTPDeliveryUnknown          HTTPDelivery = "unknown"
	HTTPDeliveryNotSent          HTTPDelivery = "not_sent"
	HTTPDeliveryResponseReceived HTTPDelivery = "response_received"
	DefaultMaxProviderErrorBytes int64        = 64 << 10
)

var ErrProviderErrorBodyTooLarge = errors.New("provider error body exceeds limit")

// HTTPObservation describes one RoundTrip, not an entire redirect or retry sequence.
// Error identifiers are bounded. Err preserves the original error chain and must not be logged as trusted metadata.
// Redirect observations are delivered when their body finishes or is closed.
// Observe may run concurrently when the client is shared. A callback must be concurrency-safe.
type HTTPObservation struct {
	Delivery           HTTPDelivery
	StatusCode         int
	ErrorType          string
	ErrorCode          string
	ErrorBodyTruncated bool
	Err                error
}

type ProviderHTTPPolicy struct {
	// Non-positive limits use DefaultMaxProviderErrorBytes.
	MaxErrorBodyBytes int64
	Observe           func(HTTPObservation)
}

// NewProviderHTTPClient preserves a supplied client's transport, redirects, timeout
// and cookie jar; failures through that client remain unknown. A nil base uses a
// shared private standard transport with environment proxies and HTTP/2, independent
// of http.DefaultClient and http.DefaultTransport. Supply an explicit client
// to preserve customizations to those globals.
// It bounds error bodies before any SDK consumes them and reports transport evidence.
func NewProviderHTTPClient(base *http.Client, policy ProviderHTTPPolicy) *http.Client {
	ownedTransport := base == nil
	if ownedTransport {
		base = &http.Client{Transport: defaultProviderHTTPTransport}
	}
	client := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if policy.MaxErrorBodyBytes <= 0 {
		policy.MaxErrorBodyBytes = DefaultMaxProviderErrorBytes
	}
	client.Transport = &providerHTTPTransport{base: transport, policy: policy, ownedTransport: ownedTransport}
	return &client
}

func providerHTTPClient(base *http.Client) *http.Client {
	if base != nil {
		if _, ok := base.Transport.(*providerHTTPTransport); ok {
			return base
		}
	}
	return NewProviderHTTPClient(base, ProviderHTTPPolicy{})
}

// These defaults match Go 1.26 net/http.DefaultTransport. Keep this transport
// private: caller hooks and registered protocols invalidate negative evidence.
var defaultProviderHTTPTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: time.Second,
}

type providerHTTPTransport struct {
	base           http.RoundTripper
	policy         ProviderHTTPPolicy
	ownedTransport bool
}

func (t *providerHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var acquiring, connected, wrote, connectionFailed atomic.Bool
	if t.ownedTransport {
		trace := &httptrace.ClientTrace{
			GetConn:      func(string) { acquiring.Store(true) },
			GotConn:      func(httptrace.GotConnInfo) { connected.Store(true) },
			WroteHeaders: func() { wrote.Store(true) },
			WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
			DNSDone: func(info httptrace.DNSDoneInfo) {
				if info.Err != nil {
					connectionFailed.Store(true)
				}
			},
			ConnectDone: func(_, _ string, err error) {
				if err != nil {
					connectionFailed.Store(true)
				} else {
					connected.Store(true)
				}
			},
		}
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	}
	response, err := t.base.RoundTrip(request)
	observation := HTTPObservation{Delivery: HTTPDeliveryUnknown, Err: err}
	if err != nil {
		// Only the private transport has a known complete trace. A caller's
		// RoundTripper (even *http.Transport) may hide dispatch or retries.
		if t.ownedTransport && acquiring.Load() && connectionFailed.Load() && !connected.Load() && !wrote.Load() {
			observation.Delivery = HTTPDeliveryNotSent
		}
		t.observe(observation)
		return response, err
	}
	if response == nil {
		err = errors.New("provider transport returned no response")
		observation.Err = err
		t.observe(observation)
		return nil, err
	}
	observation.Delivery = HTTPDeliveryResponseReceived
	observation.StatusCode = response.StatusCode
	if isProviderRedirect(response) && response.Body != nil {
		// net/http may follow a redirect without draining a long response body.
		// Defer reading it so the body limit does not stall that decision.
		response.Body = newProviderRedirectBody(response.Body, t.policy, observation)
		return response, nil
	}
	if (response.StatusCode < 200 || response.StatusCode >= 300) && response.Body != nil {
		// Read at most limit+1 bytes, even if a proxy returns an unbounded error page.
		limit := t.policy.MaxErrorBodyBytes
		// Avoid overflow when a caller configures the largest int64 limit.
		extra := limit
		if extra < 1<<63-1 {
			extra++
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, extra))
		closeErr := response.Body.Close()
		observation.Err = errors.Join(readErr, closeErr)
		if int64(len(body)) > limit {
			body = body[:limit]
			observation.ErrorBodyTruncated = true
			observation.Err = errors.Join(ErrProviderErrorBodyTooLarge, observation.Err)
		}
		// Preserve HTTP status and the bounded prefix for existing SDK error handling.
		response.Body = io.NopCloser(&providerErrorBody{reader: bytes.NewReader(body), err: readErr})
		response.ContentLength = int64(len(body))
		if !observation.ErrorBodyTruncated && readErr == nil {
			observation.ErrorType, observation.ErrorCode = providerErrorIdentifiers(body)
		}
	}
	t.observe(observation)
	return response, nil
}

func (t *providerHTTPTransport) observe(observation HTTPObservation) {
	if t.policy.Observe != nil {
		t.policy.Observe(observation)
	}
}

func (t *providerHTTPTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func providerErrorIdentifiers(body []byte) (string, string) {
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return "", ""
	}
	return boundedProviderIdentifier(envelope.Error.Type), boundedProviderIdentifier(envelope.Error.Code)
}

func boundedProviderIdentifier(value string) string {
	if len(value) > 128 {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return ""
		}
	}
	return value
}

// providerErrorBody replays the bounded prefix without disguising a failed read as EOF.
type providerErrorBody struct {
	reader *bytes.Reader
	err    error
}

func (b *providerErrorBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if err == io.EOF && b.err != nil {
		return n, b.err
	}
	return n, err
}

func isProviderRedirect(response *http.Response) bool {
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return response.Header.Get("Location") != ""
	default:
		return false
	}
}

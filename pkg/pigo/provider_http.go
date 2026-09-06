package pigo

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
)

// HTTPDelivery describes transport evidence, not whether a model executed or billed.
// Unknown is intentional: custom transports without httptrace cannot prove non-delivery.
type HTTPDelivery string

const (
	HTTPDeliveryUnknown          HTTPDelivery = "unknown"
	HTTPDeliveryNotSent          HTTPDelivery = "not_sent"
	HTTPDeliveryResponseReceived HTTPDelivery = "response_received"
	DefaultMaxProviderErrorBytes int64        = 64 << 10
)

var ErrProviderErrorBodyTooLarge = errors.New("provider error body exceeds limit")

// HTTPObservation contains bounded metadata only; it never includes credentials or a body.
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

// NewProviderHTTPClient preserves the caller's transport, redirects, timeout and cookie jar.
// It bounds error bodies before any SDK consumes them and reports transport evidence.
func NewProviderHTTPClient(base *http.Client, policy ProviderHTTPPolicy) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if policy.MaxErrorBodyBytes <= 0 {
		policy.MaxErrorBodyBytes = DefaultMaxProviderErrorBytes
	}
	client.Transport = &providerHTTPTransport{base: transport, policy: policy}
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

type providerHTTPTransport struct {
	base   http.RoundTripper
	policy ProviderHTTPPolicy
}

func (t *providerHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var connected, wrote, connectionFailed atomic.Bool
	trace := &httptrace.ClientTrace{
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
			}
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err != nil {
				connectionFailed.Store(true)
			}
		},
	}
	response, err := t.base.RoundTrip(request.WithContext(httptrace.WithClientTrace(request.Context(), trace)))
	observation := HTTPObservation{Delivery: HTTPDeliveryUnknown, Err: err}
	if err != nil {
		// A later failed reconnect does not erase an earlier acquired connection/write.
		if connectionFailed.Load() && !connected.Load() && !wrote.Load() {
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
		if int64(len(body)) > limit {
			body = body[:limit]
			observation.ErrorBodyTruncated = true
			observation.Err = ErrProviderErrorBodyTooLarge
		} else if readErr != nil {
			observation.Err = readErr
		} else {
			observation.Err = closeErr
		}
		// Preserve HTTP status and the bounded prefix for existing SDK error handling.
		response.Body = io.NopCloser(bytes.NewReader(body))
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

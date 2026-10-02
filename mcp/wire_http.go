package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
)

type observedRoundTripper struct {
	observer *Observer
	base     http.RoundTripper
}

// RoundTripper observes original request/response bodies. A nil base uses the
// standard transport. The owner must forbid POST redirects and retrying base
// transports; this wrapper rejects a second SDK/OAuth physical send as well.
// It does not wrap SDK Connection and therefore preserves sessionUpdated.
func (o *Observer) RoundTripper(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &observedRoundTripper{observer: o, base: base}
}

func (rt *observedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	var requestID string
	if req.Method == http.MethodPost && req.Body != nil {
		body, err := readWireBody(req.Body, rt.observer.limits.MaxFrameBytes)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		if err := rt.observer.observeFrame(body, true); err != nil {
			return nil, err
		}
		var message wireEnvelope
		if json.Unmarshal(body, &message) == nil {
			requestID, _ = rpcKey(message.ID)
		}
		copyReq := req.Clone(req.Context())
		copyReq.Body = io.NopCloser(bytes.NewReader(body))
		// No body replay mechanism is introduced by observation.
		copyReq.GetBody = nil
		req = copyReq
	}
	resp, err := rt.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mediaType {
	case "application/json":
		resp.Body = &observedJSONBody{observer: rt.observer, source: resp.Body, requestID: requestID}
	case "text/event-stream":
		resp.Body = &observedSSEBody{observer: rt.observer, source: resp.Body, reader: bufio.NewReader(resp.Body), requestID: requestID}
	}
	return resp, nil
}

func readWireBody(reader io.Reader, limit int) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if len(body) > limit {
		return nil, ErrWireLimit
	}
	return body, err
}

type observedJSONBody struct {
	observer  *Observer
	requestID string
	source    io.ReadCloser
	mu        sync.Mutex
	read      bool
	buffer    []byte
	err       error
}

func (r *observedJSONBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.read {
		r.read = true
		r.buffer, r.err = readWireBody(r.source, r.observer.limits.MaxFrameBytes)
		if r.err == nil {
			r.err = r.observer.observeFrame(r.buffer, false)
		}
		if r.err != nil {
			r.buffer = nil
			r.observer.failRequest(r.requestID, r.err)
		}
	}
	if len(r.buffer) > 0 {
		n := copy(p, r.buffer)
		r.buffer = r.buffer[n:]
		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	return 0, io.EOF
}

func (r *observedJSONBody) Close() error { return r.source.Close() }

type observedSSEBody struct {
	observer         *Observer
	requestID        string
	source           io.ReadCloser
	reader           *bufio.Reader
	mu               sync.Mutex
	buffer           []byte
	err              error
	responseReceived bool
}

func (r *observedSSEBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buffer) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		var event, data []byte
		var name string
		for {
			line, err := readWireLine(r.reader, r.observer.limits.MaxFrameBytes-len(event))
			event = append(event, line...)
			if err != nil && !errors.Is(err, io.EOF) {
				r.err = err
				// Preserve this POST's IO cause before the SDK replaces it with
				// "request terminated without response". A completed response is
				// followed by SDK cancellation/drain, which must not fail the RPC.
				// Ordinary GET carrier errors remain SDK resumption decisions.
				if !r.responseReceived && (r.requestID != "" || errors.Is(err, ErrWireLimit)) {
					r.observer.failRequest(r.requestID, err)
				}
				return 0, err
			}
			text := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
			if len(text) > 0 {
				field, value, _ := strings.Cut(string(text), ":")
				// Match the pinned SDK's scanEventsLimited interpretation, while
				// returning the untouched event bytes to that scanner.
				value = strings.TrimSpace(value)
				switch field {
				case "data":
					if data != nil {
						data = append(data, '\n')
					}
					data = append(data, value...)
				case "event":
					name = value
				}
			}
			if len(text) == 0 || err != nil {
				// Like SDK SSE, named non-message events are ignored. Priming,
				// retry, and comment-only events carry no RPC result ownership.
				if len(data) > 0 && (name == "" || name == "message") {
					if observeErr := r.observer.observeFrame(data, false); observeErr != nil {
						r.err = observeErr
						r.observer.failRequest(r.requestID, observeErr)
						return 0, observeErr
					}
					if !r.responseReceived && r.requestID != "" {
						var message wireEnvelope
						if json.Unmarshal(data, &message) == nil && message.Method == "" && (len(message.Result) > 0 || len(message.Error) > 0) {
							id, idErr := rpcKey(message.ID)
							r.responseReceived = idErr == nil && id == r.requestID
						}
					}
				}
				r.buffer, r.err = event, err
				break
			}
		}
	}
	n := copy(p, r.buffer)
	r.buffer = r.buffer[n:]
	if n == 0 && r.err != nil {
		return 0, r.err
	}
	return n, nil
}

func (r *observedSSEBody) Close() error { return r.source.Close() }

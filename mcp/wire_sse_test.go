package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type failingWireSSEBody struct {
	err   error
	reads *atomic.Int32
}

func (r *failingWireSSEBody) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, r.err
}

func (*failingWireSSEBody) Close() error { return nil }

type wireSSEFaultTransport struct {
	base http.RoundTripper
	wrap func(context.Context, io.ReadCloser) (io.ReadCloser, error)
}

func (rt *wireSSEFaultTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var message struct {
		Method string
		Params struct{ Name string }
	}
	if req.Method == http.MethodPost && req.Body != nil {
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		if err := json.Unmarshal(body, &message); err != nil {
			return nil, err
		}
	}
	resp, err := rt.base.RoundTrip(req)
	if err != nil || message.Method != "tools/call" || message.Params.Name != "target" {
		return resp, err
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		resp.Body.Close()
		return nil, errors.New("fault fixture requires an SSE response")
	}
	resp.Body, err = rt.wrap(req.Context(), resp.Body)
	return resp, err
}

func newWireSSEFaultSession(t *testing.T, ctx context.Context, server *sdk.Server, observer *Observer, transport http.RoundTripper, middleware ...sdk.Middleware) *sdk.ClientSession {
	t.Helper()
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(httpServer.Close)
	client := sdk.NewClient(&sdk.Implementation{Name: "sse-fault-fixture", Version: "1"}, &sdk.ClientOptions{MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true}})
	if observer != nil {
		transport = observer.RoundTripper(transport)
		middleware = append([]sdk.Middleware{observer.Middleware()}, middleware...)
	}
	client.AddSendingMiddleware(middleware...)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint: httpServer.URL, HTTPClient: &http.Client{Transport: transport}, MaxRetries: -1,
	}, &sdk.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

type wireSSEDrainBody struct {
	source  io.ReadCloser
	failure error
	drained chan struct{}
	once    sync.Once
}

func (r *wireSSEDrainBody) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if errors.Is(err, io.EOF) {
		return n, r.failure
	}
	return n, err
}

func (r *wireSSEDrainBody) Close() error {
	err := r.source.Close()
	r.once.Do(func() { close(r.drained) })
	return err
}

func TestWireSSECompletedResponseIgnoresDrainFailure(t *testing.T) {
	for _, outcome := range []string{"success", "rpc_error"} {
		for _, drain := range []struct {
			name string
			err  error
		}{
			{"canceled", context.Canceled},
			{"eof", io.EOF},
			{"unexpected_eof", io.ErrUnexpectedEOF},
		} {
			t.Run(outcome+"/"+drain.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				observer := NewObserver(WireLimits{})
				defer observer.Close()
				server := wireTestServer()
				entered, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				defer releaseOnce.Do(func() { close(release) })
				addWireTool(server, "held", func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					close(entered)
					select {
					case <-release:
						return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				})
				addWireTool(server, "target", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					if outcome == "rpc_error" {
						return nil, &jsonrpc.Error{Code: -32000, Message: "fixture RPC failure"}
					}
					return &sdk.CallToolResult{Content: []sdk.Content{}, StructuredContent: map[string]any{"complete": true}}, nil
				})
				addWireTool(server, "healthy", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
					return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
				})
				drained, resultReceived, returnResult := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var returnOnce sync.Once
				defer returnOnce.Do(func() { close(returnResult) })
				transport := &wireSSEFaultTransport{
					base: http.DefaultTransport,
					wrap: func(_ context.Context, body io.ReadCloser) (io.ReadCloser, error) {
						return &wireSSEDrainBody{source: body, failure: drain.err, drained: drained}, nil
					},
				}
				// Keep the observer slot alive after the SDK has delivered the real
				// response, so the body drain cannot race past the assertion.
				pauseResult := func(next sdk.MethodHandler) sdk.MethodHandler {
					return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
						result, err := next(ctx, method, req)
						if params, ok := req.GetParams().(*sdk.CallToolParams); method == "tools/call" && ok && params.Name == "target" {
							close(resultReceived)
							select {
							case <-returnResult:
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						}
						return result, err
					}
				}
				session := newWireSSEFaultSession(t, ctx, server, observer, transport, pauseResult)
				heldDone := make(chan error, 1)
				go func() {
					_, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "held", Arguments: map[string]any{}})
					heldDone <- err
				}()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("concurrent request did not enter")
				}
				callCtx, snapshot := observer.Track(ctx)
				type result struct {
					value *sdk.CallToolResult
					err   error
				}
				completed := make(chan result, 1)
				go func() {
					value, err := session.CallTool(callCtx, &sdk.CallToolParams{Name: "target", Arguments: map[string]any{}})
					completed <- result{value, err}
				}()
				for _, event := range []<-chan struct{}{resultReceived, drained} {
					select {
					case <-event:
					case <-ctx.Done():
						t.Fatal("real response or post-response drain did not finish")
					}
				}
				returnOnce.Do(func() { close(returnResult) })
				var got result
				select {
				case got = <-completed:
				case <-ctx.Done():
					t.Fatal("completed RPC response was not returned")
				}
				if outcome == "rpc_error" {
					var rpcError *jsonrpc.Error
					if !errors.As(got.err, &rpcError) || rpcError.Code != -32000 || errors.Is(got.err, drain.err) {
						t.Fatalf("post-response drain changed RPC error: %v", got.err)
					}
				} else {
					raw, rawErr := observer.Raw(got.value)
					if got.err != nil || rawErr != nil || !bytes.Contains(raw, []byte(`"complete":true`)) {
						t.Fatalf("post-response drain changed success: result=%v err=%v raw=%s rawErr=%v", got.value, got.err, raw, rawErr)
					}
				}
				if record := snapshot(); record.Attempts != 1 || !record.ResponseReceived || record.RPCError != (outcome == "rpc_error") || (outcome == "success" && record.ResultType != "complete") {
					t.Fatalf("post-response drain changed execution facts: %+v", record)
				}
				releaseOnce.Do(func() { close(release) })
				select {
				case err := <-heldDone:
					if err != nil {
						t.Fatalf("draining a response cancelled another call: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("concurrent request did not complete")
				}
				if _, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "healthy", Arguments: map[string]any{}}); err != nil {
					t.Fatalf("draining a response ended SDK session: %v", err)
				}
			})
		}
	}
}

func TestWireSSEBodyFailureResponseOwnership(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		requestID string
		event     string
		failure   error
		wantFail  bool
	}{
		{"matching_success", "n:7", `data: {"jsonrpc":"2.0","id":7,"result":{}}` + "\n\n", context.Canceled, false},
		{"matching_rpc_error", "n:7", `data: {"jsonrpc":"2.0","id":7,"error":{"code":-32602,"message":"bad"}}` + "\n\n", context.Canceled, false},
		{"other_response", "n:7", `data: {"jsonrpc":"2.0","id":8,"result":{}}` + "\n\n", io.ErrUnexpectedEOF, true},
		{"server_request", "n:7", `data: {"jsonrpc":"2.0","id":7,"method":"ping"}` + "\n\n", io.ErrUnexpectedEOF, true},
		{"notification", "n:7", `data: {"jsonrpc":"2.0","method":"notifications/progress","params":{}}` + "\n\n", io.ErrUnexpectedEOF, true},
		{"named_non_message", "n:7", "event: other\n" + `data: {"jsonrpc":"2.0","id":7,"result":{}}` + "\n\n", io.ErrUnexpectedEOF, true},
		{"ownerless_get", "", "", io.ErrUnexpectedEOF, false},
		{"eof_without_response", "n:7", "", io.EOF, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			observer := NewObserver(WireLimits{})
			defer observer.Close()
			token, slot, err := observer.begin("tools/call")
			if err != nil {
				t.Fatal(err)
			}
			defer observer.finish(token)
			if err := observer.observeFrame(wireRequest(7, token, "tools/call"), true); err != nil {
				t.Fatal(err)
			}
			drained := make(chan struct{})
			source := &wireSSEDrainBody{source: io.NopCloser(bytes.NewBufferString(fixture.event)), failure: fixture.failure, drained: drained}
			body := &observedSSEBody{observer: observer, requestID: fixture.requestID, source: source, reader: bufio.NewReader(source)}
			_, err = io.ReadAll(body)
			body.Close()
			if fixture.failure != io.EOF && !errors.Is(err, fixture.failure) {
				t.Fatalf("reader replaced physical IO error: %v", err)
			}
			if errors.Is(slot.err, fixture.failure) != fixture.wantFail {
				t.Fatalf("physical IO error bound to wrong response: slot=%v wantFail=%t", slot.err, fixture.wantFail)
			}
		})
	}
}

func TestWireSSEBodyFailurePreservesCause(t *testing.T) {
	for _, observe := range []bool{false, true} {
		name := "plain_sdk_control"
		if observe {
			name = "observer"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			server := wireTestServer()
			entered, release, executed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			var executions, healthy, reads atomic.Int32
			addWireTool(server, "held", func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				close(entered)
				select {
				case <-release:
					return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			addWireTool(server, "target", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				executions.Add(1)
				close(executed)
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			addWireTool(server, "healthy", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				healthy.Add(1)
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			observer := NewObserver(WireLimits{})
			defer observer.Close()
			sentinel := errors.New("physical SSE response-body failure")
			failure := errors.Join(io.ErrUnexpectedEOF, sentinel)
			transport := &wireSSEFaultTransport{
				base: http.DefaultTransport,
				wrap: func(ctx context.Context, body io.ReadCloser) (io.ReadCloser, error) {
					// The real SDK server must execute the tool before its response
					// body fails. No fabricated RPC response reaches the client.
					select {
					case <-executed:
						body.Close()
						return &failingWireSSEBody{err: failure, reads: &reads}, nil
					case <-ctx.Done():
						body.Close()
						return nil, ctx.Err()
					}
				},
			}
			var sessionObserver *Observer
			if observe {
				sessionObserver = observer
			}
			session := newWireSSEFaultSession(t, ctx, server, sessionObserver, transport)
			heldDone := make(chan error, 1)
			go func() {
				_, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "held", Arguments: map[string]any{}})
				heldDone <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("concurrent request did not enter")
			}
			callCtx, snapshot := observer.Track(ctx)
			_, err := session.CallTool(callCtx, &sdk.CallToolParams{Name: "target", Arguments: map[string]any{}})
			if err == nil || errors.Is(err, sentinel) != observe || errors.Is(err, io.ErrUnexpectedEOF) != observe || reads.Load() == 0 || executions.Load() != 1 {
				t.Fatalf("body IO cause lost: error=%v sentinel=%t unexpectedEOF=%t reads=%d executions=%d", err, errors.Is(err, sentinel), errors.Is(err, io.ErrUnexpectedEOF), reads.Load(), executions.Load())
			}
			if observe && (snapshot().Attempts != 1 || snapshot().ResponseReceived || snapshot().ResultType != "") {
				t.Fatalf("body IO failure corrupted execution facts: %+v", snapshot())
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-heldDone:
				if err != nil {
					t.Fatalf("one failed body cancelled another call: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("concurrent request did not complete")
			}
			if _, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "healthy", Arguments: map[string]any{}}); err != nil || healthy.Load() != 1 {
				t.Fatalf("one failed body ended SDK session: err=%v executed=%d", err, healthy.Load())
			}
		})
	}
}

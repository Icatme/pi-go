package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/internal/jsontext"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func newWireFixture(t *testing.T, kind string, server *sdk.Server, options *sdk.ClientOptions) (*Observer, *sdk.ClientSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	observer := NewObserver(WireLimits{})
	t.Cleanup(observer.Close)
	if options == nil {
		options = &sdk.ClientOptions{}
	}
	options.MultiRoundTrip = &sdk.MultiRoundTripOptions{Disabled: true}
	client := sdk.NewClient(&sdk.Implementation{Name: "wire-test", Version: "1"}, options)
	client.AddSendingMiddleware(observer.Middleware())
	var transport sdk.Transport
	if kind == "io" {
		clientConn, serverConn := net.Pipe()
		ss, err := server.Connect(ctx, &sdk.IOTransport{Reader: serverConn, Writer: serverConn}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ss.Close() })
		transport = &sdk.IOTransport{Reader: observer.Reader(clientConn), Writer: observer.Writer(clientConn)}
	} else {
		handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: kind == "json"})
		httpServer := httptest.NewServer(handler)
		t.Cleanup(httpServer.Close)
		transport = &sdk.StreamableClientTransport{
			Endpoint: httpServer.URL,
			HTTPClient: &http.Client{
				Transport:     observer.RoundTripper(nil),
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
		}
	}
	session, err := client.Connect(ctx, transport, &sdk.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return observer, session
}

func wireTestServer() *sdk.Server {
	return sdk.NewServer(&sdk.Implementation{Name: "wire-fixture", Version: "1"}, &sdk.ServerOptions{
		PageSize: 1,
		SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) {
			c.TTLMs = 60000
			c.CacheScope = "private"
		},
	})
}

func addWireTool(server *sdk.Server, name string, handler sdk.ToolHandler) {
	server.AddTool(&sdk.Tool{
		Name: name,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{"id": map[string]any{
				"type": "number", "default": json.Number("9007199254740993"),
			}},
		},
	}, handler)
}

func TestWireTrueRawCachePaginationAndGenerations(t *testing.T) {
	for _, kind := range []string{"json", "sse", "io"} {
		t.Run(kind, func(t *testing.T) {
			server := wireTestServer()
			var lists atomic.Int32
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
					if method == "tools/list" {
						lists.Add(1)
					}
					return next(ctx, method, req)
				}
			})
			handler := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			}
			addWireTool(server, "alpha", handler)
			addWireTool(server, "beta", handler)
			observer, session := newWireFixture(t, kind, server, nil)
			page, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := observer.Raw(page)
			if err != nil || !bytes.Contains(raw, []byte("9007199254740993")) {
				t.Fatalf("real raw schema lost number: %s, %v", raw, err)
			}
			cached, err := session.ListTools(context.Background(), nil)
			if err != nil || page != cached || lists.Load() != 1 {
				t.Fatalf("not a true SDK cache hit: page identity=%v, lists=%d, ttl=%d protocol=%s err=%v", page == cached, lists.Load(), page.TTLMs, session.InitializeResult().ProtocolVersion, err)
			}
			cachedRaw, err := observer.Raw(cached)
			if err != nil || !bytes.Equal(raw, cachedRaw) {
				t.Fatalf("cache/raw source mismatch: %v", err)
			}
			raw[0] = '!'
			detached, _ := observer.Raw(page)
			if detached[0] == '!' {
				t.Fatal("Raw exposed observer storage")
			}
			if page.NextCursor == "" {
				t.Fatal("missing second page cursor")
			}
			second, err := session.ListTools(context.Background(), &sdk.ListToolsParams{Cursor: page.NextCursor})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := observer.Raw(second); err != nil || second == page || lists.Load() != 2 {
				t.Fatalf("second page unbound: %v", err)
			}
			fresh := NewObserver(WireLimits{})
			defer fresh.Close()
			if _, err := fresh.Raw(page); !errors.Is(err, ErrRawSnapshotUnavailable) {
				t.Fatalf("other generation bound old page: %v", err)
			}
			observer.Forget(page)
			if _, err := observer.Raw(cached); !errors.Is(err, ErrRawSnapshotUnavailable) {
				t.Fatalf("missing source was recreated from typed cache: %v", err)
			}
		})
	}
}

func TestWireConcurrentOutOfOrderCallsAndRPCErrorChain(t *testing.T) {
	for _, kind := range []string{"json", "sse", "io"} {
		t.Run(kind, func(t *testing.T) {
			server := wireTestServer()
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			addWireTool(server, "echo", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				var args struct {
					ID int `json:"id"`
				}
				if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
					return nil, err
				}
				if args.ID == 1 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				if args.ID == 3 {
					return nil, &jsonrpc.Error{Code: -32602, Message: "fixture bad args"}
				}
				return &sdk.CallToolResult{
					Content: []sdk.Content{},
					StructuredContent: map[string]any{
						"id": args.ID, "decimal": json.Number("0.100000000000000000001"),
					},
				}, nil
			})
			observer, session := newWireFixture(t, kind, server, nil)
			type outcome struct {
				result *sdk.CallToolResult
				err    error
			}
			firstDone := make(chan outcome, 1)
			firstCtx, firstTrace := observer.Track(context.Background())
			go func() {
				result, err := session.CallTool(firstCtx, &sdk.CallToolParams{Name: "echo", Arguments: map[string]any{"id": 1}})
				firstDone <- outcome{result, err}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("first call did not enter")
			}
			secondCtx, secondTrace := observer.Track(context.Background())
			second, err := session.CallTool(secondCtx, &sdk.CallToolParams{Name: "echo", Arguments: map[string]any{"id": 2}})
			if err != nil {
				t.Fatal(err)
			}
			secondRaw, err := observer.Raw(second)
			if err != nil || !bytes.Contains(secondRaw, []byte(`"id":2`)) || !bytes.Contains(secondRaw, []byte("0.100000000000000000001")) {
				t.Fatalf("second result incorrect: %s %v", secondRaw, err)
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case first := <-firstDone:
				if first.err != nil {
					t.Fatal(first.err)
				}
				raw, err := observer.Raw(first.result)
				if err != nil || !bytes.Contains(raw, []byte(`"id":1`)) {
					t.Fatalf("first result incorrect: %s %v", raw, err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("first call did not finish")
			}
			for _, record := range []DispatchRecord{firstTrace(), secondTrace()} {
				if record.Attempts != 1 || !record.ResponseReceived || record.RPCError || record.ResultType != "complete" || record.LogicalID == "" || record.RPCID == "" {
					t.Fatalf("completed trace facts invalid: %#v", record)
				}
			}
			if firstTrace().RPCID == secondTrace().RPCID || firstTrace().LogicalID == secondTrace().LogicalID {
				t.Fatal("concurrent traces shared identity")
			}
			errorCtx, errorTrace := observer.Track(context.Background())
			_, err = session.CallTool(errorCtx, &sdk.CallToolParams{Name: "echo", Arguments: map[string]any{"id": 3}})
			var rpcErr *jsonrpc.Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != -32602 {
				t.Fatalf("RPC error chain lost: %v", err)
			}
			if record := errorTrace(); record.Attempts != 1 || !record.ResponseReceived || !record.RPCError || record.ResultType != "" {
				t.Fatalf("RPC error fabricated tool completion: %#v", record)
			}
		})
	}
}

func TestWireCancellationTombstoneAndIDReuse(t *testing.T) {
	observer := NewObserver(WireLimits{MaxTombstones: 1})
	defer observer.Close()
	first, slot, _ := observer.begin("tools/call")
	if err := observer.observeFrame(wireRequest(1, first, "tools/call"), true); err != nil {
		t.Fatal(err)
	}
	observer.finish(first)
	if err := observer.observeFrame([]byte(`{"jsonrpc":"2.0","id":1,"result":{"late":true}}`), false); err != nil {
		t.Fatal(err)
	}
	if len(slot.raw) != 0 {
		t.Fatal("late response acquired cancelled slot")
	}
	second, secondSlot, _ := observer.begin("tools/call")
	if err := observer.observeFrame(wireRequest(2, second, "tools/call"), true); err != nil {
		t.Fatal(err)
	}
	if err := observer.observeFrame([]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed","params":{}}`), false); err != nil {
		t.Fatal(err)
	}
	if len(secondSlot.raw) != 0 {
		t.Fatal("notification bound as result")
	}
	observer.finish(second) // Evicts tombstone 1 into an exact retired range.
	reuse, _, _ := observer.begin("tools/call")
	if err := observer.observeFrame(wireRequest(1, reuse, "tools/call"), true); !errors.Is(err, ErrWireReplay) {
		t.Fatalf("evicted ID reuse allowed: %v", err)
	}
	observer.finish(reuse)
	fresh := NewObserver(WireLimits{})
	defer fresh.Close()
	newToken, newSlot, _ := fresh.begin("tools/call")
	if err := fresh.observeFrame(wireRequest(1, newToken, "tools/call"), true); err != nil {
		t.Fatalf("new generation could not reuse ID: %v", err)
	}
	if err := observer.observeFrame([]byte(`{"jsonrpc":"2.0","id":1,"result":{"late":true}}`), false); err != nil {
		t.Fatal(err)
	}
	if len(newSlot.raw) != 0 {
		t.Fatal("old generation contaminated new slot")
	}
	fresh.finish(newToken)
}

func wireRequest(id int, token, method string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":{"_meta":{%q:%q}}}`, id, method, wireTokenKey, token))
}

type wireBufferCloser struct{ bytes.Buffer }

func (*wireBufferCloser) Close() error { return nil }

func TestWireFramingUnicodeLimitsAndReplay(t *testing.T) {
	for _, invalid := range []string{
		`{"jsonrpc":"2.0","id":1,"result":{"text":"\ud800"}}`,
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"text\":\"\xff\"}}",
	} {
		observer := NewObserver(WireLimits{})
		reader := observer.Reader(io.NopCloser(strings.NewReader(invalid + "\n")))
		raw, err := io.ReadAll(reader)
		if len(raw) != 0 || !errors.Is(err, jsontext.ErrInvalidUnicode) {
			t.Fatalf("invalid Unicode reached SDK: %q %v", raw, err)
		}
		observer.Close()
	}
	observer := NewObserver(WireLimits{MaxFrameBytes: 128})
	defer observer.Close()
	if _, err := io.ReadAll(observer.Reader(io.NopCloser(strings.NewReader(strings.Repeat(" ", 129))))); !errors.Is(err, ErrWireLimit) {
		t.Fatalf("reader accepted oversized frame: %v", err)
	}
	observer = NewObserver(WireLimits{})
	defer observer.Close()
	token, _, _ := observer.begin("tools/call")
	defer observer.finish(token)
	var target wireBufferCloser
	writer := observer.Writer(&target)
	frame := append(wireRequest(1, token, "tools/call"), '\n')
	if _, err := writer.Write(frame[:10]); err != nil {
		t.Fatal(err)
	}
	if target.Len() != 0 {
		t.Fatal("partial unchecked frame was physically sent")
	}
	if _, err := writer.Write(frame[10:]); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(target.Bytes(), frame) {
		t.Fatal("writer changed original bytes")
	}
	if _, err := writer.Write(frame); !errors.Is(err, ErrWireReplay) {
		t.Fatalf("repeat physical call allowed: %v", err)
	}
	if !bytes.Equal(target.Bytes(), frame) {
		t.Fatal("duplicate reached underlying writer")
	}
}

func TestWireSSEPreservesEventsAndRejectsUnicode(t *testing.T) {
	observer := NewObserver(WireLimits{})
	defer observer.Close()
	token, slot, _ := observer.begin("tools/call")
	defer observer.finish(token)
	if err := observer.observeFrame(wireRequest(1, token, "tools/call"), true); err != nil {
		t.Fatal(err)
	}
	events := ": heartbeat\r\n\r\nevent: ignored\ndata: bad json\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n" +
		"event: message \t\ndata: {\"jsonrpc\":\"2.0\",\n" +
		"data: \"id\":1,\"result\": {\"exact\":9007199254740993}}\n\n"
	body := &observedSSEBody{observer: observer, source: io.NopCloser(strings.NewReader(events)), reader: nil}
	body.reader = bufio.NewReader(body.source)
	got, err := io.ReadAll(body)
	if err != nil || string(got) != events || !bytes.Contains(slot.raw, []byte("9007199254740993")) {
		t.Fatalf("SSE preservation failed: %q %s %v", got, slot.raw, err)
	}
	bad := "data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"s\":\"\\ud800\"}}\n\n"
	badBody := &observedSSEBody{observer: observer, source: io.NopCloser(strings.NewReader(bad))}
	badBody.reader = bufio.NewReader(badBody.source)
	if got, err := io.ReadAll(badBody); len(got) != 0 || !errors.Is(err, jsontext.ErrInvalidUnicode) {
		t.Fatalf("invalid SSE Unicode exposed: %q %v", got, err)
	}
}

func TestWireSDKCacheNotificationInvalidation(t *testing.T) {
	for _, kind := range []string{"json", "sse", "io"} {
		t.Run(kind, func(t *testing.T) {
			server := wireTestServer()
			notified := make(chan struct{}, 4)
			var lists atomic.Int32
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
					if method == "tools/list" {
						lists.Add(1)
					}
					return next(ctx, method, req)
				}
			})
			handler := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			}
			addWireTool(server, "alpha", handler)
			observer, session := newWireFixture(t, kind, server, &sdk.ClientOptions{
				ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) { notified <- struct{}{} },
			})
			first, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			firstRaw, err := observer.Raw(first)
			if err != nil {
				t.Fatal(err)
			}
			addWireTool(server, "beta", handler)
			select {
			case <-notified:
			case <-time.After(3 * time.Second):
				t.Fatal("no SDK cache invalidation notification")
			}
			second, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			secondRaw, err := observer.Raw(second)
			if err != nil || first == second || lists.Load() != 2 || bytes.Equal(firstRaw, secondRaw) {
				t.Fatalf("invalidated page reused old source: lists=%d raw=%s err=%v", lists.Load(), secondRaw, err)
			}
		})
	}
}

func TestWireSDKCacheTTLExpiry(t *testing.T) {
	for _, kind := range []string{"json", "sse", "io"} {
		t.Run(kind, func(t *testing.T) {
			server := sdk.NewServer(&sdk.Implementation{Name: "wire-fixture", Version: "1"}, &sdk.ServerOptions{
				SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) { c.TTLMs = 10 },
			})
			var lists atomic.Int32
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
					if method == "tools/list" {
						lists.Add(1)
					}
					return next(ctx, method, req)
				}
			})
			observer, session := newWireFixture(t, kind, server, nil)
			first, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			firstRaw, err := observer.Raw(first)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
			second, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := observer.Raw(second); err != nil || second == first || lists.Load() != 2 {
				t.Fatalf("expired cache source mismatch: %v", err)
			}
			// Identical content is still a different source object after TTL expiry.
			secondRaw, _ := observer.Raw(second)
			if !bytes.Equal(firstRaw, secondRaw) {
				t.Fatal("fixture expected identical directory bytes")
			}
		})
	}
}

type wireAllowAuthorization struct{ attempts atomic.Int32 }

func (a *wireAllowAuthorization) Authorize(context.Context, *http.Request, *http.Response) error {
	a.attempts.Add(1)
	return nil
}
func (*wireAllowAuthorization) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return nil, nil
}

func TestWireHTTPBlocksSDKOAuthPOSTReplay(t *testing.T) {
	server := wireTestServer()
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	var calls atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			var req wireEnvelope
			_ = json.Unmarshal(body, &req)
			if req.Method == "tools/call" {
				calls.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	observer := NewObserver(WireLimits{})
	defer observer.Close()
	auth := &wireAllowAuthorization{}
	client := sdk.NewClient(&sdk.Implementation{Name: "wire-test", Version: "1"}, &sdk.ClientOptions{MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true}})
	client.AddSendingMiddleware(observer.Middleware())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: httpServer.URL, OAuthHandler: auth, HTTPClient: &http.Client{Transport: observer.RoundTripper(nil)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, snapshot := observer.Track(ctx)
	_, err = session.CallTool(ctx, &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}})
	if !errors.Is(err, ErrWireReplay) || calls.Load() != 1 || auth.attempts.Load() != 1 {
		t.Fatalf("OAuth replay was not blocked: calls=%d auth=%d err=%v", calls.Load(), auth.attempts.Load(), err)
	}
	if record := snapshot(); record.Attempts != 1 || record.ResponseReceived || record.ResultType != "" {
		t.Fatalf("replay altered handoff facts: %#v", record)
	}
}

func TestWireSDKCancellationRetiresPending(t *testing.T) {
	for _, kind := range []string{"json", "sse", "io"} {
		t.Run(kind, func(t *testing.T) {
			server := wireTestServer()
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			var calls atomic.Int32
			addWireTool(server, "wait", func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				calls.Add(1)
				close(entered)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
				}
			})
			observer, session := newWireFixture(t, kind, server, nil)
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, snapshot := observer.Track(ctx)
			done := make(chan error, 1)
			go func() {
				_, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "wait", Arguments: map[string]any{}})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("call did not enter")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel cause lost: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation did not complete")
			}
			observer.mu.Lock()
			pending, ids, tombs := len(observer.pending), len(observer.ids), len(observer.tombstones)
			observer.mu.Unlock()
			if pending != 0 || ids != 0 || tombs != 1 || calls.Load() != 1 {
				t.Fatalf("cancelled request retained/replayed: pending=%d ids=%d tombs=%d calls=%d", pending, ids, tombs, calls.Load())
			}
			if record := snapshot(); record.Attempts != 1 || record.ResultType != "" {
				t.Fatalf("cancellation facts wrong: %#v", record)
			}
		})
	}
}

func TestWireTrackRejectedFrameReportsNoHandoff(t *testing.T) {
	observer := NewObserver(WireLimits{MaxFrameBytes: 128})
	ctx, snapshot := observer.Track(context.Background())
	var target wireBufferCloser
	writer := observer.Writer(&target)
	handler := observer.Middleware()(func(_ context.Context, method string, req sdk.Request) (sdk.Result, error) {
		params, err := json.Marshal(req.GetParams())
		if err != nil {
			return nil, err
		}
		frame := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, params))
		_, err = writer.Write(append(frame, '\n'))
		return nil, err
	})
	_, err := handler(ctx, "tools/call", &sdk.ClientRequest[*sdk.CallToolParams]{Params: &sdk.CallToolParams{Name: "write", Arguments: map[string]any{"value": strings.Repeat("x", 256)}}})
	if !errors.Is(err, ErrWireLimit) || target.Len() != 0 {
		t.Fatalf("rejected frame dispatched: bytes=%d err=%v", target.Len(), err)
	}
	record := snapshot()
	if record.Attempts != 0 || record.ResponseReceived || record.RPCID != "" || record.ResultType != "" {
		t.Fatalf("rejected frame facts invalid: %#v", record)
	}
	observer.Close()
	if snapshot() != record {
		t.Fatal("Close erased detached dispatch facts")
	}
}

func TestWireInputRequiredAndPreCancelFacts(t *testing.T) {
	for _, kind := range []string{"json", "sse", "io"} {
		t.Run(kind, func(t *testing.T) {
			server := wireTestServer()
			var calls atomic.Int32
			addWireTool(server, "interactive", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				calls.Add(1)
				return &sdk.CallToolResult{InputRequests: sdk.InputRequestMap{}}, nil
			})
			observer, session := newWireFixture(t, kind, server, nil)
			ctx, snapshot := observer.Track(context.Background())
			result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "interactive", Arguments: map[string]any{}})
			if err != nil || result == nil || !result.NeedsInput() {
				t.Fatalf("intermediate response missing: %#v %v", result, err)
			}
			if record := snapshot(); record.ResultType != "input_required" || record.Attempts != 1 || !record.ResponseReceived {
				t.Fatalf("intermediate became terminal: %#v", record)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			ctx, cancelSnapshot := observer.Track(ctx)
			_, err = session.CallTool(ctx, &sdk.CallToolParams{Name: "interactive", Arguments: map[string]any{}})
			if !errors.Is(err, context.Canceled) || cancelSnapshot().Attempts != 0 || calls.Load() != 1 {
				t.Fatalf("pre-cancelled call physically sent: %#v calls=%d err=%v", cancelSnapshot(), calls.Load(), err)
			}
		})
	}
}

func TestWireLateListAfterNotificationCannotRepopulateSDKCache(t *testing.T) {
	for _, kind := range []string{"json", "sse", "io"} {
		t.Run(kind, func(t *testing.T) {
			server := wireTestServer()
			addWireTool(server, "alpha", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			var lists atomic.Int32
			server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
				return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
					result, err := next(ctx, method, req)
					if method == "tools/list" && lists.Add(1) == 1 {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					return result, err
				}
			})
			notified := make(chan struct{}, 2)
			observer, session := newWireFixture(t, kind, server, &sdk.ClientOptions{ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) { notified <- struct{}{} }})
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			done := make(chan error, 1)
			ctx, snapshot := observer.Track(t.Context())
			go func() { _, err := session.ListTools(ctx, nil); done <- err }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("list did not generate first old page")
			}
			addWireTool(server, "beta", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			select {
			case <-notified:
			case <-time.After(3 * time.Second):
				t.Fatal("notification not delivered before old response")
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-done:
				if !errors.Is(err, ErrWireInvalidated) {
					t.Fatalf("late response accepted: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("old list response did not finish")
			}
			if record := snapshot(); record.Attempts != 1 || !record.ResponseReceived || record.RPCError || record.ResultType != "complete" {
				t.Fatalf("trusted successful list lost completion after local rejection: %#v", record)
			}
			fresh, err := session.ListTools(context.Background(), nil)
			if err != nil || fresh.NextCursor == "" || lists.Load() != 2 {
				t.Fatalf("SDK cached old terminal page: cursor=%v lists=%d err=%v", fresh, lists.Load(), err)
			}
			if _, err := observer.Raw(fresh); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWireDispatchCheckAtByteBoundaryOutsideLock(t *testing.T) {
	for _, kind := range []string{"json", "sse", "io"} {
		t.Run(kind, func(t *testing.T) {
			server := wireTestServer()
			var physical atomic.Int32
			addWireTool(server, "write", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				physical.Add(1)
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			observer, session := newWireFixture(t, kind, server, nil)
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			denied := errors.New("fixture secret denial explanation")
			var revoked atomic.Bool
			var checked atomic.Int32
			ctx, snapshot := observer.Track(context.Background())
			ctx = WithDispatchCheck(ctx, func(ctx context.Context) error {
				checked.Add(1)
				_ = snapshot() // Re-enters observer.mu; callback must run outside it.
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				if revoked.Load() {
					return denied
				}
				return nil
			})
			done := make(chan error, 1)
			go func() {
				_, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("byte-boundary check did not enter/reentrant deadlock")
			}
			revoked.Store(true)
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-done:
				if !errors.Is(err, ErrDispatchDenied) || !errors.Is(err, denied) {
					t.Fatalf("denial cause missing: %v", err)
				}
				if strings.Contains(err.Error(), "secret denial") {
					t.Fatalf("denial leaked callback text: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("denied call did not finish")
			}
			if record := snapshot(); record.Attempts != 0 || record.ResponseReceived || physical.Load() != 0 || checked.Load() != 1 {
				t.Fatalf("denied operation handed off: %#v physical=%d checks=%d", record, physical.Load(), checked.Load())
			}
		})
	}
}

func TestWireDispatchChecksComposeWithoutRetries(t *testing.T) {
	ctx := context.Background()
	var calls []int
	ctx = WithDispatchCheck(ctx, func(context.Context) error { calls = append(calls, 1); return nil })
	denied := errors.New("denied")
	ctx = WithDispatchCheck(ctx, func(context.Context) error { calls = append(calls, 2); return denied })
	ctx = WithDispatchCheck(ctx, func(context.Context) error { calls = append(calls, 3); return nil })
	check := ctx.Value(dispatchCheckKey{}).(func(context.Context) error)
	if err := check(ctx); err != denied || len(calls) != 2 || calls[0] != 1 || calls[1] != 2 {
		t.Fatalf("check composition lost prior policy: %v %#v", err, calls)
	}
}

type heldWireArguments struct {
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (a *heldWireArguments) MarshalJSON() ([]byte, error) {
	a.once.Do(func() { close(a.entered) })
	<-a.release
	return []byte(`{}`), nil
}

func TestWireSlowFirstHandoffSurvivesDefaultTombstoneCompaction(t *testing.T) {
	for _, stage := range []string{"dispatch_check", "sdk_params_marshal"} {
		t.Run(stage, func(t *testing.T) {
			server := wireTestServer()
			var calls atomic.Int32
			addWireTool(server, "write", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				calls.Add(1)
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			observer, session := newWireFixture(t, "json", server, nil)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			ctx, snapshot := observer.Track(t.Context())
			var args any = map[string]any{}
			if stage == "dispatch_check" {
				ctx = WithDispatchCheck(ctx, func(ctx context.Context) error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			} else {
				// The pinned SDK assigns an RPC ID before marshaling arguments.
				// This pauses before even the first observed byte-boundary check.
				args = &heldWireArguments{entered: entered, release: release}
			}
			done := make(chan error, 1)
			go func() {
				_, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "write", Arguments: args})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("first handoff did not park")
			}
			later := observer.limits.MaxTombstones + 2
			for i := 0; i < later; i++ {
				if _, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}}); err != nil {
					t.Fatalf("new call %d: %v", i, err)
				}
			}
			once.Do(func() { close(release) })
			select {
			case err := <-done:
				if err != nil || calls.Load() != int32(later+1) {
					t.Fatalf("first authorized handoff rejected after later completions: calls=%d err=%v record=%+v", calls.Load(), err, snapshot())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("first handoff did not finish")
			}
			if record := snapshot(); record.Attempts != 1 || !record.ResponseReceived || record.ResultType != "complete" {
				t.Fatalf("delayed handoff facts incorrect: %+v", record)
			}
			observer.mu.Lock()
			defer observer.mu.Unlock()
			if len(observer.pending) != 0 || len(observer.ids) != 0 || len(observer.tombstones) != observer.limits.MaxTombstones || len(observer.retiredIDs) != 1 {
				t.Fatalf("ID state did not stay bounded: pending=%d ids=%d tombstones=%d ranges=%v", len(observer.pending), len(observer.ids), len(observer.tombstones), observer.retiredIDs)
			}
		})
	}
}

func TestWireDispatchDenialKeepsSDKSessionUsable(t *testing.T) {
	for _, kind := range []string{"json", "sse", "io"} {
		t.Run(kind, func(t *testing.T) {
			server := wireTestServer()
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			addWireTool(server, "held", func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				close(entered)
				select {
				case <-release:
					return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			addWireTool(server, "write", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				calls.Add(1)
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			observer, session := newWireFixture(t, kind, server, nil)
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			heldCtx, heldSnapshot := observer.Track(t.Context())
			heldDone := make(chan error, 1)
			go func() {
				_, err := session.CallTool(heldCtx, &sdk.CallToolParams{Name: "held", Arguments: map[string]any{}})
				heldDone <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("concurrent call did not enter")
			}
			denied := errors.New("host revoked one tool approval")
			ctx, snapshot := observer.Track(t.Context())
			ctx = WithDispatchCheck(ctx, func(context.Context) error { return denied })
			_, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}})
			var denial *DispatchDeniedError
			if !errors.As(err, &denial) || denial.Err != denied || !errors.Is(err, denied) || ctx.Err() != nil {
				t.Fatalf("local denial lost its cause or cancelled caller: err=%v caller=%v", err, ctx.Err())
			}
			if record := snapshot(); record.Attempts != 0 || record.ResponseReceived || record.ResultType != "" || calls.Load() != 0 {
				t.Fatalf("local denial pretended to send: %+v calls=%d", record, calls.Load())
			}
			if _, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}}); err != nil || calls.Load() != 1 {
				t.Fatalf("one rejected call disabled another allowed call: calls=%d err=%v", calls.Load(), err)
			}
			once.Do(func() { close(release) })
			select {
			case err := <-heldDone:
				if err != nil || heldSnapshot().ResultType != "complete" {
					t.Fatalf("local denial cancelled concurrent call: err=%v record=%+v", err, heldSnapshot())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("concurrent call did not finish")
			}
		})
	}
}

func TestWireManagedStdioDenialDoesNotRetireSession(t *testing.T) {
	manager, err := New(Config{Scope: Scope{Identity: "account", AuthEpoch: 1}, Servers: []ServerConfig{processFixtureConfig("server")}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	connection, err := manager.Connect(t.Context(), "stdio")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	alive := captureFixtureProcess(t, connection.process.cmd.Process.Pid)
	denied := errors.New("host revoked one tool approval")
	ctx := WithDispatchCheck(t.Context(), func(context.Context) error { return denied })
	_, record, err := connection.CallToolTracked(ctx, &sdk.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if !errors.Is(err, denied) || !errors.Is(err, ErrDispatchDenied) || record.Attempts != 0 || record.ResponseReceived {
		t.Fatalf("not a local denial: err=%v record=%+v", err, record)
	}
	_, later, err := connection.CallToolTracked(t.Context(), &sdk.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if err != nil || later.Attempts != 1 || later.ResultType != "complete" {
		t.Fatalf("one local denial poisoned real managed stdio SDK session: err=%v next=%+v", err, later)
	}
	if ready, err := manager.Ready("stdio"); err != nil || ready != connection || !alive() {
		t.Fatalf("local denial replaced connection or stopped server: ready=%t err=%v alive=%t", ready == connection, err, alive())
	}
	select {
	case <-connection.sessionDone:
		t.Fatal("local denial ended actual SDK session")
	default:
	}
}

func TestWireSDKCancelledDispatchReservationIsReleased(t *testing.T) {
	for _, fixture := range []struct {
		kind         string
		returnCancel bool
	}{{"json", true}, {"json", false}, {"io", true}, {"io", false}} {
		t.Run(fmt.Sprintf("%s/return_cancel=%t", fixture.kind, fixture.returnCancel), func(t *testing.T) {
			server := wireTestServer()
			var calls atomic.Int32
			addWireTool(server, "write", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				calls.Add(1)
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			observer, session := newWireFixture(t, fixture.kind, server, nil)
			observer.limits.MaxPending = 1
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx, snapshot := observer.Track(ctx)
			entered := make(chan struct{})
			ctx = WithDispatchCheck(ctx, func(ctx context.Context) error {
				close(entered)
				<-ctx.Done()
				if fixture.returnCancel {
					return ctx.Err()
				}
				return nil
			})
			done := make(chan error, 1)
			go func() {
				_, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("reservation did not enter")
			}
			observer.mu.Lock()
			if len(observer.pending) != 1 || len(observer.ids) != 1 {
				t.Errorf("reservation not covered by pending bound: pending=%d ids=%d", len(observer.pending), len(observer.ids))
			}
			observer.mu.Unlock()
			if _, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}}); !errors.Is(err, ErrWireLimit) {
				t.Fatalf("pending bound allowed second request: %v", err)
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || snapshot().Attempts != 0 || calls.Load() != 0 {
					t.Fatalf("cancelled reservation handed off: err=%v record=%+v calls=%d", err, snapshot(), calls.Load())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled reservation did not finish")
			}
			observer.mu.Lock()
			if len(observer.pending) != 0 || len(observer.ids) != 0 {
				t.Errorf("cancelled reservation retained state: pending=%d ids=%d", len(observer.pending), len(observer.ids))
			}
			observer.mu.Unlock()
			if _, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}}); err != nil || calls.Load() != 1 {
				t.Fatalf("cancelled reservation disabled next call: err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestWireUnsentReservationIgnoresResponsesAndIDReuse(t *testing.T) {
	observer := NewObserver(WireLimits{})
	defer observer.Close()
	ctx, snapshot := observer.Track(t.Context())
	token, slot, err := observer.beginTraced("tools/call", ctx.Value(wireTraceKey{}).(*wireTrace))
	if err != nil {
		t.Fatal(err)
	}
	defer observer.finish(token)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	slot.ctx = ctx
	slot.check = func(context.Context) error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() { done <- observer.observeFrame(wireRequest(7, token, "tools/call"), true) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("reservation did not enter")
	}
	if err := observer.observeFrame([]byte(`{"jsonrpc":"2.0","id":7,"result":{"late":true}}`), false); err != nil {
		t.Fatal(err)
	}
	observer.mu.Lock()
	if len(slot.raw) != 0 || slot.sent || slot.trace.value.ResponseReceived {
		t.Error("response bound to an unsent reservation")
	}
	observer.mu.Unlock()
	reuse, _, err := observer.begin("tools/call")
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.observeFrame(wireRequest(7, reuse, "tools/call"), true); !errors.Is(err, ErrWireReplay) {
		t.Fatalf("reserved ID acquired by another call: %v", err)
	}
	observer.finish(reuse)
	once.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil || snapshot().Attempts != 1 || snapshot().ResponseReceived {
			t.Fatalf("reservation lost ownership/facts: err=%v record=%+v", err, snapshot())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reservation did not finish")
	}
}

func wireRawIDRequest(id string, token string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"_meta":{%q:%q}}}`, id, wireTokenKey, token))
}

func TestWireRetiredIDRangesAreExactAndBounded(t *testing.T) {
	t.Run("boundaries_and_holes", func(t *testing.T) {
		observer := NewObserver(WireLimits{MaxTombstones: 2})
		defer observer.Close()
		for _, n := range []int64{math.MaxInt64 - 2, math.MaxInt64 - 1, math.MaxInt64, math.MinInt64, math.MinInt64 + 1, math.MinInt64 + 2} {
			token, _, err := observer.begin("tools/call")
			if err != nil {
				t.Fatal(err)
			}
			if err := observer.observeFrame(wireRawIDRequest(fmt.Sprint(n), token), true); err != nil {
				t.Fatal(err)
			}
			observer.finish(token)
		}
		if observer.closed || len(observer.retiredIDs) != 2 || len(observer.tombstones) != 2 {
			t.Fatalf("boundary IDs did not compact: closed=%t ranges=%v recent=%v", observer.closed, observer.retiredIDs, observer.tombstones)
		}
		for _, n := range []int64{math.MaxInt64, math.MinInt64} {
			token, _, _ := observer.begin("tools/call")
			if err := observer.observeFrame(wireRawIDRequest(fmt.Sprint(n), token), true); !errors.Is(err, ErrWireReplay) {
				t.Fatalf("compacted ID %d was reusable: %v", n, err)
			}
			observer.finish(token)
		}
		token, slot, _ := observer.begin("tools/call")
		if err := observer.observeFrame(wireRequest(0, token, "tools/call"), true); err != nil {
			t.Fatalf("unobserved numeric hole was treated as retired: %v", err)
		}
		if err := observer.observeFrame([]byte(`{"jsonrpc":"2.0","id":9223372036854775807,"result":{"late":true}}`), false); err != nil || len(slot.raw) != 0 {
			t.Fatalf("retired response acquired hole: err=%v raw=%s", err, slot.raw)
		}
		observer.finish(token)
		if observer.closed || len(observer.retiredIDs) != 2 {
			t.Fatalf("numeric boundary extension overflowed: ranges=%v closed=%t", observer.retiredIDs, observer.closed)
		}
	})
	t.Run("range_limit", func(t *testing.T) {
		observer := NewObserver(WireLimits{MaxTombstones: 2})
		defer observer.Close()
		for _, n := range []int{1, 3, 5, 7, 9} {
			token, _, err := observer.begin("tools/call")
			if err != nil {
				t.Fatal(err)
			}
			if err := observer.observeFrame(wireRequest(n, token, "tools/call"), true); err != nil {
				t.Fatal(err)
			}
			observer.finish(token)
		}
		if !observer.closed || len(observer.retiredIDs) != 2 || len(observer.tombstones) != 2 {
			t.Fatalf("fragmented retirement exceeded bound: closed=%t ranges=%v recent=%v", observer.closed, observer.retiredIDs, observer.tombstones)
		}
		if _, _, err := observer.begin("tools/call"); !errors.Is(err, ErrObserverClosed) {
			t.Fatalf("range exhaustion accepted more requests: %v", err)
		}
	})
	t.Run("string_limit_and_invalid_numeric", func(t *testing.T) {
		observer := NewObserver(WireLimits{MaxTombstones: 1})
		defer observer.Close()
		token, _, _ := observer.begin("tools/call")
		for _, invalid := range []string{"9223372036854775808", "18446744073709551615", "-9223372036854775809"} {
			if err := observer.observeFrame(wireRawIDRequest(invalid, token), true); err == nil {
				t.Fatalf("out-of-int64 ID accepted: %s", invalid)
			}
		}
		if err := observer.observeFrame(wireRawIDRequest(`"old"`, token), true); err != nil {
			t.Fatal(err)
		}
		observer.finish(token)
		token, _, _ = observer.begin("tools/call")
		if err := observer.observeFrame(wireRequest(1, token, "tools/call"), true); err != nil {
			t.Fatal(err)
		}
		observer.finish(token)
		if !observer.closed {
			t.Fatal("uncompactable string retirement evicted without closing")
		}
	})
}

type failingWireWriter struct {
	target  io.WriteCloser
	failure error
	prefix  int
	enabled atomic.Bool
	writes  atomic.Int32
}

func (w *failingWireWriter) Write(p []byte) (int, error) {
	if !w.enabled.Load() {
		return w.target.Write(p)
	}
	w.writes.Add(1)
	if w.prefix > 0 {
		n, err := w.target.Write(p[:min(w.prefix, len(p))])
		if err != nil {
			return n, err
		}
		return n, w.failure
	}
	return 0, w.failure
}

func (w *failingWireWriter) Close() error { return w.target.Close() }

func TestWireStdioPhysicalFailureIsTerminal(t *testing.T) {
	for _, prefix := range []int{0, 3} {
		t.Run(fmt.Sprint(prefix), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			server := wireTestServer()
			var calls atomic.Int32
			addWireTool(server, "write", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				calls.Add(1)
				return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
			})
			clientConn, serverConn := net.Pipe()
			ss, err := server.Connect(ctx, &sdk.IOTransport{Reader: serverConn, Writer: serverConn}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			observer := NewObserver(WireLimits{})
			defer observer.Close()
			physicalErr := errors.New("fixture physical stdio failure")
			writer := &failingWireWriter{target: clientConn, failure: physicalErr, prefix: prefix}
			client := sdk.NewClient(&sdk.Implementation{Name: "write-failure", Version: "1"}, &sdk.ClientOptions{MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true}})
			client.AddSendingMiddleware(observer.Middleware())
			session, err := client.Connect(ctx, &sdk.IOTransport{Reader: observer.Reader(clientConn), Writer: observer.Writer(writer)}, &sdk.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			writer.enabled.Store(true)
			callCtx, snapshot := observer.Track(t.Context())
			_, err = session.CallTool(callCtx, &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}})
			if !errors.Is(err, physicalErr) || snapshot().Attempts != 1 || snapshot().ResponseReceived || calls.Load() != 0 {
				t.Fatalf("physical failure lost unknown-dispatch facts: err=%v record=%+v calls=%d", err, snapshot(), calls.Load())
			}
			waited := make(chan error, 1)
			go func() { waited <- session.Wait() }()
			select {
			case err := <-waited:
				if !errors.Is(err, physicalErr) {
					t.Fatalf("actual SDK session lost fatal write cause: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("actual IO failure did not end SDK session")
			}
			if _, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}}); err == nil || writer.writes.Load() != 1 {
				t.Fatalf("fatal IO failure retried physical send: err=%v writes=%d", err, writer.writes.Load())
			}
		})
	}
}

func TestWireWriterDoesNotRecoverAfterAnyPhysicalBytes(t *testing.T) {
	observer := NewObserver(WireLimits{})
	defer observer.Close()
	token, slot, _ := observer.begin("tools/call")
	defer observer.finish(token)
	slot.ctx = t.Context()
	denied := errors.New("revoked")
	slot.check = func(context.Context) error { return denied }
	var target wireBufferCloser
	writer := observer.Writer(&target)
	frame := append([]byte("\n"), append(wireRequest(1, token, "tools/call"), '\n')...)
	if n, err := writer.Write(frame); n != 1 || !errors.Is(err, denied) || target.String() != "\n" {
		t.Fatalf("mixed write denial facts incorrect: n=%d err=%v physical=%q", n, err, target.String())
	}
	if _, err := writer.Write([]byte("\n")); !errors.Is(err, denied) || target.String() != "\n" {
		t.Fatalf("writer recovered after partially delivered Write: err=%v physical=%q", err, target.String())
	}
}

func TestWireWriterClearsDeniedPartialFrame(t *testing.T) {
	observer := NewObserver(WireLimits{})
	defer observer.Close()
	token, slot, _ := observer.begin("tools/call")
	slot.ctx = t.Context()
	denied := errors.New("revoked")
	slot.check = func(context.Context) error { return denied }
	var target wireBufferCloser
	writer := observer.Writer(&target)
	frame := append(wireRequest(1, token, "tools/call"), '\n')
	if n, err := writer.Write(frame[:10]); n != 10 || err != nil || target.Len() != 0 {
		t.Fatalf("partial frame was delivered: n=%d err=%v physical=%q", n, err, target.String())
	}
	if n, err := writer.Write(frame[10:]); n != 0 || !errors.Is(err, denied) || target.Len() != 0 {
		t.Fatalf("denied frame was delivered: n=%d err=%v physical=%q", n, err, target.String())
	}
	observer.finish(token)
	token, _, _ = observer.begin("tools/call")
	defer observer.finish(token)
	allowed := append(wireRequest(2, token, "tools/call"), '\n')
	if n, err := writer.Write(allowed); n != len(allowed) || err != nil || !bytes.Equal(target.Bytes(), allowed) {
		t.Fatalf("denied bytes leaked into next frame: n=%d err=%v physical=%q", n, err, target.String())
	}
}

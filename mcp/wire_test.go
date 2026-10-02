package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	observer.finish(second) // Evicts numeric tombstone 1, retaining a safe floor.
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

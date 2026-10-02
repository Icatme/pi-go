package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

type managerTransportFunc func(*http.Request) (*http.Response, error)

func (f managerTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func closeManager(t *testing.T, m *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Errorf("manager close: %v", err)
	}
}

func newManagerFixture(t *testing.T, server *sdk.Server, modern bool) (*Manager, *Connection) {
	t.Helper()
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: modern, JSONResponse: true}))
	t.Cleanup(httpServer.Close)
	m, err := New(Config{Scope: Scope{Identity: "alice", AuthEpoch: 1}, Servers: []ServerConfig{{Name: "fixture", URL: httpServer.URL, Timeout: 3 * time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeManager(t, m) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := m.Connect(ctx, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return m, c
}

func TestManagerNewIsInertAndConfigDetached(t *testing.T) {
	var requests atomic.Int32
	headers := http.Header{"X-Fixture": []string{"original"}}
	rules := []ToolRule{{Pattern: "private*", Exposure: Hidden}}
	m, err := New(Config{Scope: Scope{Identity: "alice"}, HTTPClient: &http.Client{Transport: managerTransportFunc(func(*http.Request) (*http.Response, error) { requests.Add(1); return nil, errors.New("unexpected I/O") })}, Servers: []ServerConfig{
		{Name: "http", URL: "http://127.0.0.1:1", Headers: headers, ToolRules: rules},
		{Name: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestManagedProcessFixture$"}, Env: []string{"PI_GO_MCP_HELPER=server"}, Trusted: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, m)
	headers.Set("X-Fixture", "changed")
	rules[0].Exposure = Direct
	if requests.Load() != 0 {
		t.Fatal("New performed network I/O")
	}
	if _, err := m.Ready("stdio"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("New launched stdio: %v", err)
	}
	servers := m.Servers()
	if len(servers) != 2 || servers[0].Headers.Get("X-Fixture") != "original" || servers[0].ToolRules[0].Exposure != Hidden {
		t.Fatal("configuration retained caller maps/slices")
	}
	servers[0].Headers.Set("X-Fixture", "returned changed")
	servers[0].ToolRules[0].Exposure = Direct
	if got := m.Servers()[0]; got.Headers.Get("X-Fixture") != "original" || got.ToolRules[0].Exposure != Hidden {
		t.Fatal("Servers exposed mutable policy")
	}
	if _, err := m.Catalogs(context.Background(), false); err != nil || requests.Load() != 0 {
		t.Fatalf("indirect directory performed I/O: %v", err)
	}
}

func TestManagerModernAndLegacyExactWire(t *testing.T) {
	for _, modern := range []bool{true, false} {
		name := "legacy"
		if modern {
			name = "modern"
		}
		t.Run(name, func(t *testing.T) {
			server := wireTestServer()
			addWireTool(server, "echo", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{}, StructuredContent: map[string]any{"id": json.Number("9007199254740993"), "decimal": json.Number("0.100000000000000000001")}}, nil
			})
			m, c := newManagerFixture(t, server, modern)
			snap, err := c.Refresh(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := "2025-11-25"
			if modern {
				want = "2026-07-28"
			}
			if snap.Protocol != want {
				t.Fatalf("protocol=%s want=%s", snap.Protocol, want)
			}
			raw, ok := snap.Tools[0].InputSchema.(json.RawMessage)
			if !ok || !bytes.Contains(raw, []byte("9007199254740993")) {
				t.Fatalf("schema lexeme lost: %T %s", snap.Tools[0].InputSchema, raw)
			}
			result, err := c.CallTool(context.Background(), &sdk.CallToolParams{Name: "echo", Arguments: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			out, ok := result.StructuredContent.(map[string]any)
			if !ok {
				t.Fatalf("unexpected structuredContent %T", result.StructuredContent)
			}
			if out["id"] != json.Number("9007199254740993") || out["decimal"] != json.Number("0.100000000000000000001") {
				t.Fatalf("exact response changed: %#v", out)
			}
			if c.Scope() != m.Scope() {
				t.Fatal("connection identity differs from manager")
			}
			detached := snap.Tools[0].InputSchema.(json.RawMessage)
			detached[0] = '!'
			fresh, err := c.Refresh(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Tools[0].InputSchema.(json.RawMessage)[0] == '!' {
				t.Fatal("catalog copy exposed schema storage")
			}
		})
	}
}

func TestManagerConnectCoalescesAndFailureNeedsExplicitReconnect(t *testing.T) {
	var discover atomic.Int32
	server := wireTestServer()
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "server/discover" {
				discover.Add(1)
			}
			return next(ctx, method, req)
		}
	})
	m, c := newManagerFixture(t, server, true)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			got, err := m.Connect(context.Background(), "fixture")
			if err != nil || got != c {
				t.Errorf("Connect did not join same generation: %v", err)
			}
		})
	}
	wg.Wait()
	if discover.Load() != 1 {
		t.Fatalf("connection was duplicated: %d", discover.Load())
	}
	oldGen := c.Generation()
	next, err := m.Reconnect(context.Background(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if next == c || next.Generation() <= oldGen || discover.Load() != 2 {
		t.Fatal("Reconnect did not replace SDK generation")
	}
	if _, err := c.CallTool(context.Background(), &sdk.CallToolParams{Name: "echo"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("retired connection accepted request: %v", err)
	}
	var failures atomic.Int32
	broken, err := New(Config{Scope: Scope{Identity: "alice"}, HTTPClient: &http.Client{Transport: managerTransportFunc(func(*http.Request) (*http.Response, error) {
		failures.Add(1)
		return nil, errors.New("fixture connect failure")
	})}, Servers: []ServerConfig{{Name: "broken", URL: "http://127.0.0.1:1", Timeout: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, broken)
	_, err = broken.Connect(context.Background(), "broken")
	if err == nil {
		t.Fatal("failed connection unexpectedly succeeded")
	}
	attempts := failures.Load()
	_, err = broken.Connect(context.Background(), "broken")
	if err == nil || failures.Load() != attempts {
		t.Fatal("Connect silently retried a stored setup failure")
	}
	_, _ = broken.Reconnect(context.Background(), "broken")
	if failures.Load() <= attempts {
		t.Fatal("explicit Reconnect did not start a new attempt")
	}
}

func TestManagerScopeSwitchRetiresPendingAndPolicy(t *testing.T) {
	server := wireTestServer()
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	addWireTool(server, "write", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		if args.ID == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &sdk.CallToolResult{Content: []sdk.Content{}, StructuredContent: map[string]any{"id": args.ID}}, nil
	})
	m, old := newManagerFixture(t, server, true)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	oldResult := make(chan error, 1)
	go func() {
		_, err := old.CallTool(context.Background(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{"id": 1}})
		oldResult <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old identity call did not start")
	}
	if err := m.SetScope(Scope{Identity: "bob", AuthEpoch: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-oldResult:
		if err == nil {
			t.Fatal("retired call succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("identity change did not cancel pending call")
	}
	next, err := m.Connect(context.Background(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if next == old || next.Generation() == old.Generation() || next.Scope().Identity != "bob" {
		t.Fatal("identity reused old SDK session")
	}
	result, err := next.CallTool(context.Background(), &sdk.CallToolParams{Name: "write", Arguments: map[string]any{"id": 2}})
	if err != nil {
		t.Fatal(err)
	}
	if result.StructuredContent.(map[string]any)["id"] != json.Number("2") {
		t.Fatalf("new epoch response contaminated: %#v", result.StructuredContent)
	}
	releaseOnce.Do(func() { close(release) })
	if err := m.SetExposure("fixture", Hidden, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := next.CallTool(context.Background(), &sdk.CallToolParams{Name: "write"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("revoked connection executed again: %v", err)
	}
	catalogs, err := m.Catalogs(context.Background(), true)
	if err != nil || len(catalogs) != 0 {
		t.Fatalf("hidden server discovered: %v %#v", err, catalogs)
	}
}

func TestManagerPolicyExactRulesOverrideOrderedPatterns(t *testing.T) {
	s := ServerConfig{Exposure: Codemode, ToolRules: []ToolRule{{Pattern: "*", Exposure: Deferred}, {Pattern: "write*", Exposure: Hidden}, {Pattern: "write_safe", Exposure: Direct}}}
	if s.ToolExposure("write_safe") != Direct || s.ToolExposure("write_other") != Deferred {
		t.Fatal("rule precedence differs from explicit exact/first-pattern policy")
	}
	for _, config := range []Config{
		{Scope: Scope{Identity: "alice"}, Servers: []ServerConfig{{Name: "bad", Command: "untrusted"}}},
		{Scope: Scope{Identity: "alice"}, Servers: []ServerConfig{{Name: "bad", URL: "http://example.com/mcp"}}},
		{Scope: Scope{Identity: "alice"}, Servers: []ServerConfig{{Name: "bad", URL: "https://user:secret@example.com/mcp"}}},
	} {
		if m, err := New(config); err == nil {
			closeManager(t, m)
			t.Fatal("invalid trust/credential transport accepted")
		}
	}
}

func TestManagerOAuthConfigurationDetached(t *testing.T) {
	options := &OAuthOptions{
		MetadataURL: "https://issuer.example/.well-known/oauth-authorization-server",
		SDKConfig: auth.AuthorizationCodeHandlerConfig{
			Client:                          &http.Client{Timeout: time.Second},
			PreregisteredClient:             &oauthex.ClientCredentials{ClientID: "original-client"},
			DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{"http://127.0.0.1/original"}, GrantTypes: []string{"authorization_code"}}},
		},
	}
	m, err := New(Config{Scope: Scope{Identity: "alice"}, Servers: []ServerConfig{{Name: "oauth", URL: "https://mcp.example/mcp", OAuth: options}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, m)
	options.MetadataURL = "https://changed.example/metadata"
	options.SDKConfig.PreregisteredClient.ClientID = "changed-client"
	options.SDKConfig.DynamicClientRegistrationConfig.Metadata.RedirectURIs[0] = "http://127.0.0.1/changed"
	options.SDKConfig.Client.Timeout = 9 * time.Second
	got := m.Servers()[0].OAuth
	if got.MetadataURL != "https://issuer.example/.well-known/oauth-authorization-server" || got.SDKConfig.PreregisteredClient.ClientID != "original-client" || got.SDKConfig.DynamicClientRegistrationConfig.Metadata.RedirectURIs[0] != "http://127.0.0.1/original" || got.SDKConfig.Client.Timeout != time.Second {
		t.Fatal("OAuth retained caller-owned configuration")
	}
	got.SDKConfig.DynamicClientRegistrationConfig.Metadata.RedirectURIs[0] = "http://127.0.0.1/returned"
	got.SDKConfig.PreregisteredClient.ClientID = "returned-client"
	if latest := m.Servers()[0].OAuth; latest.SDKConfig.PreregisteredClient.ClientID != "original-client" || latest.SDKConfig.DynamicClientRegistrationConfig.Metadata.RedirectURIs[0] != "http://127.0.0.1/original" {
		t.Fatal("Servers returned mutable OAuth policy")
	}
}

func TestManagerCatalogTTLAndNotificationRevision(t *testing.T) {
	server := wireTestServer()
	addWireTool(server, "fixture", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
	})
	var lists atomic.Int32
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "tools/list" {
				n := lists.Add(1)
				return &sdk.ListToolsResult{Cacheable: sdk.Cacheable{TTLMs: 100, CacheScope: "private"}, Tools: []*sdk.Tool{{Name: "fixture", Description: strconv.Itoa(int(n)), InputSchema: map[string]any{"type": "object"}}}}, nil
			}
			return next(ctx, method, req)
		}
	})
	_, c := newManagerFixture(t, server, true)
	first, err := c.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cached, err := c.Refresh(context.Background())
	if err != nil || cached.Revision != first.Revision || lists.Load() != 1 {
		t.Fatalf("cached revision changed: %v lists=%d", err, lists.Load())
	}
	time.Sleep(120 * time.Millisecond)
	expired, err := c.Refresh(context.Background())
	if err != nil || expired.Revision <= first.Revision || lists.Load() != 2 || expired.SourceDigest == first.SourceDigest {
		t.Fatalf("catalog ignored SDK TTL: rev=%d old=%d lists=%d err=%v", expired.Revision, first.Revision, lists.Load(), err)
	}
	before := c.revision.Load()
	addWireTool(server, "another", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
	})
	deadline := time.Now().Add(3 * time.Second)
	for c.revision.Load() == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.revision.Load() == before {
		t.Fatal("directory notification did not advance owner revision")
	}
	notified, err := c.Refresh(context.Background())
	if err != nil || notified.Revision <= expired.Revision || lists.Load() != 3 {
		t.Fatalf("notification/raw cache stale: rev=%d lists=%d err=%v", notified.Revision, lists.Load(), err)
	}
}

func TestManagerRejectsSDKFilteredRawDirectory(t *testing.T) {
	server := wireTestServer()
	addWireTool(server, "valid", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
	})
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "tools/list" {
				return &sdk.ListToolsResult{Tools: []*sdk.Tool{
					{Name: "valid", InputSchema: map[string]any{"type": "object"}},
					{Name: "invalid", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"header": map[string]any{"type": "object", "x-mcp-header": "X-Fixture"}}}},
				}}, nil
			}
			return next(ctx, method, req)
		}
	})
	_, c := newManagerFixture(t, server, true)
	if _, err := c.Refresh(context.Background()); err == nil {
		t.Fatal("raw reconstruction resurrected SDK-rejected header tool")
	}
	c.mu.RLock()
	published := c.catalog
	c.mu.RUnlock()
	if published != nil {
		t.Fatal("invalid raw directory was published")
	}
}

func TestManagerDiscardsLateOldPagination(t *testing.T) {
	server := wireTestServer()
	handler := func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{}}, nil
	}
	addWireTool(server, "alpha", handler)
	addWireTool(server, "beta", handler)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var held atomic.Bool
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			result, err := next(ctx, method, req)
			if method == "tools/list" {
				params, _ := req.GetParams().(*sdk.ListToolsParams)
				if params != nil && params.Cursor != "" && held.CompareAndSwap(false, true) {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			}
			return result, err
		}
	})
	_, c := newManagerFixture(t, server, true)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	before := c.revision.Load()
	done := make(chan error, 1)
	go func() { _, err := c.Refresh(context.Background()); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("second old directory page did not enter")
	}
	addWireTool(server, "gamma", handler)
	deadline := time.Now().Add(3 * time.Second)
	for c.revision.Load() == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.revision.Load() == before {
		t.Fatal("notification did not invalidate loading directory")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if !errors.Is(err, ErrStale) && !errors.Is(err, ErrWireInvalidated) {
			t.Fatalf("old directory accepted: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old directory did not finish")
	}
	c.mu.RLock()
	old := c.catalog
	c.mu.RUnlock()
	if old != nil {
		t.Fatal("partial outdated directory was published")
	}
	fresh, err := c.Refresh(context.Background())
	if err != nil || len(fresh.Tools) != 3 {
		t.Fatalf("late cache page poisoned new revision: tools=%d err=%v", len(fresh.Tools), err)
	}
}

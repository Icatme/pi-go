package toolset

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSearchRechecksDirectoryAfterFinalPermission(t *testing.T) {
	for _, change := range []string{"hide_server", "hide_selected_tool", "directory_notification"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t, managed.Codemode, nil)
			ts, err := New(f.manager, f.sandbox, Options{MaxSelectionRecords: 1})
			if err != nil {
				t.Fatal(err)
			}
			tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			search := findTool(t, tools, "tool_search")
			scope := f.manager.Scope()
			var checks atomic.Int32
			out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "search", Name: search.Name, ParsedArgs: map[string]any{
				"query": "hidden", "namespace": "issues", "limit": 1,
			}}, agent.RunToolCallOptions{Tools: tools, CheckToolPermission: func(ctx context.Context, _ agent.BeforeToolCallContext) error {
				if checks.Add(1) != 3 {
					return nil
				}
				switch change {
				case "hide_server":
					return f.manager.SetExposure("issues", managed.Hidden, nil)
				case "hide_selected_tool":
					return f.manager.SetExposure("issues", managed.Codemode, []managed.ToolRule{{Pattern: "hidden_issue", Exposure: managed.Hidden}})
				default:
					connection, err := f.manager.Ready("issues")
					if err != nil {
						return err
					}
					catalog, err := connection.Refresh(ctx)
					if err != nil {
						return err
					}
					return changeFixtureDirectory(ctx, f, connection, catalog)
				}
			}})
			if checks.Load() != 3 || f.manager.Scope() != scope || !errors.Is(out.Err, managed.ErrStale) || out.Execution.Remote != agent.ToolRemoteNotDispatched || f.writes.Load() != 0 {
				t.Fatalf("publication accepted a changed catalog: checks=%d scope=%+v writes=%d outcome=%+v", checks.Load(), f.manager.Scope(), f.writes.Load(), out)
			}
			text := resultText(out.Result)
			if strings.Contains(text, "hidden_issue") || strings.Contains(text, "SECRET_") || out.Result.Details != nil {
				t.Fatalf("rejected search published metadata/selection: %s details=%+v", text, out.Result.Details)
			}
			ts.mu.Lock()
			records := len(ts.loaded)
			ts.mu.Unlock()
			if records != 0 {
				t.Fatal("rejected publication consumed selection record budget")
			}
			if err := f.manager.SetExposure("issues", managed.Codemode, nil); err != nil {
				t.Fatal(err)
			}
			// A fresh search can still use the bounded record budget. Restoring
			// this successful record remains limited to its transcript branch.
			selected := loadBySearch(t, ts, "hidden")
			branch, err := ts.Resolve(t.Context(), agent.AgentSnapshot{Messages: []agent.Message{selected}})
			if err != nil {
				t.Fatal(err)
			}
			findTool(t, branch, "mcp__issues__hidden_issue")
			other, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
			if err != nil || strings.Contains(toolNames(other), "hidden_issue") {
				t.Fatalf("selection crossed transcript branches: tools=%s err=%v", toolNames(other), err)
			}
		})
	}
}

func changeFixtureDirectory(ctx context.Context, f *fixture, connection *managed.Connection, frozen managed.Catalog) error {
	f.server.AddTool(&sdk.Tool{Name: "direct_issue", InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "required": []string{"id"},
	}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		f.writes.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "changed handler executed"}}}, nil
	})
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for connection.IsCurrent(frozen) {
		select {
		case <-ticker.C:
		case <-timer.C:
			return errors.New("fixture notification did not arrive")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func assertCatalogRejected(t *testing.T, err error, info agent.ToolExecutionInfo, writes int32) {
	t.Helper()
	var failure *agent.ToolExecutionError
	if !errors.As(err, &failure) || !errors.Is(err, managed.ErrStale) || failure.Code != agent.ToolFailureSchemaChanged || failure.Reason != "catalog_changed" {
		t.Fatalf("directory change lost its classified cause: %+v err=%v", failure, err)
	}
	if writes != 0 || info.Remote != agent.ToolRemoteNotDispatched || len(info.Attempts) != 0 {
		t.Fatalf("known directory change still dispatched: writes=%d execution=%+v err=%v", writes, info, err)
	}
}

func TestKnownDirectoryChangeDuringApprovalBlocksDispatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check int32
	}{{"after_schema_read", 2}, {"physical_handoff", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, managed.Direct, mixedRules())
			ts, err := New(f.manager, f.sandbox, Options{})
			if err != nil {
				t.Fatal(err)
			}
			tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
			if err != nil {
				t.Fatal(err)
			}
			selected := findTool(t, tools, "mcp__issues__direct_issue")
			connection, err := f.manager.Ready("issues")
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := connection.Refresh(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var checks atomic.Int32
			changedObserved := false
			permission := func(ctx context.Context, _ agent.BeforeToolCallContext) error {
				if checks.Add(1) == tc.check {
					if err := changeFixtureDirectory(ctx, f, connection, frozen); err != nil {
						return err
					}
					changedObserved = true
				}
				return nil
			}
			out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "changed", Name: selected.Name, ParsedArgs: map[string]any{"id": "abc"}}, agent.RunToolCallOptions{Tools: tools, CheckToolPermission: permission})
			if !changedObserved || checks.Load() != tc.check {
				t.Fatalf("fixture did not reach known invalidation: checks=%d err=%v", checks.Load(), out.Err)
			}
			assertCatalogRejected(t, out.Err, out.Execution, f.writes.Load())
		})
	}
}

func TestFrozenCatalogGuardAtHandoffWithoutPermissionCallback(t *testing.T) {
	f := newFixture(t, managed.Direct, mixedRules())
	ts, err := New(f.manager, f.sandbox, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	selected := findTool(t, tools, "mcp__issues__direct_issue")
	connection, err := f.manager.Ready("issues")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := connection.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	ctx := managed.WithDispatchCheck(t.Context(), func(ctx context.Context) error {
		if checks.Add(1) == 2 {
			return changeFixtureDirectory(ctx, f, connection, frozen)
		}
		return nil
	})
	// This legacy-protocol fixture sends tools/list before tools/call. Change
	// the catalog at the second handoff, after the fresh schema read.
	result, err := selected.Execute(ctx, agent.ToolExecutionContext{ToolCall: agent.ToolCall{ID: "direct", Name: selected.Name}, Args: map[string]any{"id": "abc"}})
	if checks.Load() != 2 || result.Execution == nil {
		t.Fatalf("fixture did not reach a guarded handoff: checks=%d result=%+v err=%v", checks.Load(), result, err)
	}
	assertCatalogRejected(t, err, *result.Execution, f.writes.Load())
	before := f.requests.Load()
	_, err = selected.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"id": "abc"}})
	var failure *agent.ToolExecutionError
	if !errors.As(err, &failure) {
		t.Fatalf("direct invocation accepted an already stale catalog: %v", err)
	}
	assertCatalogRejected(t, err, failure.Execution, f.writes.Load())
	if f.requests.Load() != before {
		t.Fatal("already stale direct invocation performed MCP I/O")
	}
}

func TestPolicyChangeDuringOtherNamespaceConnectHidesMetadata(t *testing.T) {
	servers := make(map[string]*sdk.Server)
	for _, name := range []string{"a", "b"} {
		server := sdk.NewServer(&sdk.Implementation{Name: name, Version: "1"}, nil)
		for _, tool := range []string{"public", "secret"} {
			server.AddTool(&sdk.Tool{Name: tool, Description: "description-" + name + "-" + tool, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "called"}}}, nil
			})
		}
		servers[name] = server
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var blocked atomic.Bool
	handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return servers[r.Header.Get("X-Namespace")] }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Namespace") == "a" && r.Method == http.MethodPost && blocked.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	manager, err := managed.New(managed.Config{Scope: managed.Scope{Identity: "account", AuthEpoch: 1}, Servers: []managed.ServerConfig{
		{Name: "a", URL: httpServer.URL, Exposure: managed.Direct, Headers: http.Header{"X-Namespace": []string{"a"}}},
		{Name: "b", URL: httpServer.URL, Exposure: managed.Direct, Headers: http.Header{"X-Namespace": []string{"b"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := sandbox.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	ts, err := New(manager, sandbox, Options{})
	if err != nil {
		t.Fatal(err)
	}
	type resolved struct {
		tools []agent.ToolDefinition
		err   error
	}
	done := make(chan resolved, 1)
	go func() {
		tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
		done <- resolved{tools, err}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("namespace a did not block")
	}
	if err := manager.SetExposure("b", managed.Hidden, []managed.ToolRule{{Pattern: "public", Exposure: managed.Direct}}); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	var got resolved
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("resolve did not finish")
	}
	if !errors.Is(got.err, managed.ErrStale) || len(got.tools) != 0 {
		t.Fatalf("changed configuration published an older directory: tools=%s err=%v", toolNames(got.tools), got.err)
	}
	// Explicitly resolve the new policy after rejection. The first resolution
	// must not silently mix old resource declarations with new MCP tool policy.
	got.tools, got.err = ts.Resolve(t.Context(), agent.AgentSnapshot{})
	if got.err != nil {
		t.Fatal(got.err)
	}
	findTool(t, got.tools, "mcp__b__public")
	for _, tool := range got.tools {
		if tool.Name == "mcp__b__secret" || tool.Description == "description-b-secret" {
			t.Fatalf("new hidden policy leaked metadata: name=%s description=%s revision=%s", tool.Name, tool.Description, tool.Revision)
		}
	}
}

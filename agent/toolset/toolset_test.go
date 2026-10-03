package toolset

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fixture struct {
	manager  *managed.Manager
	sandbox  *codemode.Sandbox
	server   *sdk.Server
	requests atomic.Int32
	writes   atomic.Int32
}

func newFixture(t *testing.T, exposure managed.Exposure, rules []managed.ToolRule) *fixture {
	t.Helper()
	f := &fixture{server: sdk.NewServer(&sdk.Implementation{Name: "toolset-fixture", Version: "1"}, &sdk.ServerOptions{Instructions: "Only use the allowed issue tools."})}
	for _, name := range []string{"direct_issue", "deferred_issue", "code_issue", "hidden_issue"} {
		description := "Read issue details"
		if name == "hidden_issue" {
			description = "SECRET_HIDDEN_DESCRIPTION_AND_PAYLOAD"
		}
		f.server.AddTool(&sdk.Tool{Name: name, Description: description, InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}, "description": "schema-" + name,
		}}, func(_ context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			f.writes.Add(1)
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: req.Params.Name}}}, nil
		})
	}
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return f.server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	var err error
	f.manager, err = managed.New(managed.Config{Scope: managed.Scope{Identity: "account-a", AuthEpoch: 1}, Servers: []managed.ServerConfig{{Name: "issues", URL: httpServer.URL, Exposure: exposure, ToolRules: rules}}})
	if err != nil {
		t.Fatal(err)
	}
	f.sandbox, err = codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.sandbox.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := f.manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}

func mixedRules() []managed.ToolRule {
	return []managed.ToolRule{
		{Pattern: "direct_issue", Exposure: managed.Direct},
		{Pattern: "deferred_issue", Exposure: managed.Deferred},
		{Pattern: "hidden_issue", Exposure: managed.Hidden},
	}
}

func findTool(t *testing.T, tools []agent.ToolDefinition, name string) agent.ToolDefinition {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q missing in %v", name, toolNames(tools))
	return agent.ToolDefinition{}
}

func toolNames(tools []agent.ToolDefinition) string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return strings.Join(names, ",")
}

func resultText(result agent.ToolResult) string {
	var parts []string
	for _, part := range result.Content {
		if part.Type == agent.PartTypeText {
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestProjectionExposesDirectOnlyAndNeverPreloadsIndirectSchemas(t *testing.T) {
	f := newFixture(t, managed.Codemode, mixedRules())
	ts, err := New(f.manager, f.sandbox, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f.requests.Load() != 0 {
		t.Fatal("toolset constructor performed MCP I/O")
	}
	tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toolNames(tools), "mcp__issues__direct_issue") || f.requests.Load() == 0 {
		t.Fatalf("direct declarations did not wait for their directory: %s", toolNames(tools))
	}
	serialized, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"deferred_issue", "code_issue", "hidden_issue", "SECRET_"} {
		if strings.Contains(string(serialized), hidden) {
			t.Fatalf("indirect declaration/hidden metadata preloaded: %s", serialized)
		}
	}
}

func TestIndirectServersDoNotDelayFirstResolveAndBothIndirectModesWorkInCode(t *testing.T) {
	rules := []managed.ToolRule{{Pattern: "hidden_issue", Exposure: managed.Hidden}, {Pattern: "deferred_issue", Exposure: managed.Deferred}}
	f := newFixture(t, managed.Codemode, rules)
	ts, err := New(f.manager, f.sandbox, Options{})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
	if err != nil || f.requests.Load() != 0 {
		t.Fatalf("indirect catalog delayed initial declarations: requests=%d err=%v", f.requests.Load(), err)
	}
	code := findTool(t, tools, "code")
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "script", Name: code.Name, ParsedArgs: map[string]any{"code": `text(await searchTools("issue",{namespace:"issues"})); text((await describeNamespace("issues")).instructions); text("mcp__issues__hidden_issue" in tools); const a=await tools.mcp__issues__deferred_issue({}); const b=await tools.mcp__issues__code_issue({}); return [a.isError,b.isError];`}}, agent.RunToolCallOptions{Tools: tools})
	text := resultText(out.Result)
	if out.Err != nil || f.writes.Load() != 2 || !strings.Contains(text, "Only use the allowed issue tools.") || !strings.Contains(text, "[false,false]") || strings.Contains(text, "SECRET_") || strings.Contains(text, "hidden_issue") {
		t.Fatalf("indirect code catalog failed or exposed hidden metadata: writes=%d text=%s err=%v", f.writes.Load(), text, out.Err)
	}
	if out.Result.ChildCalls == nil || len(out.Result.ChildCalls.Calls) != 2 {
		t.Fatalf("code calls did not use Agent child lifecycle: %+v", out.Result.ChildCalls)
	}
}

func loadBySearch(t *testing.T, ts *Toolset, query string) agent.Message {
	t.Helper()
	tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	search := findTool(t, tools, "tool_search")
	call := agent.ToolCall{ID: "search", Name: search.Name, ParsedArgs: map[string]any{"query": query, "namespace": "issues", "limit": 1}}
	out := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: tools})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	return agent.NewToolResultMessage(call, out.Result, out.IsError)
}

func TestSearchLoadsCurrentCatalogAndIsolatesBranchesWithSameSessionID(t *testing.T) {
	f := newFixture(t, managed.Codemode, mixedRules())
	ts, err := New(f.manager, f.sandbox, Options{})
	if err != nil {
		t.Fatal(err)
	}
	searchResult := loadBySearch(t, ts, "deferred")
	branch := agent.AgentSnapshot{SessionID: "same", Messages: []agent.Message{searchResult}}
	tools, err := ts.Resolve(t.Context(), branch)
	if err != nil {
		t.Fatal(err)
	}
	loaded := findTool(t, tools, "mcp__issues__deferred_issue")
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "read", Name: loaded.Name, ParsedArgs: map[string]any{}}, agent.RunToolCallOptions{Tools: tools})
	if out.Err != nil || f.writes.Load() != 1 {
		t.Fatalf("loaded executable was not reconstructed from the live connection: %+v", out)
	}
	other, err := ts.Resolve(t.Context(), agent.AgentSnapshot{SessionID: "same"})
	if err != nil || strings.Contains(toolNames(other), "deferred_issue") {
		t.Fatalf("selection leaked to another branch sharing SessionID: %s err=%v", toolNames(other), err)
	}
	searchResult.ToolResult.Details = selectionReport{SelectionID: "model-chosen-id", Names: []string{"mcp__issues__code_issue", "mcp__issues__hidden_issue"}}
	forged, err := ts.Resolve(t.Context(), agent.AgentSnapshot{SessionID: "same", Messages: []agent.Message{searchResult}})
	if err != nil || strings.Contains(toolNames(forged), "code_issue") || strings.Contains(toolNames(forged), "hidden_issue") {
		t.Fatalf("serialized names invented a host selection: %s err=%v", toolNames(forged), err)
	}
}

func TestEmptySearchDoesNotConsumeSelectionRecordBudget(t *testing.T) {
	f := newFixture(t, managed.Codemode, mixedRules())
	ts, err := New(f.manager, f.sandbox, Options{MaxSelectionRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	miss := loadBySearch(t, ts, "absent_topic_zyxw")
	if miss.ToolResult.Details != nil || len(miss.ToolResult.Content) == 0 || !strings.Contains(miss.ToolResult.Content[0].Text, "No allowed tools matched") {
		t.Fatalf("empty search invented a selection: %+v", miss.ToolResult)
	}
	selection := loadBySearch(t, ts, "deferred")
	tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{Messages: []agent.Message{selection}})
	if err != nil {
		t.Fatal(err)
	}
	findTool(t, tools, "mcp__issues__deferred_issue")
	search := findTool(t, tools, "tool_search")
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "another-search", Name: search.Name, ParsedArgs: map[string]any{"query": "code", "namespace": "issues", "limit": 1}}, agent.RunToolCallOptions{Tools: tools})
	if out.Err == nil || !strings.Contains(resultText(out.Result), "selection record limit exceeded") {
		t.Fatalf("selection record cap widened: %+v", out)
	}
}

func TestToolSearchLoadsOnlyInactiveIndirectTools(t *testing.T) {
	f := newFixture(t, managed.Codemode, mixedRules())
	ts, err := New(f.manager, f.sandbox, Options{MaxSearchResults: 50, MaxSelectionRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	call := agent.ToolCall{ID: "search-all", Name: "tool_search", ParsedArgs: map[string]any{"query": "issue", "namespace": "issues"}}
	out := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: initial, Context: agent.AgentContext{Tools: initial}})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	text := resultText(out.Result)
	if !strings.Contains(text, "deferred_issue") || !strings.Contains(text, "code_issue") || strings.Contains(text, "direct_issue") || strings.Contains(text, "hidden_issue") {
		t.Fatalf("search loaded active or hidden tools: %s", text)
	}
	message := agent.NewToolResultMessage(call, out.Result, out.IsError)
	loaded, err := ts.Resolve(t.Context(), agent.AgentSnapshot{Messages: []agent.Message{message}})
	if err != nil {
		t.Fatal(err)
	}
	call.ID = "search-again"
	again := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: loaded, Context: agent.AgentContext{Tools: loaded}})
	if again.Err != nil || again.Result.Details != nil || !strings.Contains(resultText(again.Result), "No allowed tools matched") {
		t.Fatalf("repeated search duplicated an active selection or consumed its record cap: %+v", again)
	}
}

type deadlineTransport struct{ started chan struct{} }

func (d deadlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	select {
	case d.started <- struct{}{}:
	default:
	}
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestResolveDirectCatalogHonorsShorterCallerDeadline(t *testing.T) {
	started := make(chan struct{}, 1)
	manager, err := managed.New(managed.Config{
		Scope:      managed.Scope{Identity: "deadline-account", AuthEpoch: 1},
		HTTPClient: &http.Client{Transport: deadlineTransport{started: started}},
		Servers:    []managed.ServerConfig{{Name: "slow", URL: "http://127.0.0.1:1", Exposure: managed.Direct, Timeout: 10 * time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := sandbox.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	ts, err := New(manager, sandbox, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	declarations, err := ts.Resolve(ctx, agent.AgentSnapshot{})
	select {
	case <-started:
	default:
		t.Fatal("direct resolver did not attempt setup")
	}
	if !errors.Is(err, context.DeadlineExceeded) || declarations != nil {
		t.Fatalf("unavailable direct setup silently returned declarations or lost deadline: declarations=%v err=%v", declarations, err)
	}
}

func TestResumeReconnectsBeforeRestoringSelectedDeclarations(t *testing.T) {
	f := newFixture(t, managed.Codemode, []managed.ToolRule{{Pattern: "hidden_issue", Exposure: managed.Hidden}})
	scope := f.manager.Scope()
	snapshot := agent.AgentSnapshot{SessionID: "saved", Messages: []agent.Message{agent.NewSystemMessage(agent.SystemMessagePayload{ToolsAdded: []agent.ToolDeclaration{{Name: "mcp__issues__deferred_issue", Description: "OLD_STALE_DESCRIPTION"}}})}}
	ts, err := New(f.manager, f.sandbox, Options{SnapshotScope: func(agent.AgentSnapshot) (managed.Scope, bool) { return scope, true }})
	if err != nil {
		t.Fatal(err)
	}
	if f.requests.Load() != 0 {
		t.Fatal("resume constructor connected before resolver")
	}
	tools, err := ts.Resolve(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	loaded := findTool(t, tools, "mcp__issues__deferred_issue")
	if f.requests.Load() == 0 || strings.Contains(loaded.Description, "OLD_STALE") {
		t.Fatalf("restore used serialized definitions or failed to reconnect: %+v", loaded)
	}
	if err := f.manager.SetScope(managed.Scope{Identity: "account-b", AuthEpoch: 1}); err != nil {
		t.Fatal(err)
	}
	other, err := ts.Resolve(t.Context(), snapshot)
	if err != nil || strings.Contains(toolNames(other), "deferred_issue") {
		t.Fatalf("old identity restored a tool selection: %s err=%v", toolNames(other), err)
	}
}

func TestAuthEpochChangeAndHiddenPolicyInvalidateSelectionsAndOldExecutors(t *testing.T) {
	f := newFixture(t, managed.Codemode, mixedRules())
	ts, err := New(f.manager, f.sandbox, Options{})
	if err != nil {
		t.Fatal(err)
	}
	message := loadBySearch(t, ts, "deferred")
	snapshot := agent.AgentSnapshot{Messages: []agent.Message{message}}
	tools, err := ts.Resolve(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	stale := findTool(t, tools, "mcp__issues__deferred_issue")
	if err := f.manager.SetScope(managed.Scope{Identity: "account-a", AuthEpoch: 2}); err != nil {
		t.Fatal(err)
	}
	next, err := ts.Resolve(t.Context(), snapshot)
	if err != nil || strings.Contains(toolNames(next), "deferred_issue") {
		t.Fatalf("old auth epoch retained selection: %s err=%v", toolNames(next), err)
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "stale", Name: stale.Name, ParsedArgs: map[string]any{}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{stale}})
	if out.Err == nil || f.writes.Load() != 0 {
		t.Fatalf("old executor dispatched after epoch change: writes=%d outcome=%+v", f.writes.Load(), out)
	}
	if err := f.manager.SetExposure("issues", managed.Hidden, nil); err != nil {
		t.Fatal(err)
	}
	hidden, err := ts.Resolve(t.Context(), snapshot)
	if err != nil || strings.Contains(toolNames(hidden), "mcp__issues__") {
		t.Fatalf("hidden policy retained declarations: %s err=%v", toolNames(hidden), err)
	}
}

func TestNativeAndResourceLeavesAreAvailableThroughDynamicCode(t *testing.T) {
	f := newFixture(t, managed.Codemode, mixedRules())
	native := agent.ToolDefinition{Name: "local_read", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "local result"}}}, nil
	}}
	ts, err := New(f.manager, f.sandbox, Options{Native: []codemodetool.Binding{codemodetool.Native(native, "local")}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := ts.Resolve(t.Context(), agent.AgentSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"local_read", "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"} {
		findTool(t, tools, name)
	}
	code := findTool(t, tools, "code")
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "script", Name: code.Name, ParsedArgs: map[string]any{"code": `text(await tools.local_read({})); return "read_mcp_resource" in tools;`}}, agent.RunToolCallOptions{Tools: tools})
	if out.Err != nil || !strings.Contains(resultText(out.Result), "local result") || !strings.Contains(resultText(out.Result), "true") {
		t.Fatalf("native/resource catalog missing from dynamic script: %+v", out)
	}
}

func TestResumeExceptionalAliasesConnectOnlyTheirOriginalNamespace(t *testing.T) {
	names := []string{
		strings.Repeat("long_name_", 6) + "first",
		strings.Repeat("long_name_", 6) + "second",
		"notes-workspace",
		"notes_workspace",
	}
	servers := make(map[string]*sdk.Server)
	counts := make(map[string]*atomic.Int32)
	for _, name := range names {
		counts[name] = new(atomic.Int32)
		server := sdk.NewServer(&sdk.Implementation{Name: name, Version: "fresh"}, nil)
		server.AddTool(&sdk.Tool{Name: "fresh_issue", Description: "Current description for " + name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: name}}}, nil
		})
		servers[name] = server
	}
	handler := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server {
		name := r.Header.Get("X-Fixture-Namespace")
		counts[name].Add(1)
		return servers[name]
	}, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	for _, selectedName := range names {
		t.Run(selectedName, func(t *testing.T) {
			for _, counter := range counts {
				counter.Store(0)
			}
			configs := make([]managed.ServerConfig, len(names))
			for i, name := range names {
				configs[i] = managed.ServerConfig{Name: name, URL: httpServer.URL, Exposure: managed.Codemode, Headers: http.Header{"X-Fixture-Namespace": []string{name}}}
			}
			scope := managed.Scope{Identity: "account", AuthEpoch: 1}
			manager, err := managed.New(managed.Config{Scope: scope, Servers: configs})
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
				if err := sandbox.Close(ctx); err != nil {
					t.Error(err)
				}
				if err := manager.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			ts, err := New(manager, sandbox, Options{SnapshotScope: func(agent.AgentSnapshot) (managed.Scope, bool) { return scope, true }})
			if err != nil {
				t.Fatal(err)
			}
			alias := codemodetool.MCP(agent.ToolDefinition{Name: "fresh_issue"}, selectedName).ExportedName()
			snapshot := agent.AgentSnapshot{Messages: []agent.Message{agent.NewSystemMessage(agent.SystemMessagePayload{ToolsAdded: []agent.ToolDeclaration{{Name: alias, Description: "stale declaration"}}})}}
			tools, err := ts.Resolve(t.Context(), snapshot)
			if err != nil {
				t.Fatal(err)
			}
			loaded := findTool(t, tools, alias)
			out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "restored", Name: loaded.Name, ParsedArgs: map[string]any{}}, agent.RunToolCallOptions{Tools: tools})
			if out.Err != nil || resultText(out.Result) != selectedName || loaded.Description == "stale declaration" {
				t.Fatalf("exceptional alias invoked wrong or serialized executor: alias=%s outcome=%+v", alias, out)
			}
			for _, name := range names {
				if name == selectedName && counts[name].Load() == 0 || name != selectedName && counts[name].Load() != 0 {
					t.Fatalf("alias matched wrong namespace prefix: selected=%s name=%s requests=%d", selectedName, name, counts[name].Load())
				}
			}
		})
	}
}

func TestTrustedRestorationCannotOverrideCurrentHiddenPolicy(t *testing.T) {
	f := newFixture(t, managed.Codemode, mixedRules())
	scope := f.manager.Scope()
	ts, err := New(f.manager, f.sandbox, Options{SnapshotScope: func(agent.AgentSnapshot) (managed.Scope, bool) { return scope, true }})
	if err != nil {
		t.Fatal(err)
	}
	saved := agent.AgentSnapshot{Messages: []agent.Message{agent.NewSystemMessage(agent.SystemMessagePayload{ToolsAdded: []agent.ToolDeclaration{{Name: "mcp__issues__deferred_issue", Description: "old schema"}}})}}
	tools, err := ts.Resolve(t.Context(), saved)
	if err != nil {
		t.Fatal(err)
	}
	stale := findTool(t, tools, "mcp__issues__deferred_issue")
	if err := f.manager.SetExposure("issues", managed.Hidden, nil); err != nil {
		t.Fatal(err)
	}
	tools, err = ts.Resolve(t.Context(), saved)
	if err != nil || strings.Contains(toolNames(tools), "mcp__issues__") {
		t.Fatalf("trusted history bypassed current policy: %s %v", toolNames(tools), err)
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "stale", Name: stale.Name, ParsedArgs: map[string]any{}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{stale}})
	if out.Err == nil || f.writes.Load() != 0 {
		t.Fatalf("restored executor dispatched after hiding: %+v", out)
	}
}

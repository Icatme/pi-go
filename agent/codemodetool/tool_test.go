package codemodetool

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/mcptools"
	"github.com/Icatme/pi-go/codemode"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newSandbox(t *testing.T) *codemode.Sandbox {
	t.Helper()
	s, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Errorf("sandbox Close: %v", err)
		}
	})
	return s
}

func localSession(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	local, remote := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), remote, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "codemode-test", Version: "1"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	cs, err := client.Connect(t.Context(), local, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func discover(t *testing.T, server *mcp.Server, names ...string) []agent.ToolDefinition {
	t.Helper()
	tools, err := mcptools.Discover(t.Context(), localSession(t, server), mcptools.Options{Names: names})
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

type finalStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func (s *finalStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *finalStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *finalStream) Close() error                        { return nil }

func immediate(message agent.Message) agent.AssistantStream {
	events := make(chan agent.AssistantEvent, 1)
	events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
	close(events)
	return &finalStream{message: message, events: events}
}

func runnerCall(t *testing.T, tool agent.ToolDefinition, code string, definition agent.AgentDefinition, gate agent.ToolGateHook) (agent.ToolResultPayload, []agent.AgentEvent) {
	t.Helper()
	requests := 0
	definition.Tools = []agent.ToolDefinition{tool}
	definition.Model = agent.StreamFunc(func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
		requests++
		if len(request.Tools) != 1 || request.Tools[0].Name != tool.Name {
			t.Errorf("model declarations=%+v", request.Tools)
		}
		serialized, _ := json.Marshal(request.Tools)
		if strings.Contains(string(serialized), "structuredContent") || strings.Contains(string(serialized), "fixture-private-schema") {
			t.Errorf("leaf schema leaked: %s", serialized)
		}
		message := agent.NewTextMessage(agent.RoleAssistant, "done")
		message.StopReason = agent.StopReasonStop
		if requests == 1 {
			arguments, _ := json.Marshal(map[string]any{"code": code})
			message.Parts = nil
			message.StopReason = agent.StopReasonToolUse
			message.ToolCalls = []agent.ToolCall{{ID: "outer", Name: tool.Name, Arguments: arguments}}
		}
		return immediate(message), nil
	})
	runner, err := agent.NewRunner(definition)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	stream := runner.RunWithHooks(ctx, agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("summarize the selected records")}, agent.LoopHooks{ToolGate: gate})
	var events []agent.AgentEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	snapshot, err := stream.Wait()
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	if requests != 2 {
		t.Fatalf("model requests=%d", requests)
	}
	var result *agent.ToolResultPayload
	for _, message := range snapshot.Messages {
		if message.Role == agent.RoleTool {
			if message.ToolResult == nil || message.ToolResult.ToolCallID != "outer" {
				t.Fatalf("unexpected leaf transcript message: %+v", message)
			}
			if result != nil {
				t.Fatal("more than one model-visible tool result")
			}
			result = message.ToolResult
		}
	}
	if result == nil {
		t.Fatal("missing code tool result")
	}
	return *result, events
}

func contentText(parts []agent.Part) string {
	var b strings.Builder
	for _, part := range parts {
		if part.Type == agent.PartTypeText {
			b.WriteString(part.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func TestRunnerMCPBatchFiltersAndHooksBeforeJavaScript(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "records", Description: "fixture-private-schema", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		rows := make([]map[string]any, 200)
		for i := range rows {
			rows[i] = map[string]any{"id": i, "open": i%2 == 0, "payload": strings.Repeat("x", 1024)}
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "SECRET_SHOULD_BE_REMOVED"}}, StructuredContent: map[string]any{"rows": rows, "secret": "SECRET_SHOULD_BE_REMOVED"}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "hidden", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.Error("hidden tool executed")
		return nil, nil
	})
	tools := discover(t, server, "records")
	codeTool, err := New(newSandbox(t), []Binding{MCP(tools[0], "fixture")}, Options{MaxOutputTokens: 256})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(codeTool.Description, "records") || strings.Contains(codeTool.Description, "fixture-private-schema") {
		t.Fatalf("description injects leaf declaration: %s", codeTool.Description)
	}
	definition := agent.AgentDefinition{AfterToolCall: func(_ context.Context, after agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		if after.ToolCall.Name != "mcp__fixture__records" {
			return agent.AfterToolCallResult{}, nil
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(after.Result.StructuredContent, &value); err != nil {
			return agent.AfterToolCallResult{}, err
		}
		delete(value, "secret")
		clean, _ := json.Marshal(value)
		result := after.Result
		result.Content = []agent.Part{{Type: agent.PartTypeText, Text: "filtered"}}
		result.StructuredContent = clean
		return agent.AfterToolCallResult{Result: &result}, nil
	}}
	code := `const batches = await Promise.all(Array.from({length:6}, () => tools.mcp__fixture__records({})));
if (await describeTool("mcp__fixture__hidden") !== undefined || "mcp__fixture__hidden" in tools) throw Error("hidden tool visible");
if (batches.some(r => r.structuredContent.secret !== undefined || r.content[0].text !== "filtered")) throw Error("hook bypassed");
const rows = batches.flatMap(r => r.structuredContent.rows).filter(r => r.open);
text({total: rows.length, ids: rows.slice(0,3).map(r => r.id)});`
	result, events := runnerCall(t, codeTool, code, definition, nil)
	text := contentText(result.Content)
	if result.IsError || calls.Load() != 6 || !strings.Contains(text, `"total":600`) || !strings.Contains(text, `"ids":[0,2,4]`) || len(text) > 1024 {
		t.Fatalf("calls=%d result=%+v", calls.Load(), result)
	}
	for _, event := range events {
		data, _ := json.Marshal(event)
		if strings.Contains(string(data), "SECRET_SHOULD_BE_REMOVED") || len(data) > 64<<10 {
			t.Fatalf("unbounded/raw event: %s", data[:min(len(data), 1024)])
		}
	}
	report, ok := result.Details.(Report)
	if !ok {
		t.Fatalf("report type=%T", result.Details)
	}
	if len(report.Children.Calls) != 6 || len(report.Sandbox.Calls) != 6 {
		t.Fatalf("report=%+v", report)
	}
	for _, call := range report.Children.Calls {
		if call.ParentToolCallID != "outer" || !strings.HasPrefix(call.ToolCallID, "outer/") || call.Execution.Remote != agent.ToolRemoteCompleteReported {
			t.Fatalf("call facts=%+v", call)
		}
	}
}

func TestBusinessErrorsResolveOnlyMCPEnvelope(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "business", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "business rejected"}}, StructuredContent: map[string]any{"reason": "quota"}}, nil
	})
	tools := discover(t, server, "business")
	native := agent.ToolDefinition{Name: "native", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{IsError: true, Content: []agent.Part{{Type: agent.PartTypeText, Text: "native rejected"}}}, nil
	}}
	tool, err := New(newSandbox(t), []Binding{MCP(tools[0], "fixture"), Native(native, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := runnerCall(t, tool, `const value = await tools.mcp__fixture__business({}); text({isError:value.isError, reason:value.structuredContent.reason});
try { await tools.native({}); throw Error("native unexpectedly resolved"); } catch (e) { text({code:e.code, callId:e.callId}); }`, agent.AgentDefinition{}, nil)
	text := contentText(result.Content)
	if result.IsError || !strings.Contains(text, `"isError":true`) || !strings.Contains(text, "tool_reported_error") || !strings.Contains(text, "outer/2") {
		t.Fatalf("result=%+v", result)
	}
}

func TestNestedGateSuspendHasNoReplayAndRetainsFirstCall(t *testing.T) {
	var first, second atomic.Int32
	leaves := []Binding{
		Native(agent.ToolDefinition{Name: "first", ExecutionMode: agent.ToolExecutionSequential, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			first.Add(1)
			return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "committed"}}}, nil
		}}, "local"),
		Native(agent.ToolDefinition{Name: "second", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			second.Add(1)
			return agent.ToolResult{}, nil
		}}, "local"),
	}
	tool, err := New(newSandbox(t), leaves, Options{})
	if err != nil {
		t.Fatal(err)
	}
	gate := func(_ context.Context, before agent.BeforeToolCallContext) (agent.ToolGateResult, error) {
		if before.ToolCall.Name == "second" {
			return agent.ToolGateResult{Action: agent.ToolGateActionSuspend, Reason: "approval required"}, nil
		}
		return agent.ToolGateResult{Action: agent.ToolGateActionAllow}, nil
	}
	result, _ := runnerCall(t, tool, `text(await tools.first({})); try { await tools.second({}); } catch (e) { text({code:e.code, execution:e.execution}); }`, agent.AgentDefinition{}, gate)
	text := contentText(result.Content)
	if result.IsError || first.Load() != 1 || second.Load() != 0 || !strings.Contains(text, "committed") || !strings.Contains(text, "nested_suspend_unsupported") || !strings.Contains(text, "not_started") {
		t.Fatalf("first=%d second=%d result=%+v", first.Load(), second.Load(), result)
	}
	report := result.Details.(Report)
	if len(report.Children.Calls) != 2 || report.Children.Calls[1].Execution.Local != agent.ToolLocalNotStarted {
		t.Fatalf("report=%+v", report)
	}
}

func TestPolicyBlockAndUnknownToolsNeverInvoke(t *testing.T) {
	var calls atomic.Int32
	leaf := agent.ToolDefinition{Name: "blocked", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls.Add(1)
		return agent.ToolResult{}, nil
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	gate := func(_ context.Context, before agent.BeforeToolCallContext) (agent.ToolGateResult, error) {
		if before.ToolCall.Name == "blocked" {
			return agent.ToolGateResult{Action: agent.ToolGateActionBlock}, nil
		}
		return agent.ToolGateResult{Action: agent.ToolGateActionAllow}, nil
	}
	result, _ := runnerCall(t, tool, `try { await tools.blocked({}); } catch(e) { text(e.code); }
try { await tools.notSelected({}); } catch(e) { text("missing"); }`, agent.AgentDefinition{}, gate)
	if result.IsError || calls.Load() != 0 || !strings.Contains(contentText(result.Content), "policy_denied") {
		t.Fatalf("calls=%d result=%+v", calls.Load(), result)
	}
}

func TestUnsafeNumericArgumentsHaveNoMCPCall(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "write", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	leaf := discover(t, server, "write")[0]
	tool, err := New(newSandbox(t), []Binding{MCP(leaf, "fixture")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := runnerCall(t, tool, `try { await tools.mcp__fixture__write({id:9007199254740993}); } catch(e) { text({code:e.code}); }`, agent.AgentDefinition{}, nil)
	if result.IsError || calls.Load() != 0 || len(result.Details.(Report).Children.Calls) != 0 {
		t.Fatalf("calls=%d result=%+v", calls.Load(), result)
	}
}

func TestHookModifiedUnsafeArgumentsRejectedBeforeMCP(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "write", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	leaf := discover(t, server, "write")[0]
	tool, err := New(newSandbox(t), []Binding{MCP(leaf, "fixture")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	definition := agent.AgentDefinition{BeforeToolCall: func(_ context.Context, before agent.BeforeToolCallContext) (agent.BeforeToolCallResult, error) {
		if before.ToolCall.Name == "mcp__fixture__write" {
			before.Args.(map[string]any)["id"] = json.Number("9007199254740993")
		}
		return agent.BeforeToolCallResult{}, nil
	}}
	result, _ := runnerCall(t, tool, `try { await tools.mcp__fixture__write({id:1}); } catch(e) { text({code:e.code,reasonCode:e.reasonCode,execution:e.execution}); }`, definition, nil)
	if result.IsError || calls.Load() != 0 || !strings.Contains(contentText(result.Content), "argument_invalid") || !strings.Contains(contentText(result.Content), `"reasonCode":"unsafe_number"`) || !strings.Contains(contentText(result.Content), "not_dispatched") {
		t.Fatalf("calls=%d result=%+v", calls.Load(), result)
	}
}

func TestUnsafeMCPResultRetainsPostExecutionErrorChain(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "unsafe", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{StructuredContent: map[string]any{"id": float64(9007199254740992)}, Content: []mcp.Content{&mcp.TextContent{Text: "response received"}}}, nil
	})
	leaf := discover(t, server, "unsafe")[0]
	tool, err := New(newSandbox(t), []Binding{MCP(leaf, "fixture")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	code := `text("before"); await tools.mcp__fixture__unsafe({});`
	arguments, _ := json.Marshal(map[string]any{"code": code})
	outcome := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: tool.Name, Arguments: arguments}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	var rejected *mcptools.ResultError
	if !outcome.IsError || calls.Load() != 1 || !errors.As(outcome.Err, &rejected) || !strings.Contains(contentText(outcome.Result.Content), "before") {
		t.Fatalf("calls=%d outcome=%+v err=%v", calls.Load(), outcome, outcome.Err)
	}
	report, ok := outcome.Result.Details.(Report)
	if !ok {
		t.Fatalf("failure lost host report: %T", outcome.Result.Details)
	}
	if len(report.Children.Calls) != 1 || report.Children.Calls[0].Execution.Remote != agent.ToolRemoteCompleteReported || report.Children.Calls[0].Failure.Code != agent.ToolFailureResultRejected {
		t.Fatalf("report=%+v", report)
	}
}

func TestNativeProjectionAndDirectExecutionRestriction(t *testing.T) {
	value := agent.ToolDefinition{Name: "value", OutputSchema: map[string]any{"type": "integer"}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{StructuredContent: json.RawMessage(`3`), Content: []agent.Part{{Type: agent.PartTypeText, Text: "irrelevant"}}}, nil
	}}
	text := agent.ToolDefinition{Name: "text", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "first"}, {Type: agent.PartTypeText, Text: "second"}}}, nil
	}}
	tool, err := New(newSandbox(t), []Binding{Native(value, "local"), Native(text, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"code": "return 1"}}); err == nil {
		t.Fatal("unbound direct call succeeded")
	}
	result, _ := runnerCall(t, tool, `text({value:await tools.value({}), text:await tools.text({})});`, agent.AgentDefinition{}, nil)
	if result.IsError || !strings.Contains(contentText(result.Content), `"value":3`) || !strings.Contains(contentText(result.Content), `"text":"first\nsecond"`) {
		t.Fatalf("result=%+v", result)
	}
}

func TestStableNameMappingAndExplicitConstructionErrors(t *testing.T) {
	leaf := func(name string) agent.ToolDefinition {
		return agent.ToolDefinition{Name: name, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			return agent.ToolResult{}, nil
		}}
	}
	bindings := []Binding{MCP(leaf("normal"), "server"), MCP(leaf("x-y"), "server"), MCP(leaf("x_y"), "server"), MCP(leaf(strings.Repeat("long", 40)), "server")}
	seen := make(map[string]bool)
	for _, binding := range bindings {
		name := exportedName(binding)
		if !identifier(name) || len(name) > 64 || seen[name] || exportedName(binding) != name {
			t.Fatalf("name=%q", name)
		}
		seen[name] = true
	}
	if exportedName(bindings[0]) != "mcp__server__normal" {
		t.Fatal("normal name unexpectedly hashed")
	}
	s := newSandbox(t)
	invalid := [][]Binding{
		{{Tool: leaf("x"), Namespace: "local"}},
		{Native(leaf("x"), "local"), Native(leaf("x"), "local")},
		{Native(leaf("x"), "same"), MCP(leaf("y"), "same")},
		{Native(agent.ToolDefinition{Name: "container", ChildTools: []agent.ToolDefinition{}, Execute: leaf("x").Execute}, "local")},
		{Native(agent.ToolDefinition{Name: "unsafe", Parameters: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"const": json.Number("9007199254740993")}}}, Execute: leaf("x").Execute}, "local")},
	}
	for i, bindings := range invalid {
		if _, err := New(s, bindings, Options{}); err == nil {
			t.Fatalf("case %d accepted invalid binding", i)
		}
	}
	if _, err := New(s, nil, Options{Timeout: -time.Second}); err == nil {
		t.Fatal("negative timeout accepted")
	}
	if _, err := New(nil, nil, Options{}); err == nil {
		t.Fatal("nil sandbox accepted")
	}
}

func TestSuccessfulNativeSchemaRevalidatedAfterHook(t *testing.T) {
	leaf := agent.ToolDefinition{Name: "schema", OutputSchema: map[string]any{"type": "integer"}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{StructuredContent: json.RawMessage(`3`)}, nil
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	definition := agent.AgentDefinition{AfterToolCall: func(_ context.Context, after agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		if after.ToolCall.Name == "schema" {
			result := after.Result
			result.StructuredContent = json.RawMessage(`"wrong"`)
			return agent.AfterToolCallResult{Result: &result}, nil
		}
		return agent.AfterToolCallResult{}, nil
	}}
	result, _ := runnerCall(t, tool, `try { await tools.schema({}); } catch(e) { text({code:e.code, local:e.execution.local}); }`, definition, nil)
	if result.IsError || !strings.Contains(contentText(result.Content), "result_rejected") || !strings.Contains(contentText(result.Content), "returned") {
		t.Fatalf("result=%+v", result)
	}
}

func TestTransportErrorUsesHookSafeTextAndRetainsOriginalCause(t *testing.T) {
	original := errors.New("SECRET_TRANSPORT_CREDENTIAL")
	leaf := agent.ToolDefinition{Name: "transport", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{}, &agent.ToolExecutionError{Code: agent.ToolFailureTransport, Execution: agent.ToolExecutionInfo{Remote: agent.ToolRemoteUnknown}, Err: original}
	}}
	tool, err := New(newSandbox(t), []Binding{MCP(leaf, "fixture")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	definition := agent.AgentDefinition{AfterToolCall: func(_ context.Context, after agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		if after.ToolCall.Name == "mcp__fixture__transport" {
			result := agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "public transport failure"}}}
			clear := false
			return agent.AfterToolCallResult{Result: &result, IsError: &clear}, nil
		}
		return agent.AfterToolCallResult{}, nil
	}}
	result, events := runnerCall(t, tool, `try { await tools.mcp__fixture__transport({}); } catch(e) { text({code:e.code,message:e.message,remote:e.execution.remote}); }`, definition, nil)
	text := contentText(result.Content)
	if result.IsError || !strings.Contains(text, "public transport failure") || !strings.Contains(text, "transport") || !strings.Contains(text, "unknown") {
		t.Fatalf("result=%+v", result)
	}
	for _, event := range events {
		serialized, _ := json.Marshal(event)
		if strings.Contains(string(serialized), "SECRET_TRANSPORT_CREDENTIAL") {
			t.Fatalf("secret leaked in default event: %s", serialized)
		}
	}
	report := result.Details.(Report)
	if len(report.Children.Calls) != 1 || !errors.Is(report.Children.Calls[0].Err, original) {
		t.Fatalf("original cause lost: %+v", report.Children.Calls)
	}
}

func TestPromiseAllPreservesSequentialPolicyAdmissionOrder(t *testing.T) {
	s := newSandbox(t)
	for _, mode := range []agent.ToolExecutionMode{agent.ToolExecutionParallel, agent.ToolExecutionSequential} {
		t.Run(string(mode), func(t *testing.T) {
			var mu sync.Mutex
			var order []string
			leaf := agent.ToolDefinition{Name: "ordered", Parameters: map[string]any{"type": "object", "properties": map[string]any{"n": map[string]any{"type": "integer"}}, "required": []string{"n"}}, Execute: func(_ context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
				mu.Lock()
				order = append(order, fmt.Sprint(execution.Args.(map[string]any)["n"]))
				mu.Unlock()
				return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "ok"}}}, nil
			}}
			if mode == agent.ToolExecutionParallel {
				leaf.ExecutionMode = agent.ToolExecutionSequential
			}
			tool, err := New(s, []Binding{Native(leaf, "local")}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			result, _ := runnerCall(t, tool, `await Promise.all(Array.from({length:32}, (_,n) => tools.ordered({n}))); text("done");`, agent.AgentDefinition{ToolExecution: mode}, nil)
			if result.IsError || len(order) != 32 {
				t.Fatalf("order=%v result=%+v", order, result)
			}
			for i, value := range order {
				if value != fmt.Sprint(i) {
					t.Fatalf("admission reordered: %v", order)
				}
			}
		})
	}
}

func TestRevokedPermissionBlocksQueuedMCPCalls(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var allowed atomic.Bool
	allowed.Store(true)
	var calls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "write", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		allowed.Store(false)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "committed"}}}, nil
	})
	leaf := discover(t, server, "write")[0]
	leaf.ExecutionMode = agent.ToolExecutionSequential
	tool, err := New(newSandbox(t), []Binding{MCP(leaf, "fixture")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	definition := agent.AgentDefinition{CheckToolPermission: func(_ context.Context, before agent.BeforeToolCallContext) error {
		if before.ToolCall.Name == "mcp__fixture__write" && !allowed.Load() {
			return errors.New("permission revoked")
		}
		return nil
	}}
	result, _ := runnerCall(t, tool, `const values = await Promise.allSettled([0,1,2].map(() => tools.mcp__fixture__write({})));
text(values.map(r => r.status === "fulfilled" ? "committed" : r.reason.code));`, definition, nil)
	if result.IsError || calls.Load() != 1 || !strings.Contains(contentText(result.Content), `["committed","policy_denied","policy_denied"]`) {
		t.Fatalf("calls=%d result=%+v", calls.Load(), result)
	}
	report := result.Details.(Report)
	if len(report.Children.Calls) != 3 {
		t.Fatalf("report=%+v", report)
	}
	for _, call := range report.Children.Calls[1:] {
		if call.Execution.Local != agent.ToolLocalNotStarted || call.Execution.Remote != agent.ToolRemoteNotDispatched {
			t.Fatalf("queued call executed: %+v", call)
		}
	}
}

func TestScriptFailureRetainsBoundedImageTextAndCompleteLedger(t *testing.T) {
	var buffer bytes.Buffer
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.Set(0, 0, color.RGBA{R: 100, A: 255})
	if err := png.Encode(&buffer, pixel); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(buffer.Bytes())
	leaf := agent.ToolDefinition{Name: "record", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{}, nil
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{MaxOutputTokens: 4096})
	if err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf(`for(let i=0;i<300;i++) await tools.record({}); text("x".repeat(12000)); image({mimeType:"image/png",data:%q}); throw Error("script failed after output");`, data)
	arguments, _ := json.Marshal(map[string]any{"code": code})
	outcome := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: tool.Name, Arguments: arguments}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	if !outcome.IsError || len(contentText(outcome.Result.Content)) < 12000 {
		t.Fatalf("bounded text lost: length=%d err=%v", len(contentText(outcome.Result.Content)), outcome.Err)
	}
	imageRetained := false
	for _, part := range outcome.Result.Content {
		if part.Type == agent.PartTypeImage && part.Data == data {
			imageRetained = true
		}
	}
	if !imageRetained {
		t.Fatal("bounded image lost on script failure")
	}
	report, ok := outcome.Result.Details.(Report)
	if !ok || len(report.Children.Calls) != 300 || !report.Children.DetailsTruncated || len(report.Sandbox.Outputs) != 0 {
		t.Fatalf("complete basic ledger lost: report=%+v", report)
	}
}

func TestInvocationStoreSharesSuccessfulScriptsAndIsolatesRuns(t *testing.T) {
	tool, err := New(newSandbox(t), nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	model := agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		phase := int(requests.Add(1)-1) % 4
		message := agent.NewTextMessage(agent.RoleAssistant, "done")
		message.StopReason = agent.StopReasonStop
		if phase < 3 {
			codes := []string{`if(load("value") !== undefined) throw Error("prior run state leaked"); store("value",{n:1}); return load("value");`,
				`store("value",{n:99}); text("partial"); throw Error("discard staged state");`,
				`return load("value");`}
			arguments, _ := json.Marshal(map[string]any{"code": codes[phase]})
			message.Parts = nil
			message.StopReason = agent.StopReasonToolUse
			message.ToolCalls = []agent.ToolCall{{ID: fmt.Sprintf("outer-%d", phase), Name: tool.Name, Arguments: arguments}}
		}
		return immediate(message), nil
	})
	runner, err := agent.NewRunner(agent.AgentDefinition{Model: model, Tools: []agent.ToolDefinition{tool}, SessionID: "same-session", MaxTurns: 4})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		stream := runner.Query(t.Context(), "exercise invocation state")
		for range stream.Events() {
		}
		snapshot, err := stream.Wait()
		if err != nil {
			t.Fatal(err)
		}
		var outputs []agent.ToolResultPayload
		for _, message := range snapshot.Messages {
			if message.ToolResult != nil {
				outputs = append(outputs, *message.ToolResult)
			}
		}
		if len(outputs) != 3 || outputs[0].IsError || !outputs[1].IsError || outputs[2].IsError {
			t.Fatalf("store run results=%+v", outputs)
		}
		if !strings.Contains(contentText(outputs[0].Content), `"n":1`) || !strings.Contains(contentText(outputs[2].Content), `"n":1`) || strings.Contains(contentText(outputs[2].Content), `99`) {
			t.Fatalf("failed middle script committed state: %+v", outputs)
		}
	}
}

func TestHookUnsafeMCPResultRejectsBeforeEventAndLedger(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "value", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{StructuredContent: map[string]any{"id": 1}}, nil
	})
	leaf := discover(t, server, "value")[0]
	tool, err := New(newSandbox(t), []Binding{MCP(leaf, "fixture")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, businessError := range []bool{false, true} {
		definition := agent.AgentDefinition{AfterToolCall: func(_ context.Context, after agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
			if after.ToolCall.Name == "mcp__fixture__value" {
				result := after.Result
				result.StructuredContent = json.RawMessage(`{"id":9007199254740993}`)
				return agent.AfterToolCallResult{Result: &result, IsError: &businessError}, nil
			}
			return agent.AfterToolCallResult{}, nil
		}}
		result, events := runnerCall(t, tool, `try { await tools.mcp__fixture__value({}); throw Error("unsafe result resolved"); } catch(e) { text({code:e.code,reasonCode:e.reasonCode,remote:e.execution.remote}); }`, definition, nil)
		if result.IsError || !strings.Contains(contentText(result.Content), "result_rejected") || !strings.Contains(contentText(result.Content), `"reasonCode":"unsafe_number"`) || !strings.Contains(contentText(result.Content), "complete_reported") {
			t.Fatalf("result=%+v", result)
		}
		report := result.Details.(Report)
		if len(report.Children.Calls) != 1 || report.Children.Calls[0].Failure == nil || report.Children.Calls[0].Failure.Code != agent.ToolFailureResultRejected || report.Children.Calls[0].Failure.Reason != "unsafe_number" || report.Children.Calls[0].Execution.Remote != agent.ToolRemoteCompleteReported {
			t.Fatalf("unsafe hook result recorded as success: %+v", report)
		}
		found := false
		for _, event := range events {
			if event.Type == agent.EventToolExecutionEnd && event.ParentToolCallID == "outer" {
				found = true
				if !event.IsError || event.Failure == nil || event.Failure.Code != agent.ToolFailureResultRejected {
					t.Fatalf("unsafe result event claimed success: %+v", event)
				}
			}
		}
		if !found {
			t.Fatal("missing child failure event")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("unsafe result was retried: calls=%d", calls.Load())
	}
}

func TestCanceledVMReportsActiveHostUntilActualExit(t *testing.T) {
	config := codemode.DefaultConfig()
	config.MaxConcurrentCalls = 1
	sandbox, err := codemode.NewSandbox(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sandbox.Close(ctx); err != nil {
			t.Errorf("sandbox Close after host release: %v", err)
		}
	})
	leaf := agent.ToolDefinition{Name: "blocked", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		close(entered)
		<-release
		return agent.ToolResult{}, nil
	}}
	tool, err := New(sandbox, []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	arguments := json.RawMessage(`{"code":"await tools.blocked({});"}`)
	done := make(chan agent.ToolCallOutcome, 1)
	go func() {
		done <- agent.RunToolCall(ctx, agent.ToolCall{ID: "outer", Name: tool.Name, Arguments: arguments}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("host did not enter")
	}
	cancel()
	var outcome agent.ToolCallOutcome
	select {
	case outcome = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("VM cancellation waited for uncooperative host")
	}
	if !outcome.IsError || !errors.Is(outcome.Err, context.Canceled) || outcome.Failure.Code != agent.ToolFailureCanceled {
		t.Fatalf("outcome=%+v", outcome)
	}
	report, ok := outcome.Result.Details.(Report)
	if !ok || report.Children.Active != 1 || len(report.Children.Calls) != 1 || report.Children.Calls[0].Completed || report.Children.Calls[0].Execution.Local != agent.ToolLocalEntered || report.Children.Calls[0].Execution.Remote != agent.ToolRemoteUnknown {
		t.Fatalf("active execution facts lost: %+v", report)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = sandbox.Close(closeCtx)
	closeCancel()
	var incomplete *codemode.CloseError
	if !errors.As(err, &incomplete) || len(incomplete.Outstanding) != 1 {
		t.Fatalf("unfinished host was reported closed: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
}

func TestParentHookFailureDiscardsRawOutputAndPreservesChildFacts(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "write", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{StructuredContent: map[string]any{"secret": "SECRET_PARENT_HOOK_BODY"}}, nil
	})
	leaf := discover(t, server, "write")[0]
	tool, err := New(newSandbox(t), []Binding{MCP(leaf, "fixture")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	hookFailure := errors.New("parent output policy failed")
	arguments := json.RawMessage(`{"code":"text((await tools.mcp__fixture__write({})).structuredContent.secret);"}`)
	outcome := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: tool.Name, Arguments: arguments}, agent.RunToolCallOptions{
		Tools: []agent.ToolDefinition{tool}, AfterToolCall: func(_ context.Context, after agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
			if after.ToolCall.Name == "code" {
				return agent.AfterToolCallResult{}, hookFailure
			}
			return agent.AfterToolCallResult{}, nil
		},
	})
	if !outcome.IsError || calls.Load() != 1 || !errors.Is(outcome.Err, hookFailure) || outcome.Result.Details != nil || strings.Contains(contentText(outcome.Result.Content), "SECRET_PARENT_HOOK_BODY") {
		t.Fatalf("calls=%d outcome=%+v", calls.Load(), outcome)
	}
	if outcome.Result.ChildCalls == nil || len(outcome.Result.ChildCalls.Calls) != 1 || outcome.Result.ChildCalls.Calls[0].Execution.Remote != agent.ToolRemoteCompleteReported || !outcome.Result.ChildCalls.Calls[0].Completed {
		t.Fatalf("parent hook discarded execution ledger: %+v", outcome.Result.ChildCalls)
	}
	serialized, _ := json.Marshal(outcome.Result)
	if strings.Contains(string(serialized), "SECRET_PARENT_HOOK_BODY") {
		t.Fatalf("raw result leaked after parent hook failure: %s", serialized)
	}
}

func ExampleMCP() {
	tool := agent.ToolDefinition{Name: "get_issue"}
	binding := MCP(tool, "github")
	fmt.Println(binding.Projection, binding.Namespace, binding.Tool.Name)
	// Output: mcp github get_issue
}

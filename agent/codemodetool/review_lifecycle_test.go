package codemodetool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReplacementErrorCannotCancelParentByForgingCallID(t *testing.T) {
	leaf := agent.ToolDefinition{Name: "bad", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{}, context.Canceled
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	outcome := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: tool.Name, ParsedArgs: map[string]any{
		"code": `try { await tools.bad({}); } catch (e) {}
const replacement = new Error("unrelated script failure");
replacement.callId = "outer/1";
throw replacement;`,
	}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
	if t.Context().Err() != nil || outcome.Err == nil || outcome.Failure == nil || outcome.Failure.Code != agent.ToolFailureScript || errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("replacement changed parent classification: %+v", outcome)
	}
	report := outcome.Result.ChildCalls
	if report == nil || len(report.Calls) != 1 || !report.Calls[0].Completed || report.Calls[0].Failure.Code != agent.ToolFailureCanceled || !errors.Is(report.Calls[0].Err, context.Canceled) {
		t.Fatalf("original child failure was lost: %+v", report)
	}
}

func TestPanickingChildRunsRedactionAndCompletesHostLedger(t *testing.T) {
	original := errors.New("SECRET_EXECUTOR_PANIC")
	leaf := agent.ToolDefinition{Name: "bad", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		panic(original)
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	hooks := 0
	definition := agent.AgentDefinition{AfterToolCall: func(_ context.Context, after agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		if after.ParentToolCallID != "" {
			hooks++
			if !errors.Is(after.Err, original) || after.Execution.Local != agent.ToolLocalReturned {
				t.Errorf("panic facts/cause unavailable to hook: %+v", after)
			}
			return agent.AfterToolCallResult{Result: &agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "redacted"}}}}, nil
		}
		return agent.AfterToolCallResult{}, nil
	}}
	result, events := runnerCall(t, tool, `try { await tools.bad({}); } catch (e) { text({code:e.code, message:e.message}); }`, definition, nil)
	if hooks != 1 || result.IsError || !strings.Contains(contentText(result.Content), "redacted") {
		t.Fatalf("panic skipped effective hook result: hooks=%d result=%+v", hooks, result)
	}
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil || strings.Contains(string(raw), original.Error()) {
			t.Fatalf("panic leaked in default event: %s err=%v", raw, err)
		}
	}
	report := result.ChildCalls
	if report == nil || len(report.Calls) != 1 || !report.Closed || report.Active != 0 {
		t.Fatalf("missing completed child ledger: %+v", report)
	}
	call := report.Calls[0]
	if !call.Completed || call.Execution.Local != agent.ToolLocalReturned || call.Failure == nil || !errors.Is(call.Err, original) {
		t.Fatalf("panic left stale execution facts: %+v", call)
	}
}

func TestCodemodeRejectsHookArgumentsBeyondChildLimit(t *testing.T) {
	calls := 0
	leaf := agent.ToolDefinition{Name: "write", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}},
	}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls++
		return agent.ToolResult{}, nil
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{ChildLimits: agent.ChildCallLimits{MaxArgumentBytes: 128}})
	if err != nil {
		t.Fatal(err)
	}
	definition := agent.AgentDefinition{BeforeToolCall: func(_ context.Context, before agent.BeforeToolCallContext) (agent.BeforeToolCallResult, error) {
		if before.ParentToolCallID != "" {
			before.Args.(map[string]any)["id"] = strings.Repeat("x", 4096)
		}
		return agent.BeforeToolCallResult{}, nil
	}}
	result, _ := runnerCall(t, tool, `try { await tools.write({id:"small"}); } catch(e) { text(e.code); }`, definition, nil)
	if calls != 0 || result.IsError || !strings.Contains(contentText(result.Content), "resource") {
		t.Fatalf("hook arguments bypassed configured limit: calls=%d result=%+v", calls, result)
	}
	if result.ChildCalls == nil || len(result.ChildCalls.Calls) != 1 {
		t.Fatalf("rejected child missing from ledger: %+v", result.ChildCalls)
	}
	call := result.ChildCalls.Calls[0]
	if !call.Completed || call.Execution.Local != agent.ToolLocalNotStarted || call.Execution.Remote != agent.ToolRemoteNotDispatched {
		t.Fatalf("oversized arguments entered executor: %+v", call)
	}
}

func TestOuterCodeRejectsUnicodeBeforeDecoderChangesToolID(t *testing.T) {
	var calls atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "outer-unicode", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "write", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.ID}}}, nil
	})
	native := agent.ToolDefinition{Name: "write", Parameters: map[string]any{"type": "object"}, Execute: func(_ context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls.Add(1)
		return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: in.Args.(map[string]any)["id"].(string)}}}, nil
	}}
	for _, binding := range []Binding{MCP(discover(t, server, "write")[0], "fixture"), Native(native, "local")} {
		tool, err := New(newSandbox(t), []Binding{binding}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		name := exportedName(binding)
		for _, raw := range []string{
			`{"code":"await tools.write({id:'abc\ud800'}); return 'accepted';"}`,
			`{"\udfff":"x","code":"await tools.write({id:'safe'}); return 'accepted';"}`,
			"{\"code\":\"await tools.write({id:'abc" + string([]byte{0xff}) + "'}); return 'accepted';\"}",
		} {
			before := calls.Load()
			out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: tool.Name, Arguments: json.RawMessage(strings.ReplaceAll(raw, "tools.write", "tools."+name))}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
			if calls.Load() != before || out.Err == nil || out.Failure == nil || out.Failure.Code != agent.ToolFailureArgumentInvalid || out.Failure.Reason != "invalid_unicode" || out.Execution.Local != agent.ToolLocalNotStarted || out.Execution.Remote != agent.ToolRemoteNotDispatched {
				t.Fatalf("outer JSON changed the ID before rejection: calls=%d outcome=%+v", calls.Load()-before, out)
			}
		}
		for _, valid := range []struct{ raw, expected string }{
			{`{"code":"return await tools.write({id:'abc\ud83d\ude00'});"}`, "abc😀"},
			{`{"code":"return await tools.write({id:'abc\ufffd'});"}`, "abc�"},
		} {
			before := calls.Load()
			out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: tool.Name, Arguments: json.RawMessage(strings.ReplaceAll(valid.raw, "tools.write", "tools."+name))}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}})
			if calls.Load() != before+1 || out.Err != nil || !strings.Contains(contentText(out.Result.Content), valid.expected) {
				t.Fatalf("valid outer JSON changed the ID: calls=%d outcome=%+v", calls.Load()-before, out)
			}
		}
	}
}

func TestAfterHookPanicKeepsCompletedWriteInCodemodeLedger(t *testing.T) {
	original := errors.New("SECRET_AFTER_HOOK_PANIC")
	writes, hooks := 0, 0
	leaf := agent.ToolDefinition{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		writes++
		return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "SECRET_COMPLETED_OUTPUT"}}, Execution: &agent.ToolExecutionInfo{Remote: agent.ToolRemoteCompleteReported}}, nil
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	definition := agent.AgentDefinition{AfterToolCall: func(_ context.Context, after agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		if after.ParentToolCallID != "" {
			hooks++
			panic(original)
		}
		return agent.AfterToolCallResult{}, nil
	}}
	result, events := runnerCall(t, tool, `try { await tools.write({}); } catch(e) { text(e.code+":"+e.reasonCode); }`, definition, nil)
	if writes != 1 || hooks != 1 || result.IsError || !strings.Contains(contentText(result.Content), "hook:after_hook_panic") {
		t.Fatalf("hook panic bypassed effective failure: writes=%d hooks=%d result=%+v", writes, hooks, result)
	}
	report := result.ChildCalls
	if report == nil || len(report.Calls) != 1 || !report.Closed || report.Active != 0 {
		t.Fatalf("hook panic lost ledger: %+v", report)
	}
	call := report.Calls[0]
	if !call.Completed || call.Execution.Local != agent.ToolLocalReturned || call.Execution.Remote != agent.ToolRemoteCompleteReported || call.Failure == nil || call.Failure.Code != agent.ToolFailureHook || !errors.Is(call.Err, original) {
		t.Fatalf("hook panic changed completed write facts: %+v", call)
	}
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil || strings.Contains(string(raw), "SECRET_") {
			t.Fatalf("hook panic leaked default event data: %s err=%v", raw, err)
		}
	}
}

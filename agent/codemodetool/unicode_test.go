package codemodetool

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUnicodeMCPAndNativeArgumentsHaveNoActualCall(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "unicode", Version: "1"}, nil)
	var mcpCalls, nativeCalls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "write", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mcpCalls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	native := agent.ToolDefinition{Name: "write", Parameters: map[string]any{"type": "object"}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		nativeCalls.Add(1)
		return agent.ToolResult{}, nil
	}}
	for _, binding := range []Binding{MCP(discover(t, server, "write")[0], "fixture"), Native(native, "local")} {
		tool, err := New(newSandbox(t), []Binding{binding}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		name := exportedName(binding)
		for _, value := range []string{`{id:"abc\ud800"}`, `{"\udfff":"id"}`} {
			result, _ := runnerCall(t, tool, `try {await tools.`+name+`(`+value+`);return "accepted"}catch(e){return e.code+":"+e.message}`, agent.AgentDefinition{}, nil)
			if result.IsError || !strings.Contains(contentText(result.Content), "invalid_unicode") || len(result.Details.(Report).Children.Calls) != 0 || mcpCalls.Load() != 0 || nativeCalls.Load() != 0 {
				t.Fatalf("invalid Unicode called %s: result=%+v mcp=%d native=%d", name, result, mcpCalls.Load(), nativeCalls.Load())
			}
		}
	}
}

func TestUnicodeNativeResultRejectedBeforeProjection(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`{"id":"\ud800"}`), json.RawMessage(`{"\udfff":"id"}`), json.RawMessage("{\"id\":\"" + string([]byte{0xff}) + "\"}")} {
		leaf := agent.ToolDefinition{Name: "read", OutputSchema: map[string]any{"type": "object"}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			return agent.ToolResult{StructuredContent: raw}, nil
		}}
		tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		result, events := runnerCall(t, tool, `try {await tools.read({});return "accepted"}catch(e){return e.code+":"+e.reasonCode}`, agent.AgentDefinition{}, nil)
		if result.IsError || !strings.Contains(contentText(result.Content), "result_rejected:invalid_unicode") {
			t.Fatalf("invalid Unicode native result exposed: %q result=%+v", raw, result)
		}
		children := result.Details.(Report).Children.Calls
		if len(children) != 1 || children[0].Failure == nil || children[0].Failure.Reason != "invalid_unicode" {
			t.Fatalf("invalid Unicode native result retained: %+v", children)
		}
		found := false
		for _, event := range events {
			if event.Type == agent.EventToolExecutionEnd && event.ParentToolCallID == "outer" && event.ToolName == "read" {
				found = true
				if !event.IsError || event.Failure == nil || event.Failure.Reason != "invalid_unicode" || (event.ToolResult != nil && len(event.ToolResult.StructuredContent) != 0) {
					t.Fatalf("invalid Unicode escaped through end event: %+v", event)
				}
			}
		}
		if !found {
			t.Fatal("missing native child end event")
		}
	}
}

func TestUnicodeHookGoArgumentsRejectedBeforeNativeAndMCP(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "unicode", Version: "1"}, nil)
	var calls atomic.Int32
	server.AddTool(&mcp.Tool{Name: "write", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	native := agent.ToolDefinition{Name: "write", Parameters: map[string]any{"type": "object"}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls.Add(1)
		return agent.ToolResult{}, nil
	}}
	for _, binding := range []Binding{MCP(discover(t, server, "write")[0], "fixture"), Native(native, "local")} {
		tool, err := New(newSandbox(t), []Binding{binding}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		name := exportedName(binding)
		for _, invalidKey := range []bool{false, true} {
			definition := agent.AgentDefinition{BeforeToolCall: func(_ context.Context, before agent.BeforeToolCallContext) (agent.BeforeToolCallResult, error) {
				if before.ToolCall.Name == name {
					invalid := string([]byte{0xff})
					if invalidKey {
						before.Args.(map[string]any)[invalid] = "id"
					} else {
						before.Args.(map[string]any)["id"] = invalid
					}
				}
				return agent.BeforeToolCallResult{}, nil
			}}
			result, _ := runnerCall(t, tool, `try {await tools.`+name+`({id:"safe"});return "accepted"}catch(e){return e.code+":"+e.reasonCode}`, definition, nil)
			if result.IsError || !strings.Contains(contentText(result.Content), "argument_invalid:invalid_unicode") || calls.Load() != 0 {
				t.Fatalf("hook Unicode sent %s: result=%+v calls=%d", name, result, calls.Load())
			}
		}
	}
}

func TestUnicodePlainNativeResultRejectedBeforeMarshal(t *testing.T) {
	for _, businessError := range []bool{false, true} {
		leaf := agent.ToolDefinition{Name: "read", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: string([]byte{0xff})}}, IsError: businessError}, nil
		}}
		tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		result, _ := runnerCall(t, tool, `try {await tools.read({});return "accepted"}catch(e){return e.code+":"+e.reasonCode}`, agent.AgentDefinition{}, nil)
		if result.IsError || !strings.Contains(contentText(result.Content), "result_rejected:invalid_unicode") {
			t.Fatalf("native text silently repaired (businessError=%t): %+v", businessError, result)
		}
	}
}

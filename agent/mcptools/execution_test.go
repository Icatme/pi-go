package mcptools

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

func TestExecutionClassificationAcrossRealSession(t *testing.T) {
	for _, test := range []struct {
		name, code, remote, reason string
		calls                      int32
	}{
		{"arguments", "argument_invalid", "not_dispatched", "input_schema", 0},
		{"schema-drift", "schema_changed", "not_dispatched", "schema_changed", 0},
		{"protocol", "protocol", "unknown", "call_failure", 1},
		{"unsafe-result", "result_rejected", "complete_reported", "unsafe_number", 1},
		{"unsupported-result", "result_rejected", "complete_reported", "unsupported_content", 1},
		{"input-required", "input_required_unsupported", "input_required", "input_required", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
			var calls atomic.Int32
			tool := &mcp.Tool{Name: "test", InputSchema: objectSchema}
			if test.name == "arguments" {
				tool.InputSchema = map[string]any{"type": "object", "required": []any{"missing"}}
			}
			handler := func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				switch test.name {
				case "protocol":
					return nil, errors.New("peer rejected the RPC")
				case "unsafe-result":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "bounded exact text"}}, StructuredContent: map[string]any{"id": int64(9007199254740993)}}, nil
				case "unsupported-result":
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ResourceLink{URI: "https://invalid.example", Name: "unused"}}}, nil
				case "input-required":
					return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{}}, nil
				default:
					return &mcp.CallToolResult{}, nil
				}
			}
			s.AddTool(tool, handler)
			protocol := "2026-07-28"
			if test.name == "schema-drift" {
				// Modern SDK invalidation is asynchronous; drift without cache
				// depends on legacy listing, not a notification delivery race.
				protocol = "2025-11-25"
			}
			cs := session(t, s, false, protocol)
			tools, err := Discover(t.Context(), cs, Options{Names: []string{"test"}})
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "schema-drift" {
				s.AddTool(&mcp.Tool{Name: "test", InputSchema: map[string]any{"type": "object", "required": []any{"new"}}}, handler)
			}
			result, err := tools[0].Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{}})
			var typed *agent.ToolExecutionError
			if !errors.As(err, &typed) || string(typed.Code) != test.code || string(typed.Execution.Remote) != test.remote || typed.Reason != test.reason || calls.Load() != test.calls {
				t.Fatalf("result=%+v error=%v typed=%+v calls=%d", result, err, typed, calls.Load())
			}
			if test.name == "unsafe-result" {
				var mapping *ResultError
				if !errors.As(err, &mapping) || len(result.StructuredContent) != 0 || len(mapping.Result.Content) != 1 || mapping.Result.Content[0].Text != "bounded exact text" {
					t.Fatalf("unsafe partial result or error chain lost: %+v %v", result, err)
				}
			}
		})
	}
}

func TestPermissionRecheckedImmediatelyBeforeSDKCall(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls, checks atomic.Int32
	add(s, "write", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{}, nil
	})
	cs := session(t, s, false, "2025-11-25")
	tools, err := Discover(t.Context(), cs, Options{Names: []string{"write"}})
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("account revoked after preflight")
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "write", Name: "write", Arguments: json.RawMessage(`{}`)}, agent.RunToolCallOptions{
		Tools: tools,
		CheckToolPermission: func(context.Context, agent.BeforeToolCallContext) error {
			if checks.Add(1) > 1 {
				return denied
			}
			return nil
		},
	})
	if checks.Load() != 2 || calls.Load() != 0 || !errors.Is(out.Err, denied) || out.Failure == nil || out.Failure.Code != "policy_denied" || out.Execution.Local != "returned" || out.Execution.Remote != "not_dispatched" {
		t.Fatalf("checks=%d calls=%d outcome=%+v", checks.Load(), calls.Load(), out)
	}
}

func TestMappingFailureSurvivesAgentAndAfterHook(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	var calls atomic.Int32
	add(s, "read", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "private raw reply"}}, StructuredContent: map[string]any{"id": int64(9007199254740993)}}, nil
	})
	cs := session(t, s, false, "2025-11-25")
	tools, err := Discover(t.Context(), cs, Options{Names: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "read", Name: "read", Arguments: json.RawMessage(`{}`)}, agent.RunToolCallOptions{
		Tools: tools,
		AfterToolCall: func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
			no := false
			return agent.AfterToolCallResult{Result: &agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "safe reply"}}}, IsError: &no}, nil
		},
	})
	var mapping *ResultError
	if calls.Load() != 1 || !errors.As(out.Err, &mapping) || out.Failure == nil || out.Failure.Code != "result_rejected" || out.Execution.Remote != "complete_reported" || !out.IsError || len(out.Result.Content) != 1 || out.Result.Content[0].Text != "safe reply" {
		t.Fatalf("calls=%d outcome=%+v", calls.Load(), out)
	}
}

func TestNumericProjectionBoundsBeforeRationalExpansion(t *testing.T) {
	for _, raw := range []string{"1e-1000000", "0e-1000000", "1e309", "1e-309", "0." + strings.Repeat("0", 256) + "1"} {
		_, err := validationProjection(json.Number(raw))
		if err == nil || failureReason(err, "") != "unsafe_number" {
			t.Fatalf("unbounded numeric domain accepted: %q %v", raw, err)
		}
	}
}

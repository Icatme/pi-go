package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestChildResolverRunsAfterApprovalAndRechecksPermission(t *testing.T) {
	for _, mode := range []string{"blocked", "permission_before", "permission_after", "allowed"} {
		t.Run(mode, func(t *testing.T) {
			resolved, entered, permissions := 0, 0, 0
			parent := ToolDefinition{Name: "container", ResolveChildTools: func(context.Context, ToolExecutionContext) ([]ToolDefinition, error) {
				resolved++
				return []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) { entered++; return ToolResult{}, nil }}}, nil
			}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				child := in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
				return child.Result, child.Err
			}}
			out := RunToolCall(t.Context(), ToolCall{Name: parent.Name}, RunToolCallOptions{Tools: []ToolDefinition{parent},
				ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
					if mode == "blocked" {
						return ToolGateResult{Action: ToolGateActionBlock}, nil
					}
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				}, CheckToolPermission: func(context.Context, BeforeToolCallContext) error {
					permissions++
					if mode == "permission_before" || mode == "permission_after" && permissions == 2 {
						return errors.New("revoked")
					}
					return nil
				}})
			switch mode {
			case "allowed":
				if out.Err != nil || resolved != 1 || entered != 1 {
					t.Fatalf("out=%+v resolved=%d entered=%d", out, resolved, entered)
				}
			case "permission_after":
				if out.Err == nil || resolved != 1 || entered != 0 || out.Execution.Remote != ToolRemoteNotDispatched {
					t.Fatal(out, resolved, entered)
				}
			default:
				if out.Err == nil || resolved != 0 || entered != 0 {
					t.Fatal(out, resolved, entered)
				}
			}
		})
	}
}

func TestChildResolverRejectsInvalidCatalogAndPanic(t *testing.T) {
	secret := errors.New("PRIVATE resolver panic")
	leaf := ToolDefinition{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		t.Fatal("invalid child executed")
		return ToolResult{}, nil
	}}
	for _, mode := range []string{"duplicate", "nested", "schema", "panic", "both"} {
		t.Run(mode, func(t *testing.T) {
			entered := false
			tool := ToolDefinition{Name: "container", ResolveChildTools: func(context.Context, ToolExecutionContext) ([]ToolDefinition, error) {
				switch mode {
				case "duplicate":
					return []ToolDefinition{leaf, leaf}, nil
				case "nested":
					child := leaf
					child.ResolveChildTools = func(context.Context, ToolExecutionContext) ([]ToolDefinition, error) { return nil, nil }
					return []ToolDefinition{child}, nil
				case "schema":
					child := leaf
					child.Parameters = map[string]any{"type": 42}
					return []ToolDefinition{child}, nil
				case "panic":
					panic(secret)
				default:
					return []ToolDefinition{leaf}, nil
				}
			}, Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				entered = true
				return ToolResult{}, nil
			}}
			if mode == "both" {
				tool.ChildTools = []ToolDefinition{leaf}
			}
			out := RunToolCall(t.Context(), ToolCall{Name: tool.Name}, RunToolCallOptions{Tools: []ToolDefinition{tool}})
			if entered || out.Err == nil || out.Execution.Local != ToolLocalNotStarted || out.Execution.Remote != ToolRemoteNotDispatched {
				t.Fatalf("out=%+v entered=%v", out, entered)
			}
			if mode == "panic" && (!errors.Is(out.Err, secret) || strings.Contains(out.Err.Error(), "PRIVATE")) {
				t.Fatal("panic presentation or cause lost", out.Err)
			}
		})
	}
}

func TestChildResolverUsesFreshDetachedDefinitions(t *testing.T) {
	resolved := 0
	parent := ToolDefinition{Name: "container", ResolveChildTools: func(context.Context, ToolExecutionContext) ([]ToolDefinition, error) {
		resolved++
		value := resolved
		return []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
			return ToolResult{Content: []Part{{Type: PartTypeText, Text: strings.Repeat("x", value)}}}, nil
		}}}, nil
	}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		out := in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
		return out.Result, out.Err
	}}
	for i := 1; i <= 2; i++ {
		out := RunToolCall(t.Context(), ToolCall{Name: parent.Name}, RunToolCallOptions{Tools: []ToolDefinition{parent}})
		if out.Err != nil || out.Result.Content[0].Text != strings.Repeat("x", i) || out.Result.ChildCalls == nil {
			t.Fatal(out)
		}
	}
}

func TestChildHookAndPermissionErrorsRetainPrivateCause(t *testing.T) {
	private := errors.New("PRIVATE host policy database error")
	for _, mode := range []string{"before", "gate", "permission", "after"} {
		t.Run(mode, func(t *testing.T) {
			var child ToolCallOutcome
			parent := ToolDefinition{Name: "container", ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) { return ToolResult{}, nil }}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				child = in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
				return child.Result, child.Err
			}}
			options := RunToolCallOptions{Tools: []ToolDefinition{parent}}
			options.BeforeToolCall = func(_ context.Context, in BeforeToolCallContext) (BeforeToolCallResult, error) {
				if mode == "before" && in.ParentToolCallID != "" {
					return BeforeToolCallResult{}, private
				}
				return BeforeToolCallResult{}, nil
			}
			options.ToolGate = func(_ context.Context, in BeforeToolCallContext) (ToolGateResult, error) {
				if mode == "gate" && in.ParentToolCallID != "" {
					return ToolGateResult{}, private
				}
				return ToolGateResult{Action: ToolGateActionAllow}, nil
			}
			options.CheckToolPermission = func(_ context.Context, in BeforeToolCallContext) error {
				if mode == "permission" && in.ParentToolCallID != "" {
					return private
				}
				return nil
			}
			options.AfterToolCall = func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
				if mode == "after" && in.ParentToolCallID != "" {
					return AfterToolCallResult{}, private
				}
				return AfterToolCallResult{}, nil
			}
			out := RunToolCall(t.Context(), ToolCall{ID: "outer", Name: parent.Name}, options)
			if !errors.Is(child.Err, private) || !errors.Is(out.Err, private) || strings.Contains(child.Err.Error(), "PRIVATE") {
				t.Fatalf("private cause leaked/lost: child=%+v out=%+v", child, out)
			}
			for _, part := range child.Result.Content {
				if strings.Contains(part.Text, "PRIVATE") {
					t.Fatal("private cause leaked to content")
				}
			}
			for _, detail := range out.Result.ChildCalls.Details {
				if strings.Contains(detail.Error+detail.Summary, "PRIVATE") {
					t.Fatal("private cause leaked to ledger presentation")
				}
			}
		})
	}
}

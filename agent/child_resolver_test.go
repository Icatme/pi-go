package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestChildResolverRunsAfterApprovalAndRechecksPermission(t *testing.T) {
	for _, mode := range []string{"blocked", "permission_before", "permission_after", "allowed"} {
		t.Run(mode, func(t *testing.T) {
			resolved, entered, permissions := 0, 0, 0
			parent := ToolDefinition{Name: "container", ResolveChildTools: func(context.Context, ToolExecutionContext) (ChildToolResolution, error) {
				resolved++
				return ChildToolResolution{Tools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) { entered++; return ToolResult{}, nil }}}}, nil
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
			tool := ToolDefinition{Name: "container", ResolveChildTools: func(context.Context, ToolExecutionContext) (ChildToolResolution, error) {
				switch mode {
				case "duplicate":
					return ChildToolResolution{Tools: []ToolDefinition{leaf, leaf}}, nil
				case "nested":
					child := leaf
					child.ResolveChildTools = func(context.Context, ToolExecutionContext) (ChildToolResolution, error) {
						return ChildToolResolution{}, nil
					}
					return ChildToolResolution{Tools: []ToolDefinition{child}}, nil
				case "schema":
					child := leaf
					child.Parameters = map[string]any{"type": 42}
					return ChildToolResolution{Tools: []ToolDefinition{child}}, nil
				case "panic":
					panic(secret)
				default:
					return ChildToolResolution{Tools: []ToolDefinition{leaf}}, nil
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
	parent := ToolDefinition{Name: "container", ResolveChildTools: func(context.Context, ToolExecutionContext) (ChildToolResolution, error) {
		resolved++
		value := resolved
		return ChildToolResolution{Tools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
			return ToolResult{Content: []Part{{Type: PartTypeText, Text: strings.Repeat("x", value)}}}, nil
		}}}}, nil
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

func TestChildResolverDeadlineBoundsRemainingLifecycle(t *testing.T) {
	for _, mode := range []string{"resolved", "earlier_caller", "zero"} {
		t.Run(mode, func(t *testing.T) {
			callerDeadline := time.Now().Add(2 * time.Second)
			ctx, cancel := context.WithDeadline(t.Context(), callerDeadline)
			defer cancel()
			resolvedDeadline := time.Now().Add(time.Second)
			if mode == "earlier_caller" {
				resolvedDeadline = callerDeadline.Add(time.Second)
			} else if mode == "zero" {
				resolvedDeadline = time.Time{}
			}
			expected := callerDeadline
			if mode == "resolved" {
				expected = resolvedDeadline
			}
			var phases []string
			checkDeadline := func(ctx context.Context, phase string) {
				deadline, ok := ctx.Deadline()
				if !ok || !deadline.Equal(expected) {
					t.Fatalf("%s deadline changed: got=%v want=%v", phase, deadline, expected)
				}
				phases = append(phases, phase)
			}
			leaf := ToolDefinition{Name: "leaf", Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				checkDeadline(ctx, "child_execute")
				if err := in.CheckPermission(ctx); err != nil {
					return ToolResult{}, err
				}
				return ToolResult{}, nil
			}}
			parent := ToolDefinition{Name: "container", ResolveChildTools: func(context.Context, ToolExecutionContext) (ChildToolResolution, error) {
				return ChildToolResolution{Tools: []ToolDefinition{leaf}, Deadline: resolvedDeadline}, nil
			}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				checkDeadline(ctx, "parent_execute")
				// A child caller must inherit the parent limit even when its caller
				// offers a fresh context without a deadline.
				child := in.ChildCaller.Call(context.Background(), ToolCall{Name: "leaf"})
				return child.Result, child.Err
			}}
			permissions := 0
			out := RunToolCall(ctx, ToolCall{Name: parent.Name}, RunToolCallOptions{Tools: []ToolDefinition{parent},
				CheckToolPermission: func(ctx context.Context, in BeforeToolCallContext) error {
					permissions++
					if permissions > 1 {
						checkDeadline(ctx, "permission")
					}
					return nil
				}, AfterToolCall: func(ctx context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
					checkDeadline(ctx, "after_hook")
					return AfterToolCallResult{}, nil
				}})
			if out.Err != nil || len(phases) != 7 {
				t.Fatalf("remaining lifecycle not completed: phases=%v outcome=%+v", phases, out)
			}
		})
	}
}

func TestChildResolverDeadlineRejectsLatePermissionSuccess(t *testing.T) {
	deadline := time.Now().Add(100 * time.Millisecond)
	entered, permissions := false, 0
	tool := ToolDefinition{Name: "container", ResolveChildTools: func(context.Context, ToolExecutionContext) (ChildToolResolution, error) {
		return ChildToolResolution{Deadline: deadline}, nil
	}, Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		entered = true
		return ToolResult{}, nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	out := RunToolCall(ctx, ToolCall{Name: tool.Name}, RunToolCallOptions{Tools: []ToolDefinition{tool},
		CheckToolPermission: func(ctx context.Context, _ BeforeToolCallContext) error {
			permissions++
			if permissions == 2 {
				<-ctx.Done()
				return nil
			}
			return nil
		}})
	if entered || permissions != 2 || !errors.Is(out.Err, context.DeadlineExceeded) || out.Failure.Code != ToolFailureDeadline || out.Execution.Local != ToolLocalNotStarted || out.Execution.Remote != ToolRemoteNotDispatched {
		t.Fatalf("late permission success entered parent: permissions=%d entered=%v outcome=%+v", permissions, entered, out)
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

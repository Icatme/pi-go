package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/internal/jsontext"
)

func TestToolArgumentBudgetBoundaries(t *testing.T) {
	for _, limit := range []int{8192, 32 << 10} {
		for _, size := range []int{limit, limit + 1} {
			calls, parses := 0, 0
			raw := json.RawMessage(`{"value":"` + strings.Repeat("x", size-12) + `"}`)
			tool := ToolDefinition{Name: "write", MaxArgumentBytes: limit,
				ParseArguments: func(call ToolCall) (any, error) { parses++; return call.Arguments, nil },
				Execute:        func(context.Context, ToolExecutionContext) (ToolResult, error) { calls++; return ToolResult{}, nil }}
			out := RunToolCall(t.Context(), ToolCall{Name: tool.Name, Arguments: raw}, RunToolCallOptions{Tools: []ToolDefinition{tool}})
			if size == limit {
				if out.Err != nil || calls != 1 || parses != 1 {
					t.Fatalf("limit %d: %+v", limit, out)
				}
			} else {
				assertArgumentBudgetRejected(t, out)
				if calls != 0 || parses != 0 {
					t.Fatal("oversized input reached parser/executor")
				}
			}
		}
	}
}

func TestToolArgumentBudgetCoversEveryRepresentation(t *testing.T) {
	for _, mode := range []string{"parsed", "raw-with-parsed", "parser", "parser-error", "hook", "blocking-hook", "escaping"} {
		t.Run(mode, func(t *testing.T) {
			cause := errors.New("parse failed")
			calls, hooks, gates := 0, 0, 0
			tool := ToolDefinition{Name: "write", MaxArgumentBytes: 32,
				Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) { calls++; return ToolResult{}, nil }}
			call := ToolCall{Name: tool.Name, Arguments: json.RawMessage(`{"value":"ok"}`)}
			large := map[string]any{"value": strings.Repeat("x", 64)}
			switch mode {
			case "parsed":
				call.ParsedArgs = large
			case "raw-with-parsed":
				call.Arguments = json.RawMessage(strings.Repeat(" ", 64) + `{}`)
				call.ParsedArgs = map[string]any{"value": "ok"}
			case "parser", "parser-error":
				tool.ParseArguments = func(ToolCall) (any, error) {
					if mode == "parser-error" {
						return large, cause
					}
					return large, nil
				}
			case "escaping":
				call.ParsedArgs = map[string]any{"value": strings.Repeat("\x00", 5)}
			}
			out := RunToolCall(t.Context(), call, RunToolCallOptions{Tools: []ToolDefinition{tool},
				BeforeToolCall: func(_ context.Context, in BeforeToolCallContext) (BeforeToolCallResult, error) {
					hooks++
					if mode == "hook" || mode == "blocking-hook" {
						in.Args.(map[string]any)["value"] = large["value"]
					}
					return BeforeToolCallResult{Block: mode == "blocking-hook"}, nil
				}, ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
					gates++
					return ToolGateResult{Action: ToolGateActionSuspend}, nil
				}})
			assertArgumentBudgetRejected(t, out)
			if mode == "parser-error" && !errors.Is(out.Err, cause) {
				t.Fatal("parser failure cause lost")
			}
			if calls != 0 || gates != 0 {
				t.Fatal("rejected arguments reached approval/execution")
			}
			if mode != "hook" && mode != "blocking-hook" && hooks != 0 {
				t.Fatal("rejected arguments reached hook")
			}
		})
	}
}

func TestToolArgumentBudgetChildLimitsRemainIndependent(t *testing.T) {
	for _, limits := range [][2]int{{64, 1024}, {1024, 64}} {
		calls := 0
		leaf := ToolDefinition{Name: "leaf", MaxArgumentBytes: limits[0], Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) { calls++; return ToolResult{}, nil }}
		parent := ToolDefinition{Name: "container", ChildTools: []ToolDefinition{leaf}, ChildLimits: ChildCallLimits{MaxArgumentBytes: limits[1]},
			Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				out := in.ChildCaller.Call(ctx, ToolCall{Name: "leaf", Arguments: json.RawMessage(`{"v":"` + strings.Repeat("x", 128) + `"}`)})
				if out.Err == nil || out.Execution.Local != ToolLocalNotStarted {
					t.Fatalf("child limit bypass: %+v", out)
				}
				return ToolResult{}, nil
			}}
		out := RunToolCall(t.Context(), ToolCall{Name: parent.Name}, RunToolCallOptions{Tools: []ToolDefinition{parent}})
		if out.Err != nil || calls != 0 {
			t.Fatalf("parent=%+v calls=%d", out, calls)
		}
	}
}

func TestToolArgumentBudgetModelBatchAndDefinitionValidation(t *testing.T) {
	if _, err := NewRunner(AgentDefinition{Tools: []ToolDefinition{{Name: "bad", MaxArgumentBytes: -1}}}); err == nil {
		t.Fatal("negative limit accepted")
	}
	calls, models := 0, 0
	definition := AgentDefinition{Tools: []ToolDefinition{{Name: "write", MaxArgumentBytes: 8, Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) { calls++; return ToolResult{}, nil }}},
		Model: StreamFunc(func(_ context.Context, r ModelRequest) (AssistantStream, error) {
			models++
			if models == 1 {
				return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonToolUse, ToolCalls: []ToolCall{{ID: "call", Name: "write", Arguments: json.RawMessage(`{"value":"large"}`)}}}, nil), nil
			}
			last := r.Messages[len(r.Messages)-1]
			if last.ToolResult == nil || !last.ToolResult.IsError || last.ToolResult.Failure.Code != ToolFailureResource {
				t.Fatalf("missing rejection receipt: %+v", last)
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
		})}
	_, err := NewEngine().Run(t.Context(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("test")}, nil)
	if err != nil || calls != 0 || models != 2 {
		t.Fatal(err, calls, models)
	}
}

func assertArgumentBudgetRejected(t *testing.T, out ToolCallOutcome) {
	t.Helper()
	if out.Err == nil || out.Failure == nil || out.Failure.Code != ToolFailureResource || out.Failure.Reason != "argument_bytes" || out.Execution.Local != ToolLocalNotStarted || out.Execution.Remote != ToolRemoteNotDispatched {
		t.Fatalf("unexpected budget outcome: %+v", out)
	}
}

func TestToolArgumentBudgetPreservesUnicodeRejectionCauses(t *testing.T) {
	for _, stage := range []string{"parser", "hook"} {
		for _, path := range []string{"direct", "child"} {
			t.Run(stage+"/"+path, func(t *testing.T) {
				cause := errors.New("host preparation failed")
				calls, gates := 0, 0
				tool := ToolDefinition{Name: "write", MaxArgumentBytes: 1024,
					Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
						calls++
						return ToolResult{}, nil
					}}
				invalid := string([]byte{0xff})
				if stage == "parser" {
					tool.ParseArguments = func(ToolCall) (any, error) {
						return map[string]any{"value": invalid}, cause
					}
				}
				options := RunToolCallOptions{Tools: []ToolDefinition{tool},
					BeforeToolCall: func(_ context.Context, in BeforeToolCallContext) (BeforeToolCallResult, error) {
						if stage == "hook" && in.ToolCall.Name == tool.Name {
							in.Args.(map[string]any)["value"] = invalid
							return BeforeToolCallResult{}, cause
						}
						return BeforeToolCallResult{}, nil
					},
					ToolGate: func(_ context.Context, in BeforeToolCallContext) (ToolGateResult, error) {
						if in.ToolCall.Name == tool.Name {
							gates++
						}
						return ToolGateResult{Action: ToolGateActionAllow}, nil
					}}
				call := ToolCall{Name: tool.Name, Arguments: json.RawMessage(`{"value":"ok"}`)}
				var out ToolCallOutcome
				if path == "child" {
					parent := ToolDefinition{Name: "parent", ChildTools: []ToolDefinition{tool},
						Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
							out = in.ChildCaller.Call(ctx, call)
							return ToolResult{}, nil
						}}
					options.Tools = []ToolDefinition{parent}
					if result := RunToolCall(t.Context(), ToolCall{Name: parent.Name}, options); result.Err != nil {
						t.Fatal(result.Err)
					}
				} else {
					out = RunToolCall(t.Context(), call, options)
				}
				if !errors.Is(out.Err, cause) || !errors.Is(out.Err, jsontext.ErrInvalidUnicode) {
					t.Errorf("lost preparation or Unicode cause: %v", out.Err)
				}
				if out.Err != nil && strings.Contains(out.Err.Error(), cause.Error()) {
					t.Errorf("host error leaked into presentation: %v", out.Err)
				}
				if out.Failure == nil || out.Failure.Code != ToolFailureArgumentInvalid || out.Failure.Reason != "invalid_unicode" ||
					out.Execution.Local != ToolLocalNotStarted || out.Execution.Remote != ToolRemoteNotDispatched || calls != 0 || gates != 0 {
					t.Fatalf("invalid arguments reached approval/execution: calls=%d gates=%d outcome=%+v", calls, gates, out)
				}
			})
		}
	}
}

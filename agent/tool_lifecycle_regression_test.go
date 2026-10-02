package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/Icatme/pi-go/internal/jsontext"
)

func TestExecutorPanicCompletesChildLifecycleAndRedacts(t *testing.T) {
	secret := errors.New("private executor panic")
	for _, test := range []struct {
		name  string
		value any
	}{
		{"string", "private executor panic"},
		{"error", secret},
	} {
		t.Run(test.name, func(t *testing.T) {
			var child ToolCallOutcome
			var escaped any
			hooks := 0
			parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				panic(test.value)
			}}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				// Keep the regression executable against the old implementation:
				// an escaped panic must be reported as a test failure, not a crash.
				func() {
					defer func() { escaped = recover() }()
					child = in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
				}()
				return ToolResult{}, nil
			}}
			out := RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent},
				AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
					if in.ParentToolCallID == "" {
						return AfterToolCallResult{}, nil
					}
					hooks++
					if in.Err == nil || in.Failure == nil || in.Failure.Code != ToolFailureProtocol || in.Failure.Reason != "executor_panic" || in.Execution.Local != ToolLocalReturned || in.Execution.Remote != ToolRemoteUnknown {
						t.Fatalf("panic facts missing: %+v", in)
					}
					if test.name == "error" && !errors.Is(in.Err, secret) {
						t.Fatal("original panic error chain lost")
					}
					return AfterToolCallResult{Result: &ToolResult{Content: []Part{{Type: PartTypeText, Text: "redacted"}}}}, nil
				}})
			if escaped != nil || hooks != 1 || child.Err == nil || child.Result.Content[0].Text != "redacted" {
				t.Fatalf("escaped=%v hooks=%d child=%+v", escaped, hooks, child)
			}
			if strings.Contains(child.Err.Error(), "private") {
				t.Fatal("default panic presentation contains private value")
			}
			if test.name == "error" && !errors.Is(child.Err, secret) {
				t.Fatal("original panic error missing from final outcome")
			}
			report := out.Result.ChildCalls
			if report == nil || !report.Closed || report.Active != 0 || len(report.Calls) != 1 || !report.Calls[0].Completed || report.Calls[0].Execution.Local != ToolLocalReturned || report.Calls[0].Failure == nil {
				t.Fatalf("incomplete panic ledger: %+v", report)
			}
			if report.Details[0].Summary != "redacted" || strings.Contains(report.Details[0].Error, "private") {
				t.Fatalf("unsafe panic details: %+v", report.Details)
			}
		})
	}
}

func TestExecutorPanicClosesContainerScope(t *testing.T) {
	var caller ChildCaller
	hooks := 0
	parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		t.Fatal("closed scope executed a leaf")
		return ToolResult{}, nil
	}}}, Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
		caller = in.ChildCaller
		panic("private parent panic")
	}}
	var out ToolCallOutcome
	var escaped any
	func() {
		defer func() { escaped = recover() }()
		out = RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent},
			AfterToolCall: func(context.Context, AfterToolCallContext) (AfterToolCallResult, error) {
				hooks++
				return AfterToolCallResult{}, nil
			}})
	}()
	if escaped != nil || out.Err == nil || hooks != 1 || out.Result.ChildCalls == nil || !out.Result.ChildCalls.Closed {
		t.Fatalf("escaped=%v hooks=%d out=%+v", escaped, hooks, out)
	}
	if denied := caller.Call(t.Context(), ToolCall{Name: "leaf"}); denied.Err == nil || denied.Execution.Local != ToolLocalNotStarted {
		t.Fatalf("closed capability admitted call: %+v", denied)
	}
}

func TestGenericErrorPreservesTrustedRemoteFacts(t *testing.T) {
	cause := errors.New("local mapping failed")
	for _, remote := range []ToolRemoteState{ToolRemoteCompleteReported, ToolRemoteInputRequired, ToolRemoteNotDispatched, ToolRemoteNotApplicable, ToolRemoteUnknown} {
		t.Run(string(remote), func(t *testing.T) {
			tool := ToolDefinition{Name: "remote", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				return ToolResult{Execution: &ToolExecutionInfo{Remote: remote, Attempts: []ToolSendAttempt{{Number: 1, Remote: remote}}}}, cause
			}}
			out := RunToolCall(t.Context(), ToolCall{Name: "remote"}, RunToolCallOptions{Tools: []ToolDefinition{tool},
				AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
					if in.Execution.Remote != remote || !errors.Is(in.Err, cause) {
						t.Fatalf("hook facts changed: %+v", in)
					}
					return AfterToolCallResult{}, nil
				}})
			if !errors.Is(out.Err, cause) || out.Execution.Remote != remote || out.Execution.Local != ToolLocalReturned || len(out.Execution.Attempts) != 1 || out.Execution.Attempts[0].Remote != remote || out.Failure.Code != ToolFailureProtocol {
				t.Fatalf("trusted facts lost: %+v", out)
			}
		})
	}
}

type budgetArgument struct {
	Value    string
	marshals *atomic.Int32
}

func (a budgetArgument) MarshalJSON() ([]byte, error) {
	a.marshals.Add(1)
	return json.Marshal(map[string]any{"value": a.Value})
}

func TestChildArgumentBudgetAtFinalPreparation(t *testing.T) {
	for _, stage := range []string{"parse", "parse_error", "coercion", "before", "before_block", "before_error", "decoded"} {
		t.Run(stage, func(t *testing.T) {
			var marshals atomic.Int32
			var bodies, gates, permissions int
			cause := errors.New("custom preparation error")
			leaf := ToolDefinition{Name: "leaf", Parameters: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}},
				Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
					bodies++
					return ToolResult{}, nil
				}}
			call := ToolCall{Name: "leaf", Arguments: json.RawMessage(`{"value":"small"}`)}
			if strings.HasPrefix(stage, "parse") {
				leaf.ParseArguments = func(ToolCall) (any, error) {
					args := budgetArgument{Value: strings.Repeat("x", 4096), marshals: &marshals}
					if stage == "parse_error" {
						return args, cause
					}
					return args, nil
				}
			}
			if stage == "coercion" {
				call.Arguments = nil
				call.ParsedArgs = map[string]any{"value": float64(1e300)}
			}
			if stage == "decoded" {
				call.Arguments = json.RawMessage(`{"a":1,"b":1,"c":1,"d":1,"e":1,"f":1}`)
			}
			var child ToolCallOutcome
			parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{leaf}, ChildLimits: ChildCallLimits{MaxArgumentBytes: 128},
				Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
					child = in.ChildCaller.Call(ctx, call)
					return ToolResult{}, nil
				}}
			out := RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent},
				BeforeToolCall: func(_ context.Context, in BeforeToolCallContext) (BeforeToolCallResult, error) {
					if in.ParentToolCallID != "" && strings.HasPrefix(stage, "before") {
						in.Args.(map[string]any)["value"] = strings.Repeat("x", 4096)
						if stage == "before_block" {
							return BeforeToolCallResult{Block: true}, nil
						}
						if stage == "before_error" {
							return BeforeToolCallResult{}, cause
						}
					}
					return BeforeToolCallResult{}, nil
				},
				ToolGate: func(_ context.Context, in BeforeToolCallContext) (ToolGateResult, error) {
					if in.ParentToolCallID != "" {
						gates++
					}
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				},
				CheckToolPermission: func(_ context.Context, in BeforeToolCallContext) error {
					if in.ParentToolCallID != "" {
						permissions++
					}
					return nil
				}})
			if out.Err != nil || child.Err == nil || child.Failure.Code != ToolFailureResource || bodies != 0 || gates != 0 || permissions != 0 || marshals.Load() != 0 || child.Execution.Remote != ToolRemoteNotDispatched || child.Execution.Local != ToolLocalNotStarted {
				t.Fatalf("stage=%s bodies=%d gates=%d permissions=%d marshals=%d child=%+v parent=%+v", stage, bodies, gates, permissions, marshals.Load(), child, out)
			}
			if strings.HasSuffix(stage, "error") && !errors.Is(child.Err, cause) {
				t.Fatal("budget rejection discarded preparation error chain")
			}
			if report := out.Result.ChildCalls; report == nil || !report.Calls[0].Completed || report.Calls[0].Failure.Code != ToolFailureResource {
				t.Fatalf("budget failure absent from ledger: %+v", report)
			}
		})
	}
}

func TestChildErrorPartialResultRespectsConfiguredBudget(t *testing.T) {
	cause := errors.New("mapping failed")
	for _, kind := range []string{"text", "structured", "details"} {
		t.Run(kind, func(t *testing.T) {
			partial := ToolResult{Execution: &ToolExecutionInfo{Remote: ToolRemoteCompleteReported}}
			switch kind {
			case "text":
				partial.Content = []Part{{Type: PartTypeText, Text: strings.Repeat("x", 4096)}}
			case "structured":
				partial.StructuredContent = json.RawMessage(`{"value":"` + strings.Repeat("x", 4096) + `"}`)
			case "details":
				partial.Details = map[string]any{"value": strings.Repeat("x", 4096)}
			}
			var child ToolCallOutcome
			parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				return partial, cause
			}}}, ChildLimits: ChildCallLimits{MaxResultBytes: 128}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				child = in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
				return ToolResult{}, nil
			}}
			out := RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent},
				AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
					if in.ParentToolCallID != "" && (!errors.Is(in.Err, cause) || in.Failure.Code != ToolFailureResource || in.Execution.Remote != ToolRemoteCompleteReported || len(in.Result.StructuredContent) != 0 || in.Result.Details != nil || resultSummaryBytes(in.Result) > 128) {
						t.Fatalf("oversize partial reached hook: %+v", in)
					}
					return AfterToolCallResult{}, nil
				}})
			if out.Err != nil || !errors.Is(child.Err, cause) || child.Failure.Code != ToolFailureResource || child.Execution.Remote != ToolRemoteCompleteReported || len(child.Result.StructuredContent) != 0 || child.Result.Details != nil || resultSummaryBytes(child.Result) > 128 {
				t.Fatalf("partial budget failed: %+v", child)
			}
			if !out.Result.ChildCalls.Calls[0].Completed || out.Result.ChildCalls.Calls[0].Failure.Code != ToolFailureResource {
				t.Fatal(out.Result.ChildCalls)
			}
		})
	}
}

func TestChildEndEventRespectsConfiguredSummaryBudget(t *testing.T) {
	text := strings.Repeat("界", 4096)
	var child ToolCallOutcome
	parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: text}}}, nil
	}}}, ChildLimits: ChildCallLimits{MaxSummaryBytes: 128}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		child = in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
		return ToolResult{}, nil
	}}
	definition := AgentDefinition{Tools: []ToolDefinition{parent}}
	engine := NewEngine()
	prepared, _, err := engine.prepareToolInvocation(t.Context(), definition, Message{}, AgentContext{}, map[string]ToolDefinition{"code": parent}, ToolCall{ID: "outer", Name: "code"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var end AgentEvent
	out, err := engine.executePreparedTool(t.Context(), definition, Message{}, prepared, func(event AgentEvent) {
		if event.Type == EventToolExecutionEnd && event.ParentToolCallID != "" {
			end = event
		}
	})
	if err != nil || child.Err != nil || child.Result.Content[0].Text != text {
		t.Fatalf("Go result was truncated: child=%+v err=%v", child, err)
	}
	if end.ToolResult == nil || resultSummaryBytes(*end.ToolResult) > 128 || !utf8.ValidString(end.ToolResult.Content[0].Text) {
		t.Fatalf("summary exceeds configured budget: %+v", end)
	}
	if out.result.ChildCalls == nil || end.ToolResult.Content[0].Text != out.result.ChildCalls.Details[0].Summary {
		t.Fatalf("event and detail bounds disagree: %+v", out.result.ChildCalls)
	}
}

func TestGeneratedChildFailureTextRespectsResultBudget(t *testing.T) {
	cause := errors.New(strings.Repeat("private-error-", 4096))
	for _, stage := range []string{"execute", "before", "gate", "permission", "after", "validate"} {
		t.Run(stage, func(t *testing.T) {
			var child ToolCallOutcome
			var hooks int
			leaf := ToolDefinition{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				if stage == "execute" {
					return ToolResult{}, cause
				}
				return ToolResult{}, nil
			}}
			if stage == "validate" {
				leaf.ValidateResult = func(ToolResult) error { return cause }
			}
			parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{leaf}, ChildLimits: ChildCallLimits{MaxResultBytes: 128},
				Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
					child = in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
					return ToolResult{}, nil
				}}
			out := RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent},
				BeforeToolCall: func(_ context.Context, in BeforeToolCallContext) (BeforeToolCallResult, error) {
					if in.ParentToolCallID != "" && stage == "before" {
						return BeforeToolCallResult{}, cause
					}
					return BeforeToolCallResult{}, nil
				},
				ToolGate: func(_ context.Context, in BeforeToolCallContext) (ToolGateResult, error) {
					if in.ParentToolCallID != "" && stage == "gate" {
						return ToolGateResult{}, cause
					}
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				},
				CheckToolPermission: func(_ context.Context, in BeforeToolCallContext) error {
					if in.ParentToolCallID != "" && stage == "permission" {
						return cause
					}
					return nil
				},
				AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
					if in.ParentToolCallID == "" {
						return AfterToolCallResult{}, nil
					}
					hooks++
					if resultSummaryBytes(in.Result) > 128 {
						t.Fatalf("unbounded generated failure reached after hook: %d bytes", resultSummaryBytes(in.Result))
					}
					if stage == "after" {
						return AfterToolCallResult{}, cause
					}
					return AfterToolCallResult{}, nil
				}})
			expected := ToolFailureHook
			remote := ToolRemoteNotApplicable
			switch stage {
			case "execute":
				expected, remote = ToolFailureProtocol, ToolRemoteUnknown
			case "before", "gate":
				remote = ToolRemoteNotDispatched
			case "permission":
				expected, remote = ToolFailurePolicyDenied, ToolRemoteNotDispatched
			case "validate":
				expected = ToolFailureResultRejected
			}
			if out.Err != nil || !errors.Is(child.Err, cause) || !child.IsError || child.Failure.Code != expected || child.Execution.Remote != remote || resultSummaryBytes(child.Result) > 128 {
				t.Fatalf("stage=%s code=%v remote=%s text_bytes=%d original_error=%v", stage, child.Failure, child.Execution.Remote, resultSummaryBytes(child.Result), errors.Is(child.Err, cause))
			}
			if record := out.Result.ChildCalls.Calls[0]; !record.Completed || record.Failure.Code != expected || !errors.Is(record.Err, cause) {
				t.Fatalf("generated failure ledger lost original error: completed=%v failure=%v", record.Completed, record.Failure)
			}
			if (stage == "execute" || stage == "after" || stage == "validate") && hooks != 1 {
				t.Fatalf("after hook calls=%d", hooks)
			}
		})
	}
}

func TestAfterHookPanicCompletesChildAndKeepsExecutionFacts(t *testing.T) {
	hookCause := errors.New("private after hook panic")
	executionCause := errors.New("private mapping error")
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed_write", true: "write_then_mapping_failure"}[failed], func(t *testing.T) {
			var writes, hooks int
			var child ToolCallOutcome
			parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "write", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				writes++
				result := ToolResult{Content: []Part{{Type: PartTypeText, Text: "private completed output"}}, Execution: &ToolExecutionInfo{Remote: ToolRemoteCompleteReported}}
				if failed {
					return result, executionCause
				}
				return result, nil
			}}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				child = in.ChildCaller.Call(ctx, ToolCall{Name: "write"})
				return ToolResult{}, nil
			}}
			definition := AgentDefinition{Tools: []ToolDefinition{parent}, AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
				if in.ParentToolCallID != "" {
					hooks++
					panic(hookCause)
				}
				return AfterToolCallResult{}, nil
			}}
			engine := NewEngine()
			prepared, _, err := engine.prepareToolInvocation(t.Context(), definition, Message{}, AgentContext{}, map[string]ToolDefinition{"code": parent}, ToolCall{ID: "outer", Name: "code"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var events []AgentEvent
			out, err := engine.executePreparedTool(t.Context(), definition, Message{}, prepared, func(event AgentEvent) { events = append(events, event) })
			if err != nil || writes != 1 || hooks != 1 || !errors.Is(child.Err, hookCause) || child.Failure == nil || child.Failure.Code != ToolFailureHook || child.Failure.Reason != "after_hook_panic" || child.Execution.Local != ToolLocalReturned || child.Execution.Remote != ToolRemoteCompleteReported {
				t.Fatalf("writes=%d hooks=%d child_failure=%v execution=%+v err=%v", writes, hooks, child.Failure, child.Execution, err)
			}
			if failed && !errors.Is(child.Err, executionCause) {
				t.Fatal("hook panic discarded prior execution error")
			}
			if strings.Contains(child.Err.Error(), "private") || strings.Contains(resultTextSummary(child.Result, 8192), "private") {
				t.Fatal("hook panic exposed private error or output")
			}
			report := out.result.ChildCalls
			if report == nil || !report.Closed || report.Active != 0 || !report.Calls[0].Completed || report.Calls[0].Failure.Code != ToolFailureHook || report.Calls[0].Execution.Remote != ToolRemoteCompleteReported {
				t.Fatal("hook panic left incomplete ledger", report)
			}
			for _, event := range events {
				data, marshalErr := json.Marshal(event)
				if marshalErr != nil || strings.Contains(string(data), "private") {
					t.Fatalf("unsafe default event: %s error=%v", data, marshalErr)
				}
			}
		})
	}
}

func TestToolArgumentsRejectUnicodeBeforeReplacement(t *testing.T) {
	invalid := string([]byte{0xff})
	for _, call := range []ToolCall{
		{Name: "leaf", Arguments: json.RawMessage(`{"code":"abc\ud800"}`)},
		{Name: "leaf", Arguments: json.RawMessage(`{"\udfff":"code"}`)},
		{Name: "leaf", Arguments: json.RawMessage("{\"code\":\"" + invalid + "\"}")},
		{Name: "leaf", ParsedArgs: map[string]any{"code": invalid}},
		{Name: "leaf", ParsedArgs: map[string]any{invalid: "code"}},
	} {
		bodies := 0
		out := RunToolCall(t.Context(), call, RunToolCallOptions{Tools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
			bodies++
			return ToolResult{}, nil
		}}}})
		if bodies != 0 || out.Err == nil || out.Failure.Code != ToolFailureArgumentInvalid || out.Failure.Reason != "invalid_unicode" || out.Execution.Local != ToolLocalNotStarted || out.Execution.Remote != ToolRemoteNotDispatched || !errors.Is(out.Err, jsontext.ErrInvalidUnicode) {
			t.Fatalf("Unicode was replaced before boundary: bodies=%d failure=%v execution=%+v", bodies, out.Failure, out.Execution)
		}
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"code":"emoji \ud83d\ude00"}`),
		json.RawMessage(`{"code":"explicit \ufffd"}`),
		json.RawMessage(`{"code":"const id = 'abc\\ud800';"}`),
		json.RawMessage(`{"code":"😀 �"}`),
	} {
		bodies := 0
		out := RunToolCall(t.Context(), ToolCall{Name: "leaf", Arguments: raw}, RunToolCallOptions{Tools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
			bodies++
			return ToolResult{}, nil
		}}}})
		if out.Err != nil || bodies != 1 {
			t.Fatalf("valid Unicode rejected: %s error=%v", raw, out.Err)
		}
	}
}

func TestToolCallUnmarshalRejectsUnicodeAndPreservesExactNumbers(t *testing.T) {
	for _, raw := range []string{
		`{"name":"leaf","parsed_args":{"id":"abc\ud800"}}`,
		`{"name":"leaf","arguments":{"\udfff":"id"}}`,
		"{\"name\":\"leaf\",\"parsed_args\":{\"id\":\"" + string([]byte{0xff}) + "\"}}",
	} {
		var call ToolCall
		err := json.Unmarshal([]byte(raw), &call)
		var typed *ToolExecutionError
		if !errors.As(err, &typed) || typed.Code != ToolFailureArgumentInvalid || typed.Reason != "invalid_unicode" || typed.Execution.Local != ToolLocalNotStarted || typed.Execution.Remote != ToolRemoteNotDispatched || !errors.Is(err, jsontext.ErrInvalidUnicode) {
			t.Fatalf("raw tool call silently repaired: err=%v", err)
		}
	}
	var call ToolCall
	err := json.Unmarshal([]byte(`{"name":"leaf","parsed_args":{"id":9007199254740993,"emoji":"\ud83d\ude00","replacement":"\ufffd","code":"const id='abc\\ud800';"}}`), &call)
	if err != nil || call.ParsedArgs["id"] != json.Number("9007199254740993") || call.ParsedArgs["emoji"] != "😀" || call.ParsedArgs["replacement"] != "�" || call.ParsedArgs["code"] != `const id='abc\ud800';` {
		t.Fatalf("valid tool call changed: call=%+v err=%v", call, err)
	}
}

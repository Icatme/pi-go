package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestExecutionOutcomePreservesErrorPartialAndRemoteFacts(t *testing.T) {
	cause := errors.New("mapping failed")
	tool := ToolDefinition{Name: "remote", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "bounded partial"}}}, &ToolExecutionError{
			Code: ToolFailureResultRejected, Reason: "unsafe_number", Err: cause,
			Execution: ToolExecutionInfo{Remote: ToolRemoteCompleteReported, Attempts: []ToolSendAttempt{{Number: 1, Remote: ToolRemoteCompleteReported}}}}
	}}
	out := RunToolCall(t.Context(), ToolCall{ID: "call", Name: "remote"}, RunToolCallOptions{Tools: []ToolDefinition{tool},
		AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
			if !errors.Is(in.Err, cause) || in.Execution.Remote != ToolRemoteCompleteReported {
				t.Fatal(in)
			}
			clear := false
			return AfterToolCallResult{IsError: &clear, Result: &ToolResult{Execution: &ToolExecutionInfo{Remote: ToolRemoteNotDispatched}}}, nil
		}})
	if !out.IsError || !errors.Is(out.Err, cause) || out.Failure.Code != ToolFailureResultRejected || out.Failure.Reason != "unsafe_number" || out.Execution.Local != ToolLocalReturned || out.Execution.Remote != ToolRemoteCompleteReported || out.Result.Content[0].Text != "bounded partial" {
		t.Fatalf("outcome=%+v", out)
	}
	message := NewToolResultMessage(out.ToolCall, out.Result, out.IsError)
	encoded, err := json.Marshal(message)
	if err != nil || !strings.Contains(string(encoded), `"remote":"complete_reported"`) || !strings.Contains(string(encoded), `"reason":"unsafe_number"`) {
		t.Fatalf("message=%s err=%v", encoded, err)
	}
	out.Execution.Attempts[0].Number = 99
	if message.ToolResult.Execution.Attempts[0].Number != 1 {
		t.Fatal("execution facts alias caller")
	}
}

func TestExecutionHookFailureRedactsRawOutputAndKeepsErrorChain(t *testing.T) {
	original := errors.New("secret transport detail")
	hook := errors.New("output withheld")
	out := RunToolCall(t.Context(), ToolCall{Name: "remote"}, RunToolCallOptions{Tools: []ToolDefinition{{Name: "remote", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "private payload"}}, StructuredContent: json.RawMessage(`{"secret":"value"}`)}, original
	}}}, AfterToolCall: func(context.Context, AfterToolCallContext) (AfterToolCallResult, error) {
		return AfterToolCallResult{}, hook
	}})
	if !errors.Is(out.Err, original) || !errors.Is(out.Err, hook) || out.Failure.Code != ToolFailureHook || out.Execution.Remote != ToolRemoteUnknown || out.Result.Content[0].Text != hook.Error() || len(out.Result.StructuredContent) != 0 {
		t.Fatalf("outcome=%+v", out)
	}
	if strings.Contains(out.Err.Error(), "secret") {
		t.Fatal("safe presentation exposed original error")
	}
}

func TestExplicitResultValidatorRunsAfterHooksForDirectBusinessErrors(t *testing.T) {
	var order []string
	tool := ToolDefinition{Name: "remote", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "before"}}, IsError: true,
			Execution: &ToolExecutionInfo{Remote: ToolRemoteCompleteReported}}, nil
	}, ValidateResult: func(result ToolResult) error {
		order = append(order, "validate")
		if !result.IsError || result.Content[0].Text != "after" || result.Execution.Remote != ToolRemoteCompleteReported {
			t.Fatal(result)
		}
		return &ToolExecutionError{Code: ToolFailureResultRejected, Reason: "unsafe_number", Err: errors.New("result rejected")}
	}}
	out := RunToolCall(t.Context(), ToolCall{Name: "remote"}, RunToolCallOptions{Tools: []ToolDefinition{tool},
		AfterToolCall: func(context.Context, AfterToolCallContext) (AfterToolCallResult, error) {
			order = append(order, "after")
			return AfterToolCallResult{Result: &ToolResult{Content: []Part{{Type: PartTypeText, Text: "after"}}}}, nil
		}})
	if out.Err == nil || out.Failure.Code != ToolFailureResultRejected || out.Failure.Reason != "unsafe_number" || out.Execution.Remote != ToolRemoteCompleteReported || strings.Join(order, ",") != "after,validate" {
		t.Fatalf("out=%+v order=%v", out, order)
	}
}

func TestAfterHookBusinessFailureTracksEffectiveIsError(t *testing.T) {
	for _, initial := range []bool{false, true} {
		tool := ToolDefinition{Name: "business", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
			return ToolResult{IsError: initial, Execution: &ToolExecutionInfo{Remote: ToolRemoteCompleteReported}}, nil
		}}
		effective := !initial
		out := RunToolCall(t.Context(), ToolCall{Name: "business"}, RunToolCallOptions{Tools: []ToolDefinition{tool}, AfterToolCall: func(context.Context, AfterToolCallContext) (AfterToolCallResult, error) {
			return AfterToolCallResult{IsError: &effective}, nil
		}})
		if out.Err != nil || out.IsError != effective || out.Execution.Remote != ToolRemoteCompleteReported {
			t.Fatal(out)
		}
		if effective && (out.Failure == nil || out.Failure.Code != ToolFailureToolReportedError) {
			t.Fatal("new business failure absent", out)
		}
		if !effective && out.Failure != nil {
			t.Fatal("cleared business failure retained", out)
		}
	}
}

func TestExecutionPreflightClassificationAndFinalPermission(t *testing.T) {
	var bodies atomic.Int32
	tool := ToolDefinition{Name: "leaf", Parameters: map[string]any{"type": "object", "required": []any{"value"}}, Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		bodies.Add(1)
		return ToolResult{}, nil
	}}
	invalid := RunToolCall(t.Context(), ToolCall{Name: "leaf"}, RunToolCallOptions{Tools: []ToolDefinition{tool}})
	if invalid.Failure.Code != ToolFailureArgumentInvalid || invalid.Execution.Remote != ToolRemoteNotDispatched || invalid.Execution.Local != ToolLocalNotStarted {
		t.Fatal(invalid)
	}
	var order []string
	out := RunToolCall(t.Context(), ToolCall{Name: "leaf", Arguments: json.RawMessage(`{"value":1}`)}, RunToolCallOptions{Tools: []ToolDefinition{tool},
		ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
			order = append(order, "gate")
			return ToolGateResult{Action: ToolGateActionAllow}, nil
		},
		CheckToolPermission: func(context.Context, BeforeToolCallContext) error {
			order = append(order, "permission")
			return errors.New("revoked")
		}})
	if out.Failure.Code != ToolFailurePolicyDenied || bodies.Load() != 0 || strings.Join(order, ",") != "gate,permission" {
		t.Fatalf("out=%+v bodies=%d order=%v", out, bodies.Load(), order)
	}
}

func TestAgentOptionsPassFinalPermissionIntoExecution(t *testing.T) {
	var bodies, checks atomic.Int32
	requests := 0
	options := AgentOptions{PolicyRevision: "policy-2", InitialState: AgentInitialState{Tools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		bodies.Add(1)
		return ToolResult{}, nil
	}}}},
		Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
			requests++
			if requests == 1 {
				return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonToolUse, ToolCalls: []ToolCall{{ID: "call", Name: "leaf"}}}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
		}), CheckToolPermission: func(context.Context, BeforeToolCallContext) error {
			checks.Add(1)
			return errors.New("permission revoked")
		}}
	definition, _ := options.build()
	if definition.PolicyRevision != "policy-2" {
		t.Fatal("policy revision was lost")
	}
	runtime, err := NewAgentWithOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.PromptText(t.Context(), "run"); err != nil {
		t.Fatal(err)
	}
	if bodies.Load() != 0 || checks.Load() != 1 {
		t.Fatalf("bodies=%d checks=%d", bodies.Load(), checks.Load())
	}
	var payload *ToolResultPayload
	for _, message := range runtime.Snapshot().Messages {
		if message.Role == RoleTool {
			payload = message.ToolResult
		}
	}
	if payload == nil || payload.Failure.Code != ToolFailurePolicyDenied || payload.Execution.Local != ToolLocalNotStarted || payload.Execution.Remote != ToolRemoteNotDispatched {
		t.Fatal(payload)
	}
}

func TestHostChildReportSurvivesParentHookFailure(t *testing.T) {
	parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "child completed"}}}, nil
	}}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		if out := in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"}); out.Err != nil {
			t.Fatal(out.Err)
		}
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "private parent content"}}, Details: map[string]any{"raw": "private"}}, nil
	}}
	out := RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent}, AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
		if in.ParentToolCallID == "" {
			return AfterToolCallResult{}, errors.New("parent output withheld")
		}
		return AfterToolCallResult{}, nil
	}})
	if out.Err == nil || out.Failure.Code != ToolFailureHook || out.Result.Details != nil || out.Result.Content[0].Text != "parent output withheld" || out.Result.ChildCalls == nil {
		t.Fatal(out)
	}
	report := out.Result.ChildCalls
	if !report.Closed || len(report.Calls) != 1 || !report.Calls[0].Completed || report.Calls[0].Failure != nil || report.Calls[0].Execution.Local != ToolLocalReturned {
		t.Fatal(report)
	}
	message := NewToolResultMessage(out.ToolCall, out.Result, out.IsError)
	out.Result.ChildCalls.Calls[0].ToolName = "mutated"
	if message.ToolResult.ChildCalls.Calls[0].ToolName != "leaf" {
		t.Fatal("host report aliases caller")
	}
	data, err := json.Marshal(message)
	if err != nil || strings.Contains(string(data), "child_calls") || strings.Contains(string(data), "child completed") || strings.Contains(string(data), "private") {
		t.Fatalf("report leaked into model JSON: %s err=%v", data, err)
	}
}

func TestLeafCannotSupplyForgedChildReport(t *testing.T) {
	forged := &ChildCallReport{Calls: []ChildCallRecord{{ToolCallID: "forged", Completed: true}}}
	out := RunToolCall(t.Context(), ToolCall{Name: "leaf"}, RunToolCallOptions{Tools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		return ToolResult{ChildCalls: forged}, nil
	}}},
		AfterToolCall: func(context.Context, AfterToolCallContext) (AfterToolCallResult, error) {
			return AfterToolCallResult{Result: &ToolResult{ChildCalls: forged}}, nil
		}})
	if out.Result.ChildCalls != nil || out.Err != nil {
		t.Fatal(out)
	}
}

func TestChildSuspendPreservesEarlierWriteAndCannotReplay(t *testing.T) {
	var writes atomic.Int32
	var report ChildCallReport
	leaves := []ToolDefinition{{Name: "write", Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
		if in.ChildCaller != nil {
			t.Fatal("leaf received child capability")
		}
		writes.Add(1)
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "written"}}}, nil
	}}}
	parent := ToolDefinition{Name: "code", ExecutionMode: ToolExecutionSequential, ChildTools: leaves, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		first := in.ChildCaller.Call(ctx, ToolCall{ID: "forged", OriginalID: "forged", Name: "write"})
		second := in.ChildCaller.Call(ctx, ToolCall{Name: "write"})
		if first.Err != nil || first.ToolCall.ID != "outer/1" || first.ToolCall.OriginalID != "" || second.Failure.Code != ToolFailureNestedSuspendUnsupported || second.Execution.Remote != ToolRemoteNotDispatched {
			t.Fatalf("first=%+v second=%+v", first, second)
		}
		report = in.ChildCaller.Report()
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "one write completed"}}}, nil
	}}
	out := RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent}, ToolGate: func(_ context.Context, in BeforeToolCallContext) (ToolGateResult, error) {
		if in.ToolCall.ID == "outer/2" {
			if in.ParentToolCallID != "outer" {
				t.Fatal(in)
			}
			return ToolGateResult{Action: ToolGateActionSuspend}, nil
		}
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	if out.Err != nil || writes.Load() != 1 || len(report.Calls) != 2 || !report.Calls[0].Completed || report.Calls[0].Failure != nil || report.Calls[1].Failure.Code != ToolFailureNestedSuspendUnsupported {
		t.Fatalf("out=%+v writes=%d report=%+v", out, writes.Load(), report)
	}
}

func TestChildAfterHookIsAuthoritativeAndOutputSchemaChecked(t *testing.T) {
	var children []ToolCallOutcome
	parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", OutputSchema: map[string]any{"type": "object", "required": []any{"safe"}}, Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "private"}}, StructuredContent: json.RawMessage(`{"safe":true,"secret":"private"}`), Execution: &ToolExecutionInfo{Remote: ToolRemoteCompleteReported}}, nil
	}}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		children = append(children, in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"}), in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"}))
		return ToolResult{}, nil
	}}
	RunToolCall(t.Context(), ToolCall{ID: "code", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent}, AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
		if in.ToolCall.ID == "code/1" {
			return AfterToolCallResult{Result: &ToolResult{Content: []Part{{Type: PartTypeText, Text: "allowed"}}, StructuredContent: json.RawMessage(`{"safe":true}`)}}, nil
		}
		if in.ToolCall.ID == "code/2" {
			return AfterToolCallResult{Result: &ToolResult{Content: []Part{{Type: PartTypeText, Text: "allowed"}}, StructuredContent: json.RawMessage(`{"changed":true}`)}}, nil
		}
		return AfterToolCallResult{}, nil
	}})
	if len(children) != 2 || children[0].Err != nil || string(children[0].Result.StructuredContent) != `{"safe":true}` || children[0].Result.Content[0].Text != "allowed" || children[1].Failure.Code != ToolFailureResultRejected || children[1].Execution.Remote != ToolRemoteCompleteReported {
		t.Fatalf("children=%+v", children)
	}
}

func TestChildLedgerCoversAllCallsAfterDetailBudget(t *testing.T) {
	var report ChildCallReport
	parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "ok"}}}, nil
	}}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		for i := 0; i < 1024; i++ {
			out := in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
			if out.Err != nil {
				t.Fatalf("call %d: %v", i, out.Err)
			}
		}
		over := in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
		if over.Failure.Code != ToolFailureResource {
			t.Fatal(over)
		}
		report = in.ChildCaller.Report()
		return ToolResult{}, nil
	}}
	out := RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent}})
	if out.Err != nil || len(report.Calls) != 1024 || len(report.Details) != 256 || !report.DetailsTruncated || !report.AdmissionStopped || report.Calls[1023].ToolCallID != "outer/1024" || !report.Calls[1023].Completed {
		t.Fatalf("out=%+v calls=%d details=%d report=%+v", out, len(report.Calls), len(report.Details), report)
	}
}

func TestChildSerialPermitWaitsForActualExitAndQueuedRevocation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	secondQueued := make(chan struct{})
	done := make(chan ToolCallOutcome, 1)
	var bodies atomic.Int32
	var revoked atomic.Bool
	var report ChildCallReport
	parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", ExecutionMode: ToolExecutionSequential, Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
		if bodies.Add(1) == 1 {
			close(started)
			<-release
		}
		return ToolResult{}, nil
	}}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		if !in.ChildSequential {
			t.Fatal("serial child flag absent")
		}
		firstCtx, cancel := context.WithCancel(ctx)
		firstDone := make(chan ToolCallOutcome, 1)
		go func() { firstDone <- in.ChildCaller.Call(firstCtx, ToolCall{Name: "leaf"}) }()
		<-started
		go func() { done <- in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"}) }()
		<-secondQueued
		cancel()
		revoked.Store(true)
		select {
		case <-done:
			t.Fatal("queued sibling entered before actual exit")
		case <-time.After(20 * time.Millisecond):
		}
		close(release)
		<-firstDone
		second := <-done
		if second.Failure.Code != ToolFailurePolicyDenied || second.Execution.Local != ToolLocalNotStarted {
			t.Fatal(second)
		}
		report = in.ChildCaller.Report()
		return ToolResult{}, nil
	}}
	RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent}, BeforeToolCall: func(_ context.Context, in BeforeToolCallContext) (BeforeToolCallResult, error) {
		if in.ToolCall.ID == "outer/2" {
			close(secondQueued)
		}
		return BeforeToolCallResult{}, nil
	}, CheckToolPermission: func(_ context.Context, in BeforeToolCallContext) error {
		if in.ToolCall.ID == "outer/2" && revoked.Load() {
			return errors.New("permission revoked while queued")
		}
		return nil
	}})
	if bodies.Load() != 1 || len(report.Calls) != 2 || report.Active != 0 {
		t.Fatalf("bodies=%d report=%+v", bodies.Load(), report)
	}
}

func TestChildScopeCloseRetainsActiveFactsAndSuppressesLateEvents(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan ToolCallOutcome, 1)
	var caller ChildCaller
	var eventsMu sync.Mutex
	var events []AgentEvent
	parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
		close(started)
		<-release
		in.OnUpdate(ToolResult{Content: []Part{{Type: PartTypeText, Text: strings.Repeat("private", 1<<18)}}})
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "late"}}}, nil
	}}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		caller = in.ChildCaller
		go func() { done <- caller.Call(ctx, ToolCall{Name: "leaf"}) }()
		<-started
		return ToolResult{}, nil
	}}
	engine := NewEngine()
	definition := AgentDefinition{Tools: []ToolDefinition{parent}}
	prepared, _, err := engine.prepareToolInvocation(t.Context(), definition, Message{}, AgentContext{}, map[string]ToolDefinition{"code": parent}, ToolCall{ID: "outer", Name: "code"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.executePreparedTool(t.Context(), definition, Message{}, prepared, func(event AgentEvent) { eventsMu.Lock(); defer eventsMu.Unlock(); events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	report := caller.Report()
	if !report.Closed || report.Active != 1 || report.Calls[0].Completed || report.Calls[0].Execution.Local != ToolLocalEntered || report.Calls[0].Execution.Remote != ToolRemoteUnknown {
		t.Fatal(report)
	}
	eventsMu.Lock()
	before := len(events)
	eventsMu.Unlock()
	close(release)
	<-done
	eventsMu.Lock()
	after := len(events)
	eventsMu.Unlock()
	report = caller.Report()
	if after != before || report.Active != 0 || !report.Calls[0].Completed || report.Calls[0].Execution.Local != ToolLocalReturned {
		t.Fatalf("before=%d after=%d report=%+v", before, after, report)
	}
}

func TestChildEventsProjectBeforeCopyingGiantUpdates(t *testing.T) {
	giant := strings.Repeat("private", 1<<20)
	parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
		in.OnUpdate(ToolResult{Content: []Part{{Type: PartTypeText, Text: giant}}, Details: map[string]any{"raw": giant}})
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: "private"}}}, nil
	}}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
		in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"})
		return ToolResult{}, nil
	}}
	definition := AgentDefinition{Tools: []ToolDefinition{parent}, AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
		if in.ParentToolCallID != "" {
			return AfterToolCallResult{Result: &ToolResult{Content: []Part{{Type: PartTypeText, Text: "safe"}}}}, nil
		}
		return AfterToolCallResult{}, nil
	}}
	engine := NewEngine()
	prepared, _, _ := engine.prepareToolInvocation(t.Context(), definition, Message{}, AgentContext{}, map[string]ToolDefinition{"code": parent}, ToolCall{ID: "outer", Name: "code"}, nil, nil)
	var childEvents []AgentEvent
	engine.executePreparedTool(t.Context(), definition, Message{}, prepared, func(event AgentEvent) {
		if event.ParentToolCallID != "" {
			childEvents = append(childEvents, event)
		}
	})
	if len(childEvents) != 3 {
		t.Fatal(childEvents)
	}
	for _, event := range childEvents {
		if event.Args != nil || event.ToolCall != nil || event.PartialToolResult != nil {
			t.Fatal(event)
		}
	}
	if childEvents[1].ToolResult != nil || childEvents[2].ToolResult.Content[0].Text != "safe" {
		t.Fatal(childEvents)
	}
}

func TestToolInvocationResourcesShareOnlyWithinOneRun(t *testing.T) {
	key := new(int)
	var creations int
	var seen []*int
	var invocations []*ToolInvocation
	container := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{}, Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
		if in.Invocation == nil {
			t.Fatal("container has no invocation holder")
		}
		resource, err := in.Invocation.LoadOrCreate(key, func() (any, error) { creations++; return new(int), nil })
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, resource.(*int))
		invocations = append(invocations, in.Invocation)
		return ToolResult{}, nil
	}}
	model := StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
		for _, message := range request.Messages {
			if message.Role == RoleTool {
				return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
			}
		}
		return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonToolUse,
			ToolCalls: []ToolCall{{ID: "first", Name: "code"}, {ID: "second", Name: "code"}}}, nil), nil
	})
	definition := AgentDefinition{Model: model, Tools: []ToolDefinition{container}, ToolExecution: ToolExecutionSequential}
	for i := 0; i < 2; i++ {
		if _, err := NewEngine().Run(t.Context(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if creations != 2 || len(seen) != 4 || seen[0] != seen[1] || seen[2] != seen[3] || seen[0] == seen[2] || invocations[0] == invocations[2] {
		t.Fatalf("creations=%d resources=%v invocations=%v", creations, seen, invocations)
	}
	for i := 0; i < 2; i++ {
		if out := RunToolCall(t.Context(), ToolCall{Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{container}}); out.Err != nil {
			t.Fatal(out.Err)
		}
	}
	if creations != 4 || seen[4] == seen[5] {
		t.Fatalf("direct calls reused invocation resources: creations=%d", creations)
	}
}

func TestChildParallelBoundAndDefinitionSequentialFlag(t *testing.T) {
	for _, mode := range []ToolExecutionMode{ToolExecutionParallel, ToolExecutionSequential} {
		t.Run(string(mode), func(t *testing.T) {
			var active, maximum atomic.Int32
			var report ChildCallReport
			parent := ToolDefinition{Name: "code", ExecutionMode: ToolExecutionSequential,
				ChildTools: []ToolDefinition{{Name: "leaf", Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
					if in.Invocation != nil || in.ChildCaller != nil {
						t.Fatal("leaf received parent capabilities")
					}
					n := active.Add(1)
					for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
					}
					time.Sleep(5 * time.Millisecond)
					active.Add(-1)
					return ToolResult{}, nil
				}}}, Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
					if in.ChildSequential != (mode == ToolExecutionSequential) {
						t.Fatalf("unexpected child scheduling flag: %v", in.ChildSequential)
					}
					var wg sync.WaitGroup
					for i := 0; i < 32; i++ {
						wg.Add(1)
						go func() {
							defer wg.Done()
							if out := in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"}); out.Err != nil {
								t.Error(out.Err)
							}
						}()
					}
					wg.Wait()
					report = in.ChildCaller.Report()
					return ToolResult{}, nil
				}}
			out := RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent}, ToolExecution: mode})
			if out.Err != nil || maximum.Load() > 16 || len(report.Calls) != 32 || report.Active != 0 {
				t.Fatalf("out=%+v maximum=%d report=%+v", out, maximum.Load(), report)
			}
			if mode == ToolExecutionSequential && maximum.Load() != 1 {
				t.Fatalf("sequential children overlapped: %d", maximum.Load())
			}
			if mode == ToolExecutionParallel && maximum.Load() < 2 {
				t.Fatal("all-parallel child allowlist did not run concurrently")
			}
		})
	}
}

func TestChildAdmissionFIFOWithOutOfOrderPreparation(t *testing.T) {
	firstHook := make(chan struct{})
	releaseHook := make(chan struct{})
	var order []string
	parent := ToolDefinition{Name: "code", ChildTools: []ToolDefinition{{Name: "leaf", ExecutionMode: ToolExecutionSequential,
		Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
			order = append(order, in.ToolCall.ID)
			return ToolResult{}, nil
		}}},
		Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
			firstDone := make(chan ToolCallOutcome, 1)
			secondDone := make(chan ToolCallOutcome, 1)
			go func() { firstDone <- in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"}) }()
			<-firstHook
			go func() { secondDone <- in.ChildCaller.Call(ctx, ToolCall{Name: "leaf"}) }()
			deadline := time.After(time.Second)
			for len(in.ChildCaller.Report().Calls) < 2 {
				select {
				case <-deadline:
					t.Fatal("second call was not admitted")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			close(releaseHook)
			if first, second := <-firstDone, <-secondDone; first.Err != nil || second.Err != nil {
				t.Fatalf("first=%+v second=%+v", first, second)
			}
			return ToolResult{}, nil
		}}
	RunToolCall(t.Context(), ToolCall{ID: "outer", Name: "code"}, RunToolCallOptions{Tools: []ToolDefinition{parent}, BeforeToolCall: func(_ context.Context, in BeforeToolCallContext) (BeforeToolCallResult, error) {
		if in.ToolCall.ID == "outer/1" {
			close(firstHook)
			<-releaseHook
		}
		return BeforeToolCallResult{}, nil
	}})
	if strings.Join(order, ",") != "outer/1,outer/2" {
		t.Fatalf("preparation completion reordered accepted siblings: %v", order)
	}
}

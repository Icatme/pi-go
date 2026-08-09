package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestToolGateSuspendsWholeParallelBatchWithoutSideEffects(t *testing.T) {
	var executed atomic.Int32
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{
					{ID: "first", OriginalID: "original-first", Name: "echo", Arguments: []byte(`{"value":"raw"}`)},
					{ID: "second", OriginalID: "original-second", Name: "echo", Arguments: []byte(`{"value":"raw"}`)},
				},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "echo",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}},
				"required":   []any{"value"},
			},
			Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
				executed.Add(1)
				return ToolResult{}, nil
			},
		}},
		BeforeToolCall: func(_ context.Context, input BeforeToolCallContext) (BeforeToolCallResult, error) {
			input.Args.(map[string]any)["value"] = "final"
			return BeforeToolCallResult{}, nil
		},
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	stream := runner.RunWithHooks(context.Background(), AgentSnapshot{}, []Message{NewUserTextMessage("run")}, LoopHooks{
		ToolGate: func(_ context.Context, input BeforeToolCallContext) (ToolGateResult, error) {
			if got := input.Args.(map[string]any)["value"]; got != "final" {
				t.Fatalf("gate saw args before final BeforeToolCall mutation: %#v", got)
			}
			if input.ToolCall.ID == "first" {
				return ToolGateResult{Action: ToolGateActionSuspend, Reason: "approval required"}, nil
			}
			return ToolGateResult{Action: ToolGateActionAllow}, nil
		},
	})
	events, snapshot, runErr := collectToolGateStream(t, stream)
	var suspended *ToolCallsSuspendedError
	if !errors.As(runErr, &suspended) {
		t.Fatalf("expected ToolCallsSuspendedError, got %v", runErr)
	}
	if executed.Load() != 0 {
		t.Fatalf("suspended parallel batch executed %d sibling tool bodies", executed.Load())
	}
	if len(suspended.Calls) != 1 || suspended.Calls[0].ToolCall.ID != "first" || suspended.Calls[0].Reason != "approval required" {
		t.Fatalf("unexpected suspended calls: %+v", suspended.Calls)
	}
	var suspendedArgs map[string]any
	if err := json.Unmarshal(suspended.Calls[0].Arguments, &suspendedArgs); err != nil || suspendedArgs["value"] != "final" {
		t.Fatalf("suspension lost canonical final arguments: %s (%v)", suspended.Calls[0].Arguments, err)
	}
	suspended.Calls[0].Arguments[0] = 'x'
	suspended.Calls[0].ToolCall.Arguments[0] = 'x'
	if got := string(snapshot.Messages[1].ToolCalls[0].Arguments); got != `{"value":"raw"}` {
		t.Fatalf("suspension error mutation leaked into snapshot: %s", got)
	}
	if len(snapshot.Messages) != 2 || snapshot.Messages[1].Role != RoleAssistant {
		t.Fatalf("suspension synthesized transcript messages: %+v", snapshot.Messages)
	}
	if snapshot.Error != "" {
		t.Fatalf("suspension persisted as snapshot error: %q", snapshot.Error)
	}
	if len(snapshot.PendingToolCalls) != 2 || snapshot.PendingToolCalls[0].OriginalToolCallID != "original-first" || snapshot.PendingToolCalls[1].ToolCallID != "second" {
		t.Fatalf("pending tool-call order/identity was not preserved: %+v", snapshot.PendingToolCalls)
	}
	if snapshot.PendingToolControl == nil || snapshot.PendingToolControl.Turn != 1 || snapshot.PendingToolControl.Binding == "" {
		t.Fatalf("suspension lost durable batch control state: %+v", snapshot.PendingToolControl)
	}
	assertGateLifecycle(t, events, 1, 0)
	for _, event := range events {
		if event.Type == EventAgentEnd && (len(event.Messages) != 2 || event.Messages[0].Role != RoleUser || event.Messages[1].Role != RoleAssistant) {
			t.Fatalf("suspended agent_end lost invocation messages: %+v", event.Messages)
		}
	}
	for _, event := range events {
		if event.Type == EventToolExecutionStart || event.Type == EventToolExecutionEnd {
			t.Fatalf("suspended batch emitted tool lifecycle event: %+v", event)
		}
	}
}

func TestToolGateUsesFinalIsolatedArgsBeforeExecutionStart(t *testing.T) {
	var (
		mu       sync.Mutex
		sequence []string
		executed any
		models   int
	)
	appendSequence := func(value string) {
		mu.Lock()
		sequence = append(sequence, value)
		mu.Unlock()
	}
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			models++
			if models == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "echo", Arguments: []byte(`{"value":"raw"}`)}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "echo",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string"}},
				"required":   []any{"value"},
			},
			Execute: func(_ context.Context, _ string, args any, _ ToolUpdateFunc) (ToolResult, error) {
				appendSequence("execute")
				executed = args.(map[string]any)["value"]
				return ToolResult{}, nil
			},
		}},
		BeforeToolCall: func(_ context.Context, input BeforeToolCallContext) (BeforeToolCallResult, error) {
			appendSequence("before")
			input.Args.(map[string]any)["value"] = "final"
			return BeforeToolCallResult{}, nil
		},
	}
	var events []AgentEvent
	next, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		if event.Type == EventToolExecutionStart {
			appendSequence("start")
		}
		events = append(events, event)
	}, LoopHooks{ToolGate: func(_ context.Context, input BeforeToolCallContext) (ToolGateResult, error) {
		appendSequence("gate")
		if got := input.Args.(map[string]any)["value"]; got != "final" {
			t.Fatalf("gate saw non-final args: %#v", got)
		}
		input.Args.(map[string]any)["value"] = "gate-mutation"
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	if err != nil {
		t.Fatalf("RunWithHooks returned error: %v", err)
	}
	if executed != "final" {
		t.Fatalf("gate input mutation leaked into execution args: %#v", executed)
	}
	want := []string{"before", "gate", "start", "execute"}
	if fmt.Sprint(sequence) != fmt.Sprint(want) {
		t.Fatalf("unexpected preflight/execution order: got %v want %v", sequence, want)
	}
	if len(next.PendingToolCalls) != 0 {
		t.Fatalf("successful batch retained pending calls: %+v", next.PendingToolCalls)
	}
	assertGateLifecycle(t, events, 2, 2)
}

func TestToolGateBlockBecomesErrorToolResult(t *testing.T) {
	var (
		executed atomic.Bool
		models   int
		events   []AgentEvent
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			models++
			if models == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "danger"}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, Parts: []Part{{Type: PartTypeText, Text: "done"}}, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "danger", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			executed.Store(true)
			return ToolResult{}, nil
		}}},
	}
	next, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		events = append(events, event)
	}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionBlock, Reason: "operator rejected"}, nil
	}})
	if err != nil {
		t.Fatalf("RunWithHooks returned error: %v", err)
	}
	if executed.Load() {
		t.Fatal("blocked tool body executed")
	}
	if len(next.Messages) != 4 || next.Messages[2].ToolResult == nil || !next.Messages[2].ToolResult.IsError || next.Messages[2].ToolResult.Content[0].Text != "operator rejected" {
		t.Fatalf("unexpected blocked tool result transcript: %+v", next.Messages)
	}
	var starts, ends int
	for _, event := range events {
		switch event.Type {
		case EventToolExecutionStart:
			starts++
		case EventToolExecutionEnd:
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("blocked tool lifecycle was not paired: starts=%d ends=%d", starts, ends)
	}
	assertGateLifecycle(t, events, 2, 2)
}

func TestToolGateInvalidDecisionFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		result ToolGateResult
	}{
		{name: "empty action", result: ToolGateResult{}},
		{name: "unknown action", result: ToolGateResult{Action: ToolGateAction("unknown")}},
		{name: "allow and terminate", result: ToolGateResult{Action: ToolGateActionAllow, Terminate: true}},
		{name: "suspend and terminate", result: ToolGateResult{Action: ToolGateActionSuspend, Terminate: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var executed atomic.Int32
			definition := AgentDefinition{
				Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
					return newStaticAssistantStream(Message{
						Role: RoleAssistant,
						ToolCalls: []ToolCall{
							{ID: "first", Name: "echo"},
							{ID: "second", Name: "echo"},
						},
						StopReason: StopReasonToolUse,
						Timestamp:  time.Now().UTC(),
					}, nil), nil
				}},
				Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
					executed.Add(1)
					return ToolResult{}, nil
				}}},
			}
			var events []AgentEvent
			next, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
				events = append(events, event)
			}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
				return tt.result, nil
			}})
			if err == nil {
				t.Fatal("expected invalid gate decision to fail")
			}
			if executed.Load() != 0 {
				t.Fatalf("invalid gate decision executed %d tool bodies", executed.Load())
			}
			if next == nil || next.Error == "" || len(next.PendingToolCalls) != 0 {
				t.Fatalf("unexpected failed snapshot: %+v", next)
			}
			for _, event := range events {
				if event.Type == EventToolExecutionStart || event.Type == EventToolExecutionEnd {
					t.Fatalf("invalid gate decision emitted tool lifecycle: %+v", event)
				}
			}
			assertGateLifecycle(t, events, 1, 1)
		})
	}
}

func TestToolGateSiblingSuspensionDiscardsPreflightFailures(t *testing.T) {
	tests := []struct {
		name       string
		firstCall  ToolCall
		beforeHook BeforeToolCallHook
	}{
		{
			name:      "invalid arguments",
			firstCall: ToolCall{ID: "first", Name: "echo", Arguments: []byte(`{"value":1}`)},
		},
		{
			name:      "tool not found",
			firstCall: ToolCall{ID: "first", Name: "missing", Arguments: []byte(`{"value":"ok"}`)},
		},
		{
			name:      "before hook error",
			firstCall: ToolCall{ID: "first", Name: "echo", Arguments: []byte(`{"value":"ok"}`)},
			beforeHook: func(_ context.Context, input BeforeToolCallContext) (BeforeToolCallResult, error) {
				if input.ToolCall.ID == "first" {
					return BeforeToolCallResult{}, errors.New("before failed")
				}
				return BeforeToolCallResult{}, nil
			},
		},
	}
	for _, executionMode := range []ToolExecutionMode{ToolExecutionParallel, ToolExecutionSequential} {
		for _, tt := range tests {
			t.Run(string(executionMode)+"/"+tt.name, func(t *testing.T) {
				var executed atomic.Int32
				definition := AgentDefinition{
					ToolExecution: executionMode,
					Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
						return newStaticAssistantStream(Message{
							Role: RoleAssistant,
							ToolCalls: []ToolCall{
								tt.firstCall,
								{ID: "second", Name: "echo", Arguments: []byte(`{"value":"ok"}`)},
							},
							StopReason: StopReasonToolUse,
							Timestamp:  time.Now().UTC(),
						}, nil), nil
					}},
					Tools: []ToolDefinition{{
						Name: "echo",
						Parameters: map[string]any{
							"type":       "object",
							"properties": map[string]any{"value": map[string]any{"type": "string"}},
							"required":   []any{"value"},
						},
						Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
							executed.Add(1)
							return ToolResult{}, nil
						},
					}},
					BeforeToolCall: tt.beforeHook,
				}
				var events []AgentEvent
				next, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
					events = append(events, event)
				}, LoopHooks{ToolGate: func(_ context.Context, input BeforeToolCallContext) (ToolGateResult, error) {
					if input.ToolCall.ID == "second" {
						return ToolGateResult{Action: ToolGateActionSuspend}, nil
					}
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				}})
				var suspended *ToolCallsSuspendedError
				if !errors.As(err, &suspended) {
					t.Fatalf("expected suspension, got %v", err)
				}
				if executed.Load() != 0 || len(next.Messages) != 2 || len(next.PendingToolCalls) != 2 || next.Error != "" {
					t.Fatalf("sibling suspension leaked preflight outcome: executed=%d snapshot=%+v", executed.Load(), next)
				}
				for _, event := range events {
					if event.Type == EventToolExecutionStart || event.Type == EventToolExecutionEnd || event.Type == EventTurnEnd {
						t.Fatalf("sibling suspension emitted completion lifecycle: %+v", event)
					}
				}
				assertGateLifecycle(t, events, 1, 0)
			})
		}
	}
}

func TestResumePendingToolCallsValidatesExactIdentity(t *testing.T) {
	assistant := Message{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{
			{ID: "first", OriginalID: "original-first", Name: "one", ThoughtSignature: "signature-first"},
			{ID: "second", OriginalID: "original-second", Name: "two", ThoughtSignature: "signature-second"},
		},
		StopReason: StopReasonToolUse,
		Timestamp:  time.Now().UTC(),
	}
	valid := AgentSnapshot{
		Messages: []Message{NewUserTextMessage("run"), assistant},
		PendingToolCalls: []PendingToolCall{
			{ToolCallID: "first", OriginalToolCallID: "original-first", ToolName: "one"},
			{ToolCallID: "second", OriginalToolCallID: "original-second", ToolName: "two"},
		},
	}
	setPendingToolControlState(&valid, 1, assistant)
	tests := []struct {
		name   string
		mutate func(*AgentSnapshot)
	}{
		{name: "missing pending", mutate: func(snapshot *AgentSnapshot) { snapshot.PendingToolCalls = nil }},
		{name: "missing control", mutate: func(snapshot *AgentSnapshot) { snapshot.PendingToolControl = nil }},
		{name: "turn tamper", mutate: func(snapshot *AgentSnapshot) { snapshot.PendingToolControl.Turn++ }},
		{name: "binding tamper", mutate: func(snapshot *AgentSnapshot) { snapshot.PendingToolControl.Binding = "other" }},
		{name: "reordered pending", mutate: func(snapshot *AgentSnapshot) {
			snapshot.PendingToolCalls[0], snapshot.PendingToolCalls[1] = snapshot.PendingToolCalls[1], snapshot.PendingToolCalls[0]
		}},
		{name: "id mismatch", mutate: func(snapshot *AgentSnapshot) { snapshot.PendingToolCalls[0].ToolCallID = "other" }},
		{name: "original id mismatch", mutate: func(snapshot *AgentSnapshot) { snapshot.PendingToolCalls[0].OriginalToolCallID = "other" }},
		{name: "name mismatch", mutate: func(snapshot *AgentSnapshot) { snapshot.PendingToolCalls[0].ToolName = "other" }},
		{name: "thought signature tamper", mutate: func(snapshot *AgentSnapshot) {
			snapshot.Messages[1].ToolCalls[0].ThoughtSignature = "tampered"
		}},
		{name: "non assistant tail", mutate: func(snapshot *AgentSnapshot) {
			snapshot.Messages = append(snapshot.Messages, NewUserTextMessage("other"))
		}},
		{name: "assistant error", mutate: func(snapshot *AgentSnapshot) { snapshot.Messages[1].ErrorMessage = "failed" }},
		{name: "assistant aborted", mutate: func(snapshot *AgentSnapshot) { snapshot.Messages[1].StopReason = StopReasonAborted }},
		{name: "assistant length", mutate: func(snapshot *AgentSnapshot) { snapshot.Messages[1].StopReason = StopReasonLength }},
		{name: "empty normalized id", mutate: func(snapshot *AgentSnapshot) {
			snapshot.Messages[1].ToolCalls[0].ID = " "
			snapshot.PendingToolCalls[0].ToolCallID = " "
		}},
		{name: "duplicate normalized id", mutate: func(snapshot *AgentSnapshot) {
			snapshot.Messages[1].ToolCalls[1].ID = " first "
			snapshot.PendingToolCalls[1].ToolCallID = " first "
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := cloneSnapshotValue(valid)
			tt.mutate(&input)
			var events []AgentEvent
			next, err := NewEngine().ResumePendingToolCallsWithHooks(context.Background(), AgentDefinition{}, &input, func(event AgentEvent) {
				events = append(events, event)
			}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
				return ToolGateResult{Action: ToolGateActionAllow}, nil
			}})
			if err == nil || next != nil {
				t.Fatalf("expected exact validation failure, got snapshot=%+v err=%v", next, err)
			}
			if len(events) != 0 {
				t.Fatalf("invalid resume emitted events: %+v", events)
			}
		})
	}
}

func TestResumePendingToolCallsRequiresGateBeforeEvents(t *testing.T) {
	assistant := Message{
		Role:       RoleAssistant,
		ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
		StopReason: StopReasonToolUse,
		Timestamp:  time.Now().UTC(),
	}
	snapshot := AgentSnapshot{
		Messages:         []Message{NewUserTextMessage("run"), assistant},
		PendingToolCalls: []PendingToolCall{{ToolCallID: "call", ToolName: "echo"}},
	}
	setPendingToolControlState(&snapshot, 1, assistant)
	var events []AgentEvent
	next, err := NewEngine().ResumePendingToolCallsWithHooks(context.Background(), AgentDefinition{}, &snapshot, func(event AgentEvent) {
		events = append(events, event)
	}, LoopHooks{})
	if !errors.Is(err, ErrToolGateRequired) || next != nil {
		t.Fatalf("expected gate-required failure, got snapshot=%+v err=%v", next, err)
	}
	if len(events) != 0 {
		t.Fatalf("gate-required rejection emitted events: %+v", events)
	}
}

func TestNormalRunRejectsPendingToolStateWithoutExecution(t *testing.T) {
	assistant := Message{
		Role:       RoleAssistant,
		ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
		StopReason: StopReasonToolUse,
		Timestamp:  time.Now().UTC(),
	}
	snapshot := AgentSnapshot{
		Messages:         []Message{NewUserTextMessage("run"), assistant},
		PendingToolCalls: []PendingToolCall{{ToolCallID: "call", ToolName: "echo"}},
	}
	setPendingToolControlState(&snapshot, 1, assistant)
	var (
		modelCalls atomic.Int32
		executed   atomic.Int32
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			modelCalls.Add(1)
			return nil, errors.New("model must not run")
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			executed.Add(1)
			return ToolResult{}, nil
		}}},
	}
	for _, invoke := range []struct {
		name string
		run  func(EventSink) (*AgentSnapshot, error)
	}{
		{name: "run", run: func(emit EventSink) (*AgentSnapshot, error) {
			return NewEngine().RunWithHooks(context.Background(), definition, &snapshot, []Message{NewUserTextMessage("bypass")}, emit, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
				return ToolGateResult{Action: ToolGateActionAllow}, nil
			}})
		}},
		{name: "continue", run: func(emit EventSink) (*AgentSnapshot, error) {
			return NewEngine().ContinueWithHooks(context.Background(), definition, &snapshot, emit, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
				return ToolGateResult{Action: ToolGateActionAllow}, nil
			}})
		}},
	} {
		t.Run(invoke.name, func(t *testing.T) {
			var events []AgentEvent
			next, err := invoke.run(func(event AgentEvent) { events = append(events, event) })
			if !errors.Is(err, ErrPendingToolCallsRequireResume) || next != nil {
				t.Fatalf("expected explicit-resume rejection, got snapshot=%+v err=%v", next, err)
			}
			if len(events) != 0 {
				t.Fatalf("pending run rejection emitted events: %+v", events)
			}
		})
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	events, _, runErr := collectToolGateStream(t, runner.Run(context.Background(), snapshot, []Message{NewUserTextMessage("bypass")}))
	if !errors.Is(runErr, ErrPendingToolCallsRequireResume) {
		t.Fatalf("runner bypass returned %v", runErr)
	}
	assertGateLifecycle(t, events, 0, 0)
	if modelCalls.Load() != 0 || executed.Load() != 0 {
		t.Fatalf("pending run reached side effects: model=%d tool=%d", modelCalls.Load(), executed.Load())
	}
}

func TestResumePreExecutionErrorsKeepOldTurnOpenAndPending(t *testing.T) {
	assistant := Message{
		Role:       RoleAssistant,
		ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
		StopReason: StopReasonToolUse,
		Timestamp:  time.Now().UTC(),
	}
	baseSnapshot := AgentSnapshot{
		Messages:         []Message{NewUserTextMessage("run"), assistant},
		PendingToolCalls: []PendingToolCall{{ToolCallID: "call", ToolName: "echo"}},
	}
	setPendingToolControlState(&baseSnapshot, 1, assistant)
	wantBinding := baseSnapshot.PendingToolControl.Binding
	tests := []struct {
		name       string
		definition AgentDefinition
		hooks      LoopHooks
	}{
		{
			name:       "definition resolver",
			definition: AgentDefinition{},
			hooks: LoopHooks{
				ResolveDefinition: func(context.Context, AgentDefinition, AgentSnapshot) (AgentDefinition, error) {
					return AgentDefinition{}, errors.New("definition failed")
				},
				ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				},
			},
		},
		{
			name: "tool resolver",
			definition: AgentDefinition{ToolResolver: func(context.Context, AgentSnapshot) ([]ToolDefinition, error) {
				return nil, errors.New("tools failed")
			}},
			hooks: LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
				return ToolGateResult{Action: ToolGateActionAllow}, nil
			}},
		},
		{
			name:       "tool gate",
			definition: AgentDefinition{Tools: []ToolDefinition{{Name: "echo"}}},
			hooks: LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
				return ToolGateResult{}, errors.New("gate failed")
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := cloneSnapshotValue(baseSnapshot)
			var events []AgentEvent
			next, err := NewEngine().ResumePendingToolCallsWithHooks(context.Background(), tt.definition, &input, func(event AgentEvent) {
				events = append(events, event)
			}, tt.hooks)
			if err == nil || next == nil {
				t.Fatalf("expected pre-execution failure, got snapshot=%+v err=%v", next, err)
			}
			if len(next.PendingToolCalls) != 1 || next.PendingToolControl == nil || next.PendingToolControl.Binding != wantBinding {
				t.Fatalf("pre-execution failure lost pending state: %+v", next)
			}
			for _, event := range events {
				if event.Type == EventTurnEnd || event.Type == EventToolExecutionStart || event.Type == EventToolExecutionEnd {
					t.Fatalf("pre-execution failure closed old turn: %+v", event)
				}
			}
			assertGateLifecycle(t, events, 0, 0)
		})
	}
}

func TestResumePendingToolCallsRetriesPreExecutionFailureAndEndsOldTurnOnce(t *testing.T) {
	assistant := Message{
		Role:       RoleAssistant,
		ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
		StopReason: StopReasonToolUse,
		Timestamp:  time.Now().UTC(),
	}
	snapshot := AgentSnapshot{
		Messages:         []Message{NewUserTextMessage("run"), assistant},
		PendingToolCalls: []PendingToolCall{{ToolCallID: "call", ToolName: "echo"}},
	}
	setPendingToolControlState(&snapshot, 1, assistant)
	var (
		resolveCalls atomic.Int32
		executed     atomic.Int32
	)
	definition := AgentDefinition{
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			executed.Add(1)
			return ToolResult{}, nil
		}}},
		ShouldStopAfterTurn: func(context.Context, ShouldStopAfterTurnContext) (bool, error) {
			return true, nil
		},
	}
	hooks := LoopHooks{
		ResolveDefinition: func(_ context.Context, current AgentDefinition, _ AgentSnapshot) (AgentDefinition, error) {
			if resolveCalls.Add(1) == 1 {
				return AgentDefinition{}, errors.New("transient definition failure")
			}
			return current, nil
		},
		ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
			return ToolGateResult{Action: ToolGateActionAllow}, nil
		},
	}
	var firstEvents []AgentEvent
	failed, firstErr := NewEngine().ResumePendingToolCallsWithHooks(context.Background(), definition, &snapshot, func(event AgentEvent) {
		firstEvents = append(firstEvents, event)
	}, hooks)
	if firstErr == nil || failed == nil {
		t.Fatalf("expected first resume to fail before execution, got snapshot=%+v err=%v", failed, firstErr)
	}
	if len(failed.PendingToolCalls) != 1 || failed.PendingToolControl == nil || executed.Load() != 0 {
		t.Fatalf("first resume did not preserve retry state: executed=%d snapshot=%+v", executed.Load(), failed)
	}
	assertGateLifecycle(t, firstEvents, 0, 0)

	var retryEvents []AgentEvent
	resumed, retryErr := NewEngine().ResumePendingToolCallsWithHooks(context.Background(), definition, failed, func(event AgentEvent) {
		retryEvents = append(retryEvents, event)
	}, hooks)
	if retryErr != nil {
		t.Fatalf("retry resume returned error: %v", retryErr)
	}
	if executed.Load() != 1 || resolveCalls.Load() != 2 {
		t.Fatalf("unexpected retry calls: resolver=%d tool=%d", resolveCalls.Load(), executed.Load())
	}
	if len(resumed.PendingToolCalls) != 0 || resumed.PendingToolControl != nil || resumed.Error != "" || len(resumed.Messages) != 3 {
		t.Fatalf("retry did not commit the pending batch exactly once: %+v", resumed)
	}
	assertGateLifecycle(t, retryEvents, 0, 1)
}

func TestToolGateCancellationAfterSiblingSuspensionTakesPrecedence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var executed atomic.Int32
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{
					{ID: "first", Name: "echo"},
					{ID: "second", Name: "echo"},
				},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			executed.Add(1)
			return ToolResult{}, nil
		}}},
	}
	var events []AgentEvent
	next, err := NewEngine().RunWithHooks(ctx, definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
		events = append(events, event)
	}, LoopHooks{ToolGate: func(_ context.Context, input BeforeToolCallContext) (ToolGateResult, error) {
		if input.ToolCall.ID == "first" {
			return ToolGateResult{Action: ToolGateActionSuspend}, nil
		}
		cancel()
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation to win, got %v", err)
	}
	var suspended *ToolCallsSuspendedError
	if errors.As(err, &suspended) {
		t.Fatalf("cancellation was persisted as suspension: %+v", suspended)
	}
	if executed.Load() != 0 || next.PendingToolControl != nil || len(next.PendingToolCalls) != 0 || len(next.Messages) != 2 {
		t.Fatalf("canceled preflight persisted/ran batch: executed=%d snapshot=%+v", executed.Load(), next)
	}
	for _, event := range events {
		if event.Type == EventToolExecutionStart || event.Type == EventToolExecutionEnd {
			t.Fatalf("canceled preflight emitted tool lifecycle: %+v", event)
		}
	}
}

func TestResumePendingToolCallsRejectsSuspendedAssistantArgumentTamper(t *testing.T) {
	var executed atomic.Int32
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				ToolCalls:  []ToolCall{{ID: "call", Name: "echo", Arguments: []byte(`{"value":"original"}`)}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			executed.Add(1)
			return ToolResult{}, nil
		}}},
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	stream := runner.RunWithHooks(context.Background(), AgentSnapshot{}, []Message{NewUserTextMessage("run")}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionSuspend}, nil
	}})
	_, snapshot, suspendErr := collectToolGateStream(t, stream)
	var suspended *ToolCallsSuspendedError
	if !errors.As(suspendErr, &suspended) {
		t.Fatalf("expected suspension, got %v", suspendErr)
	}
	snapshot.Messages[len(snapshot.Messages)-1].ToolCalls[0].Arguments = []byte(`{"value":"tampered"}`)
	var events []AgentEvent
	next, resumeErr := NewEngine().ResumePendingToolCallsWithHooks(context.Background(), definition, &snapshot, func(event AgentEvent) {
		events = append(events, event)
	}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	if resumeErr == nil || next != nil {
		t.Fatalf("expected tampered binding rejection, got snapshot=%+v err=%v", next, resumeErr)
	}
	if executed.Load() != 0 || len(events) != 0 {
		t.Fatalf("tampered binding reached execution/events: executed=%d events=%v", executed.Load(), runnerEventTypes(events))
	}
}

func TestResumePendingToolCallsAcceptsCanonicalRawArgumentsAfterJSONRoundTrip(t *testing.T) {
	var (
		modelCalls atomic.Int32
		executed   atomic.Int32
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			if modelCalls.Add(1) == 1 {
				return newStaticAssistantStream(Message{
					Role:       RoleAssistant,
					ToolCalls:  []ToolCall{{ID: "call", Name: "echo", Arguments: json.RawMessage(" { \n \t\"value\" : 1 } ")}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "echo",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "number"}},
				"required":   []any{"value"},
			},
			Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
				executed.Add(1)
				return ToolResult{}, nil
			},
		}},
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	initial := runner.RunWithHooks(context.Background(), AgentSnapshot{}, []Message{NewUserTextMessage("run")}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionSuspend}, nil
	}})
	_, suspendedSnapshot, suspendErr := collectToolGateStream(t, initial)
	var suspended *ToolCallsSuspendedError
	if !errors.As(suspendErr, &suspended) {
		t.Fatalf("expected suspension, got %v", suspendErr)
	}
	payload, err := json.Marshal(suspendedSnapshot)
	if err != nil {
		t.Fatalf("marshal suspended snapshot: %v", err)
	}
	var restored AgentSnapshot
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatalf("unmarshal suspended snapshot: %v", err)
	}
	if got := string(restored.Messages[len(restored.Messages)-1].ToolCalls[0].Arguments); got != `{"value":1}` {
		t.Fatalf("expected JSON round-trip to compact raw arguments, got %q", got)
	}

	resume := runner.ResumePendingToolCallsWithHooks(context.Background(), restored, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	_, finalSnapshot, resumeErr := collectToolGateStream(t, resume)
	if resumeErr != nil {
		t.Fatalf("resume after JSON round-trip returned error: %v", resumeErr)
	}
	if executed.Load() != 1 || modelCalls.Load() != 2 {
		t.Fatalf("unexpected resume calls: executed=%d model=%d", executed.Load(), modelCalls.Load())
	}
	if len(finalSnapshot.Messages) != 4 || finalSnapshot.PendingToolControl != nil || len(finalSnapshot.PendingToolCalls) != 0 {
		t.Fatalf("unexpected resumed snapshot: %+v", finalSnapshot)
	}
}

func TestResumePendingToolCallsPreservesParsedArgumentNumbersAfterJSONRoundTrip(t *testing.T) {
	const largeInteger = int64(9007199254740993)
	var (
		modelCalls atomic.Int32
		executed   atomic.Int32
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			if modelCalls.Add(1) == 1 {
				return newStaticAssistantStream(Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{{
						ID:   "call",
						Name: "echo",
						ParsedArgs: map[string]any{
							"large":    largeInteger,
							"exponent": json.Number("1e3"),
						},
					}},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(_ context.Context, _ string, args any, _ ToolUpdateFunc) (ToolResult, error) {
			parsed := args.(map[string]any)
			if fmt.Sprint(parsed["large"]) != "9007199254740993" || fmt.Sprint(parsed["exponent"]) != "1e3" {
				return ToolResult{}, fmt.Errorf("numeric precision changed: %#v", parsed)
			}
			executed.Add(1)
			return ToolResult{}, nil
		}}},
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	initial := runner.RunWithHooks(context.Background(), AgentSnapshot{}, []Message{NewUserTextMessage("run")}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionSuspend}, nil
	}})
	_, suspendedSnapshot, suspendErr := collectToolGateStream(t, initial)
	var suspended *ToolCallsSuspendedError
	if !errors.As(suspendErr, &suspended) {
		t.Fatalf("expected suspension, got %v", suspendErr)
	}
	payload, err := json.Marshal(suspendedSnapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var restored AgentSnapshot
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	restoredArgs := restored.Messages[len(restored.Messages)-1].ToolCalls[0].ParsedArgs
	large, largeOK := restoredArgs["large"].(json.Number)
	exponent, exponentOK := restoredArgs["exponent"].(json.Number)
	if !largeOK || large.String() != "9007199254740993" || !exponentOK || exponent.String() != "1e3" {
		t.Fatalf("ToolCall.UnmarshalJSON lost numeric precision/lexeme: %#v", restoredArgs)
	}
	resume := runner.ResumePendingToolCallsWithHooks(context.Background(), restored, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	_, finalSnapshot, resumeErr := collectToolGateStream(t, resume)
	if resumeErr != nil {
		t.Fatalf("resume returned error: %v", resumeErr)
	}
	if executed.Load() != 1 || modelCalls.Load() != 2 || len(finalSnapshot.Messages) != 4 {
		t.Fatalf("unexpected numeric resume result: executed=%d model=%d snapshot=%+v", executed.Load(), modelCalls.Load(), finalSnapshot)
	}
}

func TestPendingToolBindingDistinguishesRawPresenceForCustomParser(t *testing.T) {
	var parseCalls atomic.Int32
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				ToolCalls:  []ToolCall{{ID: "call", Name: "custom"}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "custom",
			ParseArguments: func(call ToolCall) (any, error) {
				parseCalls.Add(1)
				if len(call.Arguments) == 0 {
					return map[string]any{"source": "nil"}, nil
				}
				return map[string]any{"source": "explicit"}, nil
			},
			Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
				t.Fatal("tampered raw presence must not execute")
				return ToolResult{}, nil
			},
		}},
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	initial := runner.RunWithHooks(context.Background(), AgentSnapshot{}, []Message{NewUserTextMessage("run")}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionSuspend}, nil
	}})
	_, snapshot, suspendErr := collectToolGateStream(t, initial)
	var suspended *ToolCallsSuspendedError
	if !errors.As(suspendErr, &suspended) {
		t.Fatalf("expected suspension, got %v", suspendErr)
	}
	if parseCalls.Load() != 1 {
		t.Fatalf("expected one initial parser call, got %d", parseCalls.Load())
	}
	snapshot.Messages[len(snapshot.Messages)-1].ToolCalls[0].Arguments = json.RawMessage(`{}`)
	var events []AgentEvent
	next, resumeErr := NewEngine().ResumePendingToolCallsWithHooks(context.Background(), definition, &snapshot, func(event AgentEvent) {
		events = append(events, event)
	}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	if resumeErr == nil || next != nil {
		t.Fatalf("expected nil-to-object raw presence tamper rejection, got snapshot=%+v err=%v", next, resumeErr)
	}
	if parseCalls.Load() != 1 || len(events) != 0 {
		t.Fatalf("tampered raw presence reran parser/emitted events: parser=%d events=%v", parseCalls.Load(), runnerEventTypes(events))
	}
}

func TestPendingToolBindingNormalizesZeroLengthRawArgumentsAcrossJSONRoundTrip(t *testing.T) {
	var (
		parseCalls atomic.Int32
		executed   atomic.Int32
	)
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{{
					ID:        "call",
					Name:      "custom",
					Arguments: json.RawMessage{},
				}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "custom",
			ParseArguments: func(call ToolCall) (any, error) {
				parseCalls.Add(1)
				if call.Arguments != nil {
					return nil, fmt.Errorf("zero-length raw arguments were not normalized: %#v", call.Arguments)
				}
				return map[string]any{"normalized": true}, nil
			},
			Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
				executed.Add(1)
				return ToolResult{}, nil
			},
		}},
		ShouldStopAfterTurn: func(context.Context, ShouldStopAfterTurnContext) (bool, error) {
			return true, nil
		},
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	initial := runner.RunWithHooks(context.Background(), AgentSnapshot{}, []Message{NewUserTextMessage("run")}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionSuspend}, nil
	}})
	_, suspendedSnapshot, suspendErr := collectToolGateStream(t, initial)
	var suspended *ToolCallsSuspendedError
	if !errors.As(suspendErr, &suspended) {
		t.Fatalf("expected suspension, got %v", suspendErr)
	}
	if parseCalls.Load() != 1 || suspendedSnapshot.Messages[len(suspendedSnapshot.Messages)-1].ToolCalls[0].Arguments != nil {
		t.Fatalf("initial run did not normalize zero-length raw arguments: parser=%d snapshot=%+v", parseCalls.Load(), suspendedSnapshot)
	}
	payload, err := json.Marshal(suspendedSnapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var restored AgentSnapshot
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if err := ValidatePendingToolState(restored); err != nil {
		t.Fatalf("zero-length raw normalization changed pending binding: %v", err)
	}
	resume := runner.ResumePendingToolCallsWithHooks(context.Background(), restored, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	_, finalSnapshot, resumeErr := collectToolGateStream(t, resume)
	if resumeErr != nil {
		t.Fatalf("resume returned error: %v", resumeErr)
	}
	if parseCalls.Load() != 2 || executed.Load() != 1 || len(finalSnapshot.PendingToolCalls) != 0 || finalSnapshot.PendingToolControl != nil {
		t.Fatalf("zero-length raw resume was not stable: parser=%d executed=%d snapshot=%+v", parseCalls.Load(), executed.Load(), finalSnapshot)
	}
}

func TestPendingToolBindingCanonicalizesTypedParsedArgsAcrossJSONRoundTrip(t *testing.T) {
	type nestedTypedArgs struct {
		Z int `json:"z"`
		A int `json:"a"`
	}
	var executed atomic.Int32
	definition := AgentDefinition{
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			return newStaticAssistantStream(Message{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{{
					ID:   "call",
					Name: "echo",
					ParsedArgs: map[string]any{
						"typed": nestedTypedArgs{Z: 2, A: 1},
						"raw":   json.RawMessage(`{"z":2,"a":1}`),
					},
				}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{
			Name: "echo",
			Execute: func(_ context.Context, _ string, args any, _ ToolUpdateFunc) (ToolResult, error) {
				parsed := args.(map[string]any)
				for _, key := range []string{"typed", "raw"} {
					nested, ok := parsed[key].(map[string]any)
					if !ok || fmt.Sprint(nested["a"]) != "1" || fmt.Sprint(nested["z"]) != "2" {
						return ToolResult{}, fmt.Errorf("parsed %s value changed: %#v", key, parsed[key])
					}
				}
				executed.Add(1)
				return ToolResult{}, nil
			},
		}},
		ShouldStopAfterTurn: func(context.Context, ShouldStopAfterTurnContext) (bool, error) {
			return true, nil
		},
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	initial := runner.RunWithHooks(context.Background(), AgentSnapshot{}, []Message{NewUserTextMessage("run")}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionSuspend}, nil
	}})
	_, suspendedSnapshot, suspendErr := collectToolGateStream(t, initial)
	var suspended *ToolCallsSuspendedError
	if !errors.As(suspendErr, &suspended) {
		t.Fatalf("expected suspension, got %v", suspendErr)
	}
	payload, err := json.Marshal(suspendedSnapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var restored AgentSnapshot
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if err := ValidatePendingToolState(restored); err != nil {
		t.Fatalf("typed/generic parsed-args round-trip changed pending binding: %v", err)
	}
	resume := runner.ResumePendingToolCallsWithHooks(context.Background(), restored, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	_, finalSnapshot, resumeErr := collectToolGateStream(t, resume)
	if resumeErr != nil {
		t.Fatalf("resume returned error: %v", resumeErr)
	}
	if executed.Load() != 1 || len(finalSnapshot.PendingToolCalls) != 0 || finalSnapshot.PendingToolControl != nil {
		t.Fatalf("typed parsed-args resume did not commit: executed=%d snapshot=%+v", executed.Load(), finalSnapshot)
	}
}

func TestToolGateRejectsNonDurableParsedArgsBeforeGateOrExecution(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "non finite number", args: map[string]any{"bad": math.NaN()}},
		{name: "invalid nested raw json", args: map[string]any{"bad": json.RawMessage(`{"value":`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				gateCalls atomic.Int32
				executed  atomic.Int32
			)
			definition := AgentDefinition{
				Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
					return newStaticAssistantStream(Message{
						Role:       RoleAssistant,
						ToolCalls:  []ToolCall{{ID: "call", Name: "echo", ParsedArgs: tt.args}},
						StopReason: StopReasonToolUse,
						Timestamp:  time.Now().UTC(),
					}, nil), nil
				}},
				Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
					executed.Add(1)
					return ToolResult{}, nil
				}}},
			}
			var events []AgentEvent
			next, err := NewEngine().RunWithHooks(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
				events = append(events, event)
			}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
				gateCalls.Add(1)
				return ToolGateResult{Action: ToolGateActionAllow}, nil
			}})
			if err == nil || next == nil {
				t.Fatalf("expected non-durable argument failure, got snapshot=%+v err=%v", next, err)
			}
			if gateCalls.Load() != 0 || executed.Load() != 0 || len(next.PendingToolCalls) != 0 || next.PendingToolControl != nil {
				t.Fatalf("non-durable arguments reached gate/execution: gate=%d tool=%d snapshot=%+v", gateCalls.Load(), executed.Load(), next)
			}
			for _, event := range events {
				if event.Type == EventToolExecutionStart || event.Type == EventToolExecutionEnd {
					t.Fatalf("non-durable arguments emitted tool lifecycle: %+v", event)
				}
			}
		})
	}
}

func TestResumePendingToolCallsRunsPrepareThenStopWithResumeMessageWindow(t *testing.T) {
	assistant := Message{
		Role:       RoleAssistant,
		ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
		StopReason: StopReasonToolUse,
		Timestamp:  time.Now().UTC(),
	}
	snapshot := AgentSnapshot{
		Messages:         []Message{NewUserTextMessage("run"), assistant},
		PendingToolCalls: []PendingToolCall{{ToolCallID: "call", ToolName: "echo"}},
		Metadata: map[string]any{
			"pi-go.agent.pending_tool_turn":    "user-owned-turn",
			"pi-go.agent.pending_tool_binding": "user-owned-binding",
		},
	}
	setPendingToolControlState(&snapshot, 1, assistant)
	var order []string
	assertWindow := func(stage string, input ShouldStopAfterTurnContext) {
		t.Helper()
		if len(input.NewMessages) != 1 || input.NewMessages[0].Role != RoleTool {
			t.Fatalf("%s saw old assistant in resume message window: %+v", stage, input.NewMessages)
		}
		if input.Message.Role != RoleAssistant || input.Message.ToolCalls[0].ID != "call" {
			t.Fatalf("%s lost source assistant: %+v", stage, input.Message)
		}
	}
	definition := AgentDefinition{
		Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
			t.Fatal("ShouldStopAfterTurn should prevent another model call")
			return nil, nil
		}),
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			return ToolResult{Content: []Part{{Type: PartTypeText, Text: "ok"}}}, nil
		}}},
		PrepareNextTurn: func(_ context.Context, input PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
			order = append(order, "prepare")
			assertWindow("prepare", input)
			return nil, nil
		},
		ShouldStopAfterTurn: func(_ context.Context, input ShouldStopAfterTurnContext) (bool, error) {
			order = append(order, "stop")
			assertWindow("stop", input)
			return true, nil
		},
	}
	var events []AgentEvent
	next, err := NewEngine().ResumePendingToolCallsWithHooks(context.Background(), definition, &snapshot, func(event AgentEvent) {
		events = append(events, event)
	}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	if err != nil {
		t.Fatalf("ResumePendingToolCallsWithHooks returned error: %v", err)
	}
	if fmt.Sprint(order) != fmt.Sprint([]string{"prepare", "stop"}) {
		t.Fatalf("unexpected post-tool hook order: %v", order)
	}
	if len(next.Messages) != 3 || next.Messages[1].Role != RoleAssistant || next.Messages[2].Role != RoleTool {
		t.Fatalf("resume duplicated source assistant: %+v", next.Messages)
	}
	if next.PendingToolControl != nil || len(next.PendingToolCalls) != 0 {
		t.Fatalf("resume retained pending control state: %+v", next)
	}
	if next.Metadata["pi-go.agent.pending_tool_turn"] != "user-owned-turn" || next.Metadata["pi-go.agent.pending_tool_binding"] != "user-owned-binding" {
		t.Fatalf("resume overwrote user metadata: %+v", next.Metadata)
	}
	assertGateLifecycle(t, events, 0, 1)
	for _, event := range events {
		if event.Type == EventAgentEnd && (len(event.Messages) != 1 || event.Messages[0].Role != RoleTool) {
			t.Fatalf("resume agent_end included old assistant: %+v", event.Messages)
		}
	}
}

func TestRunnerResumeReSuspendsThenExecutesCurrentResolvedToolsWithoutDuplicateAssistant(t *testing.T) {
	var (
		modelCalls      atomic.Int32
		toolResolutions atomic.Int32
		definitionCalls atomic.Int32
		executedMu      sync.Mutex
		executed        []string
	)
	definition := AgentDefinition{
		Name: "resume-test",
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			call := modelCalls.Add(1)
			if call == 1 {
				return newStaticAssistantStream(Message{
					Role: RoleAssistant,
					ToolCalls: []ToolCall{
						{ID: "first", Name: "one"},
						{ID: "second", Name: "two"},
					},
					StopReason: StopReasonToolUse,
					Timestamp:  time.Now().UTC(),
				}, nil), nil
			}
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				Parts:      []Part{{Type: PartTypeText, Text: "complete"}},
				StopReason: StopReasonStop,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		ToolResolver: func(context.Context, AgentSnapshot) ([]ToolDefinition, error) {
			version := toolResolutions.Add(1)
			makeTool := func(name string) ToolDefinition {
				return ToolDefinition{Name: name, Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
					executedMu.Lock()
					executed = append(executed, fmt.Sprintf("%s@%d", name, version))
					executedMu.Unlock()
					return ToolResult{Content: []Part{{Type: PartTypeText, Text: name}}}, nil
				}}
			}
			return []ToolDefinition{makeTool("one"), makeTool("two")}, nil
		},
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	resolveDefinition := func(_ context.Context, current AgentDefinition, _ AgentSnapshot) (AgentDefinition, error) {
		definitionCalls.Add(1)
		return current, nil
	}

	initial := runner.RunWithHooks(context.Background(), AgentSnapshot{}, []Message{NewUserTextMessage("run")}, LoopHooks{
		ResolveDefinition: resolveDefinition,
		ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
			return ToolGateResult{Action: ToolGateActionSuspend}, nil
		},
	})
	initialEvents, suspendedSnapshot, initialErr := collectToolGateStream(t, initial)
	var initialSuspended *ToolCallsSuspendedError
	if !errors.As(initialErr, &initialSuspended) || len(initialSuspended.Calls) != 2 {
		t.Fatalf("expected both initial calls suspended, got %v (%+v)", initialErr, initialSuspended)
	}
	assertGateLifecycle(t, initialEvents, 1, 0)

	partial := runner.ResumePendingToolCallsWithHooks(context.Background(), suspendedSnapshot, LoopHooks{
		ResolveDefinition: resolveDefinition,
		ToolGate: func(_ context.Context, input BeforeToolCallContext) (ToolGateResult, error) {
			if input.ToolCall.ID == "second" {
				return ToolGateResult{Action: ToolGateActionSuspend}, nil
			}
			return ToolGateResult{Action: ToolGateActionAllow}, nil
		},
	})
	partialEvents, partialSnapshot, partialErr := collectToolGateStream(t, partial)
	var partialSuspended *ToolCallsSuspendedError
	if !errors.As(partialErr, &partialSuspended) || len(partialSuspended.Calls) != 1 || partialSuspended.Calls[0].ToolCall.ID != "second" {
		t.Fatalf("expected targeted re-suspension, got %v (%+v)", partialErr, partialSuspended)
	}
	if len(executed) != 0 || len(partialSnapshot.Messages) != 2 || len(partialSnapshot.PendingToolCalls) != 2 {
		t.Fatalf("partial decision executed or mutated batch: executed=%v snapshot=%+v", executed, partialSnapshot)
	}
	assertGateLifecycle(t, partialEvents, 0, 0)

	resumed := runner.ResumePendingToolCallsWithHooks(context.Background(), partialSnapshot, LoopHooks{
		ResolveDefinition: resolveDefinition,
		ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
			return ToolGateResult{Action: ToolGateActionAllow}, nil
		},
	})
	events, finalSnapshot, resumeErr := collectToolGateStream(t, resumed)
	if resumeErr != nil {
		t.Fatalf("resume returned error: %v", resumeErr)
	}
	executedMu.Lock()
	gotExecuted := append([]string(nil), executed...)
	executedMu.Unlock()
	slices.Sort(gotExecuted)
	if fmt.Sprint(gotExecuted) != fmt.Sprint([]string{"one@3", "two@3"}) {
		t.Fatalf("resume did not execute the currently resolved tools: %v (resolutions=%d)", gotExecuted, toolResolutions.Load())
	}
	if modelCalls.Load() != 2 || definitionCalls.Load() != 4 || toolResolutions.Load() != 4 {
		t.Fatalf("unexpected dynamic resolution/model counts: model=%d definition=%d tools=%d", modelCalls.Load(), definitionCalls.Load(), toolResolutions.Load())
	}
	if len(finalSnapshot.Messages) != 5 {
		t.Fatalf("resume duplicated or lost transcript messages: %+v", finalSnapshot.Messages)
	}
	var assistantCount int
	for _, message := range finalSnapshot.Messages {
		if message.Role == RoleAssistant {
			assistantCount++
		}
	}
	if assistantCount != 2 || finalSnapshot.Messages[2].Role != RoleTool || finalSnapshot.Messages[3].Role != RoleTool || finalSnapshot.Messages[4].Role != RoleAssistant {
		t.Fatalf("unexpected resumed transcript roles: %+v", finalSnapshot.Messages)
	}
	if len(finalSnapshot.PendingToolCalls) != 0 || finalSnapshot.Error != "" {
		t.Fatalf("successful resume retained control state: %+v", finalSnapshot)
	}
	assertGateLifecycle(t, events, 1, 2)
	if len(events) == 0 || events[0].Type != EventAgentStart {
		t.Fatalf("resume did not begin with agent_start: %v", runnerEventTypes(events))
	}
	for _, event := range events {
		if event.Type == EventTurnStart {
			break
		}
		if event.Type == EventToolExecutionStart {
			goto sawToolBeforeNextTurn
		}
	}
	t.Fatal("resume did not execute the pending tool batch before the next turn_start")
sawToolBeforeNextTurn:
	for _, event := range events {
		if event.Type == EventAgentEnd {
			if len(event.Messages) != 3 || event.Messages[0].Role != RoleTool || event.Messages[1].Role != RoleTool || event.Messages[2].Role != RoleAssistant {
				t.Fatalf("resume agent_end lost new-message window: %+v", event.Messages)
			}
		}
	}
}

func TestResumePendingToolCallsPreservesMaxTurnBudgetAcrossAttempts(t *testing.T) {
	var modelCalls atomic.Int32
	definition := AgentDefinition{
		MaxTurns: 1,
		Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
			modelCalls.Add(1)
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				ToolCalls:  []ToolCall{{ID: "call", Name: "echo"}},
				StopReason: StopReasonToolUse,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}},
		Tools: []ToolDefinition{{Name: "echo", Execute: func(context.Context, string, any, ToolUpdateFunc) (ToolResult, error) {
			return ToolResult{}, nil
		}}},
	}
	runner, err := NewRunner(definition)
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	initial := runner.RunWithHooks(context.Background(), AgentSnapshot{}, []Message{NewUserTextMessage("run")}, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionSuspend}, nil
	}})
	_, suspended, suspendErr := collectToolGateStream(t, initial)
	var controlErr *ToolCallsSuspendedError
	if !errors.As(suspendErr, &controlErr) {
		t.Fatalf("expected suspension, got %v", suspendErr)
	}

	resume := runner.ResumePendingToolCallsWithHooks(context.Background(), suspended, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
		return ToolGateResult{Action: ToolGateActionAllow}, nil
	}})
	events, finalSnapshot, resumeErr := collectToolGateStream(t, resume)
	if !errors.Is(resumeErr, ErrMaxTurnsExceeded) {
		t.Fatalf("expected preserved max-turn failure, got %v", resumeErr)
	}
	if modelCalls.Load() != 1 {
		t.Fatalf("resume silently reset turn budget and called model %d times", modelCalls.Load())
	}
	if len(finalSnapshot.Messages) != 3 || finalSnapshot.Messages[2].Role != RoleTool || len(finalSnapshot.PendingToolCalls) != 0 {
		t.Fatalf("resume did not complete the pending tool before enforcing turn budget: %+v", finalSnapshot)
	}
	assertGateLifecycle(t, events, 1, 2)
}

func collectToolGateStream(t *testing.T, stream *RunStream) ([]AgentEvent, AgentSnapshot, error) {
	t.Helper()
	var events []AgentEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	snapshot, err := stream.Wait()
	return events, snapshot, err
}

func assertGateLifecycle(t *testing.T, events []AgentEvent, wantTurnStarts, wantTurnEnds int) {
	t.Helper()
	var agentStarts, agentEnds, turnStarts, turnEnds int
	for _, event := range events {
		switch event.Type {
		case EventAgentStart:
			agentStarts++
		case EventAgentEnd:
			agentEnds++
		case EventTurnStart:
			turnStarts++
		case EventTurnEnd:
			turnEnds++
		}
	}
	if agentStarts != 1 || agentEnds != 1 || turnStarts != wantTurnStarts || turnEnds != wantTurnEnds {
		t.Fatalf("unbalanced lifecycle: agent=%d/%d turns=%d/%d events=%v", agentStarts, agentEnds, turnStarts, turnEnds, runnerEventTypes(events))
	}
}

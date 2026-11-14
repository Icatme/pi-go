package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func listenerResultFixture(t *testing.T, mode ToolExecutionMode, rounds int) (*Agent, *int) {
	t.Helper()
	executions := 0
	requests := 0
	a, err := NewAgent(AgentDefinition{ToolExecution: mode, Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
		requests++
		m := Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}
		if requests <= rounds {
			m.StopReason = StopReasonToolUse
			m.ToolCalls = []ToolCall{{ID: "a", Name: "x"}, {ID: "b", Name: "x"}}
		}
		return newStaticAssistantStream(m, nil), nil
	}}, Tools: []ToolDefinition{{Name: "x", Execute: func(_ context.Context, c ToolExecutionContext) (ToolResult, error) {
		executions++
		return ToolResult{Content: []Part{{Type: PartTypeText, Text: c.ToolCall.ID}}, Details: map[string]any{"value": c.ToolCall.ID}}, nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	return a, &executions
}
func listenerResultIDs(a *Agent) []string {
	var ids []string
	for _, m := range a.Snapshot().Messages {
		if m.ToolResult != nil {
			ids = append(ids, m.ToolResult.ToolCallID)
		}
	}
	return ids
}
func expectListenerPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if v := recover(); v != "listener fixture" {
			t.Fatalf("panic=%v", v)
		}
	}()
	fn()
	t.Fatal("missing panic")
}

func TestAgentListenerPanicPreservesSequentialCompletedResults(t *testing.T) {
	for _, at := range []string{"first-end", "second-start", "second-end", "first-message-end", "second-message-end"} {
		t.Run(at, func(t *testing.T) {
			a, executions := listenerResultFixture(t, ToolExecutionSequential, 1)
			var messageEvents []string
			a.Subscribe(func(e AgentEvent) {
				if e.Type == EventMessageEnd && e.Message != nil && e.Message.ToolResult != nil {
					messageEvents = append(messageEvents, e.Message.ToolResult.ToolCallID)
				}
				fail := at == "first-end" && e.Type == EventToolExecutionEnd && e.ToolCallID == "a" || at == "second-start" && e.Type == EventToolExecutionStart && e.ToolCallID == "b" || at == "second-end" && e.Type == EventToolExecutionEnd && e.ToolCallID == "b" || at == "first-message-end" && e.Type == EventMessageEnd && e.Message != nil && e.Message.ToolResult != nil && e.Message.ToolResult.ToolCallID == "a" || at == "second-message-end" && e.Type == EventMessageEnd && e.Message != nil && e.Message.ToolResult != nil && e.Message.ToolResult.ToolCallID == "b"
				if fail {
					if e.ToolResult != nil {
						e.ToolResult.Content[0].Text = "mutated"
						e.ToolResult.Details.(map[string]any)["value"] = "mutated"
					}
					panic("listener fixture")
				}
			})
			expectListenerPanic(t, func() { _ = a.Prompt(context.Background(), NewUserTextMessage("go")) })
			want := []string{"a", "b"}
			wantN := 2
			if at == "first-end" || at == "second-start" {
				want = []string{"a"}
				wantN = 1
			}
			if !reflect.DeepEqual(listenerResultIDs(a), want) || *executions != wantN {
				t.Fatalf("results%v executions%d", listenerResultIDs(a), *executions)
			}
			if a.State().IsStreaming {
				t.Fatal("run ownership not released")
			}
			for _, m := range a.Snapshot().Messages {
				if m.ToolResult != nil {
					if m.ToolResult.Content[0].Text != m.ToolResult.ToolCallID || m.ToolResult.Details.(map[string]any)["value"] != m.ToolResult.ToolCallID {
						t.Fatal("listener mutation reached snapshot")
					}
				}
			}
			if at == "second-end" && len(messageEvents) != 0 {
				t.Fatal("rescue emitted message events")
			}
		})
	}
}

func TestAgentListenerResultsNormalOrderAndRepeatedBatchIDs(t *testing.T) {
	a, executions := listenerResultFixture(t, ToolExecutionSequential, 2)
	var order []string
	a.Subscribe(func(e AgentEvent) {
		if e.Type == EventToolExecutionEnd {
			order = append(order, "end:"+e.ToolCallID)
		}
		if e.Type == EventMessageEnd && e.Message != nil && e.Message.ToolResult != nil {
			order = append(order, "message:"+e.Message.ToolResult.ToolCallID)
		}
	})
	if err := a.Prompt(context.Background(), NewUserTextMessage("go")); err != nil {
		t.Fatal(err)
	}
	want := []string{"end:a", "end:b", "message:a", "message:b", "end:a", "end:b", "message:a", "message:b"}
	if !reflect.DeepEqual(order, want) || *executions != 4 {
		t.Fatalf("events%v executions%d", order, *executions)
	}
	if !reflect.DeepEqual(listenerResultIDs(a), []string{"a", "b", "a", "b"}) {
		t.Fatal(listenerResultIDs(a))
	}
}

func TestAgentListenerResultsKeepFinalError(t *testing.T) {
	a, _ := listenerResultFixture(t, ToolExecutionSequential, 1)
	a.definition.Tools[0].Execute = func(context.Context, ToolExecutionContext) (ToolResult, error) {
		return ToolResult{}, errors.New("tool denied fixture")
	}
	a.Subscribe(func(e AgentEvent) {
		if e.Type == EventToolExecutionEnd {
			panic("listener fixture")
		}
	})
	expectListenerPanic(t, func() { _ = a.Prompt(context.Background(), NewUserTextMessage("go")) })
	for _, m := range a.Snapshot().Messages {
		if m.ToolResult != nil {
			if !m.ToolResult.IsError || m.ToolResult.Failure == nil {
				t.Fatalf("error facts lost: %+v", m.ToolResult)
			}
			return
		}
	}
	t.Fatal("missing error result")
}

func TestAgentListenerResultsPermissionDenialIsNotSuccess(t *testing.T) {
	a, executions := listenerResultFixture(t, ToolExecutionSequential, 1)
	a.definition.BeforeToolCall = func(context.Context, BeforeToolCallContext) (BeforeToolCallResult, error) {
		return BeforeToolCallResult{Block: true, Reason: "approval denied"}, nil
	}
	a.Subscribe(func(e AgentEvent) {
		if e.Type == EventToolExecutionEnd && e.ToolCallID == "b" {
			panic("listener fixture")
		}
	})
	expectListenerPanic(t, func() { _ = a.Prompt(context.Background(), NewUserTextMessage("go")) })
	if *executions != 0 || !reflect.DeepEqual(listenerResultIDs(a), []string{"a", "b"}) {
		t.Fatalf("executions%d results%v", *executions, listenerResultIDs(a))
	}
	for _, m := range a.Snapshot().Messages {
		if m.ToolResult != nil && !m.ToolResult.IsError {
			t.Fatal("denied result became success")
		}
	}
}

func TestAgentListenerResultsUseResolvedExecutionMode(t *testing.T) {
	for _, initial := range []ToolExecutionMode{ToolExecutionSequential, ToolExecutionParallel} {
		t.Run(string(initial), func(t *testing.T) {
			a, _ := listenerResultFixture(t, initial, 1)
			effective := ToolExecutionSequential
			if initial == effective {
				effective = ToolExecutionParallel
			}
			// A parallel run uses no executor body state in this control.
			a.definition.Tools[0].Execute = func(context.Context, ToolExecutionContext) (ToolResult, error) { return ToolResult{}, nil }
			a.resolver = func(_ context.Context, _ AgentSnapshot) (AgentDefinition, error) {
				d := a.definition
				d.ToolExecution = effective
				return d, nil
			}
			a.Subscribe(func(e AgentEvent) {
				if e.Type == EventToolExecutionEnd {
					a.mu.RLock()
					buffered := a.listenerResults != nil
					a.mu.RUnlock()
					if buffered != (effective == ToolExecutionSequential) {
						t.Errorf("buffer=%v effective=%s", buffered, effective)
					}
				}
			})
			if err := a.Prompt(context.Background(), NewUserTextMessage("go")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentListenerResultsExcludeChildAndWrongOwner(t *testing.T) {
	a, _ := listenerResultFixture(t, ToolExecutionSequential, 1)
	_, _, _, owner, err := a.startRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer a.releaseInterruptedRun(owner)
	call := ToolCall{ID: "same", Name: "x"}
	a.processLoopEvent(AgentEvent{Type: EventMessageEnd, Message: &Message{Role: RoleAssistant, ToolCalls: []ToolCall{call}}})
	result := ToolResult{Content: []Part{{Type: PartTypeText, Text: "child only"}}}
	a.processLoopEvent(AgentEvent{Type: EventToolExecutionEnd, ToolCall: &call, ToolResult: &result, ParentToolCallID: "parent"})
	a.flushListenerResults(owner)
	if len(listenerResultIDs(a)) != 0 {
		t.Fatal("child promoted")
	}
	a.mu.Lock()
	a.newListenerResultBatchLocked(Message{Role: RoleAssistant, ToolCalls: []ToolCall{call}})
	a.mu.Unlock()
	a.processLoopEvent(AgentEvent{Type: EventToolExecutionEnd, ToolCall: &call, ToolResult: &result})
	a.flushListenerResults(make(chan struct{}))
	if len(listenerResultIDs(a)) != 0 {
		t.Fatal("wrong owner flushed")
	}
}

func TestAgentListenerResultsSecondBatchPanicKeepsRepeatedIDs(t *testing.T) {
	a, executions := listenerResultFixture(t, ToolExecutionSequential, 2)
	ends := 0
	a.Subscribe(func(e AgentEvent) {
		if e.Type == EventToolExecutionEnd {
			ends++
			if ends == 4 {
				panic("listener fixture")
			}
		}
	})
	expectListenerPanic(t, func() { _ = a.Prompt(context.Background(), NewUserTextMessage("go")) })
	if *executions != 4 || !reflect.DeepEqual(listenerResultIDs(a), []string{"a", "b", "a", "b"}) {
		t.Fatalf("executions%d results%v", *executions, listenerResultIDs(a))
	}
}

func TestAgentListenerResultsFinalizedHookFailureAndMessageStart(t *testing.T) {
	for _, hookFail := range []bool{false, true} {
		t.Run(map[bool]string{false: "message-start", true: "hook-failure"}[hookFail], func(t *testing.T) {
			a, n := listenerResultFixture(t, ToolExecutionSequential, 1)
			if hookFail {
				a.definition.AfterToolCall = func(context.Context, AfterToolCallContext) (AfterToolCallResult, error) {
					return AfterToolCallResult{}, errors.New("private output withheld")
				}
			}
			a.Subscribe(func(e AgentEvent) {
				if hookFail && e.Type == EventToolExecutionEnd && e.ToolCallID == "b" {
					panic("listener fixture")
				}
				if !hookFail && e.Type == EventMessageStart && e.Message != nil && e.Message.ToolResult != nil {
					panic("listener fixture")
				}
			})
			expectListenerPanic(t, func() { _ = a.Prompt(context.Background(), NewUserTextMessage("go")) })
			if *n != 2 || !reflect.DeepEqual(listenerResultIDs(a), []string{"a", "b"}) {
				t.Fatalf("executions=%d ids=%v", *n, listenerResultIDs(a))
			}
			for _, m := range a.Snapshot().Messages {
				if m.ToolResult != nil && hookFail {
					if !m.ToolResult.IsError || m.ToolResult.Failure == nil {
						t.Fatalf("hook failed became success %+v", m.ToolResult)
					}
				}
			}
		})
	}
}

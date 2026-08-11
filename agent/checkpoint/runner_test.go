package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
)

func TestRunnerInterruptsBeforeToolSideEffectsAndReturnsDurableClone(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(agent.ToolCall{ID: "write-1", OriginalID: "provider-1", Name: "write", Arguments: json.RawMessage(`{"value":"one"}`)}),
		textMessage("done"),
	}}
	var executions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "approval-agent",
			Model: model,
			Tools: []agent.ToolDefinition{{
				Name: "write",
				Execute: func(context.Context, string, any, agent.ToolUpdateFunc) (agent.ToolResult, error) {
					executions.Add(1)
					return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "written"}}}, nil
				},
			}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy: func(_ context.Context, request ToolApprovalRequest) (ApprovalRequirement, error) {
			if got := string(request.Arguments); got != `{"value":"one"}` {
				return ApprovalRequirement{}, fmt.Errorf("policy received non-canonical final args %s", got)
			}
			request.Arguments[0] = '['
			return ApprovalRequirement{Required: true, Message: "confirm write"}, nil
		},
	})

	snapshot := agent.AgentSnapshot{Metadata: map[string]any{"nested": map[string]any{"value": "original"}}}
	stream := runner.Run(context.Background(), "interrupt-clone", snapshot, []agent.Message{agent.NewUserTextMessage("write")})
	outcome, err, events := awaitCheckpoint(stream)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if outcome.Status != StatusInterrupted || outcome.Revision != 2 || len(outcome.Interrupts) != 1 {
		t.Fatalf("unexpected interrupted outcome: %+v", outcome)
	}
	if !outcome.Persisted {
		t.Fatal("interrupted outcome was not marked persisted")
	}
	if executions.Load() != 0 {
		t.Fatalf("tool executed before approval: %d", executions.Load())
	}
	if outcome.Snapshot.Error != "" || len(outcome.Snapshot.PendingToolCalls) != 1 {
		t.Fatalf("suspension was not durable: %+v", outcome.Snapshot)
	}
	if got := string(outcome.Interrupts[0].Tool.Arguments); got != `{"value":"one"}` {
		t.Fatalf("policy mutation leaked into interrupt: %s", got)
	}
	assertSingleCleanAgentEnd(t, events)

	outcome.Snapshot.Metadata["nested"].(map[string]any)["value"] = "mutated"
	outcome.Interrupts[0].Tool.Arguments[0] = '['
	again, againErr := stream.Wait()
	if againErr != nil {
		t.Fatalf("second Wait returned error: %v", againErr)
	}
	if got := again.Snapshot.Metadata["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("Wait outcomes alias snapshot: %v", got)
	}
	if got := string(again.Interrupts[0].Tool.Arguments); got != `{"value":"one"}` {
		t.Fatalf("Wait outcomes alias interrupt args: %s", got)
	}
}

func TestNewRunnerRejectsDynamicToolResolver(t *testing.T) {
	_, err := NewRunner(RunnerConfig{
		Definition: agent.AgentDefinition{
			Model: &scriptedModel{},
			ToolResolver: func(context.Context, agent.AgentSnapshot) ([]agent.ToolDefinition, error) {
				return nil, nil
			},
		},
		DefinitionVersion: "v1",
		Store:             NewMemoryStore(),
	})
	if err == nil {
		t.Fatal("expected dynamic ToolResolver to be rejected")
	}
}

func TestNewRunnerRejectsPrepareNextTurn(t *testing.T) {
	_, err := NewRunner(RunnerConfig{
		Definition: agent.AgentDefinition{
			Model: &scriptedModel{},
			PrepareNextTurn: func(context.Context, agent.PrepareNextTurnContext) (*agent.AgentLoopTurnUpdate, error) {
				return nil, nil
			},
		},
		DefinitionVersion: "v1",
		Store:             NewMemoryStore(),
	})
	if err == nil {
		t.Fatal("expected PrepareNextTurn to be rejected")
	}
}

func TestNewRunnerRejectsTypedNilStore(t *testing.T) {
	var store *MemoryStore
	_, err := NewRunner(RunnerConfig{
		Definition:        agent.AgentDefinition{Model: &scriptedModel{}},
		DefinitionVersion: "v1",
		Store:             store,
	})
	if err == nil {
		t.Fatal("expected typed-nil store rejection")
	}
}

func TestNewCheckpointIDIsRandom(t *testing.T) {
	first, err := NewCheckpointID()
	if err != nil {
		t.Fatalf("first checkpoint ID: %v", err)
	}
	second, err := NewCheckpointID()
	if err != nil {
		t.Fatalf("second checkpoint ID: %v", err)
	}
	if len(first) != 32 || len(second) != 32 || first == second {
		t.Fatalf("unexpected checkpoint IDs %q and %q", first, second)
	}
}

func TestApprovalPolicyCanAllowWithoutInterrupt(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(agent.ToolCall{ID: "call", Name: "tool", Arguments: json.RawMessage(`{}`)}),
		textMessage("done"),
	}}
	var executions atomic.Int64
	var policyCalls atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "allow-agent",
			Model: model,
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(&executions)}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy: func(context.Context, ToolApprovalRequest) (ApprovalRequirement, error) {
			policyCalls.Add(1)
			return ApprovalRequirement{Required: false}, nil
		},
	})
	outcome, err, _ := awaitCheckpoint(runner.Run(context.Background(), "allow", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil || outcome.Status != StatusCompleted || outcome.Revision != 2 || executions.Load() != 1 || policyCalls.Load() != 1 {
		t.Fatalf("policy allow failed: outcome=%+v executions=%d policy=%d err=%v", outcome, executions.Load(), policyCalls.Load(), err)
	}
}

func TestApprovalDecisionIsConsumedBeforeAnIdenticalLaterTurn(t *testing.T) {
	store := NewMemoryStore()
	call := agent.ToolCall{ID: "same-call", Name: "tool", Arguments: json.RawMessage(`{}`)}
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(call),
		toolCallMessage(call),
		textMessage("done"),
	}}
	var executions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "one-shot-approval",
			Model: model,
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(&executions)}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy:    requireApproval,
	})
	initial, err, _ := awaitCheckpoint(runner.Run(context.Background(), "one-shot", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil {
		t.Fatalf("initial interruption: %v", err)
	}
	secondTurn, err, _ := awaitCheckpoint(runner.Resume(context.Background(), "one-shot", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		initial.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if err != nil {
		t.Fatalf("resume first approval: %v", err)
	}
	if secondTurn.Status != StatusInterrupted || secondTurn.Revision != 4 || len(secondTurn.Interrupts) != 1 || executions.Load() != 1 {
		t.Fatalf("identical later call reused approval: outcome=%+v executions=%d", secondTurn, executions.Load())
	}
	if secondTurn.Interrupts[0].ID == initial.Interrupts[0].ID {
		t.Fatal("later turn reused the original interrupt ID")
	}
}

func TestApprovedSiblingIsRetainedWhenBatchReinterruptsBeforeExecution(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(
			agent.ToolCall{ID: "one", Name: "one", Arguments: json.RawMessage(`{}`)},
			agent.ToolCall{ID: "two", Name: "two", Arguments: json.RawMessage(`{}`)},
		),
		textMessage("done"),
	}}
	var requireTwo atomic.Bool
	var oneExecutions atomic.Int64
	var twoExecutions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:          "sibling-reinterrupt",
			Model:         model,
			ToolExecution: agent.ToolExecutionSequential,
			Tools: []agent.ToolDefinition{
				{Name: "one", Execute: countingTool(&oneExecutions)},
				{Name: "two", Execute: countingTool(&twoExecutions)},
			},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy: func(_ context.Context, request ToolApprovalRequest) (ApprovalRequirement, error) {
			return ApprovalRequirement{Required: request.ToolName == "one" || requireTwo.Load()}, nil
		},
	})
	initial, err, _ := awaitCheckpoint(runner.Run(context.Background(), "sibling-reinterrupt", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil || len(initial.Interrupts) != 1 || initial.Interrupts[0].Tool.ToolName != "one" {
		t.Fatalf("unexpected initial approval: outcome=%+v err=%v", initial, err)
	}
	requireTwo.Store(true)
	reinterrupted, err, _ := awaitCheckpoint(runner.Resume(context.Background(), "sibling-reinterrupt", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		initial.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if err != nil || reinterrupted.Status != StatusInterrupted || len(reinterrupted.Interrupts) != 1 || reinterrupted.Interrupts[0].Tool.ToolName != "two" {
		t.Fatalf("unexpected sibling re-interruption: outcome=%+v err=%v", reinterrupted, err)
	}
	if oneExecutions.Load() != 0 || twoExecutions.Load() != 0 {
		t.Fatalf("fail-closed batch executed before all approvals: one=%d two=%d", oneExecutions.Load(), twoExecutions.Load())
	}
	completed, err, _ := awaitCheckpoint(runner.Resume(context.Background(), "sibling-reinterrupt", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		reinterrupted.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if err != nil || completed.Status != StatusCompleted || oneExecutions.Load() != 1 || twoExecutions.Load() != 1 {
		t.Fatalf("approved sibling was not retained: outcome=%+v one=%d two=%d err=%v", completed, oneExecutions.Load(), twoExecutions.Load(), err)
	}
}

func TestTransientApprovalPolicyErrorRestoresInterruptedCheckpoint(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(
			agent.ToolCall{ID: "one", Name: "one", Arguments: json.RawMessage(`{}`)},
			agent.ToolCall{ID: "two", Name: "two", Arguments: json.RawMessage(`{}`)},
		),
		textMessage("done"),
	}}
	policyErr := errors.New("approval policy temporarily unavailable")
	var failTwo atomic.Bool
	var oneExecutions atomic.Int64
	var twoExecutions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:          "transient-policy",
			Model:         model,
			ToolExecution: agent.ToolExecutionSequential,
			Tools: []agent.ToolDefinition{
				{Name: "one", Execute: countingTool(&oneExecutions)},
				{Name: "two", Execute: countingTool(&twoExecutions)},
			},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy: func(_ context.Context, request ToolApprovalRequest) (ApprovalRequirement, error) {
			if request.ToolName == "one" {
				return ApprovalRequirement{Required: true}, nil
			}
			if failTwo.CompareAndSwap(true, false) {
				return ApprovalRequirement{}, policyErr
			}
			return ApprovalRequirement{}, nil
		},
	})

	initial, err, _ := awaitCheckpoint(runner.Run(context.Background(), "transient-policy", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil || initial.Status != StatusInterrupted || initial.Revision != 2 || len(initial.Interrupts) != 1 {
		t.Fatalf("unexpected initial interruption: outcome=%+v err=%v", initial, err)
	}
	oldID := initial.Interrupts[0].ID
	failTwo.Store(true)
	restored, resumeErr, events := awaitCheckpoint(runner.Resume(context.Background(), "transient-policy", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		oldID: {Action: DecisionActionApprove},
	}}))
	if !errors.Is(resumeErr, policyErr) || restored.Status != StatusInterrupted || !restored.Persisted || restored.Revision != 4 || len(restored.Interrupts) != 1 {
		t.Fatalf("transient policy error was not restored durably: outcome=%+v err=%v", restored, resumeErr)
	}
	newID := restored.Interrupts[0].ID
	if newID == "" || newID == oldID {
		t.Fatalf("restored interrupt ID was not rotated: old=%q new=%q", oldID, newID)
	}
	if oneExecutions.Load() != 0 || twoExecutions.Load() != 0 {
		t.Fatalf("tool executed before policy preflight completed: one=%d two=%d", oneExecutions.Load(), twoExecutions.Load())
	}
	if len(events) == 0 || events[len(events)-1].Type != agent.EventAgentEnd || !errors.Is(events[len(events)-1].Err, policyErr) {
		t.Fatalf("AgentEnd did not retain the policy error: %+v", events)
	}
	record, err := store.Load(context.Background(), "transient-policy")
	if err != nil {
		t.Fatalf("load restored checkpoint: %v", err)
	}
	persisted, err := decodeStoredCheckpoint("transient-policy", record)
	if err != nil || persisted.Status != StatusInterrupted || persisted.Revision != 4 || len(persisted.Decisions) != 0 || persisted.Error != "" {
		t.Fatalf("unexpected restored envelope: %+v err=%v", persisted, err)
	}

	stale, staleErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "transient-policy", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		oldID: {Action: DecisionActionApprove},
	}}))
	if !errors.Is(staleErr, ErrInvalidResume) || stale.Status != StatusInterrupted || stale.Revision != 4 {
		t.Fatalf("old interrupt ID was not stale: outcome=%+v err=%v", stale, staleErr)
	}
	completed, err, _ := awaitCheckpoint(runner.Resume(context.Background(), "transient-policy", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		newID: {Action: DecisionActionApprove},
	}}))
	if err != nil || completed.Status != StatusCompleted || completed.Revision != 6 || oneExecutions.Load() != 1 || twoExecutions.Load() != 1 {
		t.Fatalf("rotated approval did not re-enter the gate: outcome=%+v one=%d two=%d err=%v", completed, oneExecutions.Load(), twoExecutions.Load(), err)
	}
}

func TestPolicyErrorForNewBatchDoesNotRestoreOldInterrupts(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(agent.ToolCall{ID: "old-call", Name: "tool", Arguments: json.RawMessage(`{"batch":"old"}`)}),
		toolCallMessage(agent.ToolCall{ID: "new-call", Name: "tool", Arguments: json.RawMessage(`{"batch":"new"}`)}),
	}}
	policyErr := errors.New("new batch policy failed")
	var executions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "new-batch-error",
			Model: model,
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(&executions)}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy: func(_ context.Context, request ToolApprovalRequest) (ApprovalRequirement, error) {
			if bytes.Equal(request.Arguments, []byte(`{"batch":"new"}`)) {
				return ApprovalRequirement{}, policyErr
			}
			return ApprovalRequirement{Required: true}, nil
		},
	})

	initial, err, _ := awaitCheckpoint(runner.Run(context.Background(), "new-batch-error", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil || initial.Status != StatusInterrupted || len(initial.Interrupts) != 1 {
		t.Fatalf("unexpected initial interruption: outcome=%+v err=%v", initial, err)
	}
	outcome, resumeErr, events := awaitCheckpoint(runner.Resume(context.Background(), "new-batch-error", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		initial.Interrupts[0].ID: {Action: DecisionActionReject, Reason: "reject old batch"},
	}}))
	if !errors.Is(resumeErr, ErrOutcomeIndeterminate) || !errors.Is(resumeErr, policyErr) || outcome.Status != StatusIndeterminate || outcome.Persisted {
		t.Fatalf("new batch error did not take the conservative path: outcome=%+v err=%v", outcome, resumeErr)
	}
	if executions.Load() != 0 {
		t.Fatalf("rejected/new batch executed a tool: %d", executions.Load())
	}
	if len(events) == 0 || events[len(events)-1].Type != agent.EventAgentEnd || !errors.Is(events[len(events)-1].Err, ErrOutcomeIndeterminate) {
		t.Fatalf("AgentEnd did not expose the indeterminate result: %+v", events)
	}
	record, err := store.Load(context.Background(), "new-batch-error")
	if err != nil {
		t.Fatalf("load running claim: %v", err)
	}
	persisted, err := decodeStoredCheckpoint("new-batch-error", record)
	if err != nil || persisted.Status != StatusRunning || persisted.Revision != 3 || len(persisted.Interrupts) != 0 {
		t.Fatalf("old interrupts were persisted against the new batch: %+v err=%v", persisted, err)
	}
}

func TestWhitespaceInRawArgumentsDoesNotInvalidateApproval(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(agent.ToolCall{ID: "call", Name: "tool", Arguments: json.RawMessage(`{ "value" : "one" }`)}),
		textMessage("done"),
	}}
	var executions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "whitespace",
			Model: model,
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(&executions)}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy:    requireApproval,
	})
	interrupted, err, _ := awaitCheckpoint(runner.Run(context.Background(), "whitespace", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil {
		t.Fatalf("initial interruption: %v", err)
	}
	completed, err, _ := awaitCheckpoint(runner.Resume(context.Background(), "whitespace", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		interrupted.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if err != nil || completed.Status != StatusCompleted || executions.Load() != 1 {
		t.Fatalf("whitespace changed approval binding: outcome=%+v executions=%d err=%v", completed, executions.Load(), err)
	}
}

func TestRunnerPartialDecisionsRotateIDsAndTargetBatch(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(
			agent.ToolCall{ID: "one-1", Name: "one", Arguments: json.RawMessage(`{}`)},
			agent.ToolCall{ID: "two-1", Name: "two", Arguments: json.RawMessage(`{}`)},
		),
		textMessage("done"),
	}}
	var oneExecutions atomic.Int64
	var twoExecutions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:          "batch-agent",
			Model:         model,
			ToolExecution: agent.ToolExecutionSequential,
			Tools: []agent.ToolDefinition{
				{Name: "one", Execute: countingTool(&oneExecutions)},
				{Name: "two", Execute: countingTool(&twoExecutions)},
			},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy:    requireApproval,
	})

	initial, err, _ := awaitCheckpoint(runner.Run(context.Background(), "partial", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("batch")}))
	if err != nil {
		t.Fatalf("initial run: %v", err)
	}
	if len(initial.Interrupts) != 2 {
		t.Fatalf("expected two interrupts: %+v", initial)
	}
	firstID := initial.Interrupts[0].ID
	secondOldID := initial.Interrupts[1].ID

	partial, err, partialEvents := awaitCheckpoint(runner.Resume(context.Background(), "partial", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		firstID: {Action: DecisionActionApprove},
	}}))
	if err != nil {
		t.Fatalf("partial resume: %v", err)
	}
	if partial.Status != StatusInterrupted || partial.Revision != 3 || len(partial.Interrupts) != 1 {
		t.Fatalf("unexpected partial outcome: %+v", partial)
	}
	secondNewID := partial.Interrupts[0].ID
	if secondNewID == secondOldID || secondNewID == "" {
		t.Fatalf("unresolved interrupt ID was not rotated: old=%q new=%q", secondOldID, secondNewID)
	}
	if oneExecutions.Load() != 0 || twoExecutions.Load() != 0 {
		t.Fatalf("partial decision executed tools: one=%d two=%d", oneExecutions.Load(), twoExecutions.Load())
	}
	assertSyntheticLifecycle(t, partialEvents)

	stale, staleErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "partial", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		secondOldID: {Action: DecisionActionReject},
	}}))
	if !errors.Is(staleErr, ErrInvalidResume) || stale.Revision != 3 {
		t.Fatalf("expected stale decision rejection at revision 3, outcome=%+v err=%v", stale, staleErr)
	}
	unknown, unknownErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "partial", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		"unknown": {Action: DecisionActionApprove},
	}}))
	if !errors.Is(unknownErr, ErrInvalidResume) || unknown.Revision != 3 {
		t.Fatalf("expected unknown decision rejection, outcome=%+v err=%v", unknown, unknownErr)
	}
	empty, emptyErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "partial", ResumeParams{}))
	if !errors.Is(emptyErr, ErrInvalidResume) || empty.Revision != 3 {
		t.Fatalf("expected empty decision rejection, outcome=%+v err=%v", empty, emptyErr)
	}
	invalid, invalidErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "partial", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		secondNewID: {Action: "later"},
	}}))
	if !errors.Is(invalidErr, ErrInvalidResume) || invalid.Revision != 3 {
		t.Fatalf("expected invalid action rejection, outcome=%+v err=%v", invalid, invalidErr)
	}

	completed, err, _ := awaitCheckpoint(runner.Resume(context.Background(), "partial", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		secondNewID: {Action: DecisionActionReject, Reason: "operator denied"},
	}}))
	if err != nil {
		t.Fatalf("final resume: %v", err)
	}
	if completed.Status != StatusCompleted || completed.Revision != 5 {
		t.Fatalf("unexpected completed outcome: %+v", completed)
	}
	if !completed.Persisted || len(completed.Snapshot.PendingToolCalls) != 0 || completed.Snapshot.PendingToolControl != nil {
		t.Fatalf("completed outcome retained pending state: %+v", completed)
	}
	if oneExecutions.Load() != 1 || twoExecutions.Load() != 0 {
		t.Fatalf("targeted decisions were not enforced: one=%d two=%d", oneExecutions.Load(), twoExecutions.Load())
	}
	if !hasErrorToolResult(completed.Snapshot, "two", "operator denied") {
		t.Fatalf("missing rejected tool result: %+v", completed.Snapshot.Messages)
	}
}

func TestRunnerArgumentDriftReinterruptsWholeBatch(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(agent.ToolCall{ID: "write-1", Name: "write", Arguments: json.RawMessage(`{"value":"v1"}`)}),
		textMessage("done"),
	}}
	var finalValue atomic.Value
	finalValue.Store("v1")
	var executions atomic.Int64
	var policyCalls atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "drift-agent",
			Model: model,
			BeforeToolCall: func(_ context.Context, input agent.BeforeToolCallContext) (agent.BeforeToolCallResult, error) {
				input.Args.(map[string]any)["value"] = finalValue.Load().(string)
				return agent.BeforeToolCallResult{}, nil
			},
			Tools: []agent.ToolDefinition{{
				Name: "write",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"value": map[string]any{"type": "string"}},
					"required":   []any{"value"},
				},
				Execute: func(_ context.Context, _ string, args any, _ agent.ToolUpdateFunc) (agent.ToolResult, error) {
					executions.Add(1)
					return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: args.(map[string]any)["value"].(string)}}}, nil
				},
			}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy: func(context.Context, ToolApprovalRequest) (ApprovalRequirement, error) {
			policyCalls.Add(1)
			return ApprovalRequirement{Required: true}, nil
		},
	})

	initial, err, _ := awaitCheckpoint(runner.Run(context.Background(), "drift", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("write")}))
	if err != nil {
		t.Fatalf("initial run: %v", err)
	}
	oldID := initial.Interrupts[0].ID
	finalValue.Store("v2")
	drifted, err, _ := awaitCheckpoint(runner.Resume(context.Background(), "drift", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		oldID: {Action: DecisionActionApprove},
	}}))
	if err != nil {
		t.Fatalf("drift resume: %v", err)
	}
	if drifted.Status != StatusInterrupted || drifted.Revision != 4 || len(drifted.Interrupts) != 1 {
		t.Fatalf("expected drift re-interruption: %+v", drifted)
	}
	if drifted.Interrupts[0].ID == oldID || string(drifted.Interrupts[0].Tool.Arguments) != `{"value":"v2"}` {
		t.Fatalf("drifted approval was not rebound: %+v", drifted.Interrupts)
	}
	if executions.Load() != 0 || policyCalls.Load() != 2 {
		t.Fatalf("drift handling executed=%d policy=%d", executions.Load(), policyCalls.Load())
	}

	completed, err, _ := awaitCheckpoint(runner.Resume(context.Background(), "drift", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		drifted.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if err != nil || completed.Status != StatusCompleted || executions.Load() != 1 {
		t.Fatalf("approve rebound call: outcome=%+v executions=%d err=%v", completed, executions.Load(), err)
	}
}

func TestRunnerCompletedTombstoneAndDefinitionVersionBinding(t *testing.T) {
	t.Run("completed tombstone", func(t *testing.T) {
		store := NewMemoryStore()
		model := &scriptedModel{responses: []agent.Message{textMessage("done")}}
		runner := newTestRunner(t, RunnerConfig{
			Definition:        agent.AgentDefinition{Name: "done-agent", Model: model},
			DefinitionVersion: "v1",
			Store:             store,
		})
		completed, err, _ := awaitCheckpoint(runner.Run(context.Background(), "done", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
		if err != nil || completed.Status != StatusCompleted || completed.Revision != 2 {
			t.Fatalf("complete run: outcome=%+v err=%v", completed, err)
		}
		again, againErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "done", ResumeParams{}))
		if !errors.Is(againErr, ErrCheckpointNotResumable) || again.Revision != 2 || model.Calls() != 1 {
			t.Fatalf("completed checkpoint replayed: outcome=%+v calls=%d err=%v", again, model.Calls(), againErr)
		}
	})

	t.Run("definition version mismatch", func(t *testing.T) {
		store := NewMemoryStore()
		definition := agent.AgentDefinition{
			Name:  "version-agent",
			Model: &scriptedModel{responses: []agent.Message{toolCallMessage(agent.ToolCall{ID: "call", Name: "tool", Arguments: json.RawMessage(`{}`)})}},
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(new(atomic.Int64))}},
		}
		v1 := newTestRunner(t, RunnerConfig{Definition: definition, DefinitionVersion: "v1", Store: store, ApprovalPolicy: requireApproval})
		interrupted, err, _ := awaitCheckpoint(v1.Run(context.Background(), "version", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
		if err != nil || interrupted.Status != StatusInterrupted {
			t.Fatalf("create v1 checkpoint: outcome=%+v err=%v", interrupted, err)
		}
		v2 := newTestRunner(t, RunnerConfig{Definition: definition, DefinitionVersion: "v2", Store: store, ApprovalPolicy: requireApproval})
		mismatch, mismatchErr, _ := awaitCheckpoint(v2.Resume(context.Background(), "version", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
			interrupted.Interrupts[0].ID: {Action: DecisionActionApprove},
		}}))
		if !errors.Is(mismatchErr, ErrDefinitionVersionMismatch) || mismatch.Revision != interrupted.Revision {
			t.Fatalf("expected version mismatch without mutation: outcome=%+v err=%v", mismatch, mismatchErr)
		}
	})
}

func TestRunnerBusyDoesNotRewriteRunningCheckpoint(t *testing.T) {
	store := NewMemoryStore()
	runner := newTestRunner(t, RunnerConfig{
		Definition:        agent.AgentDefinition{Name: "busy", Model: &scriptedModel{}},
		DefinitionVersion: "v1",
		Store:             store,
	})
	envelope := newEnvelope("busy", "v1", agent.AgentSnapshot{})
	stored, err := runner.save(context.Background(), "busy", 0, envelope)
	if err != nil {
		t.Fatalf("seed running checkpoint: %v", err)
	}
	outcome, resumeErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "busy", ResumeParams{}))
	if !errors.Is(resumeErr, ErrCheckpointBusy) || outcome.Status != StatusRunning || outcome.Revision != stored.Revision {
		t.Fatalf("unexpected busy result: outcome=%+v err=%v", outcome, resumeErr)
	}
	if !outcome.Persisted {
		t.Fatal("busy outcome should describe the persisted running claim")
	}
	record, err := store.Load(context.Background(), "busy")
	if err != nil || record.Revision != stored.Revision {
		t.Fatalf("busy resume rewrote checkpoint: record=%+v err=%v", record, err)
	}
}

func TestRunnerBindsEnvelopeToCheckpointStoreKey(t *testing.T) {
	store := NewMemoryStore()
	runner := newTestRunner(t, RunnerConfig{
		Definition:        agent.AgentDefinition{Name: "binding", Model: &scriptedModel{}},
		DefinitionVersion: "v1",
		Store:             store,
	})
	envelope := newEnvelope("checkpoint-a", "v1", agent.AgentSnapshot{})
	envelope.Revision = 1
	payload, err := encodeEnvelope(envelope)
	if err != nil {
		t.Fatalf("encode checkpoint A: %v", err)
	}
	if _, err := store.CompareAndSwap(context.Background(), "checkpoint-b", 0, payload); err != nil {
		t.Fatalf("copy payload under checkpoint B: %v", err)
	}
	outcome, resumeErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "checkpoint-b", ResumeParams{}))
	if !errors.Is(resumeErr, ErrInvalidCheckpoint) || outcome.Revision != 0 {
		t.Fatalf("copied payload passed key binding: outcome=%+v err=%v", outcome, resumeErr)
	}

	wrong := newEnvelope("checkpoint-a", "v1", agent.AgentSnapshot{})
	if _, err := runner.save(context.Background(), "checkpoint-c", 0, wrong); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("save accepted a mismatched envelope key: %v", err)
	}
	if _, err := store.Load(context.Background(), "checkpoint-c"); !errors.Is(err, ErrCheckpointNotFound) {
		t.Fatalf("mismatched save wrote checkpoint C: %v", err)
	}
}

func TestResumeRejectsTamperedPendingControlBeforeRunningCAS(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(agent.ToolCall{ID: "call", Name: "tool", Arguments: json.RawMessage(`{}`)}),
	}}
	var executions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "tampered-control",
			Model: model,
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(&executions)}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy:    requireApproval,
	})
	interrupted, err, _ := awaitCheckpoint(runner.Run(context.Background(), "tampered-control", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil || interrupted.Status != StatusInterrupted {
		t.Fatalf("create interrupted checkpoint: outcome=%+v err=%v", interrupted, err)
	}
	record, err := store.Load(context.Background(), "tampered-control")
	if err != nil {
		t.Fatalf("load interrupted checkpoint: %v", err)
	}
	envelope, err := decodeStoredCheckpoint("tampered-control", record)
	if err != nil {
		t.Fatalf("decode valid interrupted checkpoint: %v", err)
	}
	envelope.Snapshot.PendingToolControl.Binding = "tampered"
	envelope.Revision = record.Revision + 1
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal tampered checkpoint: %v", err)
	}
	tamperedRecord, err := store.CompareAndSwap(context.Background(), "tampered-control", record.Revision, payload)
	if err != nil {
		t.Fatalf("inject tampered checkpoint: %v", err)
	}

	outcome, resumeErr, events := awaitCheckpoint(runner.Resume(context.Background(), "tampered-control", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		interrupted.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if !errors.Is(resumeErr, ErrInvalidCheckpoint) || outcome.Persisted || outcome.Revision != 0 {
		t.Fatalf("tampered control reached running CAS: outcome=%+v err=%v", outcome, resumeErr)
	}
	if len(events) != 0 || executions.Load() != 0 || model.Calls() != 1 {
		t.Fatalf("tampered control caused runtime side effects: events=%v executions=%d modelCalls=%d", eventTypes(events), executions.Load(), model.Calls())
	}
	unchanged, err := store.Load(context.Background(), "tampered-control")
	if err != nil {
		t.Fatalf("reload tampered checkpoint: %v", err)
	}
	if unchanged.Revision != tamperedRecord.Revision {
		t.Fatalf("resume changed revision from %d to %d", tamperedRecord.Revision, unchanged.Revision)
	}
	var raw checkpointEnvelope
	if err := json.Unmarshal(unchanged.Payload, &raw); err != nil {
		t.Fatalf("inspect unchanged payload: %v", err)
	}
	if raw.Status != StatusInterrupted {
		t.Fatalf("resume changed tampered status to %q", raw.Status)
	}
}

func TestConcurrentResumeFromSameRevisionHasOneCASWinner(t *testing.T) {
	base := NewMemoryStore()
	store := newLoadBarrierStore(base)
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(agent.ToolCall{ID: "call", Name: "tool", Arguments: json.RawMessage(`{}`)}),
		textMessage("done"),
	}}
	var executions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "concurrent-resume",
			Model: model,
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(&executions)}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy:    requireApproval,
	})
	interrupted, err, _ := awaitCheckpoint(runner.Run(context.Background(), "concurrent-resume", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil {
		t.Fatalf("initial interruption: %v", err)
	}
	store.Enable(interrupted.Revision, 2)
	params := ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		interrupted.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}

	type result struct {
		outcome Outcome
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			outcome, resumeErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "concurrent-resume", params))
			results <- result{outcome: outcome, err: resumeErr}
		}()
	}
	first := <-results
	second := <-results
	var successes, conflicts int
	for _, result := range []result{first, second} {
		switch {
		case result.err == nil && result.outcome.Status == StatusCompleted:
			successes++
		case errors.Is(result.err, ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: outcome=%+v err=%v", result.outcome, result.err)
		}
	}
	if successes != 1 || conflicts != 1 || executions.Load() != 1 {
		t.Fatalf("successes=%d conflicts=%d executions=%d", successes, conflicts, executions.Load())
	}
	record, err := base.Load(context.Background(), "concurrent-resume")
	if err != nil {
		t.Fatalf("load final checkpoint: %v", err)
	}
	final, err := decodeStoredCheckpoint("concurrent-resume", record)
	if err != nil || final.Status != StatusCompleted || final.Revision != 4 {
		t.Fatalf("unexpected final checkpoint: %+v err=%v", final, err)
	}
}

func TestTerminalCASFailureReportsUnpersistedIndeterminateOutcome(t *testing.T) {
	base := NewMemoryStore()
	store := &failingCASStore{Store: base, expected: 3, failure: errors.New("terminal store unavailable")}
	model := &scriptedModel{responses: []agent.Message{
		toolCallMessage(agent.ToolCall{ID: "call", Name: "tool", Arguments: json.RawMessage(`{}`)}),
		textMessage("done"),
	}}
	var executions atomic.Int64
	runner := newTestRunner(t, RunnerConfig{
		Definition: agent.AgentDefinition{
			Name:  "indeterminate",
			Model: model,
			Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(&executions)}},
		},
		DefinitionVersion: "v1",
		Store:             store,
		ApprovalPolicy:    requireApproval,
	})
	interrupted, err, _ := awaitCheckpoint(runner.Run(context.Background(), "indeterminate", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")}))
	if err != nil {
		t.Fatalf("initial interruption: %v", err)
	}
	outcome, resumeErr, events := awaitCheckpoint(runner.Resume(context.Background(), "indeterminate", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		interrupted.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if !errors.Is(resumeErr, ErrOutcomeIndeterminate) || outcome.Status != StatusIndeterminate || executions.Load() != 1 {
		t.Fatalf("expected indeterminate side effect result: outcome=%+v executions=%d err=%v", outcome, executions.Load(), resumeErr)
	}
	if outcome.Persisted {
		t.Fatal("indeterminate terminal outcome was falsely marked persisted")
	}
	if len(events) == 0 || events[len(events)-1].Type != agent.EventAgentEnd || !errors.Is(events[len(events)-1].Err, ErrOutcomeIndeterminate) {
		t.Fatalf("terminal event did not expose indeterminate outcome: %+v", events)
	}
	record, err := base.Load(context.Background(), "indeterminate")
	if err != nil {
		t.Fatalf("load persisted running claim: %v", err)
	}
	persisted, err := decodeStoredCheckpoint("indeterminate", record)
	if err != nil || persisted.Status != StatusRunning || persisted.Revision != 3 {
		t.Fatalf("terminal failure falsely persisted an outcome: %+v err=%v", persisted, err)
	}
	busy, busyErr, _ := awaitCheckpoint(runner.Resume(context.Background(), "indeterminate", ResumeParams{}))
	if !errors.Is(busyErr, ErrCheckpointBusy) || busy.Status != StatusRunning || busy.Revision != 3 {
		t.Fatalf("running claim was replayed: outcome=%+v err=%v", busy, busyErr)
	}
}

func TestRunnerRejectsNonDurableInputBeforeCreatingCheckpoint(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{textMessage("never")}}
	runner := newTestRunner(t, RunnerConfig{
		Definition:        agent.AgentDefinition{Name: "durable", Model: model},
		DefinitionVersion: "v1",
		Store:             store,
	})
	outcome, err, _ := awaitCheckpoint(runner.Run(context.Background(), "non-durable", agent.AgentSnapshot{
		Metadata: map[string]any{"function": func() {}},
	}, []agent.Message{agent.NewUserTextMessage("go")}))
	if !errors.Is(err, ErrInvalidCheckpoint) || outcome.Revision != 0 || model.Calls() != 0 {
		t.Fatalf("non-durable snapshot was not rejected early: outcome=%+v calls=%d err=%v", outcome, model.Calls(), err)
	}
	if _, loadErr := store.Load(context.Background(), "non-durable"); !errors.Is(loadErr, ErrCheckpointNotFound) {
		t.Fatalf("non-durable input created a checkpoint: %v", loadErr)
	}
}

func TestRunnerRejectsPendingSnapshotAtRunEntry(t *testing.T) {
	store := NewMemoryStore()
	model := &scriptedModel{responses: []agent.Message{textMessage("never")}}
	runner := newTestRunner(t, RunnerConfig{
		Definition:        agent.AgentDefinition{Name: "pending-input", Model: model},
		DefinitionVersion: "v1",
		Store:             store,
	})
	for index, snapshot := range []agent.AgentSnapshot{
		{PendingToolCalls: []agent.PendingToolCall{{ToolCallID: "call", ToolName: "tool"}}},
		{PendingToolControl: &agent.PendingToolControl{Turn: 1, Binding: "binding"}},
	} {
		id := CheckpointID(fmt.Sprintf("pending-input-%d", index))
		outcome, err, _ := awaitCheckpoint(runner.Run(context.Background(), id, snapshot, []agent.Message{agent.NewUserTextMessage("go")}))
		if !errors.Is(err, ErrInvalidCheckpoint) || outcome.Persisted || model.Calls() != 0 {
			t.Fatalf("pending Run input was accepted: outcome=%+v calls=%d err=%v", outcome, model.Calls(), err)
		}
		if _, loadErr := store.Load(context.Background(), id); !errors.Is(loadErr, ErrCheckpointNotFound) {
			t.Fatalf("pending Run input created a checkpoint: %v", loadErr)
		}
	}
}

func TestRunnerCloseCancelsAndDrains(t *testing.T) {
	store := NewMemoryStore()
	started := make(chan struct{})
	var startOnce sync.Once
	model := agent.StreamFunc(func(ctx context.Context, _ agent.ModelRequest) (agent.AssistantStream, error) {
		startOnce.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	})
	runner := newTestRunner(t, RunnerConfig{
		Definition:        agent.AgentDefinition{Name: "cancel", Model: model},
		DefinitionVersion: "v1",
		Store:             store,
	})
	stream := runner.Run(context.Background(), "cancel", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("go")})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model did not start")
	}
	waitDone := make(chan struct{})
	var outcome Outcome
	var waitErr error
	go func() {
		outcome, waitErr = stream.Wait()
		close(waitDone)
	}()
	if err := stream.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not complete after Close drained events")
	}
	if !errors.Is(waitErr, context.Canceled) || outcome.Status != StatusFailed || outcome.Revision != 2 {
		t.Fatalf("unexpected canceled outcome: %+v err=%v", outcome, waitErr)
	}
}

func newTestRunner(t *testing.T, config RunnerConfig) *Runner {
	t.Helper()
	runner, err := NewRunner(config)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

func awaitCheckpoint(stream *RunStream) (Outcome, error, []agent.AgentEvent) {
	var events []agent.AgentEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	outcome, err := stream.Wait()
	return outcome, err, events
}

func assertSingleCleanAgentEnd(t *testing.T, events []agent.AgentEvent) {
	t.Helper()
	ends := 0
	for _, event := range events {
		if event.Type != agent.EventAgentEnd {
			continue
		}
		ends++
		if event.Err != nil {
			t.Fatalf("outer AgentEnd exposed internal suspension: %v", event.Err)
		}
	}
	if ends != 1 {
		t.Fatalf("expected one AgentEnd, got %d in %v", ends, eventTypes(events))
	}
}

func assertSyntheticLifecycle(t *testing.T, events []agent.AgentEvent) {
	t.Helper()
	if len(events) != 2 || events[0].Type != agent.EventAgentStart || events[1].Type != agent.EventAgentEnd {
		t.Fatalf("unexpected synthetic lifecycle: %v", eventTypes(events))
	}
	if events[0].RunID == "" || events[0].RunID != events[1].RunID || events[0].Sequence != 1 || events[1].Sequence != 2 || events[1].Err != nil {
		t.Fatalf("invalid synthetic lifecycle: %+v", events)
	}
}

func eventTypes(events []agent.AgentEvent) []agent.EventType {
	types := make([]agent.EventType, len(events))
	for index, event := range events {
		types[index] = event.Type
	}
	return types
}

func requireApproval(context.Context, ToolApprovalRequest) (ApprovalRequirement, error) {
	return ApprovalRequirement{Required: true}, nil
}

func countingTool(counter *atomic.Int64) agent.ToolExecutorFunc {
	return func(context.Context, string, any, agent.ToolUpdateFunc) (agent.ToolResult, error) {
		counter.Add(1)
		return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "ok"}}}, nil
	}
}

func hasErrorToolResult(snapshot agent.AgentSnapshot, toolName, text string) bool {
	for _, message := range snapshot.Messages {
		if message.ToolResult == nil || message.ToolResult.ToolName != toolName || !message.ToolResult.IsError {
			continue
		}
		for _, part := range message.ToolResult.Content {
			if part.Text == text {
				return true
			}
		}
	}
	return false
}

type scriptedModel struct {
	mu        sync.Mutex
	responses []agent.Message
	calls     int
}

type loadBarrierStore struct {
	Store
	enabled  atomic.Bool
	revision atomic.Uint64
	target   atomic.Int64
	arrived  atomic.Int64
	release  chan struct{}
	once     sync.Once
}

func newLoadBarrierStore(store Store) *loadBarrierStore {
	return &loadBarrierStore{Store: store, release: make(chan struct{})}
}

func (s *loadBarrierStore) Enable(revision Revision, target int64) {
	s.revision.Store(uint64(revision))
	s.target.Store(target)
	s.enabled.Store(true)
}

func (s *loadBarrierStore) Load(ctx context.Context, id CheckpointID) (StoredCheckpoint, error) {
	record, err := s.Store.Load(ctx, id)
	if err != nil || !s.enabled.Load() || record.Revision != Revision(s.revision.Load()) {
		return record, err
	}
	if s.arrived.Add(1) == s.target.Load() {
		s.once.Do(func() { close(s.release) })
	}
	select {
	case <-s.release:
		return record, nil
	case <-ctx.Done():
		return StoredCheckpoint{}, ctx.Err()
	}
}

type failingCASStore struct {
	Store
	expected Revision
	failure  error
	once     sync.Once
}

func (s *failingCASStore) CompareAndSwap(ctx context.Context, id CheckpointID, expected Revision, payload []byte) (StoredCheckpoint, error) {
	if expected == s.expected {
		failed := false
		s.once.Do(func() { failed = true })
		if failed {
			return StoredCheckpoint{}, s.failure
		}
	}
	return s.Store.CompareAndSwap(ctx, id, expected, payload)
}

func (m *scriptedModel) Stream(ctx context.Context, _ agent.ModelRequest) (agent.AssistantStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if len(m.responses) == 0 {
		return nil, fmt.Errorf("scripted model has no response for call %d", m.calls)
	}
	message := m.responses[0]
	m.responses = m.responses[1:]
	return newTestAssistantStream(message), nil
}

func (m *scriptedModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

type testAssistantStream struct {
	events chan agent.AssistantEvent
	final  agent.Message
}

func newTestAssistantStream(final agent.Message) *testAssistantStream {
	events := make(chan agent.AssistantEvent)
	close(events)
	return &testAssistantStream{events: events, final: final}
}

func (s *testAssistantStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *testAssistantStream) Wait() (agent.Message, error)        { return s.final, nil }
func (s *testAssistantStream) Close() error                        { return nil }

func toolCallMessage(calls ...agent.ToolCall) agent.Message {
	return agent.Message{
		Role:       agent.RoleAssistant,
		ToolCalls:  calls,
		StopReason: agent.StopReasonToolUse,
		Timestamp:  time.Now().UTC(),
	}
}

func textMessage(text string) agent.Message {
	return agent.Message{
		Role:       agent.RoleAssistant,
		Parts:      []agent.Part{{Type: agent.PartTypeText, Text: text}},
		StopReason: agent.StopReasonStop,
		Timestamp:  time.Now().UTC(),
	}
}

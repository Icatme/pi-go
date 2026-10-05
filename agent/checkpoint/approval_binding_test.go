package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestApprovalBindingCanonicalIdentityAndOwnership(t *testing.T) {
	call := agent.ToolCall{ID: "call", Name: "tool", Arguments: json.RawMessage(`{"z":2,"a":1}`)}
	args := map[string]any{"value": json.Number("9007199254740993")}
	original, err := makePendingApproval("v1", call, args)
	if err != nil {
		t.Fatal(err)
	}
	canonicalCall := call
	canonicalCall.OriginalID = call.ID // The historical binding falls back to ID.
	canonicalCall.Arguments = json.RawMessage("{ \"a\": 1, \"z\": 2 }")
	equivalent, err := makePendingApproval("v1", canonicalCall, map[string]any{"value": uint64(9007199254740993)})
	if err != nil || original.binding != equivalent.binding || original.binding.legacyDigest() != equivalent.binding.legacyDigest() {
		t.Fatalf("canonical identity changed: original=%+v equivalent=%+v err=%v", original.binding, equivalent.binding, err)
	}
	args["value"] = "changed"
	call.Arguments[5] = '3'
	original.request.Arguments[0] = '['
	if original.binding != equivalent.binding {
		t.Fatal("source or displayed argument mutation changed the owned binding")
	}

	for _, field := range []string{"definition", "call", "original", "name", "raw", "final"} {
		t.Run(field, func(t *testing.T) {
			changed := equivalent.binding
			switch field {
			case "definition":
				changed.definitionVersion = "v2"
			case "call":
				changed.toolCallID = "other"
			case "original":
				changed.originalToolCallID = "other"
			case "name":
				changed.toolName = "other"
			case "raw":
				changed.rawArguments = `{"a":2,"z":2}`
			case "final":
				changed.arguments = `{"value":9007199254740992}`
			}
			if changed == equivalent.binding || changed.legacyDigest() == equivalent.binding.legacyDigest() {
				t.Fatal("changed approval input matched the original")
			}
		})
	}
}

func TestApprovalGateConsumesOwnedAndLegacyDecisionsOnceConcurrently(t *testing.T) {
	input := agent.BeforeToolCallContext{ToolCall: agent.ToolCall{ID: "call", Name: "tool", Arguments: json.RawMessage(`{}`)}, Args: map[string]any{}}
	approval, err := makePendingApproval("v1", input.ToolCall, input.Args)
	if err != nil {
		t.Fatal(err)
	}
	digest := approval.binding.legacyDigest()
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprintf("owned=%t", owned), func(t *testing.T) {
			decision := storedDecision{Digest: digest, Decision: ResumeDecision{Action: DecisionActionApprove}}
			if owned {
				decision.bound = boundApproval{digest: digest, binding: approval.binding}
			}
			gate := newApprovalGate(requireApproval, []storedDecision{decision}, "v1")
			var allowed atomic.Int64
			var suspended atomic.Int64
			var workers sync.WaitGroup
			for range 16 {
				workers.Go(func() {
					result, err := gate.evaluate(context.Background(), input)
					if err != nil {
						t.Error(err)
						return
					}
					switch result.Action {
					case agent.ToolGateActionAllow:
						allowed.Add(1)
					case agent.ToolGateActionSuspend:
						suspended.Add(1)
					default:
						t.Errorf("unexpected gate action %q", result.Action)
					}
				})
			}
			workers.Wait()
			if allowed.Load() != 1 || suspended.Load() != 15 || len(gate.suspendedApprovals()) != 1 {
				t.Fatalf("decision reuse/dedup failure: allowed=%d suspended=%d approvals=%d", allowed.Load(), suspended.Load(), len(gate.suspendedApprovals()))
			}
			copy := gate.suspendedApprovals()
			copy[0].request.Arguments[0] = '['
			if string(gate.suspendedApprovals()[0].request.Arguments) != `{}` {
				t.Fatal("returned arguments alias the gate's owned approval")
			}
		})
	}
}

func TestRunnerRestartsPartialDecisionWithoutApprovingArgumentDrift(t *testing.T) {
	store := NewMemoryStore()
	var executions atomic.Int64
	newRunner := func(value string, model *scriptedModel) *Runner {
		return newTestRunner(t, RunnerConfig{
			Definition: agent.AgentDefinition{
				Model: model,
				BeforeToolCall: func(_ context.Context, input agent.BeforeToolCallContext) (agent.BeforeToolCallResult, error) {
					if input.ToolCall.ID == "first" {
						input.Args.(map[string]any)["value"] = value
					}
					return agent.BeforeToolCallResult{}, nil
				},
				Tools: []agent.ToolDefinition{{Name: "tool", Execute: countingTool(&executions)}},
			},
			DefinitionVersion: "v1",
			Store:             store,
			ApprovalPolicy:    requireApproval,
		})
	}
	firstRunner := newRunner("approved", &scriptedModel{responses: []agent.Message{toolCallMessage(
		agent.ToolCall{ID: "first", Name: "tool", Arguments: json.RawMessage(`{}`)},
		agent.ToolCall{ID: "second", Name: "tool", Arguments: json.RawMessage(`{}`)},
	)}})
	initial, err, _ := awaitCheckpoint(firstRunner.Run(context.Background(), "partial-restart", agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("run")}))
	if err != nil || len(initial.Interrupts) != 2 {
		t.Fatalf("initial: %+v, %v", initial, err)
	}
	partial, err, _ := awaitCheckpoint(firstRunner.Resume(context.Background(), "partial-restart", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		initial.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if err != nil || len(partial.Interrupts) != 1 || executions.Load() != 0 {
		t.Fatalf("partial: %+v, executions=%d, %v", partial, executions.Load(), err)
	}
	// Only v1 JSON survives a process restart. The earlier decision's final hook
	// arguments are represented solely by its legacy digest in this format.
	record, err := store.Load(context.Background(), "partial-restart")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeStoredCheckpoint("partial-restart", record)
	if err != nil || len(loaded.Decisions) != 1 || loaded.Decisions[0].bound.digest != "" {
		t.Fatalf("fixture retained in-memory decision binding: %+v, %v", loaded.Decisions, err)
	}
	restarted := newRunner("changed", &scriptedModel{responses: []agent.Message{textMessage("done")}})
	drifted, err, _ := awaitCheckpoint(restarted.Resume(context.Background(), "partial-restart", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		partial.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if err != nil || drifted.Status != StatusInterrupted || len(drifted.Interrupts) != 1 || executions.Load() != 0 {
		t.Fatalf("old partial decision approved drift: %+v, executions=%d, %v", drifted, executions.Load(), err)
	}
	if drifted.Interrupts[0].Tool.ToolCallID != "first" || string(drifted.Interrupts[0].Tool.Arguments) != `{"value":"changed"}` {
		t.Fatalf("wrong rebound request: %+v", drifted.Interrupts[0])
	}
	completed, err, _ := awaitCheckpoint(restarted.Resume(context.Background(), "partial-restart", ResumeParams{Decisions: map[InterruptID]ResumeDecision{
		drifted.Interrupts[0].ID: {Action: DecisionActionApprove},
	}}))
	if err != nil || completed.Status != StatusCompleted || executions.Load() != 2 {
		t.Fatalf("fresh approval failed after restart: %+v, executions=%d, %v", completed, executions.Load(), err)
	}
}

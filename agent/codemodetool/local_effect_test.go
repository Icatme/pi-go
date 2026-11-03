package codemodetool

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Icatme/pi-go/agent"
	"strings"
	"testing"
)

func TestNativeLocalCommitSurvivesHooksAndProjection(t *testing.T) {
	for _, mode := range []string{"hook failure", "validator failure", "effect-only error", "success then script failure"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			leaf := agent.ToolDefinition{Name: "create", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
				calls++
				facts := agent.ToolExecutionInfo{LocalEffect: agent.ToolLocalEffectCommittedReported}
				if mode == "effect-only error" {
					return agent.ToolResult{}, &agent.ToolExecutionError{Code: agent.ToolFailureResource, Message: "published, durability unknown; do not retry", Execution: facts}
				}
				return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "SECRET_RESULT"}}, Execution: &facts}, nil
			}}
			if mode == "validator failure" {
				leaf.ValidateResult = func(agent.ToolResult) error { return errors.New("output withheld") }
			}
			tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			definition := agent.AgentDefinition{AfterToolCall: func(_ context.Context, in agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
				if in.ParentToolCallID != "" && mode == "hook failure" {
					return agent.AfterToolCallResult{}, errors.New("hook withheld")
				}
				return agent.AfterToolCallResult{}, nil
			}}
			script := `try {await tools.create({})} catch(e) {text(e.execution.local_effect)}`
			if mode == "success then script failure" {
				script = `await tools.create({});throw new Error("later script failure")`
			}
			result, events := runnerCall(t, tool, script, definition, nil)
			if calls != 1 || result.ChildCalls == nil || len(result.ChildCalls.Calls) != 1 || result.ChildCalls.Calls[0].Execution.LocalEffect != agent.ToolLocalEffectCommittedReported {
				t.Fatalf("lost child fact or replay %+v", result)
			}
			if mode != "success then script failure" && !strings.Contains(contentText(result.Content), "committed_reported") {
				t.Fatalf("JS lost error fact %+v", result)
			}
			report, ok := result.Details.(Report)
			if !ok || len(report.Sandbox.Calls) != 1 || report.Sandbox.Calls[0].Execution.LocalEffect != "committed_reported" {
				t.Fatalf("sandbox ledger lost fact: %#v", result.Details)
			}
			for _, event := range events {
				if event.Type == agent.EventToolExecutionEnd && event.ParentToolCallID != "" {
					if event.Execution == nil || event.Execution.LocalEffect != agent.ToolLocalEffectCommittedReported {
						t.Fatalf("event lost fact %+v", event)
					}
				}
				if mode != "success then script failure" {
					raw, _ := json.Marshal(event)
					if strings.Contains(string(raw), "SECRET_RESULT") {
						t.Fatal("withheld output leaked")
					}
				}
			}
			if result.Execution != nil && result.Execution.LocalEffect == agent.ToolLocalEffectNoneReported {
				t.Fatal("parent falsely inferred no effects")
			}
		})
	}
}

func TestParentHookFailureRetainsChildLocalEffect(t *testing.T) {
	calls := 0
	leaf := agent.ToolDefinition{Name: "create", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		calls++
		return agent.ToolResult{Execution: &agent.ToolExecutionInfo{LocalEffect: agent.ToolLocalEffectCommittedReported}}, nil
	}}
	tool, err := New(newSandbox(t), []Binding{Native(leaf, "local")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := runnerCall(t, tool, `await tools.create({})`, agent.AgentDefinition{AfterToolCall: func(_ context.Context, in agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		if in.ParentToolCallID == "" {
			return agent.AfterToolCallResult{}, errors.New("parent output withheld")
		}
		return agent.AfterToolCallResult{}, nil
	}}, nil)
	if calls != 1 || !result.IsError || result.ChildCalls == nil || len(result.ChildCalls.Calls) != 1 || result.ChildCalls.Calls[0].Execution.LocalEffect != agent.ToolLocalEffectCommittedReported {
		t.Fatalf("parent failure erased child effects: %+v", result)
	}
}

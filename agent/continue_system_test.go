package agent

import (
	"context"
	"errors"
	"testing"
)

func TestEngineContinueWithHooksRejectsSystemOnlyHistory(t *testing.T) {
	for _, test := range []struct {
		name       string
		definition AgentDefinition
		messages   []Message
	}{
		{name: "empty"},
		{name: "initial-prompt", definition: AgentDefinition{SystemPrompt: "instructions"}},
		{name: "initial-tools", definition: AgentDefinition{Tools: []ToolDefinition{{Name: "work"}}}},
		{name: "system-updates", messages: []Message{
			NewSystemMessage(SystemMessagePayload{Content: "instructions"}),
			NewSystemMessage(SystemMessagePayload{Content: "more instructions"}),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, queueReads, events := 0, 0, 0
			definition := test.definition
			definition.Model = StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
				calls++
				return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
			})
			next, err := NewEngine().ContinueWithHooks(context.Background(), definition, &AgentSnapshot{Messages: test.messages}, func(AgentEvent) {
				events++
			}, LoopHooks{GetSteeringMessages: func(context.Context) ([]Message, error) {
				queueReads++
				return nil, nil
			}})
			if !errors.Is(err, ErrNoMessagesToContinue) || next != nil || calls != 0 || queueReads != 0 || events != 0 {
				t.Fatalf("next=%+v err=%v calls=%d queueReads=%d events=%d", next, err, calls, queueReads, events)
			}
		})
	}
}

func TestEngineContinueWithHooksAcceptsUserAfterSystem(t *testing.T) {
	calls := 0
	definition := AgentDefinition{Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
		calls++
		if len(request.Messages) != 2 || request.Messages[0].Role != RoleSystem || request.Messages[1].Role != RoleUser {
			t.Fatalf("request messages=%+v", request.Messages)
		}
		return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
	})}
	snapshot := AgentSnapshot{Messages: []Message{
		NewSystemMessage(SystemMessagePayload{Content: "instructions"}),
		NewUserTextMessage("continue"),
	}}
	next, err := NewEngine().ContinueWithHooks(context.Background(), definition, &snapshot, nil, LoopHooks{})
	if err != nil || calls != 1 || len(next.Messages) != 3 || next.Messages[2].Role != RoleAssistant {
		t.Fatalf("next=%+v err=%v calls=%d", next, err, calls)
	}
}

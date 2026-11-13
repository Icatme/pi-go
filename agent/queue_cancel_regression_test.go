package agent

import (
	"context"
	"errors"
	"testing"
)

func TestQueueHookCancellationRejectsUnacceptedMessages(t *testing.T) {
	for _, phase := range []string{"initial steering", "follow up"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			definition := AgentDefinition{Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
				calls++
				return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
			}}}
			getter := func(context.Context) ([]Message, error) {
				cancel()
				return []Message{NewUserTextMessage("unaccepted")}, nil
			}
			hooks := LoopHooks{}
			expected := 0
			if phase == "initial steering" {
				hooks.GetSteeringMessages = getter
			} else {
				hooks.GetFollowUpMessages = getter
				expected = 1
			}
			snapshot, err := NewEngine().RunWithHooks(ctx, definition, &AgentSnapshot{}, []Message{NewUserTextMessage("original")}, nil, hooks)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("want canceled, got %v", err)
			}
			if calls != expected {
				t.Errorf("model calls %d want %d", calls, expected)
			}
			for _, message := range snapshot.Messages {
				for _, part := range message.Parts {
					if part.Text == "unaccepted" {
						t.Error("canceled callback output appended")
					}
				}
			}
		})
	}
}

func TestQueueGetterNotInvokedAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	invoked := false
	messages, err := dequeueHookMessages(ctx, func(context.Context) ([]Message, error) {
		invoked = true
		return []Message{NewUserTextMessage("late")}, nil
	})
	if !errors.Is(err, context.Canceled) || invoked || len(messages) != 0 {
		t.Fatalf("got invoked=%v messages=%v err=%v", invoked, messages, err)
	}
}

func TestQueueGetterSuccessfulAcceptanceAndErrorPreservation(t *testing.T) {
	messages, err := dequeueHookMessages(t.Context(), func(context.Context) ([]Message, error) { return []Message{NewUserTextMessage("accepted")}, nil })
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%v err=%v", messages, err)
	}
	sentinel := errors.New("host queue failure")
	messages, err = dequeueHookMessages(t.Context(), func(context.Context) ([]Message, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) || len(messages) != 0 {
		t.Fatalf("messages=%v err=%v", messages, err)
	}
}

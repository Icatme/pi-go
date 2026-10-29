package prebuilt

import (
	"context"
	"errors"
	"testing"

	core "github.com/Icatme/pi-go/agent"
)

func TestAssistantErrorEventPreservesOriginalCause(t *testing.T) {
	cause := &terminalStatusError{502}
	final := core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonError, ErrorMessage: cause.Error()}
	def := core.AgentDefinition{Model: terminalModel{events: []core.AssistantEvent{{Type: core.AssistantEventError, Message: final, Err: cause}}, final: final}}
	r, err := core.NewRunner(def)
	if err != nil {
		t.Fatal(err)
	}
	stream := r.Query(t.Context(), "hello")
	var terminal error
	for e := range stream.Events() {
		if e.Type == core.EventMessageEnd {
			terminal = e.Err
		}
	}
	snapshot, err := stream.Wait()
	if err != nil {
		t.Fatalf("Runner Wait contract changed: %v", err)
	}
	if snapshot.Error == "" {
		t.Fatal("lost error snapshot")
	}
	if !errors.Is(terminal, cause) {
		t.Fatalf("lost original cause: %v", terminal)
	}
	var status *terminalStatusError
	if !errors.As(terminal, &status) {
		t.Fatalf("lost typed cause: %v", terminal)
	}
	a, err := NewChatAgent(def)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Chat(t.Context(), "hello"); !errors.Is(err, cause) {
		t.Fatalf("Chat lost cause: %v", err)
	}
}

func TestAssistantErrorEventsJoinCancellationWaitAndClose(t *testing.T) {
	first := errors.New("first event failure")
	second := errors.New("second event failure")
	waitErr := errors.New("wait failure")
	closeErr := errors.New("close failure")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	model := core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
		events := make(chan core.AssistantEvent)
		go func() {
			for _, cause := range []error{first, second} {
				select {
				case events <- core.AssistantEvent{Type: core.AssistantEventError, Err: cause}:
				case <-ctx.Done():
					return
				}
			}
			cancel()
		}()
		return terminalStream{chatAssistantStream: chatAssistantStream{events: events, final: core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonStop}, err: waitErr}, closeErr: closeErr}, nil
	})
	a, err := core.NewAgent(core.AgentDefinition{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	var terminal error
	a.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.EventMessageEnd {
			terminal = e.Err
		}
	})
	if err := a.PromptText(ctx, "hello"); err != nil {
		t.Fatalf("Prompt contract changed: %v", err)
	}
	for _, cause := range []error{first, second, context.Canceled, waitErr, closeErr} {
		if !errors.Is(terminal, cause) {
			t.Errorf("lost %v in %v", cause, terminal)
		}
	}
}

func TestAssistantErrorEventPreventsToolsFromSuccessFinal(t *testing.T) {
	cause := errors.New("event failure")
	called := 0
	a, err := core.NewAgent(core.AgentDefinition{MaxTurns: 1, Model: terminalModel{
		events: []core.AssistantEvent{{Type: core.AssistantEventError, Err: cause}},
		final:  core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonToolUse, ToolCalls: []core.ToolCall{{ID: "call", Name: "danger", Arguments: []byte("{}")}}},
	}, Tools: []core.ToolDefinition{{Name: "danger", Execute: func(context.Context, core.ToolExecutionContext) (core.ToolResult, error) {
		called++
		return core.ToolResult{}, nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PromptText(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("executed tools after terminal error: %d", called)
	}
	if a.State().Error == "" {
		t.Fatal("error event became success")
	}
	state := a.State()
	last := state.Messages[len(state.Messages)-1]
	if last.Role != core.RoleTool || last.ToolResult == nil || !last.ToolResult.IsError {
		t.Fatalf("tool intent not settled: %+v", last)
	}
}

func TestNilAssistantErrorEventDoesNotInventTypedCause(t *testing.T) {
	final := core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonError, ErrorMessage: "provider failed"}
	a, err := core.NewAgent(core.AgentDefinition{Model: terminalModel{events: []core.AssistantEvent{{Type: core.AssistantEventError, Message: final}}, final: final}})
	if err != nil {
		t.Fatal(err)
	}
	var terminal error
	a.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.EventMessageEnd {
			terminal = e.Err
		}
	})
	if err := a.PromptText(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	if terminal != nil {
		t.Fatalf("invented cause: %v", terminal)
	}
	if a.State().Error != "provider failed" {
		t.Fatalf("lost provider error: %q", a.State().Error)
	}
}

package prebuilt

import (
	"context"
	"errors"
	"testing"
	"time"

	core "github.com/Icatme/pi-go/agent"
)

type printContextModel struct {
	chatScriptedModel
	contexts chan context.Context
}

func (m *printContextModel) Stream(ctx context.Context, request core.ModelRequest) (core.AssistantStream, error) {
	m.contexts <- ctx
	return m.chatScriptedModel.Stream(ctx, request)
}

func TestPrintStreamWriteFailureCancelsOnlyItsRun(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := []core.AssistantEvent{{Type: core.AssistantEventStart, Message: core.Message{Role: core.RoleAssistant}}}
	for index := 0; index < 512; index++ {
		events = append(events, core.AssistantEvent{Type: core.AssistantEventTextDelta, Message: core.Message{Role: core.RoleAssistant}, Delta: "part"})
	}
	model := &printContextModel{
		contexts: make(chan context.Context, 2),
		chatScriptedModel: chatScriptedModel{responses: []chatScriptedResponse{{
			events: events,
			final:  core.Message{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "done"}}, StopReason: core.StopReasonStop},
		}}},
	}
	chat, err := NewChatAgent(core.AgentDefinition{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("writer disconnected")
	returned := make(chan error, 1)
	go func() { returned <- chat.PrintStream(parent, "hello", func(string) error { return writeErr }) }()
	select {
	case err := <-returned:
		if !errors.Is(err, writeErr) {
			t.Fatalf("writer error was lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PrintStream did not return writer error")
	}
	runCtx := <-model.contexts
	select {
	case <-runCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("PrintStream left the producer/subscription running")
	}
	waitCtx, stopWaiting := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopWaiting()
	if err := chat.agent.WaitForIdle(waitCtx); err != nil {
		t.Fatalf("agent did not become idle: %v", err)
	}
	if parent.Err() != nil {
		t.Fatal("writer failure cancelled the caller's context")
	}
	// Reuse the same agent; stale subscriptions must not block the next run.
	if _, err := chat.Chat(waitCtx, "again"); err != nil {
		t.Fatalf("agent could not be reused: %v", err)
	}
}

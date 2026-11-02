package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAgentHistoryMutationsRejectActiveRun(t *testing.T) {
	started := make(chan struct{})
	a, err := NewAgent(AgentDefinition{SessionID: "original-session", Model: &blockingModel{started: started}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.PromptText(context.Background(), "original-message") }()
	<-started
	defer func() {
		a.Abort()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	before := a.Snapshot()
	for _, mutate := range []func() error{
		func() error { return a.TryReplaceMessages([]Message{NewUserTextMessage("replacement")}) },
		a.TryClearMessages,
		func() error { return a.TrySetSessionID("replacement-session") },
	} {
		if err := mutate(); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("expected active-run rejection: %v", err)
		}
	}
	// Existing signatures remain available, with the documented active-run no-op.
	a.ReplaceMessages([]Message{NewUserTextMessage("replacement")})
	a.ClearMessages()
	a.SetSessionID("replacement-session")
	if got := a.Snapshot(); !reflect.DeepEqual(got, before) {
		t.Fatal("rejected mutation changed snapshot")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.definition.SessionID != "original-session" || a.activeDefinition.SessionID != "original-session" || a.state.SessionID != "original-session" {
		t.Fatal("rejected session mutation changed definition/state")
	}
}

func TestAgentHistoryMutationsSucceedWhileIdle(t *testing.T) {
	a, err := NewAgent(AgentDefinition{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.TryReplaceMessages([]Message{NewUserTextMessage("replacement")}); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot(); len(got.Messages) != 1 || got.Messages[0].Parts[0].Text != "replacement" {
		t.Fatal("idle replacement missing")
	}
	if err := a.TrySetSessionID("new-session"); err != nil {
		t.Fatal(err)
	}
	if a.Snapshot().SessionID != "new-session" || a.State().SessionID != "new-session" {
		t.Fatal("idle session not updated")
	}
	if err := a.TryClearMessages(); err != nil {
		t.Fatal(err)
	}
	if len(a.Snapshot().Messages) != 0 || len(a.State().Messages) != 0 {
		t.Fatal("idle clear failed")
	}
	a.ReplaceMessages([]Message{NewUserTextMessage("legacy")})
	a.SetSessionID("legacy-session")
	if len(a.Snapshot().Messages) != 1 || a.Snapshot().SessionID != "legacy-session" {
		t.Fatal("legacy idle methods changed")
	}
	a.ClearMessages()
	if len(a.Snapshot().Messages) != 0 {
		t.Fatal("legacy clear failed")
	}
}

func TestAgentHistoryMutationsAfterCancellationAndIdle(t *testing.T) {
	started := make(chan struct{})
	a, err := NewAgent(AgentDefinition{Model: &blockingModel{started: started}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.PromptText(ctx, "cancel-me") }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := a.WaitForIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.TryClearMessages(); err != nil {
		t.Fatal(err)
	}
	if err := a.TryReplaceMessages([]Message{NewUserTextMessage("after-cancel")}); err != nil {
		t.Fatal(err)
	}
	if err := a.TrySetSessionID("after-cancel-session"); err != nil {
		t.Fatal(err)
	}
	got := a.Snapshot()
	if len(got.Messages) != 1 || got.Messages[0].Parts[0].Text != "after-cancel" || got.SessionID != "after-cancel-session" {
		t.Fatal("idle post-cancel mutation lost")
	}
}

func TestAgentHistoryMutationRejectsTerminalCallbackUntilRunReturns(t *testing.T) {
	a, err := NewAgent(AgentDefinition{Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
		return newStaticAssistantStream(Message{Role: RoleAssistant, Parts: []Part{{Type: PartTypeText, Text: "done"}}, StopReason: StopReasonStop}, nil), nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	a.Subscribe(func(event AgentEvent) {
		if event.Type != EventAgentEnd {
			return
		}
		called = true
		if err := a.TryClearMessages(); !errors.Is(err, ErrAlreadyRunning) {
			t.Errorf("terminal callback mutation: %v", err)
		}
	})
	if err := a.PromptText(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("missing terminal callback")
	}
	if err := a.TryClearMessages(); err != nil {
		t.Fatal(err)
	}
}

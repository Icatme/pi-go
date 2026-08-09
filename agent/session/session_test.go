package session

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestSessionFacadeAppendsBranchesAndReturnsDetachedContext(t *testing.T) {
	storage, err := NewMemoryStorage(Header{ID: "facade-session"})
	if err != nil {
		t.Fatalf("NewMemoryStorage returned error: %v", err)
	}
	ids := []string{"user-1", "assistant-1", "alternative-1", "custom-1", "late-1"}
	next := 0
	session, err := New(storage, Options{IDGenerator: func() string {
		id := ids[next]
		next++
		return id
	}})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	user, err := session.AppendMessage(MainLane, agent.NewUserTextMessage("question"))
	if err != nil {
		t.Fatalf("AppendMessage(user) returned error: %v", err)
	}
	if _, err := session.AppendMessage(MainLane, agent.NewTextMessage(agent.RoleAssistant, "answer")); err != nil {
		t.Fatalf("AppendMessage(assistant) returned error: %v", err)
	}
	if err := session.CreateLane("alternative", user.ID); err != nil {
		t.Fatalf("CreateLane returned error: %v", err)
	}
	if _, err := session.AppendMessage("alternative", agent.NewTextMessage(agent.RoleAssistant, "other answer")); err != nil {
		t.Fatalf("AppendMessage(alternative) returned error: %v", err)
	}

	payload := json.RawMessage(`{"nested":{"value":"original"}}`)
	custom, err := session.AppendCustomJSON("alternative", "artifact", payload)
	if err != nil {
		t.Fatalf("AppendCustomJSON returned error: %v", err)
	}
	payload[0] = '['
	stored, ok := storage.Entry(custom.ID)
	if !ok || string(stored.Custom.Payload) != `{"nested":{"value":"original"}}` {
		t.Fatalf("caller mutation leaked into custom entry: %+v", stored)
	}

	mainContext, err := session.Context(MainLane)
	if err != nil {
		t.Fatalf("Context(main) returned error: %v", err)
	}
	alternativeContext, err := session.Context("alternative")
	if err != nil {
		t.Fatalf("Context(alternative) returned error: %v", err)
	}
	if got := messageTexts(mainContext.Messages); len(got) != 2 || got[0] != "question" || got[1] != "answer" {
		t.Fatalf("unexpected main context: %v", got)
	}
	if got := messageTexts(alternativeContext.Messages); len(got) != 2 || got[0] != "question" || got[1] != "other answer" {
		t.Fatalf("unexpected alternative context: %v", got)
	}
	alternativeContext.Messages[0].Parts[0].Text = "mutated"
	again, err := session.Context("alternative")
	if err != nil {
		t.Fatalf("second Context returned error: %v", err)
	}
	if again.Messages[0].Parts[0].Text != "question" {
		t.Fatalf("context mutation leaked into session: %+v", again.Messages)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("repeated Close returned error: %v", err)
	}
	if _, err := session.AppendMessage(MainLane, agent.NewUserTextMessage("late")); !errors.Is(err, &Error{Code: ErrorStorage}) {
		t.Fatalf("expected closed storage error, got %v", err)
	}
}

func TestSessionFacadeRejectsEmptyGeneratedID(t *testing.T) {
	storage, err := NewMemoryStorage(Header{ID: "empty-id-session"})
	if err != nil {
		t.Fatalf("NewMemoryStorage returned error: %v", err)
	}
	session, err := New(storage, Options{IDGenerator: func() string { return "" }})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if _, err := session.AppendMessage(MainLane, agent.NewUserTextMessage("message")); !errors.Is(err, &Error{Code: ErrorInvalidEntry}) {
		t.Fatalf("expected invalid entry error, got %v", err)
	}
	if got := storage.Log(); len(got) != 0 {
		t.Fatalf("empty id advanced storage: %+v", got)
	}
}

func messageTexts(messages []agent.Message) []string {
	texts := make([]string, len(messages))
	for index, message := range messages {
		if len(message.Parts) > 0 {
			texts[index] = message.Parts[0].Text
		}
	}
	return texts
}

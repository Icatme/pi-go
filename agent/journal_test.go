package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fixtureJournal struct {
	load   func(string, string) (json.RawMessage, error)
	record func(context.Context, JournalRecord) error
}

func (j fixtureJournal) LoadState(tool, namespace string) (json.RawMessage, error) {
	return j.load(tool, namespace)
}
func (j fixtureJournal) Record(ctx context.Context, record JournalRecord) error {
	return j.record(ctx, record)
}

func TestJournalStateLifetimeRejectionAndRevocation(t *testing.T) {
	revoked := false
	cause := errors.New("scope changed")
	var saved JournalRecord
	ctx := withJournal(t.Context(), fixtureJournal{
		load: func(string, string) (json.RawMessage, error) {
			if revoked {
				return nil, cause
			}
			return json.RawMessage(`{"n":0}`), nil
		},
		record: func(_ context.Context, r JournalRecord) error { saved = r; return nil },
	})
	prepared := preparedToolCall{call: ToolCall{Name: "code"}, tool: ToolDefinition{ExecutionMode: ToolExecutionSequential}}
	first := newToolState(ctx, prepared)
	value := json.RawMessage(`{"n":1}`)
	if err := first.Stage("binding", value); err != nil {
		t.Fatal(err)
	}
	value[5] = '9'
	first.finish(true)
	if err := first.Stage("binding", json.RawMessage(`{}`)); err == nil {
		t.Fatal("late executor changed accepted state")
	}
	second := newToolState(ctx, prepared)
	if got, err := second.Load("binding"); err != nil || string(got) != `{"n":1}` {
		t.Fatalf("sequential state: %s %v", got, err)
	}
	if err := second.Stage("binding", json.RawMessage(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	second.finish(false)
	third := newToolState(ctx, prepared)
	if got, _ := third.Load("binding"); string(got) != `{"n":1}` {
		t.Fatal("rejected state leaked")
	}
	revoked = true
	if _, err := third.Load("binding"); !errors.Is(err, cause) {
		t.Fatal("cached batch state bypassed scope check")
	}
	third.finish(false)
	if err := recordJournal(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(saved.State) != 1 || string(saved.State[0].Value) != `{"n":1}` {
		t.Fatalf("changes=%+v", saved.State)
	}
	prepared.child = true
	if newToolState(ctx, prepared) != nil {
		t.Fatal("child received durable state capability")
	}
}

func TestJournalPanicStopsFurtherCommitAndPreservesCause(t *testing.T) {
	cause := errors.New("fixture panic")
	calls := 0
	ctx := withJournal(t.Context(), fixtureJournal{record: func(context.Context, JournalRecord) error { calls++; panic(cause) }})
	if err := recordJournal(ctx, nil, true); !errors.Is(err, cause) {
		t.Fatalf("panic cause=%v", err)
	}
	if err := recordJournal(ctx, nil, false); err == nil || calls != 1 {
		t.Fatalf("failed commit replayed: %v %d", err, calls)
	}
}

func TestJournalContinueRejectsAssistantBeforeIntroducingSystemContext(t *testing.T) {
	for _, mode := range []string{"prompt", "tool"} {
		t.Run(mode, func(t *testing.T) {
			requests, records := 0, 0
			definition := AgentDefinition{Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
				requests++
				return nil, errors.New("unexpected model request")
			})}
			if mode == "prompt" {
				definition.SystemPrompt = "new host instructions"
			} else {
				definition.Tools = []ToolDefinition{{Name: "new_tool", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) { return ToolResult{}, nil }}}
			}
			journal := fixtureJournal{record: func(context.Context, JournalRecord) error { records++; return nil }}
			snapshot := AgentSnapshot{Messages: []Message{NewTextMessage(RoleAssistant, "done")}}
			_, err := NewEngine().ContinueWithHooks(t.Context(), definition, &snapshot, nil, LoopHooks{Journal: journal})
			if !errors.Is(err, ErrCannotContinueFromAssistant) || requests != 0 || records != 0 {
				t.Fatalf("assistant tail accepted: err=%v requests=%d records=%d", err, requests, records)
			}
		})
	}
}

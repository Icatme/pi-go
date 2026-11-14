package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func projectionTarget() ContextProjectionOptions {
	return ContextProjectionOptions{RetainedThinking: DropPrefixBoundAnthropicThinking, TargetAPI: "anthropic-messages", TargetProvider: "anthropic", TargetModel: "fixture-prefix-bound"}
}

func projectionAssistant() agent.Message {
	return agent.Message{Role: agent.RoleAssistant, API: "anthropic-messages", Provider: "anthropic", Model: "fixture-prefix-bound", Parts: []agent.Part{
		{Type: agent.PartTypeThinking, Text: "retained reasoning", Signature: "opaque"},
		{Type: agent.PartTypeThinking, Redacted: true, Signature: "redacted"},
		{Type: agent.PartTypeText, Text: "answer"},
	}}
}

func projectionSession(t *testing.T) *Session {
	t.Helper()
	store, err := NewMemoryStorage(Header{ID: "projection-test"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(store, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestContextProjectionPreservesAuditToolsAndLaterThinking(t *testing.T) {
	s := projectionSession(t)
	message := projectionAssistant()
	call := agent.ToolCall{ID: "same-id", OriginalID: "original-id", Name: "lookup", Arguments: json.RawMessage(`{"n":9007199254740993}`)}
	message.ToolCalls = []agent.ToolCall{call}
	result := agent.NewToolResultMessage(call, agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "result"}}}, false)
	tail := []agent.Message{agent.NewUserTextMessage("kept"), message, result}
	if _, err := s.AppendCompaction(MainLane, CompactionData{Summary: "host summary", RetainedTail: tail}); err != nil {
		t.Fatal(err)
	}
	later := projectionAssistant()
	later.Parts[0].Signature = "new-prefix-signature"
	if _, err := s.AppendMessages(MainLane, []agent.Message{agent.NewUserTextMessage("next"), later}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.State()
	beforeJSON, _ := json.Marshal(before)
	original, _ := s.Context(MainLane)
	defaultView, err := s.ProjectContext(MainLane, ContextProjectionOptions{})
	if err != nil || !reflect.DeepEqual(original, defaultView) {
		t.Fatalf("default changed: %v", err)
	}
	view, err := s.ProjectContext(MainLane, projectionTarget())
	if err != nil {
		t.Fatal(err)
	}
	if view.Summary != original.Summary || len(view.Messages) != len(original.Messages) || len(view.Messages[1].Parts) != 1 {
		t.Fatalf("unexpected projection: %+v", view)
	}
	if !reflect.DeepEqual(view.Messages[1].ToolCalls, original.Messages[1].ToolCalls) || !reflect.DeepEqual(view.Messages[2], original.Messages[2]) || !reflect.DeepEqual(view.Messages[4], original.Messages[4]) {
		t.Fatal("tools/result/later thinking changed")
	}
	view.Messages[1].Parts[0].Text = "mutated view"
	after, _ := s.State()
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("audit changed")
	}
}

func TestContextProjectionFailsClosedWithoutChangingSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*agent.Message)
	}{
		{"thinking only", func(m *agent.Message) { m.Parts = m.Parts[:2] }},
		{"blank text", func(m *agent.Message) { m.Parts[2].Text = " \n" }},
		{"image without text", func(m *agent.Message) {
			m.Parts[2] = agent.Part{Type: agent.PartTypeImage, Data: "aW1hZ2U=", MIMEType: "image/png"}
		}},
		{"unknown part", func(m *agent.Message) { m.Parts = append(m.Parts, agent.Part{Type: "opaque"}) }},
		{"missing source", func(m *agent.Message) { m.Model = "" }},
		{"blank source", func(m *agent.Message) { m.API = " \n" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := projectionSession(t)
			m := projectionAssistant()
			tc.edit(&m)
			if _, err := s.AppendCompaction(MainLane, CompactionData{Summary: "summary", RetainedTail: []agent.Message{agent.NewUserTextMessage("q"), m}}); err != nil {
				t.Fatal(err)
			}
			before, _ := s.Context(MainLane)
			if _, err := s.ProjectContext(MainLane, projectionTarget()); !errors.Is(err, ErrContextProjection) {
				t.Fatalf("got %v", err)
			}
			after, _ := s.Context(MainLane)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("source changed after error")
			}
		})
	}
}

func TestContextProjectionToolOnlyRetainsPair(t *testing.T) {
	s := projectionSession(t)
	m := projectionAssistant()
	m.Parts = m.Parts[:2]
	call := agent.ToolCall{ID: "tool", Name: "lookup", Arguments: json.RawMessage(`{}`)}
	m.ToolCalls = []agent.ToolCall{call}
	result := agent.NewToolResultMessage(call, agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "ok"}}}, false)
	if _, err := s.AppendCompaction(MainLane, CompactionData{Summary: "s", RetainedTail: []agent.Message{agent.NewUserTextMessage("q"), m, result}}); err != nil {
		t.Fatal(err)
	}
	baseline, err := s.Context(MainLane)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.ProjectContext(MainLane, projectionTarget())
	if err != nil || len(view.Messages) != 3 || len(view.Messages[1].Parts) != 0 || !reflect.DeepEqual(view.Messages[1].ToolCalls, baseline.Messages[1].ToolCalls) || !reflect.DeepEqual(view.Messages[2], baseline.Messages[2]) {
		t.Fatalf("pair changed: %+v %v", view, err)
	}
}

func TestContextProjectionExplicitScopeAndNoCompaction(t *testing.T) {
	s := projectionSession(t)
	unknown := projectionAssistant()
	unknown.Model = ""
	if _, err := s.AppendMessages(MainLane, []agent.Message{agent.NewUserTextMessage("q"), unknown}); err != nil {
		t.Fatal(err)
	}
	want, _ := s.Context(MainLane)
	got, err := s.ProjectContext(MainLane, projectionTarget())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("no-compaction changed")
	}
	for _, options := range []ContextProjectionOptions{{RetainedThinking: "unknown"}, {RetainedThinking: DropPrefixBoundAnthropicThinking}, {RetainedThinking: DropPrefixBoundAnthropicThinking, TargetAPI: "openai-completions", TargetProvider: "anthropic", TargetModel: "m"}} {
		if _, err := s.ProjectContext(MainLane, options); !errors.Is(err, ErrContextProjection) {
			t.Fatalf("invalid policy accepted: %v", err)
		}
	}
	other := projectionAssistant()
	other.Provider = "known-other"
	if _, err := s.AppendCompaction(MainLane, CompactionData{Summary: "s", RetainedTail: []agent.Message{agent.NewUserTextMessage("q"), other}}); err != nil {
		t.Fatal(err)
	}
	want, _ = s.Context(MainLane)
	got, err = s.ProjectContext(MainLane, projectionTarget())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("known foreign identity changed")
	}
}

func TestContextProjectionBranchesAndLatestCompaction(t *testing.T) {
	s := projectionSession(t)
	first, err := s.AppendCompaction(MainLane, CompactionData{Summary: "old", RetainedTail: []agent.Message{agent.NewUserTextMessage("q1"), projectionAssistant()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLane("fork", first.ID); err != nil {
		t.Fatal(err)
	}
	second := projectionAssistant()
	second.Parts[0].Signature = "second"
	if _, err := s.AppendCompaction(MainLane, CompactionData{Summary: "new", RetainedTail: []agent.Message{agent.NewUserTextMessage("q2"), second}}); err != nil {
		t.Fatal(err)
	}
	for lane, summary := range map[string]string{MainLane: "new", "fork": "old"} {
		view, err := s.ProjectContext(lane, projectionTarget())
		if err != nil || view.Summary != summary || len(view.Messages) != 2 || len(view.Messages[1].Parts) != 1 {
			t.Fatalf("%s: %+v %v", lane, view, err)
		}
	}
}

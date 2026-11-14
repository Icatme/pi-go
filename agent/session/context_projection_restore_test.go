package session

import (
	"encoding/json"
	"github.com/Icatme/pi-go/agent"
	"reflect"
	"testing"
)

func TestContextProjectionRestoredStateMixedToolOnly(t *testing.T) {
	s := projectionSession(t)
	m := projectionAssistant()
	m.Parts = m.Parts[:2]
	m.ToolCalls = []agent.ToolCall{{ID: "call", Name: "x", ThoughtSignature: "tool-opaque", Arguments: json.RawMessage(`{"n":9007199254740993}`)}}
	r := agent.NewToolResultMessage(m.ToolCalls[0], agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "ok"}}}, false)
	foreign := projectionAssistant()
	foreign.Model = "foreign"
	foreign.Parts = append(foreign.Parts, agent.Part{Type: "opaque", Data: "unknown"})
	_, err := s.AppendCompaction(MainLane, CompactionData{Summary: "s", RetainedTail: []agent.Message{agent.NewUserTextMessage("q"), m, r, foreign}})
	if err != nil {
		t.Fatal(err)
	}
	source, _ := s.State()
	raw, _ := json.Marshal(source)
	var restored State
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	original, _ := restored.Context(MainLane)
	v, err := restored.ProjectContext(MainLane, projectionTarget())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Messages[1].Parts) != 0 || !reflect.DeepEqual(v.Messages[1].ToolCalls, original.Messages[1].ToolCalls) || !reflect.DeepEqual(v.Messages[2], original.Messages[2]) || !reflect.DeepEqual(v.Messages[3], original.Messages[3]) {
		t.Fatal("tool-only or mixed identity changed")
	}
	v.Messages[1].ToolCalls[0].Arguments[0] = '!'
	again, _ := json.Marshal(restored)
	if string(raw) != string(again) {
		t.Fatal("mutated source")
	}
}
func TestContextProjectionSessionStorageUnchanged(t *testing.T) {
	store, err := NewMemoryStorage(Header{ID: "memory-projection"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(store, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.AppendCompaction(MainLane, CompactionData{Summary: "s", RetainedTail: []agent.Message{agent.NewUserTextMessage("q"), projectionAssistant()}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(store.Log())
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.ProjectContext(MainLane, projectionTarget())
	if err != nil {
		t.Fatal(err)
	}
	v.Messages[1].Parts[0].Text = "mutated projection"
	after, err := json.Marshal(store.Log())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("Session projection changed storage log")
	}
}

package session

import (
	"github.com/Icatme/pi-go/agent"
	"path/filepath"
	"testing"
)

func TestJSONLFailedResultProvenanceRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "audit.jsonl")
	s, e := CreateJSONLStorage(p, Header{ID: "audit"})
	if e != nil {
		t.Fatal(e)
	}
	m := agent.NewToolResultMessage(agent.ToolCall{ID: "x", Name: "echo"}, agent.ToolResult{}, true)
	m.ToolResult.Provenance = agent.ToolResultProvenanceGenerationFailedUnexecuted
	entry, e := s.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: "result", Message: &m})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	r, e := OpenJSONLStorage(p)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	got, ok := r.Entry(entry.ID)
	if !ok || got.Message == nil || got.Message.ToolResult.Provenance != agent.ToolResultProvenanceGenerationFailedUnexecuted {
		t.Fatalf("lost provenance: %+v", got)
	}
	got.Message.ToolResult.Provenance = "changed"
	again, _ := r.Entry(entry.ID)
	if again.Message.ToolResult.Provenance != agent.ToolResultProvenanceGenerationFailedUnexecuted {
		t.Fatal("entry alias mutated")
	}
}

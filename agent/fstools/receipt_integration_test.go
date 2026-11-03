//go:build linux && !android

package fstools_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/agent/fstools"
	"github.com/Icatme/pi-go/agent/session"
	"github.com/Icatme/pi-go/codemode"
	"os"
	"path/filepath"
	"testing"
)

type stream struct {
	m agent.Message
	c chan agent.AssistantEvent
}

func (s *stream) Events() <-chan agent.AssistantEvent { return s.c }
func (s *stream) Wait() (agent.Message, error)        { return s.m, nil }
func (s *stream) Close() error                        { return nil }
func model(calls []agent.ToolCall) agent.StreamFunc {
	i := 0
	return func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		m := agent.NewTextMessage(agent.RoleAssistant, "done")
		m.StopReason = agent.StopReasonStop
		if i < len(calls) {
			m.Parts = nil
			m.ToolCalls = []agent.ToolCall{calls[i]}
			m.StopReason = agent.StopReasonToolUse
			i++
		}
		c := make(chan agent.AssistantEvent, 1)
		c <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: m}
		close(c)
		return &stream{m, c}, nil
	}
}
func setup(t *testing.T) (string, []agent.ToolDefinition) {
	t.Helper()
	dir := t.TempDir()
	root, e := os.OpenRoot(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { root.Close() })
	tools, e := fstools.WorkspaceWriteDefinitions(root, fstools.WorkspaceWriteOptions{})
	if e != nil {
		t.Fatal(e)
	}
	return dir, tools
}
func calls() []agent.ToolCall {
	return []agent.ToolCall{{ID: "c", Name: "create_file", Arguments: json.RawMessage(`{"path":"file","content":"one"}`)}, {ID: "r", Name: "atomic_replace", Arguments: json.RawMessage(`{"path":"file","content":"two"}`)}, {ID: "e", Name: "edit", Arguments: json.RawMessage(`{"path":"file","old_text":"two","new_text":"three"}`)}}
}
func TestModelFileToolsWithCompatibleReceipts(t *testing.T) {
	dir, tools := setup(t)
	r, e := agent.NewRunner(agent.AgentDefinition{Tools: tools, Model: model(calls())})
	if e != nil {
		t.Fatal(e)
	}
	s, h, e := r.RunWithReceipts(t.Context(), agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("write")}, agent.LoopHooks{}, 8)
	if e != nil {
		t.Fatal(e)
	}
	for range s.Events() {
	}
	if _, e = s.Wait(); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, "file"))
	if e != nil || string(b) != "three" {
		t.Fatalf("%q %v", b, e)
	}
	facts := h.Snapshot()
	if len(facts) != 3 {
		t.Fatal(facts)
	}
	for _, f := range facts {
		if !f.Committed() || !f.Completed() {
			t.Fatal(f)
		}
	}
}
func TestLegacyFileWritesRefuseCaptureBeforeIO(t *testing.T) {
	for _, call := range calls() {
		t.Run(call.Name, func(t *testing.T) {
			dir, tools := setup(t)
			original := "two"
			if call.Name == "atomic_replace" {
				original = "one"
			}
			if call.Name != "create_file" {
				if err := os.WriteFile(filepath.Join(dir, "file"), []byte(original), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out := agent.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: tools})
			if out.Err == nil {
				t.Fatal("legacy write admitted")
			}
			if call.Name == "create_file" {
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 0 {
					t.Fatal("legacy write changed directory", err)
				}
			} else {
				content, err := os.ReadFile(filepath.Join(dir, "file"))
				if err != nil || string(content) != original {
					t.Fatalf("legacy write changed file: %q %v", content, err)
				}
			}
		})
	}
}
func TestFileCommitSurvivesHookFailure(t *testing.T) {
	dir, tools := setup(t)
	out, h, e := agent.RunToolCallWithReceipts(t.Context(), calls()[0], agent.RunToolCallOptions{Tools: tools, AfterToolCall: func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		return agent.AfterToolCallResult{}, errors.New("withheld")
	}}, 8)
	if e != nil || out.Err == nil || !h.Snapshot()[0].Committed() {
		t.Fatal(out, e)
	}
	if b, e := os.ReadFile(filepath.Join(dir, "file")); e != nil || string(b) != "one" {
		t.Fatal(string(b), e)
	}
}
func TestFileToolsSessionDirectReceipts(t *testing.T) {
	dir, tools := setup(t)
	store, e := session.NewMemoryStorage(session.Header{ID: "files"})
	if e != nil {
		t.Fatal(e)
	}
	s, e := session.New(store, session.Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	options := session.RuntimeOptions{Scope: func() session.RuntimeScope { return session.RuntimeScope{Identity: "host", AuthEpoch: 1} }}
	for _, call := range calls() {
		b, e := s.PrepareReceiptCall(session.MainLane, options)
		if e != nil {
			t.Fatal(e)
		}
		out, _, e := b.RunToolCall(t.Context(), call, agent.RunToolCallOptions{Tools: tools}, 8)
		if e != nil || out.Err != nil {
			t.Fatal(out, e)
		}
	}
	status, e := s.ReadReceiptStatus(session.MainLane, options)
	if e != nil || status.Pending() || !status.Facts()[0].Committed() {
		t.Fatal(status, e)
	}
	if _, e = s.PrepareRun(session.MainLane, options); !errors.Is(e, session.ErrInterrupted) {
		t.Fatal(e)
	}
	if b, e := os.ReadFile(filepath.Join(dir, "file")); e != nil || string(b) != "three" {
		t.Fatal(string(b), e)
	}
}
func TestModelCodemodeFileTools(t *testing.T) {
	dir, tools := setup(t)
	sandbox, e := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if e != nil {
		t.Fatal(e)
	}
	defer sandbox.Close(context.Background())
	var bindings []codemodetool.Binding
	for _, tool := range tools[5:] {
		bindings = append(bindings, codemodetool.Native(tool, "workspace"))
	}
	container, e := codemodetool.New(sandbox, bindings, codemodetool.Options{})
	if e != nil {
		t.Fatal(e)
	}
	code := `await tools.create_file({path:"file",content:"one"});await tools.atomic_replace({path:"file",content:"two"});await tools.edit({path:"file",old_text:"two",new_text:"three"});`
	raw, _ := json.Marshal(map[string]string{"code": code})
	r, e := agent.NewRunner(agent.AgentDefinition{Tools: []agent.ToolDefinition{container}, Model: model([]agent.ToolCall{{ID: "script", Name: container.Name, Arguments: raw}})})
	if e != nil {
		t.Fatal(e)
	}
	run, h, e := r.RunWithReceipts(t.Context(), agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("write")}, agent.LoopHooks{}, 8)
	if e != nil {
		t.Fatal(e)
	}
	for range run.Events() {
	}
	if _, e = run.Wait(); e != nil {
		t.Fatal(e)
	}
	commits := 0
	for _, f := range h.Snapshot() {
		if f.Committed() {
			commits++
		}
	}
	if commits != 3 {
		t.Fatalf("%d commits: %+v", commits, h.Snapshot())
	}
	if b, e := os.ReadFile(filepath.Join(dir, "file")); e != nil || string(b) != "three" {
		t.Fatal(string(b), e)
	}
}

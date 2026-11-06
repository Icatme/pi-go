//go:build linux && !android

package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/fstools"
)

// The destination, executor receipt and model's final claim can disagree.
// Check all three at the public coordinator boundary, including journal replay.
func TestFilesystemPublicationAndWorkerReport(t *testing.T) {
	for _, tc := range []struct {
		name             string
		failAfterPublish bool
		wantStatus       string
	}{
		{"success", false, "completed"},
		{"withheld_result", true, "completed_with_tool_errors"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			tool, err := fstools.CreateFileDefinition(root, fstools.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			const modelClaim = "Created the requested file."
			def := agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: agent.StreamFunc(
				func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
					if req.Messages[len(req.Messages)-1].Role == agent.RoleTool {
						return offlineAnswer(modelClaim), nil
					}
					msg := agent.NewTextMessage(agent.RoleAssistant, "")
					msg.StopReason = agent.StopReasonToolUse
					msg.ToolCalls = []agent.ToolCall{{ID: "publish", Name: "create_file", Arguments: json.RawMessage(`{"path":"output.txt","content":"hello"}`)}}
					ch := make(chan agent.AssistantEvent, 1)
					ch <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: msg}
					close(ch)
					return &offlineStream{final: msg, events: ch}, nil
				})}
			if tc.failAfterPublish {
				def.AfterToolCall = func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
					return agent.AfterToolCallResult{}, errors.New("host withheld tool result")
				}
			}
			work, err := PiWorker(def)
			if err != nil {
				t.Fatal(err)
			}
			route := func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }
			path := filepath.Join(t.TempDir(), "run.jsonl")
			s, err := Open(t.Context(), path, 1, route, work)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			event := Trigger{ID: "1", Source: "local", Thread: "publication", Kind: "request", Text: "create file"}
			submit(t, s, event)
			waitIdle(t, s)
			data, err := os.ReadFile(filepath.Join(dir, "output.txt"))
			if err != nil || string(data) != "hello" {
				t.Fatalf("file contents %q, %v", data, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(t.Context(), path, 1, route, work)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if accepted, err := s.Submit(event); err != nil || accepted {
				t.Fatalf("duplicate accepted=%t err=%v", accepted, err)
			}
			reports := s.ReportsSince(0)
			if len(reports) != 1 {
				t.Fatalf("reports: %+v", reports)
			}
			r := reports[0]
			if r.Status != tc.wantStatus || r.NeedsReview != tc.failAfterPublish || len(r.Receipts) != 1 {
				t.Fatalf("replayed report: %+v", r)
			}
			if got := r.Receipts[0]; got.CallID != "publish" || got.ToolName != "create_file" || !got.Committed || !got.Completed {
				t.Fatalf("publication receipt: %+v", got)
			}
			if tc.failAfterPublish && r.Text == modelClaim {
				t.Fatal("model's unqualified claim survived the hook failure")
			}
			r.Receipts[0].Committed = false
			if !s.Status().Reports[0].Receipts[0].Committed {
				t.Fatal("caller mutated stored evidence through report")
			}
		})
	}
}

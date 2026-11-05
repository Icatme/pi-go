package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
)

// Abort races an executor which has crossed its point of no return. An
// acknowledgement must not release the worker slot or lose the late receipt.
func TestTaskAbortRetainsLatePiToolCommit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	tool := agent.ToolDefinition{Name: "publish", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		close(entered)
		<-release
		if !executionreceipt.ReportLocalCommit(ctx) {
			return agent.ToolResult{}, errors.New("missing capture after cancel")
		}
		return agent.ToolResult{Terminate: true}, nil
	}}
	model := agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Role != agent.RoleUser || !strings.Contains(messageText(last), "publish now") {
			return offlineAnswer("acknowledged"), nil
		}
		msg := agent.NewTextMessage(agent.RoleAssistant, "")
		msg.StopReason = agent.StopReasonToolUse
		msg.ToolCalls = []agent.ToolCall{{ID: "publish-1", Name: "publish", Arguments: json.RawMessage(`{}`)}}
		ch := make(chan agent.AssistantEvent, 1)
		ch <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: msg}
		close(ch)
		return &offlineStream{final: msg, events: ch}, nil
	})
	w, err := PiWorker(agent.AgentDefinition{Tools: []agent.ToolDefinition{tool}, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	otherDone := make(chan struct{})
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "tasks.jsonl"), 2, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, func(ctx context.Context, j Job) (Outcome, error) {
		out, err := w(ctx, j)
		if j.Trigger.ID == "other" {
			close(otherDone)
		}
		return out, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	defer unblock()
	submit(t, s, Trigger{"1", "im", "release", "request", "publish now"})
	receive(t, entered)
	task := s.Status().Tasks[0]
	target := RunTarget{TaskID: task.ID, Source: task.ActiveSource, EventID: task.ActiveEventID}
	submit(t, s, Trigger{"2", "im", "release", "request", "continue"})
	if err := s.Abort(target); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(target.TaskID, target.Source, target.EventID, "tool still executing"); err == nil {
		t.Fatal("released an abort while the executor was still running")
	}
	submit(t, s, Trigger{"other", "im", "support", "request", "independent"})
	receive(t, otherDone)
	unblock()
	waitIdle(t, s)
	for _, r := range s.ReportsSince(0) {
		switch r.EventID {
		case "1":
			if r.Status != "interrupted" || !r.NeedsReview || len(r.Receipts) != 1 || !r.Receipts[0].Committed || !r.Receipts[0].Completed {
				t.Fatal(r)
			}
		case "2":
			if r.Status != "blocked" {
				t.Fatal(r)
			}
		case "other":
			if r.Status != "completed" {
				t.Fatal(r)
			}
		}
	}
	if len(s.ReportsSince(0)) != 3 {
		t.Fatal(s.Status())
	}
	if err := s.FailureCause(target.Source, target.EventID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

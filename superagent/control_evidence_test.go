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
)

func TestMainCorrectionUsesExactSavedInputEvidence(t *testing.T) {
	for _, mode := range []string{"accepted", "interrupted_accepted", "missing", "text_only", "wrong_role", "wrong_id", "wrong_origin", "wrong_task", "wrong_run", "wrong_text", "crash_after_dequeue"} {
		t.Run(mode, func(t *testing.T) {
			a, path := controlJournal(t, "steer", true)
			r := activateControlFixture(t, a)
			a.dispatchMainControl()
			messages, err := a.takeSteering(t.Context(), r, agent.QueueModeAll)
			if err != nil || len(messages) != 1 {
				t.Fatalf("poll: %+v %v", messages, err)
			}
			if a.MainControls()[0].InputAccepted {
				t.Fatal("dequeue falsely proved saved input")
			}
			switch mode {
			case "missing":
				messages = nil
			case "text_only":
				messages[0].Metadata = nil
			case "wrong_role":
				messages[0].Role = agent.RoleAssistant
			case "wrong_id":
				messages[0].Metadata[controlMetadata] = "other-control"
			case "wrong_origin":
				messages[0].Metadata["superagent.control_event"] = "other-message"
			case "wrong_task":
				messages[0].Metadata["superagent.control_task"] = "task-2"
			case "wrong_run":
				messages[0].Metadata["superagent.control_run_event"] = "next"
			case "wrong_text":
				messages[0].Parts = []agent.Part{agent.NewTextPart("different")}
			}
			if mode != "crash_after_dequeue" {
				snapshot, err := encodeSnapshot(agent.AgentSnapshot{SessionID: "task-1", Messages: messages}, "task-1")
				if err != nil {
					t.Fatal(err)
				}
				state := "completed"
				if mode == "interrupted_accepted" {
					state = "interrupted"
				}
				if err := a.commit(transition{State: state, Index: 0, Snapshot: snapshot, NeedsReview: state == "interrupted", Text: "worker ended"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			var notifications int
			route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
				if job.Input.Kind == "control_result" {
					notifications++
					if job.Input.TaskID != "task-1" || job.Input.Trigger.ID != "control" || job.Input.Control.Status != "needs_review" {
						t.Errorf("notification: %+v", job.Input)
					}
					job.Input.Control.Text = "mutated"
					return MainOutcome{Decision: MainDecision{Action: "clarify", Text: "Please verify whether the correction was received."}, Snapshot: job.Snapshot}, nil
				}
				return mainFixture(ctx, job)
			}
			b, err := OpenMain(t.Context(), path, 1, route, func(ctx context.Context, job Job) (Outcome, error) {
				if job.TaskID != "task-1" || job.Trigger.ID != "next" {
					t.Error("uncertain work reissued")
				}
				got, err := job.TakeSteering(ctx, agent.QueueModeAll)
				if err != nil || len(got) != 0 {
					t.Error("correction leaked into next run")
				}
				return mainWorker(ctx, job) // intentionally replaces old snapshot
			})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			waitIdle(t, b)
			accepted := mode == "accepted" || mode == "interrupted_accepted"
			want := "needs_review"
			if accepted {
				want = "delivered"
			}
			control := b.MainControls()[0]
			if control.Status != want || control.InputAccepted != accepted || control.Text != "correct" {
				t.Fatalf("evidence: %+v", control)
			}
			if notifications != map[bool]int{true: 0, false: 1}[accepted] {
				t.Fatalf("notifications=%d", notifications)
			}
			if !accepted {
				replies := b.Replies()
				found := false
				for _, reply := range replies {
					if reply.EventID == "control" {
						found = true
						if reply.Control == nil || reply.Control.Status != "needs_review" || reply.Control.Text != "correct" || reply.Thread != "control" {
							t.Fatal("reply lost authoritative control facts")
						}
						reply.Control.Text = "read mutation"
					}
				}
				if !found {
					t.Fatal("clarification not delivered to original correction")
				}
				for _, reply := range b.Replies() {
					if reply.Control != nil && reply.Control.Text != "correct" {
						t.Fatal("reply control aliases caller memory")
					}
				}
			}
			if mode == "interrupted_accepted" || mode == "crash_after_dequeue" {
				if b.Status().Tasks[0].Status != "needs_review" {
					t.Fatal("control acceptance cleared effects review")
				}
				if err := b.Reconcile("task-1", "im", "start", "Checked the interrupted execution"); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := b.SubmitTask(Trigger{ID: "next", Source: "im", Thread: "work", Kind: "request"}, "task-1"); !ok || err != nil {
				t.Fatal(err)
			}
			waitIdle(t, b)
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
			c, err := OpenMain(t.Context(), path, 1, route, mainWorker)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			waitIdle(t, c)
			if c.MainControls()[0].Status != want || c.MainControls()[0].InputAccepted != accepted || notifications != map[bool]int{true: 0, false: 1}[accepted] {
				t.Fatal("later snapshot/reopen lost evidence or duplicated clarification")
			}
		})
	}
}

func TestMainCorrectionNotificationCannotAuthorizeNewWork(t *testing.T) {
	for _, action := range []string{"new", "attach", "steer", "cancel"} {
		t.Run(action, func(t *testing.T) {
			a, path := controlJournal(t, "steer", true)
			r := activateControlFixture(t, a)
			a.dispatchMainControl()
			if _, err := a.takeSteering(t.Context(), r, agent.QueueModeAll); err != nil {
				t.Fatal(err)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMain(t.Context(), path, 1, func(ctx context.Context, job MainJob) (MainOutcome, error) {
				if job.Input.Kind == "control_result" {
					d := MainDecision{Action: action}
					if action == "attach" {
						d.TaskID = "task-1"
					}
					return MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
				}
				return mainFixture(ctx, job)
			}, func(context.Context, Job) (Outcome, error) {
				t.Error("notification dispatched work")
				return Outcome{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			waitIdle(t, b)
			if len(b.MainFailures()) != 1 || len(b.MainControls()) != 1 {
				t.Fatal("notification authorized another action")
			}
		})
	}
}

// Use otherwise valid controls so this tests authority, not malformed output
// or a target that finished before validation.
func TestMainInformationalInputRejectsValidControl(t *testing.T) {
	for _, kind := range []string{"task_input", "control_result"} {
		for _, action := range []string{"steer", "cancel"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				a, _ := controlJournal(t, "steer", true)
				c := a.MainControls()[0]
				if kind == "task_input" {
					trigger := Trigger{ID: "update", Source: "github", Thread: "pr", Kind: "request"}
					if err := a.commit(transition{State: "received", Index: len(a.events), Trigger: &trigger, Task: "task-1"}); err != nil {
						t.Fatal(err)
					}
				} else if err := a.commitControls("not_delivered", []string{c.ID}); err != nil {
					t.Fatal(err)
				}
				input := a.main.inbox[a.main.cursor].input
				if input.Kind != kind || a.controlEvent(c.Target).state != "running" {
					t.Fatal("invalid authority fixture")
				}
				d := MainDecision{Action: action, Target: &c.Target}
				if action == "steer" {
					d.Text = "valid correction"
				}
				m := &mainTransition{InputID: input.ID, DecisionID: "main-decision-" + input.ID, Decision: d, Snapshot: json.RawMessage(`{"session_id":"main-test"}`)}
				err := a.validateMain(transition{State: "main_decided", Main: m})
				if err == nil || !strings.Contains(err.Error(), "informational input") {
					t.Fatalf("authority not checked: %v", err)
				}
			})
		}
	}
}

func TestPiMainCorrectionRetainsEvidenceOnFailedTurn(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	work, err := PiWorker(agent.AgentDefinition{MaxTurns: 1, Model: agent.StreamFunc(func(ctx context.Context, _ agent.ModelRequest) (agent.AssistantStream, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		msg := agent.NewTextMessage(agent.RoleAssistant, "Inspecting")
		msg.StopReason = agent.StopReasonToolUse
		msg.ToolCalls = []agent.ToolCall{{ID: "call", Name: "read", Arguments: json.RawMessage(`{}`)}}
		return piReply(msg), nil
	}), Tools: []agent.ToolDefinition{{Name: "read", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		return agent.ToolResult{Content: []agent.Part{agent.NewTextPart("result")}}, nil
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pi.jsonl")
	a, err := OpenMain(t.Context(), path, 1, controlRouter, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer resume()
	submit(t, a, Trigger{ID: "start", Source: "im", Thread: "chat", Kind: "request"})
	receive(t, entered)
	submit(t, a, Trigger{ID: "control", Source: "im", Thread: "chat", Kind: "request", Text: "steer"})
	waitMainControl(t, a, "queued")
	resume()
	waitIdle(t, a)
	if !errors.Is(a.FailureCause("im", "start"), agent.ErrMaxTurnsExceeded) {
		t.Fatal(a.FailureCause("im", "start"))
	}
	if c := a.MainControls()[0]; c.Status != "delivered" || !c.InputAccepted {
		t.Fatalf("Pi partial input lost: %+v", c)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, controlRouter, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	if c := b.MainControls()[0]; c.Status != "delivered" || !c.InputAccepted {
		t.Fatalf("Pi partial input lost on reopen: %+v", c)
	}
}

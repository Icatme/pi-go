package superagent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
)

// The development plan's logs A / tests B conversation: a second topic must
// start before A finishes, and a correction in that same chat reaches only A.
func TestMainSameChatIndependentWorkAndCorrection(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan Job, 2)
	poll := make(chan struct{})
	corrected := make(chan []agent.Message, 1)
	finish := make(chan struct{})
	route := func(_ context.Context, job MainJob) (MainOutcome, error) {
		d := MainDecision{Action: "new"}
		if job.Input.Kind == "worker_result" {
			d = MainDecision{Action: "reply", Text: job.Input.Report.Text}
		} else if job.Input.Trigger.ID == "correction" {
			for _, target := range job.Runs {
				if target.EventID == "logs" {
					d = MainDecision{Action: "steer", Target: &target, Text: "Only inspect the last hour of logs"}
				}
			}
		}
		return MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
	}
	work := func(ctx context.Context, job Job) (Outcome, error) {
		started <- job
		if job.Trigger.ID == "logs" {
			select {
			case <-poll:
			case <-ctx.Done():
				return Outcome{}, ctx.Err()
			}
			messages, err := job.TakeSteering(ctx, agent.QueueModeAll)
			if err != nil {
				return Outcome{}, err
			}
			corrected <- messages
			select {
			case <-finish:
			case <-ctx.Done():
				return Outcome{}, ctx.Err()
			}
			return Outcome{Summary: "logs done", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Messages: messages}}, nil
		}
		if messages, err := job.TakeSteering(ctx, agent.QueueModeAll); err != nil || len(messages) != 0 || len(job.Snapshot.Messages) != 0 {
			t.Errorf("independent task received A context: %+v %v", messages, err)
		}
		return mainWorker(ctx, job)
	}
	path := filepath.Join(t.TempDir(), "chat.jsonl")
	a, err := OpenMain(ctx, path, 2, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	first := Trigger{ID: "logs", Source: "im", Thread: "chat", Kind: "request", Text: "Look at the logs"}
	submit(t, a, first)
	var logs Job
	select {
	case logs = <-started:
	case <-ctx.Done():
		t.Fatal("logs task did not start")
	}
	submit(t, a, Trigger{ID: "tests", Source: "im", Thread: "chat", Kind: "request", Text: "Also check the failed tests"})
	select {
	case tests := <-started:
		if tests.TaskID == logs.TaskID || tests.Trigger.ID != "tests" {
			t.Fatalf("second topic was not independent: %+v", tests)
		}
	case <-ctx.Done():
		t.Fatal("independent test task waited for the logs task")
	}
	correction := Trigger{ID: "correction", Source: "im", Thread: "chat", Kind: "request", Text: "Only inspect the last hour of logs"}
	submit(t, a, correction)
	control := waitMainControl(t, a, "queued")
	if control.Target.TaskID != logs.TaskID {
		t.Fatalf("correction went to wrong task: %+v", control)
	}
	close(poll)
	select {
	case messages := <-corrected:
		if len(messages) != 1 || messageText(messages[0]) != correction.Text {
			t.Fatalf("correction: %+v", messages)
		}
	case <-ctx.Done():
		t.Fatal("correction was not delivered")
	}
	close(finish)
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if len(a.Status().Tasks) != 2 || len(a.Replies()) != 2 {
		t.Fatalf("unexpected task/reply count: %+v %+v", a.Status(), a.Replies())
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 2, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, trigger := range []Trigger{first, correction} {
		if accepted, err := b.Submit(trigger); accepted || err != nil {
			t.Fatalf("replayed duplicate: %v %v", accepted, err)
		}
	}
	waitIdle(t, b)
	if len(b.Status().Tasks) != 2 || len(b.MainControls()) != 1 || b.MainControls()[0].ID != control.ID || len(b.Replies()) != 2 {
		t.Fatal("replay changed task/correction/reply identity")
	}
}

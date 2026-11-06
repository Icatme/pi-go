package superagent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

// M1b acceptance: a validated main decision is durably queued before it can
// reach an exact Worker invocation, and survives duplicate input and restart.
func TestMainStructuredSteeringIsDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.jsonl")
	started := make(chan Job, 1)
	poll := make(chan struct{})
	got := make(chan []agent.Message, 1)
	finish := make(chan struct{})
	route := func(_ context.Context, job MainJob) (MainOutcome, error) {
		decision := MainDecision{Action: "new"}
		if job.Input.Kind == "worker_result" {
			decision = MainDecision{Action: "ignore"}
		}
		if job.Input.Trigger.ID == "correct" {
			if len(job.Runs) != 1 {
				t.Errorf("offered runs: %+v", job.Runs)
			}
			decision = MainDecision{Action: "steer", Target: &job.Runs[0], Text: "Only the last hour"}
		}
		return MainOutcome{Decision: decision, Snapshot: job.Snapshot}, nil
	}
	work := func(ctx context.Context, job Job) (Outcome, error) {
		started <- job
		select {
		case <-poll:
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
		messages, err := job.TakeSteering(ctx, agent.QueueModeAll)
		if err != nil {
			return Outcome{}, err
		}
		got <- messages
		select {
		case <-finish:
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
		return Outcome{Summary: "done", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Messages: messages}}, nil
	}
	a, err := OpenMain(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "start", Source: "im", Thread: "work", Kind: "request"})
	job := receive(t, started)
	correction := Trigger{ID: "correct", Source: "im", Thread: "control", Kind: "request"}
	submit(t, a, correction)
	action := waitMainControl(t, a, "queued")
	if action.Target != (RunTarget{TaskID: job.TaskID, Source: job.Trigger.Source, EventID: job.Trigger.ID}) || action.ID == "" || action.DecisionID == "" || action.InputID == "" {
		t.Fatalf("action: %+v", action)
	}
	if ok, err := a.Submit(correction); ok || err != nil {
		t.Fatal("duplicate control admitted")
	}
	close(poll)
	if messages := receive(t, got); len(messages) != 1 || messageText(messages[0]) != "Only the last hour" {
		t.Fatalf("steering: %+v", messages)
	}
	waitMainControl(t, a, "delivered")
	close(finish)
	waitIdle(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	actions := b.MainControls()
	if len(actions) != 1 || actions[0].ID != action.ID || actions[0].Status != "delivered" {
		t.Fatalf("replay: %+v", actions)
	}
}

func waitMainControl(t *testing.T, a *SuperAgent, status string) MainControl {
	t.Helper()
	for {
		a.mu.Lock()
		changed := a.changed
		a.mu.Unlock()
		for _, action := range a.MainControls() {
			if action.Status == status {
				return action
			}
		}
		select {
		case <-changed:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
}

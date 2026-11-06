package superagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func activateControlFixture(t *testing.T, a *SuperAgent) *activeRun {
	t.Helper()
	ctx, cancel := context.WithCancel(a.ctx)
	t.Cleanup(cancel)
	r := &activeRun{target: a.main.controls[0].Target, ctx: ctx, cancel: cancel}
	a.tasks[r.target.TaskID].active = r
	return r
}

func TestMainControlPostCommitRechecksExactInvocation(t *testing.T) {
	for _, mode := range []string{"returned", "different", "aborted"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := controlJournal(t, "steer")
			r := activateControlFixture(t, a)
			switch mode {
			case "returned":
				r.returned = true
			case "different":
				r.target.EventID = "next"
			case "aborted":
				r.aborted = true
			}
			if !a.dispatchMainControl() || a.MainControls()[0].Status != "not_delivered" || len(r.queue) != 0 {
				t.Fatal("stale pending action dispatched")
			}
		})
	}
}

func TestMainControlJournalFailureNeverDelivers(t *testing.T) {
	for _, mode := range []string{"enqueue", "cancel", "delivery"} {
		t.Run(mode, func(t *testing.T) {
			action := "steer"
			if mode == "cancel" {
				action = "cancel"
			}
			a, _ := controlJournal(t, action)
			r := activateControlFixture(t, a)
			if mode == "delivery" {
				if !a.dispatchMainControl() {
					t.Fatal("not dispatched")
				}
			}
			if err := a.store.Close(); err != nil {
				t.Fatal(err)
			}
			if mode == "delivery" {
				native := &SteerReceipt{done: make(chan struct{})}
				r.queue = append(r.queue, steering{text: "native", receipt: native})
				messages, err := a.takeSteering(t.Context(), r, agent.QueueModeAll)
				if err == nil || len(messages) != 0 || a.main.controls[0].Status != "queued" {
					t.Fatal("failed append handed off messages")
				}
				a.dropRunSteeringLocked(r)
				if ok, err := native.Wait(t.Context()); ok || err != nil {
					t.Fatal("native receipt leaked on fatal cleanup")
				}
			} else {
				a.dispatchMainControl()
				if len(r.queue) != 0 || r.aborted || a.main.controls[0].Status != "pending" {
					t.Fatal("failed append applied control")
				}
			}
			if a.fatal == nil {
				t.Fatal("journal failure did not fail stop")
			}
		})
	}
}

func TestMainControlMixedBatchIsAtomicAndDeduplicated(t *testing.T) {
	a, _ := controlJournal(t, "steer")
	r := activateControlFixture(t, a)
	if !a.dispatchMainControl() {
		t.Fatal("not dispatched")
	}
	native := &SteerReceipt{done: make(chan struct{})}
	r.queue = append(r.queue, steering{text: "native", receipt: native})
	trigger := Trigger{ID: "second-control", Source: "im", Thread: "second-control", Kind: "request"}
	if err := a.commit(transition{State: "received", Index: 2, Trigger: &trigger}); err != nil {
		t.Fatal(err)
	}
	input := a.main.inbox[a.main.cursor].input.ID
	if err := a.commit(transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Snapshot: json.RawMessage(`{"sessionId":"main-test"}`), Decision: MainDecision{Action: "steer", Target: &r.target, Text: "second"}}}); err != nil {
		t.Fatal(err)
	}
	a.dispatchMainControl()
	before := len(a.store.Log())
	messages, err := a.takeSteering(t.Context(), r, agent.QueueModeAll)
	if err != nil || len(messages) != 3 || messageText(messages[1]) != "native" {
		t.Fatalf("mixed queue: %+v %v", messages, err)
	}
	if len(a.store.Log()) != before+1 {
		t.Fatal("batch not one durable transition")
	}
	var tr transition
	logs := a.store.Log()
	if err := json.Unmarshal(logs[len(logs)-1].Entry.Custom.Payload, &tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Main.ControlIDs) != 2 {
		t.Fatal("batch omitted a control")
	}
	if ok, err := native.Wait(t.Context()); !ok || err != nil {
		t.Fatal("native receipt did not settle")
	}
	if messages, err := a.takeSteering(t.Context(), r, agent.QueueModeAll); len(messages) != 0 || err != nil {
		t.Fatal("batch delivered twice")
	}
	copy := a.MainControls()
	copy[0].Target.EventID = "mutated"
	copy[0].Status = "mutated"
	if a.MainControls()[0].Status != "delivered" || a.MainControls()[0].Target.EventID != "start" {
		t.Fatal("control read aliases state")
	}
}

func TestMainControlTargetFinishesDuringInference(t *testing.T) {
	started := make(chan struct{})
	inference := make(chan struct{})
	finish := make(chan struct{})
	release := make(chan struct{})
	route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		if job.Input.Trigger.ID == "control" {
			target := job.Runs[0]
			close(inference)
			select {
			case <-release:
			case <-ctx.Done():
				return MainOutcome{}, ctx.Err()
			}
			return MainOutcome{Decision: MainDecision{Action: "cancel", Target: &target}, Snapshot: job.Snapshot}, nil
		}
		return controlRouter(ctx, job)
	}
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, route, func(ctx context.Context, job Job) (Outcome, error) {
		close(started)
		select {
		case <-finish:
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
		return mainWorker(ctx, job)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "start", Source: "im", Thread: "work", Kind: "request"})
	receive(t, started)
	submit(t, a, Trigger{ID: "control", Source: "im", Thread: "control", Kind: "request"})
	receive(t, inference)
	close(finish)
	for {
		a.mu.Lock()
		ch := a.changed
		done := a.events[0].state == "completed"
		a.mu.Unlock()
		if done {
			break
		}
		select {
		case <-ch:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	close(release)
	waitIdle(t, a)
	if len(a.MainFailures()) != 1 || len(a.MainControls()) != 0 || a.Status().Tasks[0].Status != "completed" {
		t.Fatal("stale inference cancelled later state")
	}
}

func TestMainClarificationUsesOriginalReplyDestination(t *testing.T) {
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, func(_ context.Context, job MainJob) (MainOutcome, error) {
		return MainOutcome{Decision: MainDecision{Action: "clarify", Text: "Which task?"}, Snapshot: job.Snapshot}, nil
	}, func(context.Context, Job) (Outcome, error) {
		t.Error("clarification delegated work")
		return Outcome{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "question", Source: "im", Thread: "original", Kind: "request"})
	waitIdle(t, a)
	replies := a.Replies()
	if len(replies) != 1 || replies[0].Thread != "original" || replies[0].Text != "Which task?" || len(a.Status().Tasks) != 0 {
		t.Fatalf("clarification: %+v", replies)
	}
}

func TestMainOffersActiveRunBeyondRecentTaskWindow(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	offered := make(chan MainJob, 1)
	route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		if job.Input.Trigger.ID == "inspect" {
			offered <- job
			return MainOutcome{Decision: MainDecision{Action: "ignore"}, Snapshot: job.Snapshot}, nil
		}
		return controlRouter(ctx, job)
	}
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 2, route, func(ctx context.Context, job Job) (Outcome, error) {
		if job.Trigger.ID == "long" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Outcome{}, ctx.Err()
			}
		}
		return mainWorker(ctx, job)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "long", Source: "im", Thread: "long", Kind: "request"})
	receive(t, started)
	for i := range maxCandidates {
		id := "short-" + strconv.Itoa(i)
		submit(t, a, Trigger{ID: id, Source: "im", Thread: id, Kind: "request"})
	}
	submit(t, a, Trigger{ID: "inspect", Source: "im", Thread: "inspect", Kind: "request"})
	job := receive(t, offered)
	found := false
	for _, target := range job.Runs {
		if target.TaskID == "task-1" && target.EventID == "long" {
			found = true
		}
	}
	if !found || len(job.Tasks) != maxCandidates || job.Tasks[0].ID == "task-1" {
		t.Errorf("old active task missing: runs=%+v tasks=%d", job.Runs, len(job.Tasks))
	}
	close(release)
	waitIdle(t, a)
}

func TestMainControlOneAtATimePreservesQueuedRemainder(t *testing.T) {
	a, _ := controlJournal(t, "steer")
	r := activateControlFixture(t, a)
	native := &SteerReceipt{done: make(chan struct{})}
	r.queue = append(r.queue, steering{text: "native", receipt: native})
	a.dispatchMainControl()
	first, err := a.takeSteering(t.Context(), r, agent.QueueModeOneAtATime)
	if err != nil || len(first) != 1 || messageText(first[0]) != "native" || a.main.controls[0].Status != "queued" {
		t.Fatal("first poll consumed durable remainder")
	}
	if ok, err := native.Wait(t.Context()); !ok || err != nil {
		t.Fatal("native receipt missing")
	}
	second, err := a.takeSteering(t.Context(), r, "")
	if err != nil || len(second) != 1 || messageText(second[0]) != "correct" || a.main.controls[0].Status != "delivered" {
		t.Fatal("default poll missed durable instruction")
	}
}

func TestMainControlUnpolledSteeringDropsOnWorkerExitOrClose(t *testing.T) {
	for _, mode := range []string{"complete", "close"} {
		t.Run(mode, func(t *testing.T) {
			started := make(chan struct{})
			finish := make(chan struct{})
			path := filepath.Join(t.TempDir(), "state.jsonl")
			a, err := OpenMain(t.Context(), path, 1, controlRouter, func(ctx context.Context, job Job) (Outcome, error) {
				close(started)
				select {
				case <-finish:
					return mainWorker(ctx, job)
				case <-ctx.Done():
					return Outcome{}, ctx.Err()
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "start", Source: "im", Thread: "work", Kind: "request"})
			receive(t, started)
			submit(t, a, Trigger{ID: "control", Source: "im", Thread: "control", Kind: "request", Text: "steer"})
			waitMainControl(t, a, "queued")
			if mode == "complete" {
				close(finish)
				waitIdle(t, a)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			if a.MainControls()[0].Status != "not_delivered" {
				t.Fatal("unpolled instruction not dropped")
			}
			b, err := OpenMain(t.Context(), path, 1, controlRouter, mainWorker)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			waitIdle(t, b)
			if len(b.MainControls()) != 1 || b.MainControls()[0].Status != "not_delivered" {
				t.Fatal("drop did not survive restart")
			}
		})
	}
}

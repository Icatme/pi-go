package superagent

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestBlockedMainDoesNotBlockAdmissionKnownWorkerOrControl(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	started := make(chan Job, 2)
	var calls atomic.Int32
	router := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		if calls.Add(1) != 1 {
			t.Error("main callbacks overlap")
		}
		defer calls.Add(-1)
		if job.Input.Kind == "user" && job.Input.Trigger.Thread == "slow" {
			close(blocked)
			select {
			case <-release:
			case <-ctx.Done():
				return MainOutcome{}, ctx.Err()
			}
		}
		return mainFixture(ctx, job)
	}
	worker := func(ctx context.Context, job Job) (Outcome, error) {
		if job.Trigger.ID == "follow" {
			started <- job
			<-ctx.Done()
			return Outcome{Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}, Receipts: []ToolReceipt{{Token: 1, Domain: "host", CallID: "write", ToolName: "write", Committed: true, Completed: true}}}, ctx.Err()
		}
		return mainWorker(ctx, job)
	}
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, router, worker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "known", Kind: "request"})
	waitIdle(t, a)
	submit(t, a, Trigger{ID: "slow", Source: "im", Thread: "slow", Kind: "request"})
	receive(t, blocked)
	if accepted, err := a.SubmitTask(Trigger{ID: "follow", Source: "im", Thread: "known", Kind: "signal"}, a.Status().Tasks[0].ID); !accepted || err != nil {
		t.Fatalf("task input: %v %v", accepted, err)
	}
	job := receive(t, started)
	target := RunTarget{TaskID: job.TaskID, Source: job.Trigger.Source, EventID: job.Trigger.ID}
	receipt, err := a.Steer(target, "stop before next write")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Abort(target); err != nil {
		t.Fatal(err)
	}
	if delivered, err := receipt.Wait(t.Context()); delivered || err != nil {
		t.Fatalf("steering: %v %v", delivered, err)
	}
	submit(t, a, Trigger{ID: "other", Source: "im", Thread: "other", Kind: "request"})
	close(release)
	waitIdle(t, a)
	var interrupted bool
	for _, reply := range a.Replies() {
		if reply.EventID == "follow" {
			interrupted = reply.Report != nil && reply.Report.Status == "interrupted" && reply.Report.NeedsReview && reply.Report.Receipts[0].Committed
		}
	}
	if !interrupted {
		t.Fatal("interrupted committed effect not returned to main")
	}
}

func TestMainAdmissionOrderIsNotChangedByLaterThreadBinding(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var routed []string
	router := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		if job.Input.Kind == "worker_result" {
			return mainFixture(ctx, job)
		}
		routed = append(routed, job.Input.Trigger.ID)
		decision := MainDecision{Action: "new"}
		if job.Input.Trigger.ID == "first" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return MainOutcome{}, ctx.Err()
			}
		} else {
			decision = MainDecision{Action: "attach", TaskID: job.Tasks[0].ID}
		}
		return MainOutcome{Decision: decision, Snapshot: job.Snapshot}, nil
	}
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, router, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	first := Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"}
	submit(t, a, first)
	receive(t, entered)
	submit(t, a, Trigger{ID: "second", Source: "im", Thread: "chat", Kind: "request"})
	if ok, err := a.Submit(first); ok || err != nil {
		t.Fatal("duplicate admitted")
	}
	changed := first
	changed.Text = "different"
	if ok, err := a.Submit(changed); ok || err == nil {
		t.Fatal("conflicting duplicate admitted")
	}
	close(release)
	waitIdle(t, a)
	if len(routed) != 2 || routed[0] != "first" || routed[1] != "second" || len(a.Replies()) != 2 || len(a.Status().Tasks) != 1 {
		t.Fatalf("routing reordered/dropped: %v, failures=%+v", routed, a.MainFailures())
	}
}

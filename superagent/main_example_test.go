package superagent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

func ExampleOpenMain() {
	ctx := context.Background()
	def := agent.AgentDefinition{Model: superagent.OfflineModel()}
	router, err := superagent.PiMainRouter(def, 64<<10)
	if err != nil {
		panic(err)
	}
	worker, err := superagent.PiWorker(def)
	if err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "superagent-main-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	a, err := superagent.OpenMain(ctx, filepath.Join(dir, "main.jsonl"), 2, router, worker)
	if err != nil {
		panic(err)
	}
	defer a.Close()
	if _, err := a.Submit(superagent.Trigger{ID: "27", Source: "support", Thread: "build", Kind: "request", Text: "Check the build."}); err != nil {
		panic(err)
	}
	// WaitIdle is a test barrier, not a requirement for normal service calls.
	// A running host can keep submitting and read the durable outbox separately.
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	for _, reply := range a.Replies() {
		if reply.Status != "pending" {
			continue
		}
		fmt.Println(reply.EventID, reply.Report.Status, reply.Status)
		if err := a.AcknowledgeReply(reply.ID); err != nil {
			panic(err)
		}
	}
	// Output: 27 completed pending
}

func ExampleSuperAgent_MainControls() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "superagent-controls-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	started := make(chan struct{})
	router := func(_ context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
		decision := superagent.MainDecision{Action: "new"}
		if job.Input.Kind == "worker_result" {
			decision.Action = "ignore"
		} else if job.Input.Trigger.ID == "stop" {
			// Select a full offered identity, never only a reusable task ID.
			target := job.Runs[0]
			decision = superagent.MainDecision{Action: "cancel", Target: &target}
		}
		return superagent.MainOutcome{Decision: decision, Snapshot: job.Snapshot}, nil
	}
	worker := func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
		close(started)
		<-ctx.Done()
		return superagent.Outcome{Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, ctx.Err()
	}
	a, err := superagent.OpenMain(ctx, filepath.Join(dir, "main.jsonl"), 1, router, worker)
	if err != nil {
		panic(err)
	}
	defer a.Close()
	if _, err := a.Submit(superagent.Trigger{ID: "start", Source: "im", Thread: "work", Kind: "request", Text: "Analyze logs"}); err != nil {
		panic(err)
	}
	<-started
	// An ordinary message in the same chat still reaches main. Only the exact
	// invocation selected by its validated decision is cancelled.
	if _, err := a.Submit(superagent.Trigger{ID: "stop", Source: "im", Thread: "work", Kind: "request", Text: "Stop analyzing logs"}); err != nil {
		panic(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	action := a.MainControls()[0]
	fmt.Println(action.EventID, action.Status, a.Status().Tasks[0].Status)
	// Output: stop cancel_requested needs_review
}

func ExampleSuperAgent_BeginReplyDelivery() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "superagent-reply-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	router := func(_ context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: "clarify", Text: "Which build?"}, Snapshot: job.Snapshot}, nil
	}
	worker := func(_ context.Context, job superagent.Job) (superagent.Outcome, error) {
		return superagent.Outcome{Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	}
	a, err := superagent.OpenMain(ctx, filepath.Join(dir, "main.jsonl"), 1, router, worker)
	if err != nil {
		panic(err)
	}
	defer a.Close()
	if _, err := a.Submit(superagent.Trigger{ID: "ask", Source: "im", Thread: "build", Kind: "request", Text: "Check the build"}); err != nil {
		panic(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	reply := a.Replies()[0]
	delivery, err := a.BeginReplyDelivery(reply.ID)
	if err != nil {
		panic(err)
	}
	// An in-memory transport stands in for an external adapter. In production,
	// send only after Begin succeeds, to reply.Source/reply.Thread, using reply.ID
	// as the platform idempotency key. Keep delivery for the exact callback.
	channel := make(chan superagent.Reply, 1)
	channel <- reply
	message := <-channel
	if err := a.ObserveReply(message.Source, message.Thread, delivery, "platform-message-1"); err != nil {
		panic(err)
	}
	accepted, err := a.Submit(superagent.Trigger{ID: "platform-message-1", Source: message.Source, Thread: message.Thread, Kind: "signal", Text: message.Text})
	if err != nil {
		panic(err)
	}
	fmt.Println(message.Text, a.Replies()[0].Status, accepted)
	// Output: Which build? acknowledged false
}

func ExampleSuperAgent_SubmitTask() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "superagent-task-input-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	def := agent.AgentDefinition{Model: superagent.OfflineModel()}
	router, err := superagent.PiMainRouter(def, 64<<10)
	if err != nil {
		panic(err)
	}
	worker, err := superagent.PiWorker(def)
	if err != nil {
		panic(err)
	}
	a, err := superagent.OpenMain(ctx, filepath.Join(dir, "main.jsonl"), 2, router, worker)
	if err != nil {
		panic(err)
	}
	defer a.Close()
	if _, err := a.Submit(superagent.Trigger{ID: "request", Source: "im", Thread: "chat", Kind: "request", Text: "Check the build"}); err != nil {
		panic(err)
	}
	// These are example barriers; a live host submits and consumes outbox items
	// independently. It retains the association from the original task result.
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	taskID := a.Replies()[0].TaskID
	// The host has authenticated this PR update and resolved its authorized task.
	update := superagent.Trigger{ID: "review-1", Source: "github", Thread: "pr-27", Kind: "signal", Text: "The build has a new review"}
	accepted, err := a.SubmitTask(update, taskID)
	if err != nil {
		panic(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	duplicate, err := a.SubmitTask(update, taskID)
	if err != nil {
		panic(err)
	}
	fmt.Println(accepted, duplicate, len(a.Status().Tasks), a.Replies()[1].Thread)
	// Output: true false 1 pr-27
}

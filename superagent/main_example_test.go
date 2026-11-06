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
	// M1 retains legacy known-thread routing. This unbound control conversation
	// reaches main; same-chat topic/control classification is the M2 follow-on.
	if _, err := a.Submit(superagent.Trigger{ID: "stop", Source: "im", Thread: "operator", Kind: "request", Text: "Stop analyzing logs"}); err != nil {
		panic(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	action := a.MainControls()[0]
	fmt.Println(action.EventID, action.Status, a.Status().Tasks[0].Status)
	// Output: stop cancel_requested needs_review
}

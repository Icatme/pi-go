package superagent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

func ExampleSuperAgent_SubmitThread() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "superagent-thread-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	router := func(_ context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
		decision := superagent.MainDecision{Action: "new"}
		if job.Input.Kind == "worker_result" {
			decision = superagent.MainDecision{Action: "reply", Text: job.Input.Report.Text}
		} else if job.Input.Kind == "task_input" || job.Input.Kind == "control_result" {
			decision = superagent.MainDecision{Action: "ignore"}
		} else if job.Input.Kind == "handback" {
			decision = superagent.MainDecision{Action: "clarify", Text: "Who should handle the remaining scope?"}
		}
		return superagent.MainOutcome{Decision: decision, Snapshot: job.Snapshot}, nil
	}
	worker := func(_ context.Context, job superagent.Job) (superagent.Outcome, error) {
		return superagent.Outcome{Summary: job.Trigger.Text, Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	}
	a, err := superagent.OpenMain(ctx, filepath.Join(dir, "thread.jsonl"), 3, router, worker)
	if err != nil {
		panic(err)
	}
	defer a.Close()
	ids, names := map[string]string{}, map[string]string{}
	for _, name := range []string{"logs", "tests", "docs"} {
		if _, err := a.Submit(superagent.Trigger{ID: name, Source: "chat", Thread: "planning", Kind: "request", Text: name}); err != nil {
			panic(err)
		}
		if err := a.WaitIdle(ctx); err != nil {
			panic(err)
		}
		// Discover actual IDs through the public projection; never guess task IDs.
		for _, task := range a.Status().Tasks {
			if task.Summary == name {
				ids[name], names[task.ID] = task.ID, name
			}
		}
	}
	// The trusted host authorizes logs and tests to receive future PR updates.
	for _, name := range []string{"logs", "tests"} {
		if err := a.SetThreadSubscription(ids[name], "github", "pr-27", true); err != nil {
			panic(err)
		}
	}
	first := superagent.Trigger{ID: "review-1", Source: "github", Thread: "pr-27", Kind: "signal", Text: "New review"}
	if _, err := a.SubmitThread(first); err != nil {
		panic(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	// Change only future deliveries: tests leaves, docs joins.
	if err := a.SetThreadSubscription(ids["tests"], "github", "pr-27", false); err != nil {
		panic(err)
	}
	if err := a.SetThreadSubscription(ids["docs"], "github", "pr-27", true); err != nil {
		panic(err)
	}
	duplicate, err := a.SubmitThread(first)
	if err != nil {
		panic(err)
	}
	second := first
	second.ID = "review-2"
	if _, err := a.SubmitThread(second); err != nil {
		panic(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	var deliveries []string
	seen := map[string]bool{} // Scoped to this journal, not globally unique IDs.
	for _, report := range a.Status().Reports {
		if report.Source == "github" {
			// One source/event can produce several reports. Deduplicate replay by
			// delivery ID, never by the source/event pair used at ingress.
			if seen[report.DeliveryID] {
				continue
			}
			seen[report.DeliveryID] = true
			deliveries = append(deliveries, report.EventID+" "+names[report.TaskID]+" "+report.Status)
		}
	}
	sort.Strings(deliveries)
	for _, line := range deliveries {
		fmt.Println(line)
	}
	fmt.Println("duplicate accepted:", duplicate)
	// Output:
	// review-1 logs completed
	// review-1 tests completed
	// review-2 docs completed
	// review-2 logs completed
	// duplicate accepted: false
}

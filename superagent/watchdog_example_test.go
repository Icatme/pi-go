package superagent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

func ExampleSuperAgent_CheckWatchdog() {
	dir, err := os.MkdirTemp("", "watchdog-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()
	a, err := superagent.OpenMain(ctx, filepath.Join(dir, "journal.jsonl"), 1,
		func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
			action := "ignore"
			if j.Input.Kind == "user" {
				action = "new"
			}
			return superagent.MainOutcome{Decision: superagent.MainDecision{Action: action}, Snapshot: j.Snapshot}, nil
		},
		func(_ context.Context, j superagent.Job) (superagent.Outcome, error) {
			return superagent.Outcome{Summary: "Offline fixture finished.", Snapshot: agent.AgentSnapshot{SessionID: j.TaskID}}, nil
		})
	if err != nil {
		panic(err)
	}
	defer a.Close()
	if _, err = a.Submit(superagent.Trigger{ID: "request", Source: "example", Thread: "chat", Kind: "request", Text: "Run the offline fixture"}); err != nil {
		panic(err)
	}
	if err = a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	check, err := a.CheckWatchdog(ctx, superagent.WatchdogOptions{Policy: superagent.WatchdogPolicy{Revision: "v1", Interval: time.Minute, BatchSize: 8, IdleAfter: 10 * time.Minute, IdleTurns: 5}})
	if err != nil {
		panic(err)
	}
	fmt.Println(check.Findings[0].Class)
	fmt.Println("existing tasks:", len(a.Status().Tasks))
	// Output:
	// settled
	// existing tasks: 1
}

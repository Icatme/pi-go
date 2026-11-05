package superagent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/superagent"
)

type watchdogExampleBackend struct{}

func (watchdogExampleBackend) Start(context.Context, background.Request) error { return nil }
func (watchdogExampleBackend) Stop(context.Context, string) error {
	panic("refresh never stops an operation")
}
func (watchdogExampleBackend) Inspect(_ context.Context, id string) (background.Observation, error) {
	return background.Observation{ID: id, State: "unknown"}, nil
}

func ExampleSuperAgent_RefreshWatchdog() {
	dir, err := os.MkdirTemp("", "watchdog-refresh-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	ctx := context.Background()
	a, err := superagent.OpenMainBackground(ctx, filepath.Join(dir, "journal.jsonl"), 1,
		func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
			action := "ignore"
			if j.Input.Kind == "user" {
				action = "new"
			}
			return superagent.MainOutcome{Decision: superagent.MainDecision{Action: action}, Snapshot: j.Snapshot}, nil
		},
		func(ctx context.Context, j superagent.Job) (superagent.Outcome, error) {
			if j.Trigger.ID == "request" {
				if _, err := j.Background.Start(ctx, superagent.BackgroundStart{Tool: "fixture", CallID: "call", Arguments: json.RawMessage(`{}`)}); err != nil {
					return superagent.Outcome{}, err
				}
			}
			return superagent.Outcome{Summary: "Waiting for the original operation.", Snapshot: j.Snapshot}, nil
		}, superagent.BackgroundOptions{MaxConcurrent: 1, PollInterval: time.Minute, Tools: []superagent.BackgroundTool{{Name: "fixture", Scope: "offline", PolicyRevision: "v1", Backend: watchdogExampleBackend{}, Authorize: func(context.Context, superagent.BackgroundAuthorization) error { return nil }}}})
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
	// Settle the original operation's unknown facts before taking the diagnostic
	// snapshot; the independent recovery watcher may have already done this.
	op := a.BackgroundOperations()[0]
	if err = a.RefreshBackground(ctx, op.ID); err != nil {
		panic(err)
	}
	check, err := a.CheckWatchdog(ctx, superagent.WatchdogOptions{Policy: superagent.WatchdogPolicy{Revision: "v1", Interval: time.Minute, BatchSize: 8, IdleAfter: 10 * time.Minute, IdleTurns: 5}})
	if err != nil {
		panic(err)
	}
	finding := check.Findings[0]
	request := superagent.WatchdogRefreshRequest{FindingID: finding.ID, TaskID: op.TaskID, Revision: finding.Task.Revision, OperationID: op.ID, Origin: op.Origin, DeliveryID: op.DeliveryID}
	result, err := a.RefreshWatchdog(ctx, request, func(_ context.Context, r superagent.WatchdogRefreshRequest) error {
		// The host approves this exact existing operation, not a model hypothesis.
		if r.OperationID != op.ID || r.Origin != op.Origin {
			return fmt.Errorf("unexpected operation")
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Result, result.Operation.NeedsReview)
	fmt.Println(result.RecheckCondition)
	// Output:
	// unknown true
	// new_finding_and_host_authorization
}

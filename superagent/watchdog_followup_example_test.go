package superagent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Icatme/pi-go/superagent"
)

func ExampleSuperAgent_WatchdogFollowupsSince() {
	dir, err := os.MkdirTemp("", "watchdog-followup-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
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
	if _, err := a.Submit(superagent.Trigger{ID: "request", Source: "example", Thread: "chat", Kind: "request", Text: "Run the offline fixture"}); err != nil {
		panic(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	op := a.BackgroundOperations()[0]
	if err := a.RefreshBackground(ctx, op.ID); err != nil {
		panic(err)
	}
	options := superagent.WatchdogOptions{Policy: superagent.WatchdogPolicy{Revision: "v1", Interval: time.Millisecond, BatchSize: 8, IdleAfter: time.Minute, IdleTurns: 5}}
	check, err := a.CheckWatchdog(ctx, options)
	if err != nil {
		panic(err)
	}
	finding := check.Findings[0]
	request := superagent.WatchdogRefreshRequest{FindingID: finding.ID, TaskID: op.TaskID, Revision: finding.Task.Revision, OperationID: op.ID, Origin: op.Origin, DeliveryID: op.DeliveryID}
	intervention, err := a.RefreshWatchdog(ctx, request, func(_ context.Context, r superagent.WatchdogRefreshRequest) error {
		if r != request {
			return fmt.Errorf("unexpected operation binding")
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	// A real host keeps its usual Check/RunWatchdog cadence. The short interval
	// here only avoids a long example; follow-up performs no new backend Inspect.
	time.Sleep(max(time.Until(time.UnixMilli(intervention.RecheckAt)), 0))
	if _, err := a.CheckWatchdog(ctx, options); err != nil {
		panic(err)
	}
	page := a.WatchdogFollowupsSince(0, 16)
	if len(page.Followups) != 1 {
		panic("missing due follow-up")
	}
	followup := page.Followups[0]
	fmt.Println(followup.Source, followup.Class)
	fmt.Println(followup.Request.OperationID == op.ID, page.Cursor, page.HasMore)
	// Persist page.Cursor only after durable local handling. Sending a message
	// requires the host's separately authorized delivery path; this API sends none.
	// Output:
	// coordinator_observation needs_review
	// true 1 false
}

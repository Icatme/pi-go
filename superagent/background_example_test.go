package superagent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/superagent"
)

func ExampleOpenMainBackground() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "superagent-background-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	release := make(chan struct{})
	backend, err := background.NewLocal(ctx, "example-workspace", agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "slow", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		select {
		case <-release:
		case <-ctx.Done():
			return agent.ToolResult{}, ctx.Err()
		}
		return agent.ToolResult{Content: []agent.Part{agent.NewTextPart("finished")}}, nil
	}}}}, 1)
	if err != nil {
		panic(err)
	}
	route := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		action := "ignore"
		if j.Input.Kind == "user" {
			action = "new"
		}
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: action}, Snapshot: j.Snapshot}, nil
	}
	worker := func(ctx context.Context, j superagent.Job) (superagent.Outcome, error) {
		if j.Trigger.ID == "first" {
			if _, err := j.Background.Start(ctx, superagent.BackgroundStart{Tool: "slow", CallID: "original-call", Arguments: json.RawMessage(`{}`)}); err != nil {
				return superagent.Outcome{}, err
			}
		}
		return superagent.Outcome{Summary: "input handled", Snapshot: agent.AgentSnapshot{SessionID: j.TaskID}}, nil
	}
	a, err := superagent.OpenMainBackground(ctx, filepath.Join(dir, "events.jsonl"), 1, route, worker, superagent.BackgroundOptions{MaxConcurrent: 1, PollInterval: time.Millisecond, Tools: []superagent.BackgroundTool{{Name: "slow", Scope: "example-workspace", PolicyRevision: "v1", Backend: backend, Authorize: func(_ context.Context, auth superagent.BackgroundAuthorization) error {
		if auth.Target.TaskID == "" || auth.DeliveryID == "" {
			return fmt.Errorf("missing trusted origin")
		}
		return nil
	}}}})
	if err != nil {
		panic(err)
	}
	defer a.Close()
	if _, err = a.Submit(superagent.Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start background work"}); err != nil {
		panic(err)
	}
	if err = a.WaitIdle(ctx); err != nil {
		panic(err)
	} // Only Worker/main input processing.
	op := a.BackgroundOperations()[0]
	if _, err = a.SubmitTask(superagent.Trigger{ID: "followup", Source: "im", Thread: "chat", Kind: "request", Text: "new input"}, op.TaskID); err != nil {
		panic(err)
	}
	if err = a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	fmt.Println("same task handled new input:", len(a.ReportsSince(0)) == 2)
	close(release)
	for !a.BackgroundOperations()[0].Exited {
		if err = a.RefreshBackground(ctx, op.ID); err != nil {
			panic(err)
		}
		select {
		case <-ctx.Done():
			panic(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if err = a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	reports := a.ReportsSince(0)
	fmt.Println("completion keeps original call:", reports[2].Background.CallID)
	// Output:
	// same task handled new input: true
	// completion keeps original call: original-call
}

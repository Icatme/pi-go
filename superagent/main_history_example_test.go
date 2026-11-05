package superagent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

func ExampleMainHistory_Read() {
	dir, err := os.MkdirTemp("", "public-history-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	router := func(ctx context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
		decision := superagent.MainDecision{Action: "new"}
		if job.Input.Kind == "worker_result" {
			var request superagent.HistoryRequest
			for _, ref := range job.Input.HistoryRefs {
				if ref.Kind == "report" {
					request.Ref = ref
				}
			}
			request.MaxBytes, request.MaxItems = 1024, 4
			var data strings.Builder
			for {
				page, err := job.History.Read(ctx, request)
				if err != nil {
					return superagent.MainOutcome{}, err
				}
				data.WriteString(page.Data)
				if page.Next == "" {
					break
				}
				request.Cursor = page.Next
			}
			var report superagent.Report
			if err := json.Unmarshal([]byte(data.String()), &report); err != nil {
				return superagent.MainOutcome{}, err
			}
			decision = superagent.MainDecision{Action: "reply", Text: report.Status + ": " + report.Text}
		}
		return superagent.MainOutcome{Decision: decision, Snapshot: job.Snapshot}, nil
	}
	worker := func(_ context.Context, job superagent.Job) (superagent.Outcome, error) {
		return superagent.Outcome{Summary: "Verified public result", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	}
	a, err := superagent.OpenMain(context.Background(), filepath.Join(dir, "state.jsonl"), 1, router, worker)
	if err != nil {
		panic(err)
	}
	defer a.Close()
	if _, err := a.Submit(superagent.Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request", Text: "Check the result"}); err != nil {
		panic(err)
	}
	if err := a.WaitIdle(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println(a.Replies()[0].Text)
	// Output: completed: Verified public result
}

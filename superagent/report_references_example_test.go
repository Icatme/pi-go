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

func ExampleMainDecision_reportRefs() {
	dir, err := os.MkdirTemp("", "report-references-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var refs []superagent.HistoryRef
	route := func(_ context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
		d := superagent.MainDecision{Action: "new"}
		if job.Input.Kind == "worker_result" {
			for _, ref := range job.Input.HistoryRefs {
				if ref.Kind == "report" {
					refs = append(refs, ref)
				}
			}
			d = superagent.MainDecision{Action: "ignore"}
			if len(refs) == 2 {
				d = superagent.MainDecision{Action: "reply", Text: "One task completed; the other has tool errors and needs review.", ReportRefs: refs}
			}
		}
		return superagent.MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
	}
	worker := func(_ context.Context, job superagent.Job) (superagent.Outcome, error) {
		out := superagent.Outcome{Summary: job.Trigger.Text, Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}
		if job.Trigger.ID == "two" {
			out.Status = "completed_with_tool_errors"
			out.NeedsReview = true
		}
		return out, nil
	}
	a, err := superagent.OpenMain(ctx, filepath.Join(dir, "journal.jsonl"), 1, route, worker)
	if err != nil {
		panic(err)
	}
	defer a.Close()
	for _, id := range []string{"one", "two"} {
		if _, err := a.Submit(superagent.Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request", Text: id}); err != nil {
			panic(err)
		}
		if err := a.WaitIdle(ctx); err != nil {
			panic(err)
		}
	}
	reply := a.Replies()[0]
	fmt.Println("Primary:", reply.Report.EventID)
	for _, c := range reply.Reports {
		fmt.Println(c.Report.EventID, c.Report.Status, c.Report.NeedsReview)
	}
	// The host chooses how to render these facts; nothing has been sent.
	// Output:
	// Primary: two
	// one completed false
	// two completed_with_tool_errors true
}

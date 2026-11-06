package superagent_test

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

func threadAcceptanceRouter(_ context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
	d := superagent.MainDecision{Action: "new"}
	switch job.Input.Kind {
	case "worker_result":
		d = superagent.MainDecision{Action: "reply", Text: job.Input.Report.Text}
	case "handback":
		d = superagent.MainDecision{Action: "clarify", Text: "Which remaining scope should be handled?"}
	case "task_input", "control_result":
		d = superagent.MainDecision{Action: "ignore"}
	}
	return superagent.MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
}

func threadAcceptanceOutcome(job superagent.Job) superagent.Outcome {
	snapshot := job.Snapshot
	snapshot.SessionID = job.TaskID
	snapshot.Messages = append(snapshot.Messages, agent.NewUserTextMessage(job.Trigger.ID))
	return superagent.Outcome{Summary: job.Trigger.Text, Snapshot: snapshot}
}

func threadAcceptanceSeed(t *testing.T, ctx context.Context, a *superagent.SuperAgent, names ...string) map[string]string {
	t.Helper()
	ids := make(map[string]string)
	for _, name := range names {
		if ok, err := a.Submit(superagent.Trigger{ID: "seed-" + name, Source: "chat", Thread: "planning", Kind: "request", Text: name}); !ok || err != nil {
			t.Fatalf("seed: %v %v", ok, err)
		}
		if err := a.WaitIdle(ctx); err != nil {
			t.Fatal(err)
		}
		for _, task := range a.Status().Tasks {
			if task.Summary == name {
				ids[name] = task.ID
			}
		}
		if ids[name] == "" {
			t.Fatalf("no public task identity for %s", name)
		}
	}
	return ids
}

func threadAcceptanceReceive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}

// Only exported APIs are used: the host discovers task IDs, authenticates a
// Thread association, and consumes separate reports for a shared event.
func TestThreadAcceptanceSerialTasksWithoutUnrelatedBlocking(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "thread.jsonl")
	entered := make(chan superagent.Job, 8)
	release := make(chan struct{})
	var mu sync.Mutex
	var calls []superagent.RunTarget
	work := func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
		if job.Trigger.Source == "github" {
			mu.Lock()
			calls = append(calls, superagent.RunTarget{TaskID: job.TaskID, Source: job.Trigger.Source, EventID: job.Trigger.ID})
			mu.Unlock()
			entered <- job
			if job.Snapshot.Messages[0].Parts[0].Text == "seed-A" && job.Trigger.ID == "first" {
				select {
				case <-release:
				case <-ctx.Done():
					return threadAcceptanceOutcome(job), ctx.Err()
				}
			}
		}
		return threadAcceptanceOutcome(job), nil
	}
	a, err := superagent.OpenMain(ctx, path, 2, threadAcceptanceRouter, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ids := threadAcceptanceSeed(t, ctx, a, "A", "B")
	for _, name := range []string{"A", "B"} {
		if err := a.SetThreadSubscription(ids[name], "github", "pr-27", true); err != nil {
			t.Fatal(err)
		}
	}
	first := superagent.Trigger{ID: "first", Source: "github", Thread: "pr-27", Kind: "signal", Text: "review one"}
	if ok, err := a.SubmitThread(first); !ok || err != nil {
		t.Fatalf("first: %v %v", ok, err)
	}
	for range 2 {
		job := threadAcceptanceReceive(t, ctx, entered)
		if job.Trigger.ID != "first" || len(job.Snapshot.Messages) != 1 {
			t.Fatalf("initial delivery: %+v", job)
		}
	}
	second := first
	second.ID = "second"
	second.Text = "review two"
	if ok, err := a.SubmitThread(second); !ok || err != nil {
		t.Fatalf("second: %v %v", ok, err)
	}
	// B's next event must finish admission while A's first invocation is held.
	job := threadAcceptanceReceive(t, ctx, entered)
	if job.TaskID != ids["B"] || job.Trigger.ID != "second" || len(job.Snapshot.Messages) != 2 {
		t.Fatalf("B blocked or A overlapped: %+v", job)
	}
	close(release)
	job = threadAcceptanceReceive(t, ctx, entered)
	if job.TaskID != ids["A"] || job.Trigger.ID != "second" || len(job.Snapshot.Messages) != 2 {
		t.Fatalf("A lost ordered snapshot: %+v", job)
	}
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if failures := a.MainFailures(); len(failures) != 0 {
		t.Fatalf("main failed on informational input: %+v", failures)
	}
	var reports []superagent.Report
	for _, report := range a.Status().Reports {
		if report.Source == "github" {
			reports = append(reports, report)
		}
	}
	if len(reports) != 4 {
		t.Fatalf("fanout reports: %+v", reports)
	}
	for _, report := range reports {
		if report.Status != "completed" {
			t.Fatalf("report: %+v", report)
		}
	}
	if ok, err := a.SubmitThread(first); ok || err != nil {
		t.Fatalf("duplicate: %v %v", ok, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := superagent.OpenMain(ctx, path, 2, threadAcceptanceRouter, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got := b.ThreadSubscriptions("github", "pr-27"); !reflect.DeepEqual(got, []string{ids["A"], ids["B"]}) {
		t.Fatalf("subscriptions after restart: %v", got)
	}
	for _, event := range []superagent.Trigger{first, second} {
		if ok, err := b.SubmitThread(event); ok || err != nil {
			t.Fatalf("restart duplicate: %v %v", ok, err)
		}
	}
	if err := b.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("duplicate executed: %+v", calls)
	}
	if !reflect.DeepEqual(a.Replies(), b.Replies()) {
		t.Fatal("restart changed durable replies")
	}
}

func TestThreadAcceptanceHandbackPreservesIndependentEffects(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "handback.jsonl")
	entered := make(chan superagent.Job, 2)
	work := func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
		out := threadAcceptanceOutcome(job)
		if job.Trigger.Source != "github" {
			return out, nil
		}
		entered <- job
		if job.Snapshot.Messages[0].Parts[0].Text == "seed-A" {
			<-ctx.Done()
			return out, ctx.Err()
		}
		if err := job.HandBack(ctx, "Need an owner for the remaining work", "Saved the review summary"); err != nil {
			return out, err
		}
		if err := job.HandBack(ctx, "Need an owner for the remaining work", "Saved the review summary"); err != nil {
			return out, err
		}
		if err := job.SetThreadSubscription(ctx, false); err != nil {
			return out, err
		}
		out.Receipts = []superagent.ToolReceipt{{Token: 1, Domain: "host", CallID: "save", ToolName: "save", Committed: true, Completed: true}}
		return out, nil
	}
	a, err := superagent.OpenMain(ctx, path, 2, threadAcceptanceRouter, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ids := threadAcceptanceSeed(t, ctx, a, "A", "B")
	for _, name := range []string{"A", "B"} {
		if err := a.SetThreadSubscription(ids[name], "github", "pr-27", true); err != nil {
			t.Fatal(err)
		}
	}
	event := superagent.Trigger{ID: "review", Source: "github", Thread: "pr-27", Kind: "signal", Text: "Review saved"}
	if ok, err := a.SubmitThread(event); !ok || err != nil {
		t.Fatalf("submit: %v %v", ok, err)
	}
	var saved superagent.Job
	for range 2 {
		job := threadAcceptanceReceive(t, ctx, entered)
		if job.TaskID == ids["B"] {
			saved = job
		}
	}
	target := superagent.RunTarget{TaskID: ids["A"], Source: event.Source, EventID: event.ID}
	if err := a.Abort(target); err != nil {
		t.Fatal(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	h := a.Handbacks()
	if len(h) != 1 || h[0].Target.TaskID != ids["B"] || h[0].Trigger != event || h[0].Handled != "Saved the review summary" {
		t.Fatalf("handback: %+v", h)
	}
	if err := saved.SetThreadSubscription(ctx, true); err == nil {
		t.Fatal("expired worker changed a subscription")
	}
	if err := saved.HandBack(ctx, "late request", "nothing"); err == nil {
		t.Fatal("expired worker handed back")
	}
	if got := a.ThreadSubscriptions(event.Source, event.Thread); !reflect.DeepEqual(got, []string{ids["A"]}) {
		t.Fatalf("worker subscription scope: %v", got)
	}
	results := make(map[string]superagent.Report)
	for _, report := range a.Status().Reports {
		if report.EventID == event.ID {
			results[report.TaskID] = report
		}
	}
	if results[ids["A"]].Status != "interrupted" || !results[ids["A"]].NeedsReview {
		t.Fatalf("A interruption: %+v", results)
	}
	bReport := results[ids["B"]]
	if bReport.Status != "completed" || len(bReport.Receipts) != 1 || !bReport.Receipts[0].Committed || !bReport.Receipts[0].Completed {
		t.Fatalf("B effects erased: %+v", bReport)
	}
	var replies int
	for _, reply := range a.Replies() {
		if reply.EventID == event.ID {
			replies++
		}
	}
	if replies != 3 {
		t.Fatalf("want two terminal replies and a clarification, got %d: %+v", replies, a.Replies())
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := superagent.OpenMain(ctx, path, 2, threadAcceptanceRouter, work)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if ok, err := reopened.SubmitThread(event); ok || err != nil {
		t.Fatalf("restart duplicate: %v %v", ok, err)
	}
	if err := reopened.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.Handbacks(), h) || !reflect.DeepEqual(reopened.Replies(), a.Replies()) {
		t.Fatal("restart lost handback or independent replies")
	}
}

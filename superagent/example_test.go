package superagent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

func ExampleOpen() {
	ctx := context.Background()
	// A service supplies its own AgentDefinition and a persistent journal path.
	def := agent.AgentDefinition{Model: superagent.OfflineModel()}
	router, err := superagent.PiRouter(def)
	if err != nil {
		panic(err)
	}
	worker, err := superagent.PiWorker(def)
	if err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "superagent-example-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	tasks, err := superagent.Open(ctx, filepath.Join(dir, "tasks.jsonl"), 2, router, worker)
	if err != nil {
		panic(err)
	}
	defer tasks.Close()

	_, err = tasks.Submit(superagent.Trigger{
		ID: "27", Source: "support", Thread: "build", Kind: "request", Text: "Check the build.",
	})
	if err != nil {
		panic(err)
	}
	if err := tasks.WaitIdle(ctx); err != nil {
		panic(err)
	}
	for _, report := range tasks.ReportsSince(0) {
		fmt.Println(report.EventID, report.Status, "has delivery ID:", report.DeliveryID != "")
	}
	// Output: 27 completed has delivery ID: true
}

func ExampleSuperAgent_ReportsSince() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "superagent-consumer-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "journal.jsonl")
	router := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		action := "ignore"
		if j.Input.Kind == "user" {
			action = "new"
		}
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: action}, Snapshot: j.Snapshot}, nil
	}
	worker := func(_ context.Context, j superagent.Job) (superagent.Outcome, error) {
		return superagent.Outcome{Summary: j.Trigger.Text}, nil
	}
	open := func() *superagent.SuperAgent {
		a, err := superagent.OpenMain(ctx, path, 2, router, worker)
		if err != nil {
			panic(err)
		}
		return a
	}
	a := open()
	// The host chooses a stable namespace for this journal's lifetime. It must
	// change the namespace if it replaces the journal with unrelated history.
	const journalNamespace = "support-coordinator"
	type key struct{ journal, delivery string }
	// This map is only a demonstration sink. A real sink must durably commit
	// its effect and dedup key atomically, or honor an equivalent idempotency key.
	sink := map[key]superagent.Report{}
	cursor := 0
	consume := func() {
		for _, report := range a.ReportsSince(cursor) {
			k := key{journalNamespace, report.DeliveryID}
			if _, done := sink[k]; !done {
				sink[k] = report
			}
			// Advance only after the sink accepts this report, including replay.
			cursor++
		}
	}
	for _, id := range []string{"logs", "tests"} {
		if _, err := a.Submit(superagent.Trigger{ID: id, Source: "chat", Thread: "planning", Kind: "request", Text: id}); err != nil {
			panic(err)
		}
		if err := a.WaitIdle(ctx); err != nil {
			panic(err)
		}
	}
	consume()
	for _, task := range a.Status().Tasks {
		if err := a.SetThreadSubscription(task.ID, "github", "pr", true); err != nil {
			panic(err)
		}
	}
	before := cursor
	if _, err := a.SubmitThread(superagent.Trigger{ID: "review", Source: "github", Thread: "pr", Kind: "signal", Text: "Review ready"}); err != nil {
		panic(err)
	}
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	consume()
	fmt.Println("reports from one event:", cursor-before)
	fmt.Println("distinct sink deliveries:", len(sink))
	if err := a.Close(); err != nil {
		panic(err)
	}
	a = open()
	defer a.Close()
	// Simulate losing the count checkpoint, while retaining the sink's dedup
	// records. The two fanout recipients must not collapse into one source/event.
	cursor = 0
	consume()
	fmt.Println("after replay:", len(sink))
	// Output:
	// reports from one event: 2
	// distinct sink deliveries: 4
	// after replay: 4
}

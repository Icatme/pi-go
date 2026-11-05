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
		fmt.Println(report.EventID, report.Status)
	}
	// Output: 27 completed
}

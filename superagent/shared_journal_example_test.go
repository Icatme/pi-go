//go:build (linux || darwin || windows) && (amd64 || arm64)

package superagent_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
	"github.com/Icatme/pi-go/superagent/sharedsqlite"
)

func ExampleOpenMainJournal() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "superagent-shared-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	// Host-owned private directory, same machine and reliable local locks.
	if err := os.Chmod(dir, 0700); err != nil {
		panic(err)
	}
	journal, err := sharedsqlite.Open(ctx, filepath.Join(dir, "tasks.db"), sharedsqlite.Options{Domain: "workspace-A", Owner: "instance-A"})
	if err != nil {
		panic(err)
	}
	router := func(_ context.Context, j superagent.MainJob) (superagent.MainOutcome, error) {
		return superagent.MainOutcome{Decision: superagent.MainDecision{Action: "reply", Text: "Ready."}, Snapshot: j.Snapshot}, nil
	}
	worker := func(_ context.Context, j superagent.Job) (superagent.Outcome, error) {
		return superagent.Outcome{Snapshot: agent.AgentSnapshot{SessionID: j.TaskID}}, nil
	}
	a, err := superagent.OpenMainJournal(ctx, journal, superagent.MainJournalOptions{Parallel: 2, Router: router, Worker: worker})
	if err != nil {
		_ = journal.Close()
		panic(err)
	}
	defer a.Close()
	if _, err := a.Submit(superagent.Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request", Text: "Ready?"}); err != nil {
		panic(err)
	}
	// Test barrier only. A service normally keeps admitting inputs.
	if err := a.WaitIdle(ctx); err != nil {
		panic(err)
	}
	fmt.Println(a.Replies()[0].Text)
	// Output: Ready.
}

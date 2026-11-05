package sharedpostgres_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Icatme/pi-go/superagent"
	"github.com/Icatme/pi-go/superagent/sharedpostgres"
)

var _ superagent.LeasedJournal = (*sharedpostgres.Journal)(nil)

func ExampleOpen() {
	// Host code supplies a context-aware driver, verified TLS, bounded pool and
	// a single durable primary, and applies sharedpostgres.Schema once beforehand.
	start := func(ctx context.Context, db *sql.DB, router superagent.MainRouter, worker superagent.Worker) (*superagent.SuperAgent, error) {
		journal, err := sharedpostgres.Open(ctx, db, sharedpostgres.Options{
			Domain: "workspace-A", Owner: "instance-A",
		})
		if err != nil {
			return nil, err
		}
		a, err := superagent.OpenMainJournal(ctx, journal, superagent.MainJournalOptions{
			Parallel: 2, Router: router, Worker: worker,
		})
		if err != nil {
			_ = journal.Close()
		}
		return a, err
	}
	_ = start // Do not invent a connection or contact a database in this example.
	fmt.Println("Host-owned database; one leased coordinator per domain.")
	// Output: Host-owned database; one leased coordinator per domain.
}

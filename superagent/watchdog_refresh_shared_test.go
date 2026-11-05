//go:build (linux || darwin || windows) && (amd64 || arm64)

package superagent

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/agent/session"
	"github.com/Icatme/pi-go/superagent/sharedsqlite"
)

func TestWatchdogRefreshTakeoverFencesAuthorizationAndResult(t *testing.T) {
	for _, stage := range []string{"authorization", "inspection"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(privateSharedDir(t), "refresh.db")
			old := sharedJournal(t, path, "old")
			backend := &refreshBackend{}
			options := refreshOptions(backend)
			a, err := OpenMainJournal(t.Context(), old, MainJournalOptions{Parallel: 1, Router: bgMain, Worker: refreshWorker, Background: &options})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "start", Source: "im", Thread: "chat", Kind: "request", Text: "existing request"})
			waitIdle(t, a)
			waitBG(t, a, func(o BackgroundOperation) bool { return o.NeedsReview })
			f := checkWatchdog(t, a, watchdogOptions(newWatchdogClock())).Findings[0]
			op := a.BackgroundOperations()[0]
			request := WatchdogRefreshRequest{FindingID: f.ID, TaskID: op.TaskID, Revision: f.Task.Revision, OperationID: op.ID, Origin: op.Origin, DeliveryID: op.DeliveryID}
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			grant := refreshGrant
			if stage == "authorization" {
				grant = func(context.Context, WatchdogRefreshRequest) error { close(entered); <-release; return nil }
			} else {
				backend.mu.Lock()
				backend.inspect = func(_ context.Context, id string) (background.Observation, error) {
					close(entered)
					<-release
					return background.Observation{ID: id, State: "exited"}, nil
				}
				backend.mu.Unlock()
			}
			done := make(chan error, 1)
			go func() { _, e := a.RefreshWatchdog(t.Context(), request, grant); done <- e }()
			<-entered
			_, _, calls := backend.counts()
			if err = old.Close(); err != nil {
				t.Fatal(err)
			}
			next := sharedJournal(t, path, "replacement")
			if next.Epoch() <= old.Epoch() {
				t.Fatal("epoch did not advance")
			}
			replacementBackend := &refreshBackend{}
			replacementOptions := refreshOptions(replacementBackend)
			b, err := OpenMainJournal(t.Context(), next, MainJournalOptions{Parallel: 1, Router: bgMain, Worker: mainWorker, Background: &replacementOptions})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			// Wait for its existing recovery watcher once. The new intervention must not
			// introduce any additional automatic Inspect on an inherited reservation.
			waitBG(t, b, func(o BackgroundOperation) bool { return o.NeedsReview })
			before := next.Log()
			close(release)
			if err = <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("stale owner not canceled: %v", err)
			}
			_, _, after := backend.counts()
			if after != calls {
				t.Fatal("old owner dispatched after authorization fence")
			}
			if !reflect.DeepEqual(before, next.Log()) || b.BackgroundOperations()[0].Exited {
				t.Fatal("late result changed new owner's facts")
			}
			records := b.WatchdogStatus().Interventions
			if stage == "authorization" && len(records) != 0 || stage == "inspection" && (len(records) != 1 || records[0].State != "pending") {
				t.Fatalf("takeover record: %+v", records)
			}
			if _, err = old.AppendEntry(session.MainLane, session.NewEntry{}); !errors.Is(err, sharedsqlite.ErrLeaseLost) {
				t.Fatalf("storage fence: %v", err)
			}
		})
	}
}

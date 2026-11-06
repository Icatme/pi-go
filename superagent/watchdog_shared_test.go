//go:build (linux || darwin || windows) && (amd64 || arm64)

package superagent

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
	"github.com/Icatme/pi-go/superagent/sharedsqlite"
)

func TestWatchdogSharedTakeoverFencesLateDiagnosis(t *testing.T) {
	path := filepath.Join(privateSharedDir(t), "watchdog.db")
	old := sharedJournal(t, path, "old")
	a, err := OpenMainJournal(t.Context(), old, MainJournalOptions{Parallel: 1, Router: bgMain, Worker: mainWorker})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "one", Source: "im", Thread: "chat", Kind: "request", Text: "existing request"})
	waitIdle(t, a)
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	first := checkWatchdog(t, a, opts)
	clock.advance(opts.Policy.Interval)
	entered, release := make(chan struct{}), make(chan struct{})
	opts.Diagnose = func(_ context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		close(entered)
		<-release
		return WatchdogOutcome{Snapshot: agent.AgentSnapshot{SessionID: j.SessionID, Metadata: map[string]any{"stale": "owner"}}, Diagnoses: []WatchdogDiagnosis{{TaskID: "task-1", Kind: "drift_suspected", Reason: "old owner result"}}}, nil
	}
	result := make(chan error, 1)
	go func() { _, e := a.CheckWatchdog(t.Context(), opts); result <- e }()
	<-entered
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	next := sharedJournal(t, path, "replacement")
	if next.Epoch() <= old.Epoch() {
		t.Fatal("epoch did not advance")
	}
	b, err := OpenMainJournal(t.Context(), next, MainJournalOptions{Parallel: 1, Router: bgMain, Worker: mainWorker})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	before := next.Log()
	close(release)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("stale callback not canceled: %v", err)
	}
	if !reflect.DeepEqual(before, next.Log()) || len(b.WatchdogStatus().Checks) != 1 {
		t.Fatal("late diagnosis changed new owner state")
	}
	// The actual storage fence also rejects a stale direct append independently
	// of asynchronous cancellation delivery to the coordinator.
	if _, err := old.AppendEntry(session.MainLane, session.NewEntry{}); !errors.Is(err, sharedsqlite.ErrLeaseLost) {
		t.Fatalf("old journal fence: %v", err)
	}
	opts.Diagnose = nil
	second := checkWatchdog(t, b, opts)
	if second.Findings[0].PreviousID != first.Findings[0].ID || second.Findings[0].Task.RuntimeID == first.Findings[0].Task.RuntimeID || second.Findings[0].QuietSince != second.ObservedAt {
		t.Fatal("takeover did not reset process-local progress baseline")
	}
	if len(b.Status().Tasks) != 1 {
		t.Fatal("takeover invented work")
	}
}

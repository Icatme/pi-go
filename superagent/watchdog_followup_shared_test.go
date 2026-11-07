//go:build (linux || darwin || windows) && (amd64 || arm64)

package superagent

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/session"
	"github.com/Icatme/pi-go/superagent/sharedsqlite"
)

func TestWatchdogFollowupTakeoverFencesCheckAndKeepsCursor(t *testing.T) {
	path := filepath.Join(privateSharedDir(t), "followup.db")
	old := sharedJournal(t, path, "old")
	backend := &refreshBackend{}
	background := refreshOptions(backend)
	a, err := OpenMainJournal(t.Context(), old, MainJournalOptions{Parallel: 1, Router: bgMain, Worker: refreshWorker, Background: &background})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "start", Source: "im", Thread: "chat", Kind: "request", Text: "existing request"})
	waitIdle(t, a)
	waitBG(t, a, func(o BackgroundOperation) bool { return o.NeedsReview })
	clock := newWatchdogClock()
	options := watchdogOptions(clock)
	options.Policy.Interval = time.Millisecond
	options.Policy.BatchSize = 1
	finding := checkWatchdog(t, a, options).Findings[0]
	op := a.BackgroundOperations()[0]
	request := WatchdogRefreshRequest{FindingID: finding.ID, TaskID: op.TaskID, Revision: finding.Task.Revision, OperationID: op.ID, Origin: op.Origin, DeliveryID: op.DeliveryID}
	intervention, err := a.RefreshWatchdog(t.Context(), request, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	// Follow-up due time is the real wall clock, not the diagnostic clock above.
	timer := time.NewTimer(max(time.Until(time.UnixMilli(intervention.RecheckAt)), 0))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	clock.advance(options.Policy.Interval)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	options.Diagnose = func(_ context.Context, job WatchdogJob) (WatchdogOutcome, error) {
		close(entered)
		<-release
		return WatchdogOutcome{Snapshot: job.Snapshot}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := a.CheckWatchdog(t.Context(), options); done <- err }()
	<-entered
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	next := sharedJournal(t, path, "replacement")
	if next.Epoch() <= old.Epoch() {
		t.Fatal("ownership epoch did not advance")
	}
	replacementBackend := &refreshBackend{}
	replacementBackground := refreshOptions(replacementBackend)
	b, err := OpenMainJournal(t.Context(), next, MainJournalOptions{Parallel: 1, Router: bgMain, Worker: mainWorker, Background: &replacementBackground})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitBG(t, b, func(o BackgroundOperation) bool { return o.NeedsReview })
	before := next.Log()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("old check not canceled: %v", err)
	}
	if !reflect.DeepEqual(before, next.Log()) || len(b.WatchdogFollowupsSince(0, 32).Followups) != 0 || len(b.WatchdogStatus().Checks) != 1 {
		t.Fatal("stale owner published a check or follow-up")
	}
	if _, err := old.AppendEntry(session.MainLane, session.NewEntry{}); !errors.Is(err, sharedsqlite.ErrLeaseLost) {
		t.Fatalf("old storage fence: %v", err)
	}
	options.Diagnose = nil
	checkWatchdog(t, b, options)
	page := b.WatchdogFollowupsSince(0, 1)
	if len(page.Followups) != 1 || page.Cursor != 1 || page.HasMore || page.Followups[0].Request != request || page.Followups[0].InterventionID != intervention.ID || page.Followups[0].Class != "needs_review" {
		t.Fatalf("replacement follow-up: %+v", page)
	}
	if !b.BackgroundOperations()[0].NeedsReview || len(b.Replies()) != 0 || len(b.Status().Tasks) != 1 {
		t.Fatal("read-only follow-up changed work or review")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	third := sharedJournal(t, path, "reopened")
	c, err := OpenMainJournal(t.Context(), third, MainJournalOptions{Parallel: 1, Router: bgMain, Worker: mainWorker, Background: &replacementBackground})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := c.WatchdogFollowupsSince(0, 1); !reflect.DeepEqual(got, page) {
		t.Fatal("reopen changed public evidence or cursor")
	}
	clock.advance(options.Policy.Interval)
	checkWatchdog(t, c, options)
	if got := c.WatchdogFollowupsSince(page.Cursor, 1); len(got.Followups) != 0 || got.Cursor != page.Cursor || got.HasMore {
		t.Fatalf("reopen duplicated intervention evidence: %+v", got)
	}
}

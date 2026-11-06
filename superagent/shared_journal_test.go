//go:build (linux || darwin || windows) && (amd64 || arm64)

package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Icatme/pi-go/agent/background"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/session"
	"github.com/Icatme/pi-go/superagent/sharedsqlite"
)

func privateSharedDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func sharedJournal(t *testing.T, path, owner string) *sharedsqlite.Journal {
	t.Helper()
	j, err := sharedsqlite.Open(t.Context(), path, sharedsqlite.Options{Domain: "workspace", Owner: owner, LeaseTTL: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func TestSharedJournalMigratesEveryMainCommitBoundary(t *testing.T) {
	path := filepath.Join(privateSharedDir(t), "source.jsonl")
	a, err := OpenMain(t.Context(), path, 1, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	event := Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request", Text: "work"}
	submit(t, a, event)
	waitIdle(t, a)
	reply := a.Replies()[0]
	if _, err := a.BeginReplyDelivery(reply.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var prefix []byte
	for i, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		prefix = append(prefix, line...)
		if i < 3 {
			continue
		}
		var item session.LogItem
		if err := json.Unmarshal(line, &item); err != nil {
			t.Fatal(err)
		}
		var tr transition
		if err := json.Unmarshal(item.Entry.Custom.Payload, &tr); err != nil {
			t.Fatal(err)
		}
		t.Run(tr.State+"-"+item.Entry.ID, func(t *testing.T) {
			dir := privateSharedDir(t)
			source := filepath.Join(dir, "legacy.jsonl")
			database := filepath.Join(dir, "shared.db")
			if err := os.WriteFile(source, prefix, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := sharedsqlite.ImportJSONL(t.Context(), database, "workspace", source); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(source)
			if !bytes.Equal(after, prefix) {
				t.Fatal("migration changed source")
			}
			journal := sharedJournal(t, database, "replacement")
			var calls atomic.Int32
			b, err := OpenMainJournal(t.Context(), journal, MainJournalOptions{Parallel: 1, Router: mainFixture, Worker: func(ctx context.Context, j Job) (Outcome, error) { calls.Add(1); return mainWorker(ctx, j) }})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if ok, err := b.Submit(event); ok || err != nil {
				t.Fatalf("duplicate changed %v %v", ok, err)
			}
			waitIdle(t, b)
			got := b.Replies()
			if len(got) != 1 || got[0].ID != reply.ID || got[0].TaskID != reply.TaskID || got[0].DecisionID != reply.DecisionID {
				t.Fatalf("reply identity changed: %+v", got)
			}
			wantCalls := int32(0)
			if tr.State == "main_chat_opened" || tr.State == "received" || (tr.State == "main_decided" && tr.Main.Decision.Action == "new") {
				wantCalls = 1
			}
			// main_chat_opened precedes admission; the explicit Submit above is
			// only a duplicate after received. This fixture starts after it.
			if calls.Load() != wantCalls {
				t.Fatalf("work replayed: %d want %d", calls.Load(), wantCalls)
			}
			if tr.State == "running" && (!got[0].Report.NeedsReview || b.Status().Tasks[0].Status != "needs_review") {
				t.Fatal("unknown external effect lost review barrier")
			}
			if tr.State == "reply_claimed" {
				if got[0].Status != "needs_review" {
					t.Fatal("uncertain send became retryable")
				}
				if _, err := b.BeginReplyDelivery(reply.ID); err == nil {
					t.Fatal("uncertain send was claimed again")
				}
				// Only the new owner records trusted evidence for the original
				// stable attempt; it does not adopt an old executor's decision.
				if err := b.ObserveReply("im", "chat", got[0].Attempts[0].Delivery, "verified-message"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSharedJournalMigrationPreservesSubscriptionsSnapshotsAndReports(t *testing.T) {
	a, path := threadStoppedFixture(t)
	if err := a.SetThreadSubscription("task-1", "github", "pr-27", true); err != nil {
		t.Fatal(err)
	}
	replies := a.Replies()
	if _, err := a.BeginReplyDelivery(replies[0].ID); err != nil {
		t.Fatal(err)
	}
	wantReplies, wantReports := a.Replies(), a.ReportsSince(0)
	wantSnapshot, _, err := a.TaskSnapshot("task-1")
	if err != nil {
		t.Fatal(err)
	}
	wantLog := a.store.Log()
	wantHeader := a.store.Header()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(privateSharedDir(t), "state.db")
	if err := sharedsqlite.ImportJSONL(t.Context(), db, "workspace", path); err != nil {
		t.Fatal(err)
	}
	j := sharedJournal(t, db, "next")
	if !reflect.DeepEqual(j.Log(), wantLog) || j.Header() != wantHeader {
		t.Fatal("import rewrote persisted identities or timestamps")
	}
	b, err := OpenMainJournal(t.Context(), j, MainJournalOptions{Parallel: 2, Router: mainFixture, Worker: mainWorker})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, b)
	if !reflect.DeepEqual(b.Replies(), wantReplies) || !reflect.DeepEqual(b.ReportsSince(0), wantReports) {
		t.Fatal("migration changed replies or reports")
	}
	if !reflect.DeepEqual(b.ThreadSubscriptions("github", "pr-27"), []string{"task-1"}) {
		t.Fatal("subscription lost")
	}
	got, ok, err := b.TaskSnapshot("task-1")
	if !ok || err != nil || !reflect.DeepEqual(got, wantSnapshot) {
		t.Fatal("private task snapshot changed")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	next := sharedJournal(t, db, "third")
	c, err := OpenMainJournal(t.Context(), next, MainJournalOptions{Parallel: 2, Router: mainFixture, Worker: mainWorker})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !reflect.DeepEqual(c.Replies(), wantReplies) || !reflect.DeepEqual(c.ReportsSince(0), wantReports) {
		t.Fatal("database restart changed restored state")
	}
}

func TestSharedJournalTakeoverRejectsLateWorkerDecision(t *testing.T) {
	path := filepath.Join(privateSharedDir(t), "shared.db")
	old := sharedJournal(t, path, "old")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	a, err := OpenMainJournal(t.Context(), old, MainJournalOptions{Parallel: 1, Router: mainFixture, Worker: func(_ context.Context, j Job) (Outcome, error) {
		close(entered)
		<-release // Deliberately ignores cancellation like an external executor.
		out, err := mainWorker(context.Background(), j)
		out.Summary = "stale owner claims success"
		out.Receipts = []ToolReceipt{{Token: 1, Domain: "agent", CallID: "late-write", ToolName: "write", Committed: true, Completed: true}}
		return out, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	event := Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "work"}
	submit(t, a, event)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	// Force ownership loss without waiting for the uncooperative callback.
	// Backend process tests separately cover actual expiry and SIGKILL.
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	replacement := sharedJournal(t, path, "new")
	var calls atomic.Int32
	b, err := OpenMainJournal(t.Context(), replacement, MainJournalOptions{Parallel: 1, Router: mainFixture, Worker: func(ctx context.Context, j Job) (Outcome, error) { calls.Add(1); return mainWorker(ctx, j) }})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	before := replacement.Log()
	releaseOnce.Do(func() { close(release) })
	if err := a.Close(); !errors.Is(err, sharedsqlite.ErrLeaseLost) {
		t.Fatalf("stale worker was not fenced: %v", err)
	}
	if !reflect.DeepEqual(before, replacement.Log()) {
		t.Fatal("stale worker mutated replacement journal")
	}
	reports := b.ReportsSince(0)
	if calls.Load() != 0 || len(reports) != 1 || !reports[0].NeedsReview || reports[0].Status != "interrupted" {
		t.Fatalf("unknown effect replayed/hidden: %+v", reports)
	}
	state := b.Status().Tasks[0]
	if state.Status != "needs_review" || state.ReviewSource != event.Source || state.ReviewEventID != event.ID {
		t.Fatalf("review lost original invocation: %+v", state)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := sharedJournal(t, path, "durable-check")
	if !reflect.DeepEqual(before, reopened.Log()) {
		t.Fatal("stale worker changed durable database")
	}
}

func TestSharedJournalMigrationKeepsBackgroundReviewAndLateEffects(t *testing.T) {
	backend := newBarrierBackground()
	defer func() {
		select {
		case <-backend.release:
		default:
			close(backend.release)
		}
	}()
	path := filepath.Join(privateSharedDir(t), "background.jsonl")
	started := make(chan BackgroundOperation, 1)
	worker := func(ctx context.Context, j Job) (Outcome, error) {
		operation, err := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "original-call", Arguments: json.RawMessage(`{}`)})
		if err != nil {
			return Outcome{}, err
		}
		started <- operation
		return mainWorker(ctx, j)
	}
	a, err := OpenMainBackground(t.Context(), path, 1, bgMain, worker, backgroundOptions(backend))
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "background work"})
	var original BackgroundOperation
	select {
	case original = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background not started")
	}
	waitIdle(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(privateSharedDir(t), "shared.db")
	if err := sharedsqlite.ImportJSONL(t.Context(), db, "workspace", path); err != nil {
		t.Fatal(err)
	}
	// The replacement can neither prove exit nor reissue Start. Its host can
	// later reconnect the same backend and obtain exact-operation evidence.
	var known atomic.Bool
	proxy := scriptedBackground{start: func(context.Context, background.Request) error {
		t.Error("reissued background Start")
		return errors.New("unexpected Start")
	}, inspect: func(ctx context.Context, id string) (background.Observation, error) {
		if known.Load() {
			return backend.Inspect(ctx, id)
		}
		return background.Observation{ID: id, State: "unknown"}, nil
	}}
	opts := backgroundOptions(proxy)
	opts.PollInterval = time.Minute
	journal := sharedJournal(t, db, "replacement")
	b, err := OpenMainJournal(t.Context(), journal, MainJournalOptions{Parallel: 1, Router: bgMain, Worker: mainWorker, Background: &opts})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	operations := b.BackgroundOperations()
	if len(operations) != 1 || operations[0].ID != original.ID || operations[0].CallID != original.CallID || operations[0].DeliveryID != original.DeliveryID || !operations[0].NeedsReview || operations[0].Exited {
		t.Fatalf("in-flight identity/review changed: %+v", operations)
	}
	if err := b.ReconcileBackground(original.ID, "assume no effects"); err == nil {
		t.Fatal("unknown operation released by note")
	}
	close(backend.release)
	<-backend.done
	known.Store(true)
	if err := b.RefreshBackground(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, b)
	completed := b.BackgroundOperations()[0]
	if !completed.Exited || len(completed.Receipts) != 1 || !completed.Receipts[0].Committed || completed.Receipts[0].CallID != "original-call" {
		t.Fatalf("late effect not retained for original call: %+v", completed)
	}
	if err := b.RefreshBackground(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, b)
	if backend.calls != 1 || len(b.ReportsSince(0)) != 2 {
		t.Fatal("late effect duplicated original work or completion")
	}
}

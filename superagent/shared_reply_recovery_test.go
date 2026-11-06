//go:build (linux || darwin || windows) && (amd64 || arm64)

package superagent

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent/session"
	"github.com/Icatme/pi-go/superagent/sharedsqlite"
)

var errLostReplyClaimResult = errors.New("test: lost committed reply claim result")

// Commit to the real SQLite journal, then lose one append's successful result.
// Closing the handle also enforces LeasedJournal's uncertain-ownership contract.
type lostReplyClaimJournal struct {
	*sharedsqlite.Journal
	loseResult atomic.Bool
}

func (j *lostReplyClaimJournal) AppendEntry(lane string, input session.NewEntry) (session.Entry, error) {
	entry, err := j.Journal.AppendEntry(lane, input)
	if err != nil {
		return entry, err
	}
	if j.loseResult.Swap(false) {
		return session.Entry{}, errors.Join(errLostReplyClaimResult, j.Journal.Close())
	}
	return entry, nil
}

func TestSharedJournalUnknownReplyClaimIsNotResent(t *testing.T) {
	path := filepath.Join(privateSharedDir(t), "outbox.db")
	old := &lostReplyClaimJournal{Journal: sharedJournal(t, path, "old")}
	a, err := OpenMainJournal(t.Context(), old, MainJournalOptions{Parallel: 1, Router: mainFixture, Worker: mainWorker})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	submit(t, a, Trigger{ID: "reply", Source: "im", Thread: "chat", Kind: "request", Text: "work"})
	waitIdle(t, a)
	before := a.Replies()
	reply := before[0]
	old.loseResult.Store(true)
	if delivery, err := a.BeginReplyDelivery(reply.ID); !errors.Is(err, errLostReplyClaimResult) || delivery != (ReplyDelivery{}) {
		t.Fatalf("uncertain claim granted send authority: %+v %v", delivery, err)
	}
	if old.loseResult.Load() || old.Context().Err() == nil || !reflect.DeepEqual(a.Replies(), before) {
		t.Fatal("uncertain append changed published state or retained ownership")
	}
	if err := a.Close(); !errors.Is(err, errLostReplyClaimResult) {
		t.Fatalf("lost append result was not retained: %v", err)
	}

	var workers atomic.Int32
	options := MainJournalOptions{Parallel: 1, Router: mainFixture, Worker: func(ctx context.Context, job Job) (Outcome, error) {
		workers.Add(1)
		return mainWorker(ctx, job)
	}}
	b, err := OpenMainJournal(t.Context(), sharedJournal(t, path, "replacement"), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	waitIdle(t, b)
	replies := b.Replies()
	if len(replies) != 1 || replies[0].ID != reply.ID || replies[0].Status != "needs_review" || len(replies[0].Attempts) != 1 || workers.Load() != 0 {
		t.Fatalf("uncertain claim was lost or work re-executed: %+v", replies)
	}
	attempt := replies[0].Attempts[0].Delivery
	if delivery, err := b.BeginReplyDelivery(reply.ID); err == nil || delivery != (ReplyDelivery{}) {
		t.Fatal("replacement granted send authority for the uncertain attempt")
	}
	if err := a.ObserveReply(reply.Source, reply.Thread, attempt, "stale-evidence"); err == nil {
		t.Fatal("old owner accepted a reply receipt")
	}
	if err := b.ObserveReply(reply.Source, reply.Thread, attempt, "verified-message"); err != nil {
		t.Fatal(err)
	}
	wantReplies, wantLog := b.Replies(), b.store.Log()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	c, err := OpenMainJournal(t.Context(), sharedJournal(t, path, "verified"), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	waitIdle(t, c)
	if !reflect.DeepEqual(c.Replies(), wantReplies) || !reflect.DeepEqual(c.store.Log(), wantLog) || workers.Load() != 0 {
		t.Fatal("verified original reply attempt changed after restart")
	}
	if wantReplies[0].Status != "acknowledged" || len(wantReplies[0].Attempts) != 1 || wantReplies[0].Attempts[0].Delivery != attempt {
		t.Fatal("receipt was not attached to the original attempt")
	}
}

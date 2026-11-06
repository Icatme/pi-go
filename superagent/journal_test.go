package superagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/session"
)

type testLeasedJournal struct {
	*session.MemoryStorage
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *testLeasedJournal) Context() context.Context { return s.ctx }
func leasedMemory(t *testing.T) *testLeasedJournal {
	t.Helper()
	mem, err := session.NewMemoryStorage(session.Header{ID: "superagent"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &testLeasedJournal{mem, ctx, cancel}
}

func TestMainJournalCancellationStopsInference(t *testing.T) {
	store := leasedMemory(t)
	started := make(chan struct{})
	stopped := make(chan struct{})
	a, err := OpenMainJournal(t.Context(), store, MainJournalOptions{Parallel: 1, Router: func(ctx context.Context, job MainJob) (MainOutcome, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return MainOutcome{}, ctx.Err()
	}, Worker: mainWorker})
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request", Text: "work"})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("inference did not start")
	}
	store.cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("lost lease did not cancel callback")
	}
	if _, err := a.Submit(Trigger{ID: "2", Source: "im", Thread: "chat", Kind: "request"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("accept after loss: %v", err)
	}
	_ = a.Close()
}

func TestMainJournalRejectsInvalidConfigurationBeforeOwnershipTransfer(t *testing.T) {
	store := leasedMemory(t)
	if a, err := OpenMainJournal(t.Context(), store, MainJournalOptions{Parallel: 0, Router: mainFixture, Worker: mainWorker}); err == nil || a != nil {
		t.Fatalf("invalid config accepted: %v", err)
	}
	if _, err := store.ReadBranch(session.MainLane); err != nil {
		t.Fatalf("invalid config closed caller journal: %v", err)
	}
}

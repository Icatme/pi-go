//go:build (linux || darwin || windows) && (amd64 || arm64)

package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/background"
)

func TestMainBatchBackgroundLeaseTakeoverAndUnknownRecovery(t *testing.T) {
	backend := newBarrierBackground()
	defer func() {
		select {
		case <-backend.release:
		default:
			close(backend.release)
		}
	}()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	oldRoute := func(ctx context.Context, j MainBatchJob) (MainBatchOutcome, error) {
		if j.Inputs[0].Trigger.ID == "hold" {
			close(entered)
			<-release // A late read-only inference deliberately ignores cancellation.
		}
		return backgroundBatch(ctx, j)
	}
	worker := func(ctx context.Context, j Job) (Outcome, error) {
		if _, err := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "original-call", Arguments: json.RawMessage(`{}`)}); err != nil {
			return Outcome{}, err
		}
		return mainWorker(ctx, j)
	}
	options := backgroundOptions(backend)
	path := filepath.Join(privateSharedDir(t), "shared.db")
	old := sharedJournal(t, path, "old")
	batchOptions := MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}
	a, err := OpenMainJournal(t.Context(), old, MainJournalOptions{Parallel: 1, BatchRouter: oldRoute, BatchOptions: batchOptions, Worker: worker, Background: &options})
	if err != nil {
		t.Fatal(err)
	}
	// Unblock before closing if any assertion fails while inference is held.
	defer func() { once.Do(func() { close(release) }); _ = a.Close() }()
	event := Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"}
	submit(t, a, event)
	waitIdle(t, a)
	original := a.BackgroundOperations()[0]
	submit(t, a, Trigger{ID: "hold", Source: "im", Thread: "chat", Kind: "request"})
	receive(t, entered)
	for _, id := range []string{"queued-one", "queued-two"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	var known atomic.Bool
	var restarts, completions atomic.Int32
	proxy := scriptedBackground{start: func(context.Context, background.Request) error {
		restarts.Add(1)
		return errors.New("background Start must not replay")
	}, inspect: func(ctx context.Context, id string) (background.Observation, error) {
		if known.Load() {
			return backend.Inspect(ctx, id)
		}
		return background.Observation{ID: id, State: "unknown"}, nil
	}}
	recoveredOptions := backgroundOptions(proxy)
	recoveredOptions.PollInterval = time.Minute
	recoveredWorker := func(ctx context.Context, j Job) (Outcome, error) {
		completions.Add(1)
		if j.BackgroundCompletion == nil || j.BackgroundCompletion.OperationID != original.ID || j.BackgroundCompletion.CallID != original.CallID || j.TaskID != original.TaskID {
			return Outcome{}, errors.New("unexpected recovered work")
		}
		return mainWorker(ctx, j)
	}
	replacement := sharedJournal(t, path, "replacement")
	config := MainJournalOptions{Parallel: 1, BatchRouter: backgroundBatch, BatchOptions: batchOptions, Worker: recoveredWorker, Background: &recoveredOptions}
	b, err := OpenMainJournal(t.Context(), replacement, config)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	op := b.BackgroundOperations()[0]
	if op.ID != original.ID || op.DeliveryID != original.DeliveryID || op.Origin != original.Origin || !op.NeedsReview || op.Exited || restarts.Load() != 0 {
		t.Fatalf("recovery changed original authority or replayed Start: %+v", op)
	}
	if err := b.ReconcileBackground(op.ID, "assume no effects"); err == nil {
		t.Fatal("unknown released by note")
	}
	if accepted, err := b.SubmitTask(Trigger{ID: "withheld", Source: "im", Thread: "chat", Kind: "request"}, op.TaskID); !accepted || err != nil {
		t.Fatalf("follow-up: %v %v", accepted, err)
	}
	waitIdle(t, b)
	if report := waitBatchBackgroundReport(t, b, "withheld"); report.Status != "blocked" || completions.Load() != 0 {
		t.Fatal("unknown crossed review barrier")
	}
	before := replacement.Log()
	once.Do(func() { close(release) })
	_ = a.Close()
	if !reflect.DeepEqual(before, replacement.Log()) {
		t.Fatal("stale batch owner committed a late decision")
	}
	for _, id := range []string{"hold", "queued-one", "queued-two"} {
		count := 0
		for _, reply := range b.Replies() {
			if reply.EventID == id {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("recovered input %s: replies=%d", id, count)
		}
	}
	close(backend.release)
	receive(t, backend.done)
	known.Store(true)
	if err := b.RefreshBackground(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, b)
	terminal := b.BackgroundOperations()[0]
	if !terminal.Exited || terminal.NeedsReview || len(terminal.Receipts) != 1 || !terminal.Receipts[0].Committed || terminal.CallID != original.CallID || completions.Load() != 1 || backend.calls != 1 || restarts.Load() != 0 {
		t.Fatalf("recovery lost late effect or duplicated work: %+v", terminal)
	}
	if err := b.RefreshBackground(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, b)
	status, replies := b.Status(), b.Replies()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := sharedJournal(t, path, "terminal-check")
	c, err := OpenMainJournal(t.Context(), reopened, config)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if accepted, err := c.Submit(event); accepted || err != nil {
		t.Fatalf("duplicate: %v %v", accepted, err)
	}
	waitIdle(t, c)
	if !reflect.DeepEqual(status, c.Status()) || !reflect.DeepEqual(replies, c.Replies()) || !reflect.DeepEqual(terminal, c.BackgroundOperations()[0]) || completions.Load() != 1 || restarts.Load() != 0 {
		t.Fatal("terminal reopen changed identity or dispatched duplicate work")
	}
}

package superagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

// Exercise only the public control surface: the client never reads any event
// before cancellation. The worker checks cancellation at its write boundary.
func TestUnreadProgressCannotDelayAbortBeforeEffect(t *testing.T) {
	dir := t.TempDir()
	sideEffect := filepath.Join(dir, "published")
	id := make(chan string, 1)
	observing := make(chan struct{})
	atBoundary := make(chan struct{})
	check := make(chan struct{})
	work := func(ctx context.Context, j Job) (Outcome, error) {
		id <- j.TaskID
		select {
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		case <-observing:
		}
		for range 64 {
			j.NotifyProgress("tool_start")
		}
		close(atBoundary)
		select {
		case <-check:
		case <-ctx.Done():
		}
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		return Outcome{Summary: "Published"}, os.WriteFile(sideEffect, []byte("side effect"), 0600)
	}
	a, err := Open(t.Context(), filepath.Join(dir, "state.jsonl"), 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "write", Source: "local", Thread: "t", Kind: "request", Text: "publish"})
	target := RunTarget{TaskID: receive(t, id), Source: "local", EventID: "write"}
	slow, err := a.Subscribe(t.Context(), target.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	// Only drain during cleanup, so a regression cannot deadlock test teardown.
	defer func() {
		go func() {
			for range slow {
			}
		}()
	}()
	close(observing)
	receive(t, atBoundary)
	aborted := make(chan error, 1)
	go func() { aborted <- a.Abort(target) }()
	if err := receive(t, aborted); err != nil {
		t.Fatal(err)
	}
	close(check)
	waitIdle(t, a)
	if _, err := os.Stat(sideEffect); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled write reached filesystem: %v", err)
	}
	v := a.Status()
	if len(v.Reports) != 1 || v.Reports[0].Status != "interrupted" || !v.Reports[0].NeedsReview || v.Tasks[0].Status != "needs_review" {
		t.Fatal(v)
	}
}

type gatedSnapshotEncoding struct{ entered, release chan struct{} }

func (g gatedSnapshotEncoding) MarshalJSON() ([]byte, error) {
	close(g.entered)
	<-g.release
	return []byte("true"), nil
}

func TestFinalizingWorkerIsObservableButNoLongerControllable(t *testing.T) {
	working := make(chan struct{})
	returnWorker := make(chan struct{})
	encoding, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	work := func(ctx context.Context, j Job) (Outcome, error) {
		close(working)
		select {
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		case <-returnWorker:
		}
		return Outcome{Summary: "done", Snapshot: agent.AgentSnapshot{Metadata: map[string]any{"gate": gatedSnapshotEncoding{encoding, release}}}}, nil
	}
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer unblock()
	submit(t, a, Trigger{ID: "e", Source: "s", Thread: "t", Kind: "request", Text: "go"})
	receive(t, working)
	target := RunTarget{TaskID: "task-1", Source: "s", EventID: "e"}
	early, err := a.Subscribe(t.Context(), target.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if p := receive(t, early); p.Kind != "running" {
		t.Fatal(p)
	}
	close(returnWorker)
	receive(t, encoding)
	status := a.Status().Tasks[0]
	if status.Status != "finalizing" || status.ActiveSource != "" || status.ActiveEventID != "" {
		t.Fatalf("obsolete control target exposed during snapshot finalization: %+v", status)
	}
	if _, err := a.Steer(target, "too late"); err == nil {
		t.Fatal("accepted steering after return")
	}
	if err := a.Abort(target); err == nil {
		t.Fatal("accepted abort after return")
	}
	late, err := a.Subscribe(t.Context(), target.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []<-chan Progress{early, late} {
		p := receive(t, ch)
		if p.Kind != "finalizing" || p.EventID != target.EventID || p.Source != target.Source {
			t.Fatalf("incorrect finalization identity: %+v", p)
		}
	}
	unblock()
	waitIdle(t, a)
	if p := receive(t, early); p.Kind != "completed" {
		t.Fatal(p)
	}
}

func TestCanceledSubscriptionImmediatelyFreesGlobalCapacity(t *testing.T) {
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, func(context.Context, Job) (Outcome, error) { return Outcome{Summary: "done"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "e", Source: "s", Thread: "t", Kind: "request", Text: "go"})
	submit(t, a, Trigger{ID: "other", Source: "s", Thread: "other", Kind: "request", Text: "another task"})
	waitIdle(t, a)
	for range 127 {
		if _, err := a.Subscribe(t.Context(), "task-1"); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 16 {
		slot, cancel := context.WithCancel(t.Context())
		id := "task-1"
		if i%2 == 0 {
			id = "task-2"
		}
		if _, err := a.Subscribe(slot, id); err != nil {
			cancel()
			t.Fatalf("replacement was rejected despite canceled predecessor: %v", err)
		}
		if _, err := a.Subscribe(t.Context(), "task-1"); err == nil {
			cancel()
			t.Fatal("accepted more than 128 live subscriptions")
		}
		cancel()
	}
}

package superagent

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A stalled observer must neither block execution nor receive tool payloads
// or lifecycle states invented by a custom worker.
func TestTaskProgressIsolationAndSlowReader(t *testing.T) {
	jobs := make(chan Job, 2)
	release := make(chan struct{})
	route := func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }
	work := func(ctx context.Context, j Job) (Outcome, error) {
		jobs <- j
		select {
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		case <-release:
			return Outcome{Summary: "finished"}, nil
		}
	}
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "events.jsonl"), 2, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{"one", "im", "a", "request", "A"})
	first := receive(t, jobs)
	submit(t, a, Trigger{"two", "git", "b", "request", "B"})
	second := receive(t, jobs)
	subCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fast, err := a.Subscribe(subCtx, first.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	slow, err := a.Subscribe(t.Context(), first.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	initial := receive(t, fast)
	if initial.Kind != "running" || initial.EventID != "one" {
		t.Fatal(initial)
	}
	second.NotifyProgress("turn_start")
	first.NotifyProgress("private tool payload")
	first.NotifyProgress("completed")
	first.NotifyProgress("tool_start")
	started := receive(t, fast)
	if started.Sequence != initial.Sequence+1 || started.Kind != "tool_start" || started.Source != "im" || started.TaskID != first.TaskID {
		t.Fatal(started)
	}
	for range 100 {
		first.NotifyProgress("tool_end")
	}
	close(release)
	waitIdle(t, a)
	latest := receive(t, slow)
	if latest.Sequence <= initial.Sequence+1 {
		t.Fatalf("slow reader should see a gap, got %+v", latest)
	}
	finished := latest
	for finished.Kind != "completed" {
		next := receive(t, slow)
		if next.Sequence <= finished.Sequence {
			t.Fatalf("out-of-order progress: %+v then %+v", finished, next)
		}
		finished = next
	}
	if finished.Kind != "completed" || finished.EventID != "one" || finished.Sequence <= started.Sequence+100 {
		t.Fatal(finished)
	}
	// Draining a task never allows a retained Job callback to impersonate it.
	first.NotifyProgress("turn_start")
	select {
	case late := <-slow:
		t.Fatalf("stale progress: %+v", late)
	default:
	}
	cancel()
	deadline := time.After(5 * time.Second)
	for fast != nil {
		select {
		case _, open := <-fast:
			if !open {
				fast = nil
			}
		case <-deadline:
			t.Fatal("subscription still open after cancellation")
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, open := <-slow; open {
		t.Fatal("subscription survived coordinator shutdown")
	}
}

package superagent

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
)

type watchdogClock struct {
	mu   sync.Mutex
	now  time.Time
	wake chan time.Time
}

func newWatchdogClock() *watchdogClock {
	return &watchdogClock{now: time.Unix(1700000000, 0), wake: make(chan time.Time, 1)}
}
func (c *watchdogClock) Now() time.Time                       { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *watchdogClock) After(time.Duration) <-chan time.Time { return c.wake }
func (c *watchdogClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	n := c.now
	c.mu.Unlock()
	select {
	case c.wake <- n:
	default:
	}
}
func watchdogOptions(c *watchdogClock) WatchdogOptions {
	return WatchdogOptions{Policy: WatchdogPolicy{Revision: "v1", Interval: time.Minute, BatchSize: 2, IdleAfter: 2 * time.Minute, IdleTurns: 3}, Clock: c}
}
func checkWatchdog(t *testing.T, a *SuperAgent, o WatchdogOptions) WatchdogCheck {
	t.Helper()
	c, err := a.CheckWatchdog(t.Context(), o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func watchdogAgent(t *testing.T, work Worker) (*SuperAgent, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state.jsonl")
	a, e := OpenMain(t.Context(), p, 1, bgMain, work)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	return a, p
}

func TestWatchdogEmptyJournalAndRestartCursor(t *testing.T) {
	a, p := watchdogAgent(t, mainWorker)
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	first := checkWatchdog(t, a, opts)
	if first.ID == "" || len(first.Findings) != 0 {
		t.Fatalf("empty check: %+v", first)
	}
	for _, id := range []string{"a", "b", "c"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: id, Kind: "request", Text: "existing task"})
	}
	waitIdle(t, a)
	clock.advance(time.Minute)
	second := checkWatchdog(t, a, opts)
	if len(second.Findings) != 2 || second.Cursor != 2 {
		t.Fatalf("cursor: %+v", second)
	}
	state := a.WatchdogStatus()
	before := a.Status()
	if state.SessionID == a.main.id || state.ProviderSessionID == a.main.providerID {
		t.Fatal("shared main session")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), p, 1, bgMain, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got := checkWatchdog(t, b, opts); got.ID != "" {
		t.Fatal("restart repeated committed check")
	}
	if !reflect.DeepEqual(before, b.Status()) {
		t.Fatal("watchdog changed task state")
	}
	clock.advance(time.Minute)
	third := checkWatchdog(t, b, opts)
	if len(third.Findings) != 1 || third.Findings[0].Task.TaskID != "task-3" || third.Cursor != 0 {
		t.Fatalf("lost cursor: %+v", third)
	}
	if b.WatchdogStatus().SessionID != state.SessionID || b.WatchdogStatus().ProviderSessionID != state.ProviderSessionID {
		t.Fatal("identity changed")
	}
	opts.Policy.Revision = "v2"
	if _, err = b.CheckWatchdog(t.Context(), opts); err == nil {
		t.Fatal("silent policy change")
	}
}

func TestWatchdogDistinguishesSlowQueueToolAndIdle(t *testing.T) {
	entered := make(chan Job, 1)
	a, _ := watchdogAgent(t, func(ctx context.Context, j Job) (Outcome, error) {
		entered <- j
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	})
	submit(t, a, Trigger{ID: "slow", Source: "im", Thread: "one", Kind: "request", Text: "slow task"})
	j := <-entered
	submit(t, a, Trigger{ID: "queued", Source: "im", Thread: "two", Kind: "request", Text: "waiting task"})
	// Wait for the independent main to assign the queued task.
	deadline := time.After(3 * time.Second)
	for len(a.Status().Tasks) != 2 {
		select {
		case <-deadline:
			t.Fatal("queue missing")
		case <-time.After(time.Millisecond):
		}
	}
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	first := checkWatchdog(t, a, opts)
	if first.Findings[0].Class != "normal_slow" || first.Findings[1].Class != "queued" {
		t.Fatalf("initial classes: %+v", first.Findings)
	}
	clock.advance(24 * time.Hour)
	slow := checkWatchdog(t, a, opts)
	if slow.Findings[0].Class != "normal_slow" {
		t.Fatal("elapsed time alone classified stuck")
	}
	for range 4 {
		j.NotifyProgress("turn_end")
	}
	clock.advance(3 * time.Minute)
	idle := checkWatchdog(t, a, opts)
	if idle.Findings[0].Class != "idle_suspected" || idle.Findings[0].PreviousID != slow.Findings[0].ID {
		t.Fatalf("idle/recheck: %+v", idle)
	}
	j.NotifyProgress("tool_start")
	clock.advance(time.Hour)
	tool := checkWatchdog(t, a, opts)
	if tool.Findings[0].Class != "tool_running" {
		t.Fatalf("tool canceled/misclassified: %+v", tool)
	}
	select {
	case <-jContextDone(a, j.TaskID):
		t.Fatal("watchdog canceled worker")
	default:
	}
	if len(a.Replies()) != 0 || len(a.Status().Tasks) != 2 {
		t.Fatal("watchdog created business output")
	}
}
func jContextDone(a *SuperAgent, id string) <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tasks[id].active.ctx.Done()
}

func TestWatchdogModelReadOnlyStaleAndIndependent(t *testing.T) {
	a, _ := watchdogAgent(t, mainWorker)
	submit(t, a, Trigger{ID: "one", Source: "im", Thread: "chat", Kind: "request", Text: "goal"})
	waitIdle(t, a)
	clock := newWatchdogClock()
	opts := watchdogOptions(clock)
	entered, release := make(chan WatchdogJob, 1), make(chan struct{})
	opts.Diagnose = func(ctx context.Context, j WatchdogJob) (WatchdogOutcome, error) {
		entered <- j
		select {
		case <-release:
		case <-ctx.Done():
			return WatchdogOutcome{}, ctx.Err()
		}
		return WatchdogOutcome{Snapshot: agent.AgentSnapshot{SessionID: j.SessionID, Metadata: map[string]any{"watch": "memory"}}, Diagnoses: []WatchdogDiagnosis{{TaskID: j.Tasks[0].TaskID, Kind: "drift_suspected", Reason: "Public report diverges from goal."}}}, nil
	}
	result := make(chan error, 1)
	go func() { _, e := a.CheckWatchdog(t.Context(), opts); result <- e }()
	job := <-entered
	if job.SessionID == a.main.id || job.Snapshot.SessionID != job.SessionID {
		t.Fatal("not independent")
	}
	if _, err := a.CheckWatchdog(t.Context(), opts); !errors.Is(err, ErrWatchdogBusy) {
		t.Fatalf("overlapping check: %v", err)
	}
	if _, err := a.SubmitTask(Trigger{ID: "update", Source: "im", Thread: "chat", Kind: "request", Text: "new goal input"}, "task-1"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	findings := a.WatchdogStatus().Checks[0].Findings
	if !findings[0].Stale {
		t.Fatal("old result advertised as current")
	}
	if len(a.Status().Tasks) != 1 {
		t.Fatal("diagnosis started business task")
	}
}

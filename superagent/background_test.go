package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/background"
)

type barrierBackground struct {
	mu        sync.Mutex
	request   background.Request
	calls     int
	entered   chan struct{}
	release   chan struct{}
	stopped   chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	exited    bool
	committed bool
}

func newBarrierBackground() *barrierBackground {
	return &barrierBackground{entered: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{}), done: make(chan struct{})}
}
func (b *barrierBackground) Start(_ context.Context, r background.Request) error {
	b.mu.Lock()
	b.calls++
	b.request = r
	b.mu.Unlock()
	b.startOnce.Do(func() {
		close(b.entered)
		go func() {
			<-b.release
			b.mu.Lock()
			b.committed = true
			b.exited = true
			b.mu.Unlock()
			close(b.done)
		}()
	})
	return nil
}
func (b *barrierBackground) Inspect(_ context.Context, id string) (background.Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.request.ID != id {
		return background.Observation{ID: id, State: "unknown"}, nil
	}
	o := background.Observation{ID: id, State: "running"}
	if b.exited {
		o.State = "exited"
		o.Output = json.RawMessage(`{"content":[{"type":"text","text":"PRIVATE OUTPUT"}]}`)
		o.Receipts = []background.Receipt{{Token: 1, Domain: "agent", CallID: b.request.Call.ID, ToolName: b.request.Call.Name, Committed: b.committed, Completed: true}}
	}
	return o, nil
}
func (b *barrierBackground) Stop(_ context.Context, _ string) error {
	b.stopOnce.Do(func() { close(b.stopped) })
	return nil
}
func backgroundOptions(b background.Backend) BackgroundOptions {
	return BackgroundOptions{MaxConcurrent: 1, PollInterval: time.Millisecond, Tools: []BackgroundTool{{Name: "slow", Scope: "workspace-A", PolicyRevision: "v1", Backend: b, Authorize: func(context.Context, BackgroundAuthorization) error { return nil }}}}
}
func bgMain(_ context.Context, j MainJob) (MainOutcome, error) {
	d := MainDecision{Action: "ignore"}
	if j.Input.Kind == "user" {
		d.Action = "new"
	}
	return MainOutcome{Decision: d, Snapshot: j.Snapshot}, nil
}
func waitBG(t *testing.T, a *SuperAgent, p func(BackgroundOperation) bool) BackgroundOperation {
	t.Helper()
	for {
		a.mu.Lock()
		ch := a.changed
		a.mu.Unlock()
		for _, o := range a.BackgroundOperations() {
			if p(o) {
				return o
			}
		}
		select {
		case <-ch:
		case <-t.Context().Done():
			t.Fatal("background wait timeout")
		}
	}
}
func TestBackgroundWorkerHandlesNewInputBeforeToolExit(t *testing.T) {
	b := newBarrierBackground()
	defer func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	}()
	started := make(chan BackgroundOperation, 1)
	inputs := make(chan Job, 4)
	var a *SuperAgent
	worker := func(ctx context.Context, j Job) (Outcome, error) {
		inputs <- j
		if j.Trigger.ID == "first" {
			o, err := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "original-call", Arguments: json.RawMessage(`{}`)})
			if err != nil {
				return Outcome{}, err
			}
			started <- o
		}
		return mainWorker(ctx, j)
	}
	var err error
	a, err = OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "bg.jsonl"), 1, bgMain, worker, backgroundOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	var op BackgroundOperation
	select {
	case op = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("no Started")
	}
	if op.ID == "" || op.TaskID == "" || op.Scope != "workspace-A" || op.DeliveryID == "" || op.CallID != "original-call" {
		t.Fatalf("missing durable identity/grant: %+v", op)
	}
	if _, err = a.SubmitTask(Trigger{ID: "followup", Source: "im", Thread: "chat", Kind: "request", Text: "new input"}, op.TaskID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	var first, follow Job
	for i := 0; i < 2; i++ {
		select {
		case j := <-inputs:
			if j.Trigger.ID == "first" {
				first = j
			} else {
				follow = j
			}
		default:
			t.Fatal("same Worker did not process follow-up before exit")
		}
	}
	if first.TaskID != follow.TaskID || follow.Trigger.ID != "followup" {
		t.Fatal("wrong ownership")
	}
	close(b.release)
	terminal := waitBG(t, a, func(o BackgroundOperation) bool { return o.Exited })
	waitIdle(t, a)
	var completion Job
	select {
	case completion = <-inputs:
	default:
		t.Fatal("completion not delivered")
	}
	if completion.TaskID != op.TaskID || completion.BackgroundCompletion == nil || completion.BackgroundCompletion.OperationID != op.ID || completion.BackgroundCompletion.CallID != "original-call" || !terminal.Receipts[0].Committed {
		t.Fatalf("bad completion: %+v %+v", completion, terminal)
	}
	if err = a.RefreshBackground(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	select {
	case <-inputs:
		t.Fatal("duplicate completion")
	default:
	}
}
func TestBackgroundStopRetainsLateCommitAndQuota(t *testing.T) {
	b := newBarrierBackground()
	defer func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	}()
	starts := make(chan BackgroundOperation, 1)
	worker := func(ctx context.Context, j Job) (Outcome, error) {
		if j.Trigger.ID == "first" {
			o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)})
			if e != nil {
				return Outcome{}, e
			}
			starts <- o
		}
		return mainWorker(ctx, j)
	}
	a, e := OpenMainBackground(t.Context(), filepath.Join(t.TempDir(), "bg.jsonl"), 1, bgMain, worker, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	o := <-starts
	waitIdle(t, a)
	if e = a.StopBackground(t.Context(), o.ID); e != nil {
		t.Fatal(e)
	}
	if x := a.BackgroundOperations()[0]; x.Exited || !x.StopRequested {
		t.Fatal("stop falsely released operation")
	}
	close(b.release)
	x := waitBG(t, a, func(o BackgroundOperation) bool { return o.Exited })
	waitIdle(t, a)
	if !x.NeedsReview || !x.StopRequested || len(x.Receipts) != 1 || !x.Receipts[0].Committed || !x.Receipts[0].Completed {
		t.Fatalf("lost late effects: %+v", x)
	}
	if e = a.ReconcileBackground(o.ID, "Confirmed the late write and its final exit."); e != nil {
		t.Fatal(e)
	}
}
func TestBackgroundUnknownRestartDoesNotReplayStart(t *testing.T) {
	b := newBarrierBackground()
	defer func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	}()
	starts := make(chan BackgroundOperation, 1)
	path := filepath.Join(t.TempDir(), "bg.jsonl")
	worker := func(ctx context.Context, j Job) (Outcome, error) {
		o, e := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)})
		if e != nil {
			return Outcome{}, e
		}
		starts <- o
		return mainWorker(ctx, j)
	}
	a, e := OpenMainBackground(t.Context(), path, 1, bgMain, worker, backgroundOptions(b))
	if e != nil {
		t.Fatal(e)
	}
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
	o := <-starts
	waitIdle(t, a)
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	lost := newBarrierBackground()
	c, e := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, backgroundOptions(lost))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	x := waitBG(t, c, func(o BackgroundOperation) bool { return o.NeedsReview })
	if x.ID != o.ID || x.Exited || lost.calls != 0 {
		t.Fatal("unknown operation restarted or released")
	}
	if e = c.ReconcileBackground(o.ID, "assume no effects"); e == nil {
		t.Fatal("unknown exit released by note")
	}
	if e = c.RefreshBackground(t.Context(), o.ID); e != nil && !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

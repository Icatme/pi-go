package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/background"
)

type refreshBackend struct {
	mu                         sync.Mutex
	starts, stops, inspections int
	observation                background.Observation
	err                        error
	inspect                    func(context.Context, string) (background.Observation, error)
}

func (b *refreshBackend) Start(context.Context, background.Request) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starts++
	return nil
}
func (b *refreshBackend) Stop(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stops++
	return nil
}
func (b *refreshBackend) Inspect(ctx context.Context, id string) (background.Observation, error) {
	b.mu.Lock()
	b.inspections++
	f, o, e := b.inspect, b.observation, b.err
	b.mu.Unlock()
	if f != nil {
		return f(ctx, id)
	}
	o.ID = id
	if o.State == "" {
		o.State = "unknown"
	}
	return o, e
}
func (b *refreshBackend) counts() (int, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.starts, b.stops, b.inspections
}
func (b *refreshBackend) set(o background.Observation, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observation = o
	b.err = err
}
func refreshOptions(b *refreshBackend) BackgroundOptions {
	o := backgroundOptions(b)
	o.PollInterval = time.Minute
	return o
}
func refreshWorker(ctx context.Context, j Job) (Outcome, error) {
	if j.Trigger.ID == "start" {
		if _, err := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "original-call", Arguments: json.RawMessage(`{"secret":"private-refresh-arguments"}`)}); err != nil {
			return Outcome{}, err
		}
	}
	return mainWorker(ctx, j)
}
func refreshFixture(t *testing.T) (*SuperAgent, string, *refreshBackend, WatchdogRefreshRequest) {
	t.Helper()
	b := &refreshBackend{}
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	a, err := OpenMainBackground(t.Context(), path, 1, bgMain, refreshWorker, refreshOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	submit(t, a, Trigger{ID: "start", Source: "im", Thread: "chat", Kind: "request", Text: "existing request"})
	waitIdle(t, a)
	waitBG(t, a, func(o BackgroundOperation) bool { return o.Status == "needs_review" })
	c := checkWatchdog(t, a, watchdogOptions(newWatchdogClock()))
	op := a.BackgroundOperations()[0]
	f := c.Findings[0]
	return a, path, b, WatchdogRefreshRequest{FindingID: f.ID, TaskID: op.TaskID, Revision: f.Task.Revision, OperationID: op.ID, Origin: op.Origin, DeliveryID: op.DeliveryID}
}
func refreshGrant(context.Context, WatchdogRefreshRequest) error { return nil }

func TestWatchdogRefreshUnknownDurableAndDeduplicated(t *testing.T) {
	a, path, b, r := refreshFixture(t)
	_, _, before := b.counts()
	result, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || result.Result != "unknown" || result.RecheckCondition != "new_finding_and_host_authorization" || result.RecheckAt <= result.ObservedAt {
		t.Fatalf("result: %+v", result)
	}
	if !result.Operation.NeedsReview || a.backgroundInUse() != 1 {
		t.Fatal("unknown released review or quota")
	}
	_, _, after := b.counts()
	if after != before+1 {
		t.Fatalf("inspect calls %d -> %d", before, after)
	}
	copy, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil || !reflect.DeepEqual(copy, result) {
		t.Fatalf("duplicate: %+v %v", copy, err)
	}
	_, _, after = b.counts()
	if after != before+1 {
		t.Fatal("duplicate inspected")
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, beforeRestart := b.counts()
	reopened, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	waitRefreshInspections(t, b, beforeRestart+1)
	copy, err = reopened.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil || !reflect.DeepEqual(copy, result) {
		t.Fatalf("restart changed result: %+v %v", copy, err)
	}
	if got := reopened.WatchdogStatus().Interventions; len(got) != 1 || !reflect.DeepEqual(got[0], result) {
		t.Fatalf("lost record: %+v", got)
	}
	starts, stops, inspections := b.counts()
	if inspections != beforeRestart+1 {
		t.Fatal("restart replayed intervention Inspect beyond existing recovery watcher")
	}
	if starts != 1 || stops != 0 {
		t.Fatalf("external dispatch: starts=%d stops=%d", starts, stops)
	}
}

func TestWatchdogRefreshRequiresCurrentExplicitAuthority(t *testing.T) {
	for _, kind := range []string{"nil", "denied", "panic", "finding", "task", "revision", "operation", "origin", "delivery", "new-input", "new-check"} {
		t.Run(kind, func(t *testing.T) {
			a, _, b, r := refreshFixture(t)
			grant := refreshGrant
			switch kind {
			case "nil":
				grant = nil
			case "denied":
				grant = func(context.Context, WatchdogRefreshRequest) error { return errors.New("private-denial") }
			case "panic":
				grant = func(context.Context, WatchdogRefreshRequest) error { panic("private-panic") }
			case "finding":
				r.FindingID += "wrong"
			case "task":
				r.TaskID = "task-other"
			case "revision":
				r.Revision = "wrong"
			case "operation":
				r.OperationID += "wrong"
			case "origin":
				r.Origin.EventID = "wrong"
			case "delivery":
				r.DeliveryID = "delivery-999"
			case "new-input":
				if _, e := a.SubmitTask(Trigger{ID: "update", Source: "im", Thread: "chat", Kind: "request", Text: "changed input"}, r.TaskID); e != nil {
					t.Fatal(e)
				}
			case "new-check":
				clock := newWatchdogClock()
				clock.advance(time.Minute)
				checkWatchdog(t, a, watchdogOptions(clock))
			}
			_, _, before := b.counts()
			if _, err := a.RefreshWatchdog(t.Context(), r, grant); err == nil {
				t.Fatal("invalid authority accepted")
			}
			_, _, after := b.counts()
			if before != after || len(a.WatchdogStatus().Interventions) != 0 {
				t.Fatal("denial changed state or inspected")
			}
		})
	}
}

func TestWatchdogRefreshRecordsTerminalOriginalOperation(t *testing.T) {
	a, path, b, r := refreshFixture(t)
	b.set(background.Observation{State: "exited", Output: json.RawMessage(`{"secret":"private-refresh-output"}`), Receipts: []background.Receipt{{Token: 1, Domain: "agent", CallID: "original-call", ToolName: "slow", Committed: true, Completed: true}}}, nil)
	result, err := a.RefreshWatchdog(t.Context(), r, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	op := a.BackgroundOperations()[0]
	if result.Result != "exited" || result.Operation.ID != r.OperationID || !op.Exited || op.CompletionEventID != r.OperationID || a.backgroundInUse() != 0 {
		t.Fatalf("terminal: %+v %+v", result, op)
	}
	if len(a.BackgroundOperations()) != 1 || len(a.Status().Tasks) != 1 {
		t.Fatal("refresh created new operation/task")
	}
	if _, err = a.RefreshWatchdog(t.Context(), r, refreshGrant); err != nil {
		t.Fatal(err)
	}
	completions := 0
	for _, e := range a.events {
		if e.trigger.Source == backgroundSource {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("completion count %d", completions)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(b))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	waitIdle(t, reopened)
	if got, err := reopened.RefreshWatchdog(t.Context(), r, refreshGrant); err != nil || !reflect.DeepEqual(got, result) {
		t.Fatalf("terminal replay: %+v %v", got, err)
	}
	completions = 0
	for _, e := range reopened.events {
		if e.trigger.Source == backgroundSource {
			completions++
		}
	}
	if completions != 1 {
		t.Fatal("replay duplicated completion")
	}
}

func TestWatchdogRefreshActiveRunReplays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.jsonl")
	backend := &refreshBackend{}
	started := make(chan struct{})
	a, err := OpenMainBackground(t.Context(), path, 1, bgMain, func(ctx context.Context, j Job) (Outcome, error) {
		if _, err := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "call", Arguments: json.RawMessage(`{}`)}); err != nil {
			return Outcome{}, err
		}
		close(started)
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	}, refreshOptions(backend))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "start", Source: "im", Thread: "chat", Kind: "request", Text: "existing active request"})
	<-started
	waitBG(t, a, func(o BackgroundOperation) bool { return o.NeedsReview })
	finding := checkWatchdog(t, a, watchdogOptions(newWatchdogClock())).Findings[0]
	op := a.BackgroundOperations()[0]
	r := WatchdogRefreshRequest{FindingID: finding.ID, TaskID: op.TaskID, Revision: finding.Task.Revision, OperationID: op.ID, Origin: op.Origin, DeliveryID: op.DeliveryID}
	if finding.Task.Target != op.Origin {
		t.Fatal("fixture lost live run identity")
	}
	if _, err = a.RefreshWatchdog(t.Context(), r, refreshGrant); err != nil {
		t.Fatal(err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(backend))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if len(b.WatchdogStatus().Interventions) != 1 {
		t.Fatal("active refresh did not replay")
	}
}

func waitRefreshInspections(t *testing.T, b *refreshBackend, want int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		_, _, n := b.counts()
		if n >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatal("recovery watcher did not inspect")
		case <-time.After(time.Millisecond):
		}
	}
}

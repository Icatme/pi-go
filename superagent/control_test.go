package superagent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
)

func assertSteerDelivery(t *testing.T, receipt *SteerReceipt, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got, err := receipt.Wait(ctx)
	if err != nil || got != want {
		t.Fatalf("steer delivered=%v err=%v, want %v", got, err, want)
	}
}

func TestSteeringQueuesAtMostSixteenAndSettlesOnlyOnPoll(t *testing.T) {
	type result struct {
		msgs []agent.Message
		err  error
	}
	started := make(chan Job, 1)
	poll := make(chan agent.QueueMode)
	results := make(chan result, 1)
	finish := make(chan struct{})
	route := func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }
	work := func(ctx context.Context, j Job) (Outcome, error) {
		started <- j
		for {
			select {
			case mode := <-poll:
				msgs, err := j.TakeSteering(ctx, mode)
				results <- result{msgs, err}
			case <-finish:
				return Outcome{Summary: "ok"}, nil
			case <-ctx.Done():
				return Outcome{}, ctx.Err()
			}
		}
	}
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "steering.jsonl"), 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	e := Trigger{"first", "chat", "thread", "request", "work"}
	submit(t, a, e)
	j := receive(t, started)
	target := RunTarget{TaskID: j.TaskID, Source: e.Source, EventID: e.ID}
	for _, wrong := range []RunTarget{{TaskID: j.TaskID, Source: e.Source, EventID: "stale"}, {TaskID: j.TaskID, Source: "another", EventID: e.ID}} {
		if _, err := a.Steer(wrong, "wrong task"); err == nil {
			t.Fatal("misdirected steer accepted")
		}
		if err := a.Abort(wrong); err == nil {
			t.Fatal("misdirected cancellation accepted")
		}
	}
	for _, text := range []string{"  ", strings.Repeat("x", 4097), string([]byte{0xff})} {
		if _, err := a.Steer(target, text); err == nil {
			t.Fatal("invalid instruction accepted")
		}
	}
	receipts := make([]*SteerReceipt, 16)
	for i := range receipts {
		receipts[i], err = a.Steer(target, fmt.Sprint(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Steer(target, "overflow"); err == nil {
		t.Fatal("steering exceeded queue capacity")
	}
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	if delivered, err := receipts[0].Wait(expired); delivered || !errors.Is(err, context.Canceled) {
		t.Fatal("steer falsely claimed prior delivery", err)
	}
	poll <- ""
	first := receive(t, results)
	if first.err != nil || len(first.msgs) != 1 || messageText(first.msgs[0]) != "0" {
		t.Fatal(first)
	}
	assertSteerDelivery(t, receipts[0], true)
	for i := 1; i < len(receipts); i++ {
		if got, err := receipts[i].Wait(expired); got || !errors.Is(err, context.Canceled) {
			t.Fatal("one-at-a-time dequeued another instruction")
		}
	}
	poll <- agent.QueueModeAll
	batch := receive(t, results)
	if batch.err != nil || len(batch.msgs) != 15 {
		t.Fatal(batch)
	}
	for i, msg := range batch.msgs {
		if messageText(msg) != fmt.Sprint(i+1) {
			t.Fatal("out of order:", msg)
		}
		assertSteerDelivery(t, receipts[i+1], true)
	}
	undelivered, err := a.Steer(target, "arrived after last poll")
	if err != nil {
		t.Fatal(err)
	}
	close(finish)
	assertSteerDelivery(t, undelivered, false)
	waitIdle(t, a)
	if _, err := a.Steer(target, "stale"); err == nil {
		t.Fatal("closed run accepted steer")
	}
	if _, err := j.TakeSteering(t.Context(), agent.QueueModeAll); err == nil {
		t.Fatal("escaped worker poll remained valid")
	}
}

func TestAbortCancelsOneRunAndRetainsConfirmedEffectsOnLateSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "abort.jsonl")
	entered := make(chan Job, 3)
	stopA := make(chan struct{})
	releaseA := sync.OnceFunc(func() { close(stopA) })
	stopB := make(chan struct{})
	releaseB := sync.OnceFunc(func() { close(stopB) })
	cancelled := make(chan struct{})
	work := func(ctx context.Context, j Job) (Outcome, error) {
		entered <- j
		if j.Trigger.ID == "a" {
			<-ctx.Done()
			close(cancelled)
			<-stopA
			return Outcome{Summary: "late success", Snapshot: agent.AgentSnapshot{SessionID: j.TaskID}, Receipts: []ToolReceipt{{Token: 1, Completed: true, Committed: true}}}, nil
		}
		select {
		case <-stopB:
			return Outcome{Summary: "other task finished"}, nil
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
	}
	route := func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }
	a, err := Open(t.Context(), path, 2, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer releaseB()
	defer releaseA()
	submit(t, a, Trigger{"a", "chat", "alpha", "request", "first"})
	ja := receive(t, entered)
	submit(t, a, Trigger{"b", "chat", "beta", "request", "second"})
	jb := receive(t, entered)
	target := RunTarget{TaskID: ja.TaskID, Source: "chat", EventID: "a"}
	receipt, err := a.Steer(target, "unread")
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{"next", "chat", "alpha", "request", "must not replay"})
	if err := a.Abort(target); err != nil {
		t.Fatal(err)
	}
	receive(t, cancelled)
	assertSteerDelivery(t, receipt, false)
	if err := a.Abort(target); err != nil {
		t.Fatal("repeated abort while callback still retiring:", err)
	}
	if _, err := a.Steer(target, "too late"); err == nil {
		t.Fatal("aborted run accepted steering")
	}
	if err := a.Reconcile(ja.TaskID, "chat", "a", "premature"); err == nil {
		t.Fatal("active callback was reconciled")
	}
	status := a.Status().Tasks
	if len(status) != 2 || status[0].Status != "abort_requested" || status[0].ActiveSource != "chat" || status[0].ActiveEventID != "a" || status[1].Status != "running" || status[1].ID != jb.TaskID {
		t.Fatal(status)
	}
	releaseB()
	releaseA()
	waitIdle(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.Context(), path, 2, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	view := b.Status()
	if len(view.Tasks) != 2 || view.Tasks[0].Status != "needs_review" || view.Tasks[1].Status != "completed" {
		t.Fatal(view.Tasks)
	}
	reports := map[string]Report{}
	for _, r := range view.Reports {
		reports[r.EventID] = r
	}
	r := reports["a"]
	if r.Status != "interrupted" || !r.NeedsReview || len(r.Receipts) != 1 || !r.Receipts[0].Committed || reports["b"].Status != "completed" || reports["next"].Status != "blocked" {
		t.Fatal(reports)
	}
}

func TestCloseReleasesUnpolledSteeringBeforeWorkerExits(t *testing.T) {
	entered := make(chan Job, 1)
	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "close.jsonl"), 1,
		func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil },
		func(ctx context.Context, j Job) (Outcome, error) { entered <- j; <-hold; return Outcome{}, ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	defer release()
	submit(t, a, Trigger{"one", "s", "t", "request", "start"})
	j := receive(t, entered)
	r, err := a.Steer(RunTarget{TaskID: j.TaskID, Source: "s", EventID: "one"}, "please stop")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- a.Close() }()
	assertSteerDelivery(t, r, false)
	select {
	case <-closed:
		t.Fatal("Close returned before actual worker exit")
	default:
	}
	release()
	if err := receive(t, closed); err != nil {
		t.Fatal(err)
	}
}

func TestControlsCannotCrossSuccessiveInvocationsOfOneTask(t *testing.T) {
	entered := make(chan Job, 1)
	finish := make(chan struct{}, 2)
	work := func(ctx context.Context, j Job) (Outcome, error) {
		entered <- j
		select {
		case <-finish:
			return Outcome{Summary: "finished"}, nil
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
	}
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "consecutive.jsonl"), 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{"old", "s", "thread", "request", "one"})
	old := receive(t, entered)
	submit(t, a, Trigger{"new", "s", "thread", "signal", "two"})
	finish <- struct{}{}
	next := receive(t, entered)
	if next.TaskID != old.TaskID {
		t.Fatal("not the same task")
	}
	oldTarget := RunTarget{TaskID: old.TaskID, Source: "s", EventID: "old"}
	newTarget := RunTarget{TaskID: next.TaskID, Source: "s", EventID: "new"}
	if err := a.Abort(oldTarget); err == nil {
		t.Fatal("late command canceled successor")
	}
	if _, err := a.Steer(oldTarget, "late"); err == nil {
		t.Fatal("late steer accepted")
	}
	r, err := a.Steer(newTarget, "current")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.TakeSteering(t.Context(), agent.QueueModeAll); err == nil {
		t.Fatal("old callback accessed successor steering")
	}
	watch, err := a.Subscribe(t.Context(), next.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	before := receive(t, watch)
	old.NotifyProgress("turn_start")
	next.NotifyProgress("completed") // a worker cannot forge host lifecycle progress.
	select {
	case p := <-watch:
		t.Fatalf("unexpected progress: %+v", p)
	default:
	}
	next.NotifyProgress("tool_start")
	after := receive(t, watch)
	if after.Kind != "tool_start" || after.EventID != "new" || after.Sequence != before.Sequence+1 {
		t.Fatal(after)
	}
	msgs, err := next.TakeSteering(t.Context(), "")
	if err != nil || len(msgs) != 1 || messageText(msgs[0]) != "current" {
		t.Fatal(msgs, err)
	}
	assertSteerDelivery(t, r, true)
	finish <- struct{}{}
	waitIdle(t, a)
	if r := a.ReportsSince(1); len(r) != 1 || r[0].Status != "completed" {
		t.Fatal(r)
	}
}

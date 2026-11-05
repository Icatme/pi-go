package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not make expected progress")
		var zero T
		return zero
	}
}

func waitIdle(t *testing.T, a *SuperAgent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
}

func submit(t *testing.T, a *SuperAgent, e Trigger) {
	t.Helper()
	if ok, err := a.Submit(e); !ok || err != nil {
		t.Fatalf("submit %+v: accepted=%v err=%v", e, ok, err)
	}
}

func TestDispatchDeduplicatesAndSerializesTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.jsonl")
	var routeCalls atomic.Int32
	routedThird := make(chan struct{})
	route := func(_ context.Context, e Trigger, tasks []TaskInfo) (Route, error) {
		routeCalls.Add(1)
		switch e.Source {
		case "third":
			close(routedThird)
		case "alias":
			for _, candidate := range tasks {
				if candidate.Summary == "Investigate outage" {
					return Route{Action: "attach", TaskID: candidate.ID}, nil
				}
			}
			return Route{}, errors.New("missing initial task summary")
		}
		return Route{Action: "new"}, nil
	}
	type invocation struct {
		job  Job
		done chan struct{}
	}
	started := make(chan invocation, 10)
	work := func(ctx context.Context, job Job) (Outcome, error) {
		ch := make(chan struct{})
		started <- invocation{job, ch}
		select {
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		case <-ch:
		}
		return Outcome{Summary: "handled " + job.Trigger.Text, Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Metadata: map[string]any{"previous": job.Trigger.Text, "large": json.Number("9007199254740993")}}}, nil
	}
	a, err := Open(t.Context(), path, 2, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	first := Trigger{"1", "chat", "same", "request", "Investigate outage"}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for range 16 {
		wg.Go(func() {
			ok, err := a.Submit(first)
			if err != nil {
				t.Error(err)
			}
			if ok {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	x := receive(t, started)
	if accepted.Load() != 1 || x.job.Trigger != first {
		t.Fatal("duplicate or wrong first call")
	}
	second := Trigger{"1", "email", "same", "request", "Separate work"}
	submit(t, a, second)
	y := receive(t, started)
	if y.job.Trigger != second || y.job.TaskID == x.job.TaskID {
		t.Fatal("sources were conflated")
	}
	follow := Trigger{"2", "chat", "same", "signal", "Follow up"}
	alias := Trigger{"1", "alias", "other", "request", "Same outage"}
	third := Trigger{"1", "third", "other", "request", "Third work"}
	submit(t, a, follow)
	submit(t, a, alias)
	submit(t, a, third)
	receive(t, routedThird)
	close(y.done)
	z := receive(t, started)
	if z.job.Trigger != third {
		t.Fatalf("expected independent work, got %+v", z.job)
	}
	close(z.done)
	close(x.done)
	for _, pair := range []struct {
		event    Trigger
		previous string
	}{{follow, first.Text}, {alias, follow.Text}} {
		v := receive(t, started)
		if v.job.Trigger != pair.event || v.job.TaskID != x.job.TaskID || v.job.Snapshot.Metadata["previous"] != pair.previous || v.job.Snapshot.Metadata["large"] != json.Number("9007199254740993") {
			t.Fatalf("wrong task sequence or snapshot: %+v", v.job)
		}
		close(v.done)
	}
	waitIdle(t, a)
	v := a.Status()
	if len(v.Tasks) != 3 || len(v.Reports) != 5 || routeCalls.Load() != 4 {
		t.Fatalf("view=%+v router=%d", v, routeCalls.Load())
	}
}

func TestIgnoredAndFailedRoutesRemainObservableAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	var routed atomic.Int32
	route := func(_ context.Context, e Trigger, _ []TaskInfo) (Route, error) {
		routed.Add(1)
		switch e.ID {
		case "ignore":
			return Route{Action: "ignore"}, nil
		case "fail":
			return Route{}, errors.New("routing unavailable")
		default:
			return Route{Action: "attach", TaskID: "nonexistent"}, nil
		}
	}
	work := func(context.Context, Job) (Outcome, error) { t.Error("unexpected worker"); return Outcome{}, nil }
	a, err := Open(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	events := []Trigger{{"noise", "a", "b", "noise", ""}, {"ignore", "a", "b", "signal", ""}, {"fail", "a", "b", "request", ""}, {"invalid", "a", "b", "request", ""}}
	for _, e := range events {
		submit(t, a, e)
	}
	waitIdle(t, a)
	v := a.Status()
	if len(v.Tasks) != 0 || len(v.Reports) != 4 || routed.Load() != 3 {
		t.Fatal(v)
	}
	statuses := map[string]string{}
	for _, r := range v.Reports {
		statuses[r.EventID] = r.Status
		if r.Text == "" {
			t.Fatal("empty report")
		}
	}
	if statuses["noise"] != "ignored" || statuses["ignore"] != "ignored" || statuses["fail"] != "failed" || statuses["invalid"] != "failed" {
		t.Fatal(statuses)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, e := range events {
		if ok, err := b.Submit(e); ok || err != nil {
			t.Fatalf("replayed %+v: %v", e, err)
		}
	}
	conflict := events[0]
	conflict.Text = "different"
	if ok, err := b.Submit(conflict); ok || err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
	if len(b.Status().Reports) != 4 {
		t.Fatal("terminal reports lost")
	}
}

func TestSubscribedThreadBypassesSlowRouter(t *testing.T) {
	coldEntered := make(chan struct{})
	release := make(chan struct{})
	delivered := make(chan Trigger, 4)
	var routing atomic.Int32
	route := func(ctx context.Context, tr Trigger, _ []TaskInfo) (Route, error) {
		if routing.Add(1) != 1 {
			t.Error("multiple routers executing concurrently")
		}
		defer routing.Add(-1)
		if tr.Thread == "slow" {
			close(coldEntered)
			select {
			case <-release:
			case <-ctx.Done():
				return Route{}, ctx.Err()
			}
		}
		return Route{Action: "new"}, nil
	}
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "routing.jsonl"), 2, route, func(_ context.Context, j Job) (Outcome, error) {
		delivered <- j.Trigger
		return Outcome{Summary: "done"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{"1", "s", "known", "request", "first"})
	receive(t, delivered)
	waitIdle(t, a)
	submit(t, a, Trigger{"2", "s", "slow", "request", "cold"})
	receive(t, coldEntered)
	submit(t, a, Trigger{"3", "s", "other", "request", "also cold"})
	followup := Trigger{"4", "s", "known", "signal", "known follow-up"}
	submit(t, a, followup)
	if got := receive(t, delivered); got != followup {
		t.Fatalf("unexpected delivery while router blocked: %+v", got)
	}
	close(release)
	waitIdle(t, a)
}

func TestCloseInterruptsActiveWorkAndBlocksQueuedFollowup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	first := Trigger{"1", "chat", "work", "request", "initial"}
	second := Trigger{"2", "chat", "work", "signal", "follow-up"}
	started := make(chan Job, 1)
	route := func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }
	a, err := Open(t.Context(), path, 1, route, func(ctx context.Context, job Job) (Outcome, error) {
		started <- job
		<-ctx.Done()
		return Outcome{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, first)
	j := receive(t, started)
	submit(t, a, second)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) {
		t.Error("lost thread subscription")
		return Route{}, errors.New("unexpected router")
	}, func(_ context.Context, job Job) (Outcome, error) {
		started <- job
		return Outcome{Summary: "finished"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	// Deliberately acknowledge immediately, before the scheduler is guaranteed
	// to have classified the second delivery. Its admission predates this note.
	if err := b.Reconcile(j.TaskID, first.Source, first.ID, "Checked external effects."); err != nil {
		t.Fatal(err)
	}
	fresh := Trigger{"3", "chat", "work", "request", "fresh instruction"}
	submit(t, b, fresh)
	next := receive(t, started)
	if next.TaskID != j.TaskID || next.Trigger != fresh {
		t.Fatal(next)
	}
	waitIdle(t, b)
	reports := b.Status().Reports
	if len(reports) != 3 || reports[0].Status != "interrupted" || !reports[0].NeedsReview || reports[1].Status != "blocked" || reports[2].Status != "completed" {
		t.Fatal(reports)
	}
}

func TestFailedAdmissionCannotDispatch(t *testing.T) {
	route := func(context.Context, Trigger, []TaskInfo) (Route, error) {
		t.Error("router called")
		return Route{}, nil
	}
	work := func(context.Context, Job) (Outcome, error) { t.Error("worker called"); return Outcome{}, nil }
	a, err := Open(t.Context(), filepath.Join(t.TempDir(), "broken.jsonl"), 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if accepted, err := a.Submit(Trigger{"1", "s", "t", "request", "x"}); accepted || err == nil {
		t.Fatal("failed append reported as accepted")
	}
	if err := a.WaitIdle(t.Context()); err == nil {
		t.Fatal("lost journal failure")
	}
	if err := a.Close(); err == nil {
		t.Fatal("close lost journal failure")
	}
}

func TestDurableReportKeepsWorkerReplySeparateFromRouterSummary(t *testing.T) {
	reply := strings.Repeat("可读报告\n", 400)
	large := strings.Repeat("阅", maxReportBytes)
	route := func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }
	work := func(_ context.Context, j Job) (Outcome, error) {
		if j.Trigger.ID == "2" {
			return Outcome{Summary: large}, nil
		}
		return Outcome{Summary: reply}, nil
	}
	path := filepath.Join(t.TempDir(), "reports.jsonl")
	a, err := Open(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{"1", "s", "t", "request", "one"})
	submit(t, a, Trigger{"2", "s", "t", "request", "two"})
	waitIdle(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	v := b.Status()
	if len(v.Reports) != 2 || v.Reports[0].Text != reply {
		t.Fatal("ordinary reply did not round-trip")
	}
	if n := len(v.Reports[1].Text); n > maxReportBytes || n < maxReportBytes-3 || !strings.HasSuffix(v.Reports[1].Text, "See task snapshot: "+v.Reports[1].TaskID+"]") {
		t.Fatal("oversized report lost its explicit truncation and snapshot reference")
	}
	if len(v.Tasks) != 1 || len(v.Tasks[0].Summary) > 1024 {
		t.Fatal("report leaked into router summary")
	}
}

func TestUndecodableWorkerSnapshotCannotPoisonJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots.jsonl")
	route := func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }
	work := func(_ context.Context, j Job) (Outcome, error) {
		switch j.Trigger.ID {
		case "first":
			return Outcome{Summary: "ready", Snapshot: agent.AgentSnapshot{Metadata: map[string]any{"huge": json.Number("1e500")}}}, nil
		case "bad":
			// RawMessage marshals successfully, but Agent rejects an unpaired
			// surrogate in tool arguments when reconstructing this snapshot.
			bad := agent.ToolCall{Arguments: json.RawMessage(`{"value":"\ud800"}`)}
			return Outcome{Summary: "bad snapshot", Snapshot: agent.AgentSnapshot{Messages: []agent.Message{{ToolCalls: []agent.ToolCall{bad}}}}}, nil
		default:
			if j.Snapshot.Metadata["huge"] != json.Number("1e500") {
				return Outcome{}, errors.New("lost last valid snapshot")
			}
			return Outcome{Summary: "continued"}, nil
		}
	}
	a, err := Open(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{"first", "s", "t", "request", "first"})
	submit(t, a, Trigger{"bad", "s", "t", "request", "bad"})
	waitIdle(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Reconcile("task-1", "s", "bad", "Investigated invalid snapshot."); err != nil {
		t.Fatal(err)
	}
	submit(t, b, Trigger{"last", "s", "t", "request", "last"})
	waitIdle(t, b)
	r := b.Status().Reports
	if len(r) != 3 || r[0].Status != "completed" || r[1].Status != "failed" || r[2].Status != "completed" {
		t.Fatalf("reports=%+v", r)
	}
}

func TestReviewFenceCoversLateAttachAndStaleConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.jsonl")
	ctx := t.Context()
	routing := make(chan struct{})
	allowRoute := make(chan struct{})
	working := make(chan struct{})
	endWork := make(chan struct{})
	secret := errors.New("sensitive provider detail")
	route := func(ctx context.Context, e Trigger, _ []TaskInfo) (Route, error) {
		if e.Thread == "alias" {
			close(routing)
			select {
			case <-allowRoute:
			case <-ctx.Done():
				return Route{}, ctx.Err()
			}
			return Route{Action: "attach", TaskID: "task-1"}, nil
		}
		return Route{Action: "new"}, nil
	}
	var executions atomic.Int32
	work := func(ctx context.Context, j Job) (Outcome, error) {
		executions.Add(1)
		if j.Trigger.ID == "one" {
			close(working)
			select {
			case <-endWork:
			case <-ctx.Done():
				return Outcome{}, ctx.Err()
			}
		}
		return Outcome{Snapshot: agent.AgentSnapshot{SessionID: j.TaskID, Metadata: map[string]any{"failed": j.Trigger.ID}},
			Receipts: []ToolReceipt{{Token: 1, Domain: "agent", CallID: "write-1", ToolName: "write", Committed: true, Completed: true}}}, secret
	}
	a, err := Open(ctx, path, 2, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{"one", "chat", "main", "request", "write"})
	receive(t, working)
	if err := a.Reconcile("task-1", "chat", "one", "premature"); err == nil {
		t.Fatal("released a running worker")
	}
	submit(t, a, Trigger{"old", "other", "alias", "request", "attach"})
	receive(t, routing)
	close(endWork)
	for {
		a.mu.Lock()
		ready := a.tasks["task-1"].review != 0
		ch := a.changed
		a.mu.Unlock()
		if ready {
			break
		}
		receive(t, ch)
	}
	if err := a.Reconcile("task-1", "other", "one", "wrong source"); err == nil {
		t.Fatal("wrong source released task")
	}
	if err := a.Reconcile("task-1", "chat", "one", "verified initial operation"); err != nil {
		t.Fatal(err)
	}
	close(allowRoute)
	waitIdle(t, a)
	if executions.Load() != 1 {
		t.Fatal("old in-flight route escaped reconciliation admission cutoff")
	}
	r := a.ReportsSince(0)
	if len(r) != 2 || r[0].Status != "failed" || !r[0].NeedsReview || len(r[0].Receipts) != 1 || !r[0].Receipts[0].Committed || strings.Contains(r[0].Text, "sensitive") || r[1].Status != "blocked" {
		t.Fatalf("reports=%+v", r)
	}
	if cause := a.FailureCause("chat", "one"); !errors.Is(cause, secret) {
		t.Fatalf("cause=%v", cause)
	}
	submit(t, a, Trigger{"two", "other", "alias", "request", "fresh command"})
	waitIdle(t, a)
	if err := a.Reconcile("task-1", "chat", "one", "stale approval"); err == nil {
		t.Fatal("previous confirmation released later review")
	}
	if err := ReconcileFile(path, "task-1", "other", "two", "conflicting writer"); err == nil {
		t.Fatal("offline command opened active journal")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(ctx, path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	status := b.Status().Tasks[0]
	if status.Status != "needs_review" || status.ReviewSource != "other" || status.ReviewEventID != "two" {
		t.Fatal(status)
	}
	if got, ok, err := b.TaskSnapshot("task-1"); err != nil || !ok || got.Metadata["failed"] != "two" {
		t.Fatalf("failed snapshot=%+v ok=%v err=%v", got, ok, err)
	}
	submit(t, b, Trigger{"blocked", "chat", "main", "request", "still held"})
	waitIdle(t, b)
	if executions.Load() != 2 || b.ReportsSince(3)[0].Status != "blocked" {
		t.Fatal("frozen task ran after restart")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileFile(path, "task-1", "other", "two", "checked second operation"); err != nil {
		t.Fatal(err)
	}
	c, err := Open(ctx, path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if st := c.Status().Tasks[0]; st.Status != "ready" || st.ReviewEventID != "" {
		t.Fatal(st)
	}
	if got := c.ReportsSince(2)[0]; got.Status != "failed" || !got.NeedsReview || len(got.Receipts) != 1 || !got.Receipts[0].Committed {
		t.Fatalf("lost historical proof: %+v", got)
	}
	if c.FailureCause("chat", "one") != nil {
		t.Fatal("private cause survived restart")
	}
}

func TestCompletionStatusesAndRejectedForeignSnapshotPreserveReceipts(t *testing.T) {
	receipt := ToolReceipt{Token: 1, Completed: true, Committed: true, ToolName: "store"}
	jobs := []Outcome{
		{Summary: "partial", Status: "truncated", Snapshot: agent.AgentSnapshot{SessionID: "task-1"}, Receipts: []ToolReceipt{receipt}},
		{Summary: "completed with recoverable issue", Status: "completed_with_tool_errors", Snapshot: agent.AgentSnapshot{SessionID: "task-1"}, Receipts: []ToolReceipt{receipt}, NeedsReview: true},
		{Summary: "must not replace snapshot", Snapshot: agent.AgentSnapshot{SessionID: "another-task"}, Receipts: []ToolReceipt{receipt}},
	}
	run := 0
	path := filepath.Join(t.TempDir(), "facts.jsonl")
	a, err := Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }, func(context.Context, Job) (Outcome, error) { out := jobs[run]; run++; return out, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{"a", "s", "t", "request", "one"})
	submit(t, a, Trigger{"b", "s", "t", "request", "two"})
	waitIdle(t, a)
	if err := a.Reconcile("task-1", "s", "b", "checked"); err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{"c", "s", "t", "request", "three"})
	waitIdle(t, a)
	reports := a.Status().Reports
	if len(reports) != 3 {
		t.Fatal(reports)
	}
	for i, want := range []string{"truncated", "completed_with_tool_errors", "failed"} {
		if reports[i].Status != want || reports[i].NeedsReview != (i > 0) || len(reports[i].Receipts) != 1 || reports[i].Receipts[0] != receipt {
			t.Fatalf("report %d=%+v", i, reports[i])
		}
	}
	if got, ok, err := a.TaskSnapshot("task-1"); err != nil || !ok || got.SessionID != "task-1" {
		t.Fatal("cross-task snapshot accepted")
	}
}

func TestOfflineReconcileRejectsForeignJournalBeforeRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.jsonl")
	if err := ReconcileFile(path, "task-1", "s", "e", "checked"); err == nil {
		t.Fatal("opened nonexistent journal")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	data := []byte("{\"kind\":\"session\",\"version\":2,\"id\":\"other\",\"created_at\":1}\n{\"kind\":\"entr")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileFile(path, "task-1", "s", "e", "checked"); err == nil {
		t.Fatal("opened foreign journal")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("foreign journal modified")
	}
}

// Before the review barrier, an interrupted turn did not prevent a later
// delivery from running. Replay those historical facts without dispatching
// them or releasing still-pending work.
func TestLegacyInterruptedHistoryPreservesLaterCompletedWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	store, err := session.CreateJSONLStorage(path, session.Header{ID: "superagent"})
	if err != nil {
		t.Fatal(err)
	}
	first := Trigger{"one", "s", "t", "request", "old interrupted"}
	second := Trigger{"two", "s", "t", "request", "already completed"}
	third := Trigger{"three", "s", "t", "request", "still queued"}
	lines := []transition{
		{State: "received", Index: 0, Trigger: &first}, {State: "routing", Index: 0}, {State: "queued", Index: 0, Task: "task-1"}, {State: "running", Index: 0}, {State: "interrupted", Index: 0},
		{State: "received", Index: 1, Trigger: &second}, {State: "queued", Index: 1, Task: "task-1"}, {State: "running", Index: 1}, {State: "completed", Index: 1, Text: "historical answer", Snapshot: json.RawMessage(`{"session_id":"task-1"}`)},
		{State: "received", Index: 2, Trigger: &third}, {State: "queued", Index: 2, Task: "task-1"},
	}
	for i, line := range lines {
		data, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.AppendEntry(session.MainLane, session.NewEntry{Type: session.EntryTypeCustom, ID: strconv.Itoa(i + 1), Custom: &session.CustomData{CustomType: journalKind, Payload: data}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) {
		t.Error("unexpected reroute")
		return Route{}, nil
	}, func(context.Context, Job) (Outcome, error) {
		t.Error("replayed historical or pending work")
		return Outcome{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	waitIdle(t, a)
	v := a.Status()
	if len(v.Tasks) != 1 || v.Tasks[0].Status != "needs_review" || v.Tasks[0].ReviewEventID != "one" || len(v.Reports) != 3 || v.Reports[1].Text != "historical answer" || v.Reports[2].Status != "blocked" {
		t.Fatalf("legacy state: %+v", v)
	}
}

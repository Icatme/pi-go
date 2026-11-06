package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

// Stop the real coordinator after creating tasks, then reopen only its journal.
// This models admission/metadata commits before any delivery can be claimed.
func threadStoppedFixture(t *testing.T) (*SuperAgent, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "thread.jsonl")
	a, err := OpenMain(t.Context(), path, 3, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "C"} {
		submit(t, a, Trigger{ID: name, Source: "im", Thread: "planning", Kind: "request", Text: name})
		waitIdle(t, a)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := openState(t.Context(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b, path
}

func TestThreadFrozenRecipientsAcrossChurnAndRestart(t *testing.T) {
	a, path := threadStoppedFixture(t)
	for _, id := range []string{"task-1", "task-2"} {
		if err := a.SetThreadSubscription(id, "github", "pr-27", true); err != nil {
			t.Fatal(err)
		}
	}
	before := len(a.store.Log())
	event := Trigger{ID: "before-churn", Source: "github", Thread: "pr-27", Kind: "signal", Text: "review"}
	if ok, err := a.SubmitThread(event); !ok || err != nil {
		t.Fatalf("admission: %v %v", ok, err)
	}
	if len(a.store.Log()) != before+1 {
		t.Fatal("fanout was not one journal admission")
	}
	var tr transition
	log := a.store.Log()
	if err := json.Unmarshal(log[len(log)-1].Entry.Custom.Payload, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.State != "thread_received" || !reflect.DeepEqual(tr.Thread.Targets, []string{"task-1", "task-2"}) {
		t.Fatalf("frozen record: %+v", tr)
	}
	for _, change := range []ThreadSubscription{{"task-2", "github", "pr-27", false}, {"task-3", "github", "pr-27", true}} {
		if err := a.SetThreadSubscription(change.TaskID, change.Source, change.Thread, change.Subscribed); err != nil {
			t.Fatal(err)
		}
		count := len(a.store.Log())
		if err := a.SetThreadSubscription(change.TaskID, change.Source, change.Thread, change.Subscribed); err != nil || len(a.store.Log()) != count {
			t.Fatal("subscription retry not a no-op", err)
		}
	}
	if ok, err := a.SubmitThread(event); ok || err != nil {
		t.Fatalf("duplicate after churn: %v %v", ok, err)
	}
	if ok, err := a.Submit(event); ok || err == nil {
		t.Fatal("fanout duplicate changed entry mode")
	}
	if ok, err := a.SubmitTask(event, "task-1"); ok || err == nil {
		t.Fatal("fanout duplicate changed to task entry")
	}
	next := event
	next.ID = "after-churn"
	if ok, err := a.SubmitThread(next); !ok || err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []RunTarget
	work := func(ctx context.Context, job Job) (Outcome, error) {
		mu.Lock()
		calls = append(calls, RunTarget{job.TaskID, job.Trigger.Source, job.Trigger.ID})
		mu.Unlock()
		return mainWorker(ctx, job)
	}
	b, err := OpenMain(t.Context(), path, 3, mainFixture, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, event := range []Trigger{event, next} {
		if ok, err := b.SubmitThread(event); ok || err != nil {
			t.Fatal("replay duplicate", err)
		}
	}
	waitIdle(t, b)
	want := []RunTarget{{"task-1", "github", "before-churn"}, {"task-2", "github", "before-churn"}, {"task-1", "github", "after-churn"}, {"task-3", "github", "after-churn"}}
	mu.Lock()
	defer mu.Unlock()
	less := func(v []RunTarget) func(int, int) bool {
		return func(i, j int) bool { return v[i].EventID+v[i].TaskID < v[j].EventID+v[j].TaskID }
	}
	sort.Slice(calls, less(calls))
	sort.Slice(want, less(want))
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("deliveries: %+v want %+v", calls, want)
	}
	if !reflect.DeepEqual(b.ThreadSubscriptions("github", "pr-27"), []string{"task-1", "task-3"}) {
		t.Fatal("subscription churn lost")
	}
	if len(b.MainFailures()) != 0 || len(b.ReportsSince(3)) != 4 {
		t.Fatal("fanout observations or report cursor lost")
	}
}

func TestThreadEmptyAdmissionNeverAcquiresLaterSubscribers(t *testing.T) {
	a, path := threadStoppedFixture(t)
	event := Trigger{ID: "empty", Source: "github", Thread: "pr", Kind: "signal", Text: "orphan review"}
	if ok, err := a.SubmitThread(event); !ok || err != nil {
		t.Fatal(err)
	}
	if err := a.SetThreadSubscription("task-1", "github", "pr", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.SubmitThread(event); ok || err != nil {
		t.Fatal("empty duplicate recomputed targets", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	seen := make(chan MainInput, 1)
	b, err := OpenMain(t.Context(), path, 1, func(_ context.Context, j MainJob) (MainOutcome, error) {
		seen <- j.Input
		return MainOutcome{Decision: MainDecision{Action: "ignore"}, Snapshot: j.Snapshot}, nil
	}, func(context.Context, Job) (Outcome, error) {
		t.Error("late subscription took empty admission")
		return Outcome{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	input := receive(t, seen)
	if input.Kind != "user" || input.TaskID != "" || input.Trigger != event {
		t.Fatalf("empty input: %+v", input)
	}
	if ok, err := b.SubmitThread(event); ok || err != nil {
		t.Fatal("empty replay duplicate", err)
	}
}

func TestThreadSecondRecipientDurableControlAndReceipts(t *testing.T) {
	for _, action := range []string{"steer", "cancel"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), "controls.jsonl")
			entered := make(chan Job, 2)
			poll := make(chan struct{})
			accepted := make(chan []agent.Message, 1)
			finish := make(chan struct{})
			route := func(ctx context.Context, j MainJob) (MainOutcome, error) {
				if j.Input.Kind == "user" && j.Input.Trigger.ID == "control" {
					for _, target := range j.Runs {
						if target.TaskID == "task-2" {
							return MainOutcome{Decision: MainDecision{Action: action, Target: &target, Text: map[string]string{"steer": "only B"}[action]}, Snapshot: j.Snapshot}, nil
						}
					}
					t.Error("second target not offered")
				}
				return mainFixture(ctx, j)
			}
			work := func(ctx context.Context, j Job) (Outcome, error) {
				out, _ := mainWorker(ctx, j)
				if j.Trigger.Source != "github" || j.Trigger.ID != "review" {
					return out, nil
				}
				entered <- j
				out.Receipts = []ToolReceipt{{Token: 1, Domain: "host", CallID: j.TaskID, ToolName: "save", Committed: true, Completed: j.TaskID == "task-1"}}
				if j.TaskID == "task-1" {
					return out, nil
				}
				if action == "cancel" {
					<-ctx.Done()
					return out, ctx.Err()
				}
				select {
				case <-poll:
				case <-ctx.Done():
					return out, ctx.Err()
				}
				messages, err := j.TakeSteering(ctx, agent.QueueModeAll)
				if err != nil {
					return out, err
				}
				accepted <- messages
				out.Snapshot.Messages = messages
				select {
				case <-finish:
				case <-ctx.Done():
					return out, ctx.Err()
				}
				out.Receipts[0].Completed = true
				return out, nil
			}
			a, err := OpenMain(ctx, path, 2, route, work)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			for _, id := range []string{"A", "B"} {
				submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
				waitIdle(t, a)
			}
			for _, id := range []string{"task-1", "task-2"} {
				if err := a.SetThreadSubscription(id, "github", "pr", true); err != nil {
					t.Fatal(err)
				}
			}
			event := Trigger{ID: "review", Source: "github", Thread: "pr", Kind: "signal"}
			if _, err := a.SubmitThread(event); err != nil {
				t.Fatal(err)
			}
			receive(t, entered)
			receive(t, entered)
			submit(t, a, Trigger{ID: "control", Source: "im", Thread: "chat", Kind: "request", Text: action + " second task"})
			if action == "steer" {
				waitMainControl(t, a, "queued")
				close(poll)
				if messages := receive(t, accepted); len(messages) != 1 || messageText(messages[0]) != "only B" {
					t.Fatalf("wrong steering: %+v", messages)
				}
				close(finish)
			}
			if err := a.WaitIdle(ctx); err != nil {
				t.Fatal(err)
			}
			controls := a.MainControls()
			if len(controls) != 1 || controls[0].Target != (RunTarget{"task-2", "github", "review"}) {
				t.Fatalf("control: %+v", controls)
			}
			if action == "steer" && (!controls[0].InputAccepted || controls[0].Status != "delivered") {
				t.Fatalf("missing second-target evidence: %+v", controls)
			}
			if action == "cancel" && controls[0].Status != "cancel_requested" {
				t.Fatalf("cancel: %+v", controls)
			}
			reports := a.ReportsSince(2)
			byTask := map[string]Report{}
			for _, r := range reports {
				if r.Source == event.Source && r.EventID == event.ID {
					byTask[r.TaskID] = r
				}
			}
			if len(byTask) != 2 || byTask["task-1"].Status != "completed" || !byTask["task-1"].Receipts[0].Completed {
				t.Fatalf("A changed: %+v", reports)
			}
			if action == "cancel" {
				b := byTask["task-2"]
				if b.Status != "interrupted" || !b.NeedsReview || len(b.Receipts) != 1 || !b.Receipts[0].Committed || b.Receipts[0].Completed {
					t.Fatalf("B effects lost: %+v", b)
				}
				target := RunTarget{"task-2", "github", "review"}
				if !errors.Is(a.DeliveryFailureCause(target), context.Canceled) || !errors.Is(a.FailureCause("github", "review"), context.Canceled) || a.DeliveryFailureCause(RunTarget{"task-1", "github", "review"}) != nil {
					t.Fatal("diagnostics confused fanout targets")
				}
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMain(ctx, path, 2, mainFixture, mainWorker)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			waitIdle(t, b)
			if !reflect.DeepEqual(b.MainControls(), controls) || !reflect.DeepEqual(b.ReportsSince(2), reports) {
				t.Fatal("restart altered second-target control or receipts")
			}
			if action == "cancel" {
				if err := b.Reconcile("task-1", "github", "review", "wrong target"); err == nil {
					t.Fatal("A gained B barrier")
				}
				if err := b.Reconcile("task-2", "github", "review", "verified partial save"); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(b.ReportsSince(2), reports) {
					t.Fatal("reconciliation rewrote historical effects")
				}
				next := event
				next.ID = "next"
				if _, err := b.SubmitThread(next); err != nil {
					t.Fatal(err)
				}
				waitIdle(t, b)
				if len(b.ReportsSince(2+len(reports))) != 2 {
					t.Fatal("reconciled tasks did not independently resume")
				}
			}
		})
	}
}

// Replay each durable boundary, including after a handback but before that
// worker's terminal receipt. Running work is never retried after a crash.
func TestThreadReplayEveryFanoutCommitBoundary(t *testing.T) {
	a, path := threadStoppedFixture(t)
	for _, id := range []string{"task-1", "task-2"} {
		if err := a.SetThreadSubscription(id, "github", "pr", true); err != nil {
			t.Fatal(err)
		}
	}
	event := Trigger{ID: "review", Source: "github", Thread: "pr", Kind: "signal", Text: "review event"}
	if _, err := a.SubmitThread(event); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	route := func(ctx context.Context, j MainJob) (MainOutcome, error) {
		if j.Input.Kind == "handback" {
			return MainOutcome{Decision: MainDecision{Action: "clarify", Text: "Which remaining scope?"}, Snapshot: j.Snapshot}, nil
		}
		return mainFixture(ctx, j)
	}
	b, err := OpenMain(t.Context(), path, 2, route, func(ctx context.Context, j Job) (Outcome, error) {
		if j.TaskID == "task-2" {
			if err := j.HandBack(ctx, "Need owner", "Saved summary"); err != nil {
				return Outcome{}, err
			}
		}
		return mainWorker(ctx, j)
	})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, b)
	identityByTask := map[string]string{}
	for _, report := range b.ReportsSince(3) {
		if report.EventID == event.ID {
			identityByTask[report.TaskID] = report.DeliveryID
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var prefix []byte
	admitted := false
	for lineNumber, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		prefix = append(prefix, line...)
		if lineNumber == 0 {
			continue
		}
		var entry session.LogItem
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		var tr transition
		if err := json.Unmarshal(entry.Entry.Custom.Payload, &tr); err != nil {
			t.Fatal(err)
		}
		if tr.State == "thread_received" {
			admitted = true
		}
		if !admitted {
			continue
		}
		t.Run(fmt.Sprintf("%d-%s", lineNumber, tr.State), func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "cut.jsonl")
			if err := os.WriteFile(file, prefix, 0600); err != nil {
				t.Fatal(err)
			}
			state, err := openState(t.Context(), file, false)
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]int{}
			hadHandback := len(state.Handbacks())
			for _, e := range state.events {
				if e.trigger.ID == event.ID {
					if e.state == "received" || e.state == "queued" {
						expected[e.entryTask] = 1
					}
				}
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			calls := map[string]int{}
			c, err := OpenMain(t.Context(), file, 2, route, func(ctx context.Context, j Job) (Outcome, error) {
				mu.Lock()
				calls[j.TaskID]++
				mu.Unlock()
				return mainWorker(ctx, j)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if ok, err := c.SubmitThread(event); ok || err != nil {
				t.Fatal("duplicate at cut", err)
			}
			waitIdle(t, c)
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(calls, expected) {
				t.Fatalf("invocations: %+v want %+v", calls, expected)
			}
			reports := map[string]Report{}
			for _, r := range c.Status().Reports {
				if r.EventID == event.ID {
					if r.DeliveryID == "" || r.DeliveryID != identityByTask[r.TaskID] {
						t.Fatal("crash boundary changed delivery identity", r)
					}
					if _, exists := reports[r.TaskID]; exists {
						t.Fatal("duplicate report")
					}
					reports[r.TaskID] = r
				}
			}
			if len(reports) != 2 || reports["task-1"].TaskID == "" || reports["task-2"].TaskID == "" {
				t.Fatalf("frozen target reports: %+v", reports)
			}
			if len(c.Handbacks()) != hadHandback || len(c.MainFailures()) != 0 {
				t.Fatal("handback replay failed")
			}
			clarifications := 0
			for _, r := range c.Replies() {
				if r.Handback != nil {
					clarifications++
				}
			}
			if clarifications != hadHandback {
				t.Fatal("handback clarification lost or repeated")
			}
		})
	}
	// A torn fanout admission cannot expose just its first delivery. JSONL repair
	// drops the entire unfinished record and the host may submit the event again.
	lines := bytes.SplitAfter(data, []byte{'\n'})
	prefix = nil
	for _, line := range lines {
		if bytes.Contains(line, []byte(`"state":"thread_received"`)) {
			file := filepath.Join(t.TempDir(), "torn.jsonl")
			prefix = append(prefix, line[:len(line)/2]...)
			if err := os.WriteFile(file, prefix, 0600); err != nil {
				t.Fatal(err)
			}
			c, err := openState(t.Context(), file, false)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, exists := c.accepted[[2]string{event.Source, event.ID}]; exists {
				t.Fatal("partial admission became visible")
			}
			if ok, err := c.SubmitThread(event); !ok || err != nil {
				t.Fatal("torn admission could not be retried", err)
			}
			first := c.accepted[[2]string{event.Source, event.ID}]
			if len(c.events)-first != 2 {
				t.Fatal("repaired fanout was partial")
			}
			return
		}
		prefix = append(prefix, line...)
	}
	t.Fatal("fanout record not found")
}

func TestThreadLegacyPendingCannotRestoreExplicitUnsubscribe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	a, err := openState(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	first := Trigger{ID: "old", Source: "im", Thread: "chat", Kind: "request"}
	threadSafetyCommit(t, a, transition{State: "received", Trigger: &first})
	threadSafetyCommit(t, a, transition{State: "routing"})
	threadSafetyCommit(t, a, transition{State: "queued", Task: "task-1", ProviderSessionID: "original-provider"})
	threadSafetyCommit(t, a, transition{State: "running"})
	threadSafetyCommit(t, a, transition{State: "completed", Snapshot: json.RawMessage(`{"session_id":"task-1"}`)})
	pending := first
	pending.ID, pending.Kind = "legacy-pending", "signal"
	threadSafetyCommit(t, a, transition{State: "received", Index: 1, Trigger: &pending})
	threadSafetyCommit(t, a, transition{State: "main_opened", Main: &mainTransition{SessionID: "main-upgrade", ProviderSessionID: "main-provider"}})
	threadSafetyCommit(t, a, transition{State: "main_chat_opened", Main: &mainTransition{}})
	if got := a.ThreadSubscriptions("im", "chat"); !reflect.DeepEqual(got, []string{"task-1"}) {
		t.Fatalf("legacy binding was not imported: %v", got)
	}
	if err := a.SetThreadSubscription("task-1", "im", "chat", false); err != nil {
		t.Fatal(err)
	}
	// The admitted legacy input still routes to its original task. It must not
	// recreate the future subscription that the host has explicitly withdrawn.
	threadSafetyCommit(t, a, transition{State: "queued", Index: 1, Task: "task-1"})
	if len(a.ThreadSubscriptions("im", "chat")) != 0 {
		t.Fatal("legacy pending delivery resurrected an explicit unsubscribe")
	}
	next := pending
	next.ID = "after-unsubscribe"
	if ok, err := a.SubmitThread(next); !ok || err != nil {
		t.Fatalf("new thread admission: %v %v", ok, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := openState(t.Context(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if len(b.ThreadSubscriptions("im", "chat")) != 0 || b.events[1].task != "task-1" || b.tasks["task-1"].providerSessionID != "original-provider" {
		t.Fatal("replay changed the explicit relation or legacy task identity")
	}
	if len(b.events) != 3 || b.events[2].entryTask != "" || !b.events[2].mainInput || b.main.inbox[0].input.Kind != "user" {
		t.Fatal("future event was delivered to the unsubscribed legacy task")
	}
}

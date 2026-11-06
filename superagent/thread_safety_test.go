package superagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent/session"
)

// A real journal without dispatch goroutines makes callback lifetime and replay
// validation tests deterministic, including the interval before finalization.
func threadSafetyJournal(t *testing.T) (*SuperAgent, string, *activeRun) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "thread.jsonl")
	a, err := openState(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	threadSafetyCommit(t, a, transition{State: "main_opened", Main: &mainTransition{SessionID: "main-test", ProviderSessionID: "main-provider"}})
	threadSafetyCommit(t, a, transition{State: "main_chat_opened", Main: &mainTransition{}})
	trigger := Trigger{ID: "seed", Source: "github", Thread: "pr-27", Kind: "request", Text: "work"}
	threadSafetyCommit(t, a, transition{State: "received", Trigger: &trigger})
	input := a.main.inbox[0].input.ID
	threadSafetyCommit(t, a, transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Snapshot: json.RawMessage(`{"session_id":"main-test"}`), Decision: MainDecision{Action: "new"}, Assignment: &transition{State: "queued", Task: "task-1", ProviderSessionID: "worker-provider"}}})
	threadSafetyCommit(t, a, transition{State: "running"})
	ctx, cancel := context.WithCancel(a.ctx)
	t.Cleanup(cancel)
	r := &activeRun{target: RunTarget{TaskID: "task-1", Source: trigger.Source, EventID: trigger.ID}, ctx: ctx, cancel: cancel}
	a.tasks["task-1"].active = r
	return a, path, r
}

func threadSafetyCommit(t *testing.T, a *SuperAgent, tr transition) {
	t.Helper()
	if err := a.commit(tr); err != nil {
		t.Fatal(err)
	}
}

func TestThreadSafetyWorkerMutationBudgetAndScope(t *testing.T) {
	a, path, r := threadSafetyJournal(t)
	if err := a.SetThreadSubscription("task-1", "github", "unrelated", true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxWorkerSubscriptionChanges; i++ {
		want := i%2 == 0
		if err := a.workerSubscription(t.Context(), 0, r, want); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
		before := a.sequence
		if err := a.workerSubscription(t.Context(), 0, r, want); err != nil || a.sequence != before {
			t.Fatalf("idempotent change consumed journal/budget: %v", err)
		}
	}
	before := a.sequence
	if err := a.workerSubscription(t.Context(), 0, r, true); err == nil || a.sequence != before {
		t.Fatal("worker exceeded mutation budget")
	}
	if a.events[0].routeActions != maxWorkerSubscriptionChanges || len(a.ThreadSubscriptions("github", "unrelated")) != 1 {
		t.Fatal("scope or budget changed")
	}
	// Host authorization is independent of the exhausted worker capability.
	if err := a.SetThreadSubscription("task-1", "github", "pr-27", true); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := openState(t.Context(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.events[0].routeActions != maxWorkerSubscriptionChanges || !reflect.DeepEqual(b.ThreadSubscriptions("github", "pr-27"), []string{"task-1"}) {
		t.Fatal("replay lost subscription or budget")
	}
}

func TestThreadSafetyStaleWorkerCallbacks(t *testing.T) {
	for _, mode := range []string{"returned", "aborted", "cancelled", "replacement", "later-event", "terminal", "nil-context", "cancelled-context"} {
		t.Run(mode, func(t *testing.T) {
			a, _, r := threadSafetyJournal(t)
			ctx := t.Context()
			switch mode {
			case "returned":
				r.returned = true
			case "aborted":
				r.aborted = true
			case "cancelled":
				r.cancel()
			case "replacement":
				replacement := *r
				a.tasks["task-1"].active = &replacement
			case "later-event":
				replacement := *r
				replacement.target.EventID = "next"
				a.tasks["task-1"].active = &replacement
			case "terminal":
				a.events[0].state = "completed"
			case "nil-context":
				ctx = nil
			case "cancelled-context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := a.sequence
			if err := a.workerSubscription(ctx, 0, r, true); err == nil {
				t.Fatal("stale subscription accepted")
			}
			if err := a.workerHandback(ctx, 0, r, "clarify", "done"); err == nil {
				t.Fatal("stale handback accepted")
			}
			if a.sequence != before || len(a.Handbacks()) != 0 || len(a.ThreadSubscriptions("github", "pr-27")) != 0 {
				t.Fatal("rejected callback mutated state")
			}
		})
	}
}

func TestThreadSafetyHandbackDurabilityAndClarifyOnly(t *testing.T) {
	a, path, r := threadSafetyJournal(t)
	for _, note := range []string{"", " ", strings.Repeat("x", 4097), string([]byte{255})} {
		if err := a.workerHandback(t.Context(), 0, r, note, "handled"); err == nil {
			t.Fatal("invalid reason accepted")
		}
		if err := a.workerHandback(t.Context(), 0, r, "reason", note); err == nil {
			t.Fatal("invalid handled scope accepted")
		}
	}
	if err := a.workerHandback(t.Context(), 0, r, "Need scope", "Saved summary"); err != nil {
		t.Fatal(err)
	}
	before := a.sequence
	if err := a.workerHandback(t.Context(), 0, r, "Need scope", "Saved summary"); err != nil || a.sequence != before {
		t.Fatal("duplicate handback not idempotent")
	}
	if err := a.workerHandback(t.Context(), 0, r, "Changed scope", "Saved summary"); err == nil || a.sequence != before {
		t.Fatal("changed handback accepted")
	}
	handbacks := a.Handbacks()
	if len(handbacks) != 1 || handbacks[0].Trigger != a.events[0].trigger || handbacks[0].Target != r.target {
		t.Fatalf("handback identity: %+v", handbacks)
	}
	copy := a.Handbacks()
	copy[0].Reason = "mutated"
	if a.Handbacks()[0].Reason != "Need scope" {
		t.Fatal("handback read aliases state")
	}
	input := a.main.inbox[a.main.cursor].input.ID
	for _, action := range []string{"new", "attach", "steer", "cancel", "ignore", "reply"} {
		tr := transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Snapshot: json.RawMessage(`{"session_id":"main-test"}`), Decision: MainDecision{Action: action}}}
		if err := a.validateMain(tr); err == nil || !strings.Contains(err.Error(), "handback requires clarification") {
			t.Fatalf("handback action %s did not hit the read-only boundary: %v", action, err)
		}
	}
	threadSafetyCommit(t, a, transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Snapshot: json.RawMessage(`{"session_id":"main-test"}`), Decision: MainDecision{Action: "clarify", Text: "Which scope remains?"}, ReplyID: "reply-" + input}})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := openState(t.Context(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !reflect.DeepEqual(b.Handbacks(), handbacks) || len(b.Replies()) != 1 || b.Replies()[0].Thread != "pr-27" {
		t.Fatal("handback/clarification not durable")
	}
}

func TestThreadSafetyMalformedAdmissionAtomicAndReplayRejected(t *testing.T) {
	for _, mode := range []string{"unknown", "duplicate", "omitted", "wrong-index", "noise", "mixed-envelope", "invalid-trigger", "wrong-order"} {
		t.Run(mode, func(t *testing.T) {
			a, path, _ := threadSafetyJournal(t)
			if err := a.SetThreadSubscription("task-1", "github", "pr-27", true); err != nil {
				t.Fatal(err)
			}
			seed := Trigger{ID: "second-seed", Source: "chat", Thread: "planning", Kind: "request"}
			threadSafetyCommit(t, a, transition{State: "received", Index: 1, Trigger: &seed})
			input := a.main.inbox[a.main.cursor].input.ID
			threadSafetyCommit(t, a, transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Snapshot: json.RawMessage(`{"session_id":"main-test"}`), Decision: MainDecision{Action: "new"}, Assignment: &transition{State: "queued", Index: 1, Task: "task-2", ProviderSessionID: "second-provider"}}})
			if err := a.SetThreadSubscription("task-2", "github", "pr-27", true); err != nil {
				t.Fatal(err)
			}
			trigger := Trigger{ID: "update", Source: "github", Thread: "pr-27", Kind: "signal"}
			tr := transition{State: "thread_received", Index: 2, Trigger: &trigger, Thread: &threadTransition{Targets: []string{"task-1", "task-2"}}}
			switch mode {
			case "unknown":
				tr.Thread.Targets = []string{"missing"}
			case "duplicate":
				tr.Thread.Targets = []string{"task-1", "task-1"}
			case "omitted":
				tr.Thread.Targets = nil
			case "wrong-index":
				tr.Index = 0
			case "noise":
				trigger.Kind = "noise"
			case "mixed-envelope":
				tr.Text = "unexpected"
			case "invalid-trigger":
				trigger.ID = ""
			case "wrong-order":
				tr.Thread.Targets = []string{"task-2", "task-1"}
			}
			before := a.sequence
			for _, replay := range []bool{false, true} {
				if err := a.apply(tr, replay); err == nil {
					t.Fatal("malformed admission applied")
				}
				if a.sequence != before || len(a.events) != 2 || len(a.accepted) != 2 || len(a.main.inbox) != 2 {
					t.Fatal("malformed admission partially mutated state")
				}
			}
			if err := a.commit(tr); err == nil || a.sequence != before {
				t.Fatal("malformed admission appended")
			}
			payload, err := json.Marshal(tr)
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.store.AppendEntry(session.MainLane, session.NewEntry{Type: session.EntryTypeCustom, ID: strconv.Itoa(before + 1), Custom: &session.CustomData{CustomType: journalKind, Payload: payload}})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			b, err := openState(t.Context(), path, false)
			if err == nil {
				b.Close()
				t.Fatal("disk replay accepted malformed admission")
			}
		})
	}
}

func TestThreadSafetyMetadataSubscriptionDoesNotIndexEvents(t *testing.T) {
	a, err := openState(t.Context(), filepath.Join(t.TempDir(), "empty.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	threadSafetyCommit(t, a, transition{State: "main_opened", Main: &mainTransition{SessionID: "main-test", ProviderSessionID: "main-provider"}})
	threadSafetyCommit(t, a, transition{State: "main_chat_opened", Main: &mainTransition{}})
	// Minimal synthetic catalog isolates metadata publication from event lookup.
	a.tasks["task-1"] = &task{id: "task-1"}
	a.work = mainWorker
	if err := a.SetThreadSubscription("task-1", "github", "pr-27", true); err != nil {
		t.Fatal(err)
	}
	if len(a.events) != 0 || len(a.ThreadSubscriptions("github", "pr-27")) != 1 {
		t.Fatal("metadata created a delivery")
	}
}

func TestThreadSafetyForgedSubscriptionAuthorityRejected(t *testing.T) {
	for _, mode := range []string{"source", "thread", "task", "event", "budget", "terminal", "index"} {
		t.Run(mode, func(t *testing.T) {
			a, _, r := threadSafetyJournal(t)
			s := ThreadSubscription{TaskID: "task-1", Source: "github", Thread: "pr-27", Subscribed: true}
			target := r.target
			switch mode {
			case "source":
				s.Source = "other"
			case "thread":
				s.Thread = "other"
			case "task":
				a.tasks["task-2"] = &task{id: "task-2"}
				s.TaskID = "task-2"
			case "event":
				target.EventID = "other"
			case "budget":
				a.events[0].routeActions = maxWorkerSubscriptionChanges
			case "terminal":
				a.events[0].state = "completed"
			}
			tr := transition{State: "thread_subscription", Thread: &threadTransition{Subscription: &s, Target: &target}}
			if mode == "index" {
				tr.Index = 1
			}
			before := a.sequence
			if err := a.commit(tr); err == nil {
				t.Fatal("forged subscription accepted")
			}
			if err := a.apply(tr, true); err == nil {
				t.Fatal("replay accepted forged subscription")
			}
			if a.sequence != before || len(a.threadSubscriptions) != 0 {
				t.Fatal("rejected authority mutated state")
			}
		})
	}
}

func TestThreadSafetyMainAssignmentRejectsNestedThreadEnvelope(t *testing.T) {
	for _, action := range []string{"new", "attach"} {
		t.Run(action, func(t *testing.T) {
			a, _, _ := threadSafetyJournal(t)
			trigger := Trigger{ID: "new-input", Source: "chat", Thread: "planning", Kind: "request"}
			threadSafetyCommit(t, a, transition{State: "received", Index: 1, Trigger: &trigger})
			input := a.main.inbox[a.main.cursor].input.ID
			d := MainDecision{Action: action}
			assignment := &transition{State: "queued", Index: 1, Task: "task-2", ProviderSessionID: "second-provider", Thread: &threadTransition{}}
			if action == "attach" {
				d.TaskID = "task-1"
				assignment.Task = "task-1"
				assignment.ProviderSessionID = ""
			}
			tr := transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Snapshot: json.RawMessage(`{"session_id":"main-test"}`), Decision: d, Assignment: assignment}}
			before, cursor := a.sequence, a.main.cursor
			if err := a.commit(tr); err == nil {
				t.Fatal("nested Thread assignment appended")
			}
			if err := a.apply(tr, true); err == nil {
				t.Fatal("nested Thread assignment replayed")
			}
			if a.sequence != before || a.main.cursor != cursor || len(a.tasks) != 1 || a.events[1].state != "received" || a.events[1].task != "" || a.fatal != nil {
				t.Fatal("invalid nested assignment partially mutated state")
			}
		})
	}
}

func TestThreadSafetyHandbackAppendFailureDoesNotMutate(t *testing.T) {
	a, _, r := threadSafetyJournal(t)
	before := len(a.main.inbox)
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.workerHandback(t.Context(), 0, r, "Need scope", "Saved summary"); err == nil {
		t.Fatal("failed append reported success")
	}
	if len(a.Handbacks()) != 0 || len(a.main.inbox) != before || a.fatal == nil {
		t.Fatal("failed append published handback or failed to stop")
	}
}

package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

func TestMainTaskAdmissionIdentityAndObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.jsonl")
	var observed []MainInput
	route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		if job.Input.Kind == "task_input" {
			observed = append(observed, job.Input)
		}
		return mainFixture(ctx, job)
	}
	a, err := OpenMain(t.Context(), path, 2, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, id := range []string{"A", "B"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
	}
	waitIdle(t, a)
	update := Trigger{ID: "review", Source: "github", Thread: "pr-1", Kind: "signal", Text: strings.Repeat("review ", 1000)}
	if accepted, err := a.SubmitTask(update, "task-1"); !accepted || err != nil {
		t.Fatalf("admission: %v %v", accepted, err)
	}
	waitIdle(t, a)
	if len(observed) != 1 || observed[0].TaskID != "task-1" || observed[0].Trigger.Text != brief(update.Text) || len(observed[0].Trigger.Text) > 1024 {
		t.Fatalf("main audit: %+v", observed)
	}
	check := func(a *SuperAgent) {
		t.Helper()
		if accepted, err := a.SubmitTask(update, "task-1"); accepted || err != nil {
			t.Fatal("duplicate task event admitted")
		}
		if accepted, err := a.SubmitTask(update, "task-2"); accepted || err == nil {
			t.Fatal("duplicate changed task")
		}
		if accepted, err := a.Submit(update); accepted || err == nil {
			t.Fatal("duplicate changed entry point")
		}
		if accepted, err := a.SubmitTask(Trigger{ID: "A", Source: "im", Thread: "chat", Kind: "request"}, "task-1"); accepted || err == nil {
			t.Fatal("chat duplicate changed entry point")
		}
	}
	check(a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 2, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	check(b)
	waitIdle(t, b)
	if len(observed) != 1 || len(b.Status().Tasks) != 2 || len(b.Replies()) != 3 {
		t.Fatal("task update replayed or reassigned")
	}
	for _, r := range b.Status().Reports {
		if r.EventID == update.ID && r.TaskID != "task-1" {
			t.Fatalf("wrong task: %+v", r)
		}
	}
}

func TestMainChatUpgradePreservesLegacyPendingTask(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	a, err := openState(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(tr transition) {
		t.Helper()
		if err := a.commit(tr); err != nil {
			t.Fatal(err)
		}
	}
	commit(transition{State: "main_opened", Main: &mainTransition{SessionID: "main-old", ProviderSessionID: "main-wire"}})
	first := Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"}
	commit(transition{State: "received", Index: 0, Trigger: &first})
	input := a.main.inbox[0].input.ID
	commit(transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Snapshot: json.RawMessage(`{"session_id":"main-old"}`), Decision: MainDecision{Action: "new"}, Assignment: &transition{State: "queued", Index: 0, Task: "task-1", ProviderSessionID: "original-worker-wire"}}})
	commit(transition{State: "running", Index: 0})
	commit(transition{State: "completed", Index: 0, Text: "old completed", Snapshot: json.RawMessage(`{"session_id":"task-1"}`)})
	input = a.main.inbox[1].input.ID
	commit(transition{State: "main_decided", Main: &mainTransition{InputID: input, DecisionID: "main-decision-" + input, Snapshot: json.RawMessage(`{"session_id":"main-old"}`), Decision: MainDecision{Action: "reply", Text: "old reply"}, ReplyID: "reply-" + input}})
	oldReply := a.Replies()[0]
	pending := Trigger{ID: "legacy-pending", Source: "im", Thread: "chat", Kind: "signal"}
	commit(transition{State: "received", Index: 1, Trigger: &pending})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	var jobs []Job
	b, err := OpenMain(t.Context(), path, 1, mainFixture, func(ctx context.Context, job Job) (Outcome, error) {
		jobs = append(jobs, job)
		return mainWorker(ctx, job)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	if len(jobs) != 1 || jobs[0].TaskID != "task-1" || jobs[0].ProviderSessionID != "original-worker-wire" || jobs[0].Trigger != pending {
		t.Fatalf("legacy pending reassigned: %+v", jobs)
	}
	if b.Replies()[0].ID != oldReply.ID || b.Replies()[0].Text != oldReply.Text {
		t.Fatal("legacy reply changed")
	}
	submit(t, b, Trigger{ID: "new-topic", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, b)
	if len(jobs) != 2 || jobs[1].TaskID != "task-2" || jobs[1].ProviderSessionID == jobs[0].ProviderSessionID {
		t.Fatal("new chat inherited legacy subscription")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := OpenMain(t.Context(), path, 1, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	waitIdle(t, c)
	if len(c.Status().Tasks) != 2 || len(c.Replies()) != 3 {
		t.Fatal("upgrade replay shifted inbox identities")
	}
	snapshot, _, err := c.MainSnapshot()
	if err != nil || snapshot.SessionID != "main-old" {
		t.Fatal("main identity replaced")
	}
}

func TestMainTaskReviewBarrierDoesNotBlockNewChatTopic(t *testing.T) {
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 2, mainFixture, func(ctx context.Context, job Job) (Outcome, error) {
		return Outcome{Summary: "done", NeedsReview: job.Trigger.ID == "A", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "A", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	submit(t, a, Trigger{ID: "B", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	if tasks := a.Status().Tasks; len(tasks) != 2 || tasks[0].Status != "needs_review" || tasks[1].Status != "completed" {
		t.Fatalf("task barriers crossed: %+v", tasks)
	}
}

func TestMainTaskObservationCannotDispatchAgain(t *testing.T) {
	for _, action := range []string{"new", "attach", "steer", "cancel"} {
		t.Run(action, func(t *testing.T) {
			var workers atomic.Int32
			a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, func(ctx context.Context, job MainJob) (MainOutcome, error) {
				if job.Input.Kind == "task_input" {
					d := MainDecision{Action: action}
					if action == "attach" {
						d.TaskID = job.Input.TaskID
					}
					return MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
				}
				return mainFixture(ctx, job)
			}, func(ctx context.Context, job Job) (Outcome, error) { workers.Add(1); return mainWorker(ctx, job) })
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
			waitIdle(t, a)
			if accepted, err := a.SubmitTask(Trigger{ID: "update", Source: "github", Thread: "pr", Kind: "request"}, "task-1"); !accepted || err != nil {
				t.Fatal(err)
			}
			waitIdle(t, a)
			if workers.Load() != 2 || len(a.MainFailures()) != 1 || len(a.MainControls()) != 0 || len(a.Status().Tasks) != 1 {
				t.Fatal("observation dispatched or suppressed its original delivery")
			}
			if a.FailureCause("github", "update") != nil {
				t.Fatal("main observation failure replaced worker result")
			}
		})
	}
}

func TestMainTaskAdmissionInvalidTargetAndAppendFailure(t *testing.T) {
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	event := Trigger{ID: "invalid", Source: "im", Thread: "chat", Kind: "signal"}
	for _, id := range []string{"", "missing", " ", strings.Repeat("x", 257)} {
		if accepted, err := a.SubmitTask(event, id); accepted || err == nil {
			t.Fatalf("invalid target %q", id)
		}
	}
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	noise := event
	noise.Kind = "noise"
	if accepted, err := a.SubmitTask(noise, "task-1"); accepted || err == nil {
		t.Fatal("task-associated noise admitted")
	}
	before := len(a.events)
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if accepted, err := a.SubmitTask(event, "task-1"); accepted || err == nil || len(a.events) != before {
		t.Fatal("failed append admitted task input")
	}
}

func TestMainTaskAdmissionReplayBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	a, err := OpenMain(t.Context(), path, 1, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	update := Trigger{ID: "review", Source: "github", Thread: "pr", Kind: "signal"}
	if _, err := a.SubmitTask(update, "task-1"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var prefix []byte
	for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		prefix = append(prefix, line...)
		var log session.LogItem
		if err := json.Unmarshal(line, &log); err != nil {
			t.Fatal(err)
		}
		if log.Entry == nil || log.Entry.Custom == nil {
			continue
		}
		var tr transition
		if err := json.Unmarshal(log.Entry.Custom.Payload, &tr); err != nil {
			t.Fatal(err)
		}
		if tr.Index != 1 || tr.Main != nil {
			continue
		}
		t.Run(tr.State, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "boundary.jsonl")
			if err := os.WriteFile(file, prefix, 0600); err != nil {
				t.Fatal(err)
			}
			var workers atomic.Int32
			b, err := OpenMain(t.Context(), file, 1, mainFixture, func(ctx context.Context, job Job) (Outcome, error) {
				workers.Add(1)
				if job.TaskID != "task-1" || job.Trigger.ID != update.ID {
					t.Error("wrong replay target")
				}
				return mainWorker(ctx, job)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if accepted, err := b.SubmitTask(update, "task-1"); accepted || err != nil {
				t.Fatal("task input duplicated")
			}
			waitIdle(t, b)
			want := int32(0)
			if tr.State == "received" || tr.State == "queued" {
				want = 1
			}
			if workers.Load() != want || len(b.Status().Tasks) != 1 || len(b.Replies()) != 2 {
				t.Fatalf("replayed runs=%d state=%s replies=%d", workers.Load(), tr.State, len(b.Replies()))
			}
		})
	}
}

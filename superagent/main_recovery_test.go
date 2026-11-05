package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

func mainFixture(_ context.Context, job MainJob) (MainOutcome, error) {
	decision := MainDecision{Action: "new"}
	if job.Input.Kind == "worker_result" {
		decision = MainDecision{Action: "reply", Text: job.Input.Report.Status + ": " + job.Input.Report.Text}
	}
	return MainOutcome{Decision: decision, Snapshot: job.Snapshot}, nil
}

func mainWorker(_ context.Context, job Job) (Outcome, error) {
	return Outcome{Summary: "public result", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Metadata: map[string]any{"private": "not for main"}}}, nil
}

func TestMainReplayEveryCommitBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	a, err := OpenMain(t.Context(), path, 1, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	event := Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request", Text: "Run work"}
	submit(t, a, event)
	waitIdle(t, a)
	reply := a.Replies()[0]
	if err := a.BeginReplyDelivery(reply.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.AcknowledgeReply(reply.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	var prefix []byte
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		prefix = append(prefix, line...)
		if i == 0 {
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
		if tr.State == "main_opened" {
			continue
		}
		t.Run(tr.State+"-"+entry.Entry.ID, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "boundary.jsonl")
			if err := os.WriteFile(file, prefix, 0o600); err != nil {
				t.Fatal(err)
			}
			var runs atomic.Int32
			b, err := OpenMain(t.Context(), file, 1, mainFixture, func(ctx context.Context, job Job) (Outcome, error) {
				runs.Add(1)
				return mainWorker(ctx, job)
			})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if ok, err := b.Submit(event); ok || err != nil {
				t.Fatalf("duplicate admitted: %v %v", ok, err)
			}
			waitIdle(t, b)
			got := b.Replies()
			if len(got) != 1 || got[0].ID != reply.ID || got[0].DecisionID != reply.DecisionID || got[0].TaskID != reply.TaskID {
				t.Fatalf("lost or duplicated association: %+v", got)
			}
			wantStatus := "pending"
			if tr.State == "reply_started" {
				wantStatus = "needs_review"
			}
			if tr.State == "reply_acknowledged" {
				wantStatus = "acknowledged"
			}
			if got[0].Status != wantStatus {
				t.Fatalf("delivery state: %+v", got[0])
			}
			wantRuns := int32(0)
			if tr.State == "received" || (tr.State == "main_decided" && tr.Main.Decision.Action == "new") {
				wantRuns = 1
			}
			if runs.Load() != wantRuns {
				t.Fatalf("runs=%d want=%d", runs.Load(), wantRuns)
			}
			if tr.State == "running" {
				if !got[0].Report.NeedsReview || got[0].Report.Status != "interrupted" || b.Status().Tasks[0].Status != "needs_review" {
					t.Fatalf("uncertain effect was retried or hidden: %+v", got[0])
				}
			}
		})
	}
}

func TestMainUncommittedInferenceResumesSameInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	entered := make(chan MainJob, 1)
	a, err := OpenMain(t.Context(), path, 1, func(ctx context.Context, job MainJob) (MainOutcome, error) {
		entered <- job
		<-ctx.Done()
		return MainOutcome{}, ctx.Err()
	}, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request", Text: "work"})
	first := receive(t, entered)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, func(ctx context.Context, job MainJob) (MainOutcome, error) {
		if job.SessionID != first.SessionID || job.ProviderSessionID != first.ProviderSessionID {
			t.Error("main identity changed")
		}
		if job.Input.Kind == "user" && job.Input.ID != first.Input.ID {
			t.Error("uncommitted input was replaced")
		}
		return mainFixture(ctx, job)
	}, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	if len(b.Replies()) != 1 || len(b.Status().Tasks) != 1 {
		t.Fatal("main did not resume")
	}
}

func TestMainFailuresDoNotPoisonJournalOrBlockQueue(t *testing.T) {
	for _, mode := range []string{"error", "panic", "wrong_snapshot", "invalid_action", "unoffered_task"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.jsonl")
			route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
				if job.Input.Trigger.ID != "bad" {
					return mainFixture(ctx, job)
				}
				switch mode {
				case "error":
					return MainOutcome{}, errors.New("private diagnostic")
				case "panic":
					panic("private panic")
				case "wrong_snapshot":
					return MainOutcome{Decision: MainDecision{Action: "new"}, Snapshot: agent.AgentSnapshot{SessionID: "wrong"}}, nil
				case "invalid_action":
					return MainOutcome{Decision: MainDecision{Action: "delete"}, Snapshot: job.Snapshot}, nil
				default:
					return MainOutcome{Decision: MainDecision{Action: "attach", TaskID: "invented"}, Snapshot: job.Snapshot}, nil
				}
			}
			a, err := OpenMain(t.Context(), path, 1, route, mainWorker)
			if err != nil {
				t.Fatal(err)
			}
			submit(t, a, Trigger{ID: "bad", Source: "im", Thread: "bad", Kind: "request"})
			submit(t, a, Trigger{ID: "good", Source: "im", Thread: "good", Kind: "request"})
			waitIdle(t, a)
			if len(a.MainFailures()) != 1 || len(a.Replies()) != 1 || len(a.Status().Tasks) != 1 {
				t.Fatal("failed input blocked unrelated work")
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMain(t.Context(), path, 1, route, mainWorker)
			if err != nil {
				t.Fatalf("invalid decision poisoned replay: %v", err)
			}
			defer b.Close()
			waitIdle(t, b)
			if len(b.MainFailures()) != 1 || len(b.Replies()) != 1 {
				t.Fatal("failure replay differs")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("private diagnostic")) || bytes.Contains(data, []byte("private panic")) {
				t.Fatal("diagnostic leaked")
			}
		})
	}
}

func TestMainReportFactsAndReadIsolation(t *testing.T) {
	var result MainJob
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, func(ctx context.Context, job MainJob) (MainOutcome, error) {
		if job.Input.Kind == "worker_result" {
			result = job
			job.Input.Report.Text = "attempted callback mutation"
			job.Input.Report.Receipts[0].Committed = false
		}
		return mainFixture(ctx, job)
	}, func(_ context.Context, job Job) (Outcome, error) {
		return Outcome{Summary: "partially failed", Status: "completed_with_tool_errors", NeedsReview: true, Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Metadata: map[string]any{"private": "secret tool output"}}, Receipts: []ToolReceipt{{Token: 1, Domain: "host", CallID: "write-1", ToolName: "write", Committed: true, Completed: true}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	if result.Input.Report == nil || result.Snapshot.Metadata["private"] != nil {
		t.Fatal("worker history exposed to main")
	}
	reply := a.Replies()[0]
	if reply.Report.Text != "partially failed" || reply.Report.Status != "completed_with_tool_errors" || !reply.Report.NeedsReview || !reply.Report.Receipts[0].Committed {
		t.Fatalf("report facts altered: %+v", reply.Report)
	}
	reply.Report.Text, reply.Report.Receipts[0].Committed = "read mutation", false
	if !a.Replies()[0].Report.Receipts[0].Committed || a.Replies()[0].Report.Text != "partially failed" {
		t.Fatal("reply read aliases state")
	}
	if err := a.BeginReplyDelivery(reply.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.BeginReplyDelivery(reply.ID); err == nil {
		t.Fatal("uncertain send was allowed twice")
	}
	if err := a.AcknowledgeReply("invented"); err == nil {
		t.Fatal("unknown reply acknowledged")
	}
}

func TestLegacyUpgradeDoesNotReissueHistoricalResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	legacy := func(context.Context, Trigger, []TaskInfo) (Route, error) { return Route{Action: "new"}, nil }
	a, err := Open(t.Context(), path, 1, legacy, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{ID: "old", Source: "im", Thread: "old", Kind: "request"})
	waitIdle(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, b)
	if len(b.Replies()) != 0 {
		t.Fatal("historical result created a new reply")
	}
	submit(t, b, Trigger{ID: "new", Source: "im", Thread: "new", Kind: "request"})
	waitIdle(t, b)
	if len(b.Replies()) != 1 || len(b.Status().Reports) != 2 {
		t.Fatal("upgrade lost reports or new main work")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := Open(t.Context(), path, 1, legacy, mainWorker); err == nil {
		c.Close()
		t.Fatal("legacy API opened main journal")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("rejected downgrade modified journal")
	}
	if !strings.Contains(string(after), "main_opened") {
		t.Fatal("upgrade was not durable")
	}
}

func TestMainResultCannotAuthorizeAnotherTask(t *testing.T) {
	var workers atomic.Int32
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, func(_ context.Context, job MainJob) (MainOutcome, error) {
		// Even a syntactically valid new decision cannot promote tool output or
		// a Worker report into authorization for independent business work.
		return MainOutcome{Decision: MainDecision{Action: "new"}, Snapshot: job.Snapshot}, nil
	}, func(ctx context.Context, job Job) (Outcome, error) {
		workers.Add(1)
		return mainWorker(ctx, job)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	if workers.Load() != 1 || len(a.Status().Tasks) != 1 || len(a.MainFailures()) != 1 || len(a.Replies()) != 0 {
		t.Fatal("worker report spawned unrequested work")
	}
}

func TestMainSignalCannotAuthorizeIndependentWork(t *testing.T) {
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, mainFixture, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "1", Source: "ci", Thread: "unassociated", Kind: "signal", Text: "New business idea"})
	waitIdle(t, a)
	if len(a.Status().Tasks) != 0 || len(a.MainFailures()) != 1 {
		t.Fatal("unassociated signal spawned new work")
	}
}

func TestMainReplyIdentitiesAreUniqueAcrossJournals(t *testing.T) {
	seen := make(map[string]bool)
	for range 2 {
		a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, mainFixture, mainWorker)
		if err != nil {
			t.Fatal(err)
		}
		submit(t, a, Trigger{ID: "same-event", Source: "same-account", Thread: "same-thread", Kind: "request"})
		waitIdle(t, a)
		reply := a.Replies()[0]
		for _, id := range []string{reply.ID, reply.DecisionID, reply.InputID} {
			if seen[id] {
				t.Errorf("cross-journal identity collision: %s", id)
			}
			seen[id] = true
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMainBlockedDeliveryStillReplies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	var runs atomic.Int32
	worker := func(_ context.Context, job Job) (Outcome, error) {
		runs.Add(1)
		return Outcome{Summary: "effect needs checking", NeedsReview: true, Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	}
	a, err := OpenMain(t.Context(), path, 1, mainFixture, worker)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	submit(t, a, Trigger{ID: "blocked", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	if replies := a.Replies(); len(replies) != 2 || replies[1].Report.Status != "blocked" || replies[1].EventID != "blocked" || runs.Load() != 1 {
		t.Fatalf("blocked feedback missing: %+v", replies)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, mainFixture, worker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	if len(b.Replies()) != 2 || runs.Load() != 1 {
		t.Fatal("blocked delivery was lost or retried")
	}
	if err := b.Reconcile("task-1", "im", "first", "Inspected the effect"); err != nil {
		t.Fatal(err)
	}
	if ok, err := b.Submit(Trigger{ID: "blocked", Source: "im", Thread: "chat", Kind: "request"}); ok || err != nil {
		t.Fatal("reconciled old input was re-admitted")
	}
	waitIdle(t, b)
	if len(b.Replies()) != 2 || runs.Load() != 1 {
		t.Fatal("reconciliation replayed blocked work")
	}
}

func TestMainInputAdmittedBeforeReconcileRemainsBlocked(t *testing.T) {
	var runs atomic.Int32
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, mainFixture, func(_ context.Context, job Job) (Outcome, error) {
		runs.Add(1)
		return Outcome{Summary: "needs verification", NeedsReview: true, Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	// Hold the scheduler only to install the two exact durable boundary records:
	// this delivery was admitted before reconciliation, not a fresh instruction.
	a.mu.Lock()
	stale := Trigger{ID: "stale", Source: "im", Thread: "chat", Kind: "request"}
	err = a.commit(transition{State: "received", Index: len(a.events), Trigger: &stale})
	if err == nil {
		err = a.commit(transition{State: "reconciled", Index: 0, Task: "task-1", Text: "Verified the original effect"})
	}
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a)
	if replies := a.Replies(); len(replies) != 2 || replies[1].Report.Status != "blocked" || replies[1].EventID != "stale" || runs.Load() != 1 {
		t.Fatalf("stale delivery not withheld/reported: %+v", replies)
	}
}

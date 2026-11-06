package sharedpostgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/agent/session"
	"github.com/Icatme/pi-go/superagent"
)

// These are coordinator acceptance tests, not just storage tests. Every entry
// point gates through postgresFixture BEFORE generating work or opening a DB.
// Router, Worker, background and transport evidence are deterministic in-process
// fixtures: no model, platform, subprocess or real external effect is invoked.
func coordinatorRoute(_ context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
	d := superagent.MainDecision{Action: "new"}
	switch job.Input.Kind {
	case "worker_result":
		d = superagent.MainDecision{Action: "reply", Text: job.Input.Report.Status + ": " + job.Input.Report.Text}
	case "task_input", "control_result":
		d.Action = "ignore"
	}
	return superagent.MainOutcome{Decision: d, Snapshot: job.Snapshot}, nil
}

func coordinatorWork(_ context.Context, job superagent.Job) (superagent.Outcome, error) {
	return superagent.Outcome{Summary: "fixture result", Snapshot: agent.AgentSnapshot{
		SessionID: job.TaskID, Metadata: map[string]any{"private": job.TaskID},
	}}, nil
}

func coordinatorOpen(t *testing.T, j *Journal, options superagent.MainJournalOptions) *superagent.SuperAgent {
	t.Helper()
	if options.Parallel == 0 {
		options.Parallel = 1
	}
	if options.Router == nil {
		options.Router = coordinatorRoute
	}
	if options.Worker == nil {
		options.Worker = coordinatorWork
	}
	a, err := superagent.OpenMainJournal(t.Context(), j, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func coordinatorIdle(t *testing.T, a *superagent.SuperAgent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := a.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if failures := a.MainFailures(); len(failures) != 0 {
		t.Fatalf("unexpected Router failure: %+v", failures)
	}
}

func coordinatorEvent(id string) superagent.Trigger {
	return superagent.Trigger{ID: id, Source: "fixture-im", Thread: "original-chat", Kind: "request", Text: "fixture work"}
}

func coordinatorSubmit(t *testing.T, a *superagent.SuperAgent, event superagent.Trigger) {
	t.Helper()
	if accepted, err := a.Submit(event); !accepted || err != nil {
		t.Fatalf("admission: accepted=%v err=%v", accepted, err)
	}
}

func coordinatorClose(t *testing.T, a *superagent.SuperAgent) {
	t.Helper()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func coordinatorImport(t *testing.T, db *sql.DB, domain, path string) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := ImportJSONL(ctx, db, domain, path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("cutover changed the stopped JSONL source")
	}
}

func coordinatorCounts(t *testing.T, db *sql.DB, domain string, j *Journal) {
	t.Helper()
	seq, records := postgresCounts(t, db, domain)
	if seq != int64(len(j.Log())) || records != len(j.Log()) {
		t.Fatalf("database/cache conservation: seq=%d records=%d cache=%d", seq, records, len(j.Log()))
	}
}

func TestPostgresCoordinatorMigrationAtEveryMainBoundary(t *testing.T) {
	db, _ := postgresFixture(t)
	path := filepath.Join(t.TempDir(), "stopped-source.jsonl")
	a, err := superagent.OpenMain(t.Context(), path, 1, coordinatorRoute, coordinatorWork)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	event := coordinatorEvent("migration")
	coordinatorSubmit(t, a, event)
	coordinatorIdle(t, a)
	if len(a.Replies()) != 1 {
		t.Fatal("source must have exactly one reply")
	}
	reply := a.Replies()[0]
	delivery, err := a.BeginReplyDelivery(reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ObserveReply(reply.Source, reply.Thread, delivery, "fixture-message"); err != nil {
		t.Fatal(err)
	}
	coordinatorClose(t, a)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var prefix []byte
	for i, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		prefix = append(prefix, line...)
		if i == 0 || len(line) == 0 {
			continue
		}
		var item session.LogItem
		if err := json.Unmarshal(line, &item); err != nil {
			t.Fatal(err)
		}
		var tr struct {
			State string                                     `json:"state"`
			Main  struct{ Decision superagent.MainDecision } `json:"main"`
		}
		if item.Entry == nil || item.Entry.Custom == nil {
			t.Fatal("source contains a non-transition record")
		}
		if err := json.Unmarshal(item.Entry.Custom.Payload, &tr); err != nil {
			t.Fatal(err)
		}
		if tr.State == "main_opened" || tr.State == "main_chat_opened" {
			continue // These precede event admission, so duplicate checks do not apply.
		}
		seen[tr.State] = true
		t.Run(tr.State+"-"+item.Entry.ID, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "boundary.jsonl")
			if err := os.WriteFile(source, prefix, 0600); err != nil {
				t.Fatal(err)
			}
			domain := postgresDomain(t, db)
			coordinatorImport(t, db, domain, source)
			j := postgresOpen(t, db, postgresOptions(domain, "cutover"))
			var workers atomic.Int32
			b := coordinatorOpen(t, j, superagent.MainJournalOptions{Worker: func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
				workers.Add(1)
				return coordinatorWork(ctx, job)
			}})
			if accepted, err := b.Submit(event); accepted || err != nil {
				t.Fatalf("cutover re-admitted original event: %v %v", accepted, err)
			}
			coordinatorIdle(t, b)
			got := b.Replies()
			if len(got) != 1 || got[0].ID != reply.ID || got[0].TaskID != reply.TaskID || got[0].DecisionID != reply.DecisionID || got[0].EventID != event.ID || got[0].Source != event.Source || got[0].Thread != event.Thread {
				t.Fatalf("reply association changed: %+v", got)
			}
			wantWorkers := int32(0)
			if tr.State == "received" || (tr.State == "main_decided" && tr.Main.Decision.Action == "new") {
				wantWorkers = 1
			}
			if workers.Load() != wantWorkers {
				t.Fatalf("worker replay: got=%d want=%d", workers.Load(), wantWorkers)
			}
			if tr.State == "running" && (!got[0].Report.NeedsReview || got[0].Report.Status != "interrupted" || b.Status().Tasks[0].Status != "needs_review") {
				t.Fatal("unknown external effects bypassed review")
			}
			wantStatus := "pending"
			if tr.State == "reply_claimed" {
				wantStatus = "needs_review"
				if _, err := b.BeginReplyDelivery(reply.ID); err == nil {
					t.Fatal("uncertain send was automatically retryable")
				}
			}
			if tr.State == "reply_resolved" {
				wantStatus = "acknowledged"
			}
			if got[0].Status != wantStatus {
				t.Fatalf("reply state=%s want=%s", got[0].Status, wantStatus)
			}
			coordinatorCounts(t, db, domain, j)
			coordinatorClose(t, b)
		})
	}
	for _, state := range []string{"received", "main_decided", "running", "completed", "reply_claimed", "reply_resolved"} {
		if !seen[state] {
			t.Fatalf("source never exercised required boundary %q", state)
		}
	}
}

func TestPostgresCoordinatorSubscriptionsSnapshotsAndOutboxSurviveRestart(t *testing.T) {
	db, config := postgresFixture(t)
	path := filepath.Join(t.TempDir(), "source.jsonl")
	a, err := superagent.OpenMain(t.Context(), path, 1, coordinatorRoute, coordinatorWork)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	for _, id := range []string{"A", "B"} {
		coordinatorSubmit(t, a, coordinatorEvent(id))
		coordinatorIdle(t, a)
	}
	for _, task := range a.Status().Tasks {
		if err := a.SetThreadSubscription(task.ID, "fixture-github", "pr-27", true); err != nil {
			t.Fatal(err)
		}
	}
	reply := a.Replies()[0]
	attempt, err := a.BeginReplyDelivery(reply.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantStatus, wantReplies := a.Status(), a.Replies()
	wantSnapshots := map[string]agent.AgentSnapshot{}
	for _, task := range wantStatus.Tasks {
		snapshot, ok, err := a.TaskSnapshot(task.ID)
		if !ok || err != nil {
			t.Fatal("missing source snapshot", err)
		}
		wantSnapshots[task.ID] = snapshot
	}
	coordinatorClose(t, a)
	domain := postgresDomain(t, db)
	coordinatorImport(t, db, domain, path)
	var workers atomic.Int32
	options := superagent.MainJournalOptions{Worker: func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
		workers.Add(1)
		return coordinatorWork(ctx, job)
	}}
	j := postgresOpen(t, db, postgresOptions(domain, "migration"))
	b := coordinatorOpen(t, j, options)
	coordinatorIdle(t, b)
	if !reflect.DeepEqual(b.Status(), wantStatus) || !reflect.DeepEqual(b.Replies(), wantReplies) || workers.Load() != 0 {
		t.Fatal("migration changed task/report/outbox state or reran completed work")
	}
	for task, want := range wantSnapshots {
		got, ok, err := b.TaskSnapshot(task)
		if !ok || err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("private snapshot changed at cutover")
		}
	}
	if !reflect.DeepEqual(b.ThreadSubscriptions("fixture-github", "pr-27"), []string{"task-1", "task-2"}) {
		t.Fatal("subscriptions changed at cutover")
	}
	if _, err := b.BeginReplyDelivery(reply.ID); err == nil {
		t.Fatal("uncertain original send could be reclaimed")
	}
	if err := b.ObserveReply(reply.Source, reply.Thread, attempt, "fixture-message"); err != nil {
		t.Fatal(err)
	}
	callback := superagent.Trigger{ID: "callback", Source: "fixture-github", Thread: "pr-27", Kind: "signal", Text: "fixture review"}
	if accepted, err := b.SubmitThread(callback); !accepted || err != nil {
		t.Fatalf("subscribed callback: %v %v", accepted, err)
	}
	coordinatorIdle(t, b)
	if workers.Load() != 2 || len(b.ReportsSince(2)) != 2 || len(b.Replies()) != 4 {
		t.Fatal("callback was not conserved once per subscribed task")
	}
	wantStatus, wantReplies = b.Status(), b.Replies()
	wantLog := j.Log()
	coordinatorClose(t, b)
	// A new pool proves replay is from PostgreSQL, not a retained journal cache.
	next := postgresOpen(t, postgresDB(t, config, nil), postgresOptions(domain, "restart"))
	c := coordinatorOpen(t, next, options)
	if accepted, err := c.SubmitThread(callback); accepted || err != nil {
		t.Fatalf("restarted callback deduplication: %v %v", accepted, err)
	}
	coordinatorIdle(t, c)
	if !reflect.DeepEqual(c.Status(), wantStatus) || !reflect.DeepEqual(c.Replies(), wantReplies) || !reflect.DeepEqual(next.Log(), wantLog) || workers.Load() != 2 {
		t.Fatal("restart changed reports/replies or dispatched duplicate work")
	}
	if !reflect.DeepEqual(c.ThreadSubscriptions("fixture-github", "pr-27"), []string{"task-1", "task-2"}) {
		t.Fatal("restart lost subscriptions")
	}
	for task, want := range wantSnapshots {
		got, ok, err := c.TaskSnapshot(task)
		if !ok || err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("restart changed private task snapshot")
		}
	}
	if got := c.Replies()[0]; got.Status != "acknowledged" || len(got.Attempts) != 1 || got.Attempts[0].Delivery != attempt || got.Attempts[0].MessageID != "fixture-message" {
		t.Fatal("restart lost original verified reply attempt")
	}
	coordinatorCounts(t, db, domain, next)
}

func coordinatorExpired(t *testing.T, j *Journal) {
	t.Helper()
	j.stopRenewal() // Test-only pause, not graceful release or a server crash.
	select {
	case <-j.Context().Done():
	case <-time.After(6 * time.Second):
		t.Fatal("ownership deadline did not cancel the old coordinator")
	}
}

func coordinatorTakeover(t *testing.T, db *sql.DB, domain string) *Journal {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, err := Open(t.Context(), db, postgresOptions(domain, "replacement"))
		if err == nil {
			t.Cleanup(func() { _ = j.Close() })
			return j
		}
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			t.Fatalf("takeover failed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPostgresCoordinatorLeaseExpiryFencesLateCallbacks(t *testing.T) {
	db, config := postgresFixture(t)
	for _, kind := range []string{"router", "worker"} {
		t.Run(kind, func(t *testing.T) {
			domain := postgresDomain(t, db)
			old := postgresOpen(t, db, postgresOptions(domain, "old"))
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			options := superagent.MainJournalOptions{}
			if kind == "router" {
				options.Router = func(ctx context.Context, job superagent.MainJob) (superagent.MainOutcome, error) {
					close(entered)
					<-release // Intentionally return a late read-only decision.
					return coordinatorRoute(ctx, job)
				}
			} else {
				options.Worker = func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
					close(entered)
					<-release // Model an uncooperative executor, without an effect.
					out, err := coordinatorWork(ctx, job)
					out.Summary = "stale claimed success"
					return out, err
				}
			}
			a := coordinatorOpen(t, old, options)
			// Registered after coordinator cleanup: always unblock before Close.
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			event := coordinatorEvent(kind)
			coordinatorSubmit(t, a, event)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("callback never started")
			}
			coordinatorExpired(t, old)
			next := coordinatorTakeover(t, postgresDB(t, config, nil), domain)
			var workers atomic.Int32
			b := coordinatorOpen(t, next, superagent.MainJournalOptions{Worker: func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
				workers.Add(1)
				return coordinatorWork(ctx, job)
			}})
			coordinatorIdle(t, b)
			if next.Epoch() <= old.Epoch() {
				t.Fatal("takeover did not advance the epoch")
			}
			if accepted, err := a.Submit(coordinatorEvent("stale-admission")); accepted || err == nil {
				t.Fatal("lost owner admitted new work")
			}
			before := next.Log()
			once.Do(func() { close(release) })
			_ = a.Close() // A lost owner may report cancellation or lease loss.
			if !reflect.DeepEqual(before, next.Log()) {
				t.Fatal("late callback changed replacement cache")
			}
			reports := b.ReportsSince(0)
			if len(reports) != 1 || len(b.Replies()) != 1 {
				t.Fatal("takeover lost or duplicated the original result/reply")
			}
			if kind == "worker" {
				state := b.Status().Tasks[0]
				if workers.Load() != 0 || !reports[0].NeedsReview || reports[0].Status != "interrupted" || state.Status != "needs_review" || state.ReviewEventID != event.ID || state.ReviewDeliveryID != reports[0].DeliveryID {
					t.Fatal("interrupted work replayed or lost original review identity")
				}
			} else if workers.Load() != 1 || reports[0].NeedsReview || reports[0].Status != "completed" {
				t.Fatal("uncommitted Router input was not resumed exactly once")
			}
			coordinatorCounts(t, db, domain, next)
			coordinatorClose(t, b)
			check := postgresOpen(t, db, postgresOptions(domain, "durable-check"))
			if !reflect.DeepEqual(before, check.Log()) {
				t.Fatal("late callback changed durable PostgreSQL state")
			}
		})
	}
}

func TestPostgresCoordinatorUnknownReplyClaimIsNotResent(t *testing.T) {
	db, config := postgresFixture(t)
	domain := postgresDomain(t, db)
	fault := &postgresFault{}
	old := postgresOpen(t, postgresDB(t, config, fault), postgresOptions(domain, "old-outbox"))
	a := coordinatorOpen(t, old, superagent.MainJournalOptions{})
	coordinatorSubmit(t, a, coordinatorEvent("reply"))
	coordinatorIdle(t, a)
	reply := a.Replies()[0]
	old.stopRenewal() // Ensure the injected ACK loss targets this claim only.
	fault.loseCommitAck.Store(true)
	if delivery, err := a.BeginReplyDelivery(reply.ID); !errors.Is(err, ErrCommitUnknown) || delivery != (superagent.ReplyDelivery{}) {
		t.Fatalf("uncertain claim granted send authority: %+v %v", delivery, err)
	}
	if fault.loseCommitAck.Load() || old.Context().Err() == nil {
		t.Fatal("claim did not consume fault and invalidate ownership")
	}
	_ = a.Close()
	next := coordinatorTakeover(t, postgresDB(t, config, nil), domain)
	var workers atomic.Int32
	b := coordinatorOpen(t, next, superagent.MainJournalOptions{Worker: func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
		workers.Add(1)
		return coordinatorWork(ctx, job)
	}})
	coordinatorIdle(t, b)
	recovered := b.Replies()[0]
	if recovered.ID != reply.ID || recovered.Status != "needs_review" || len(recovered.Attempts) != 1 || workers.Load() != 0 {
		t.Fatal("unknown committed claim was lost, retried or re-executed")
	}
	attempt := recovered.Attempts[0].Delivery
	if _, err := b.BeginReplyDelivery(reply.ID); err == nil {
		t.Fatal("replacement reissued the uncertain send")
	}
	if err := a.ObserveReply(reply.Source, reply.Thread, attempt, "late-old-evidence"); err == nil {
		t.Fatal("stale outbox accepted a late delivery receipt")
	}
	if err := b.ObserveReply(reply.Source, reply.Thread, attempt, "fixture-verified-message"); err != nil {
		t.Fatal(err)
	}
	want := b.Replies()
	coordinatorClose(t, b)
	reopened := postgresOpen(t, db, postgresOptions(domain, "verified-outbox"))
	c := coordinatorOpen(t, reopened, superagent.MainJournalOptions{})
	coordinatorIdle(t, c)
	if !reflect.DeepEqual(c.Replies(), want) || want[0].Status != "acknowledged" || len(want[0].Attempts) != 1 {
		t.Fatal("verified receipt did not survive restart on its original attempt")
	}
	coordinatorCounts(t, db, domain, reopened)
}

// A synthetic backend with explicitly controlled observations. Start only
// increments an in-memory count; no process or external operation is launched.
type coordinatorBackground struct {
	mu      sync.Mutex
	request background.Request
	starts  int
	known   bool
	exited  bool
}

func (b *coordinatorBackground) Start(_ context.Context, r background.Request) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.starts++
	b.request = r
	return nil
}
func (*coordinatorBackground) Stop(context.Context, string) error { return nil }
func (b *coordinatorBackground) Inspect(_ context.Context, id string) (background.Observation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o := background.Observation{ID: id, State: "unknown"}
	if b.known && id == b.request.ID {
		o.State = "running"
		if b.exited {
			o.State = "exited"
			o.Output = json.RawMessage(`{"content":[{"type":"text","text":"private fixture output"}]}`)
			o.Receipts = []background.Receipt{{Token: 1, Domain: "agent", CallID: b.request.Call.ID, ToolName: b.request.Call.Name, Committed: true, Completed: true}}
		}
	}
	return o, nil
}

func TestPostgresCoordinatorBackgroundReviewAndLateCompletion(t *testing.T) {
	db, config := postgresFixture(t)
	backend := &coordinatorBackground{known: true}
	options := superagent.BackgroundOptions{MaxConcurrent: 1, PollInterval: time.Minute, Tools: []superagent.BackgroundTool{{
		Name: "fixture", Scope: "isolated-test", PolicyRevision: "v1", Backend: backend,
		Authorize: func(context.Context, superagent.BackgroundAuthorization) error { return nil },
	}}}
	path := filepath.Join(t.TempDir(), "background.jsonl")
	a, err := superagent.OpenMainBackground(t.Context(), path, 1, coordinatorRoute, func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
		if _, err := job.Background.Start(ctx, superagent.BackgroundStart{Tool: "fixture", CallID: "original-call", Arguments: json.RawMessage(`{}`)}); err != nil {
			return superagent.Outcome{}, err
		}
		return coordinatorWork(ctx, job)
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	event := coordinatorEvent("background")
	coordinatorSubmit(t, a, event)
	coordinatorIdle(t, a)
	if len(a.BackgroundOperations()) != 1 {
		t.Fatal("source did not persist the background operation")
	}
	original := a.BackgroundOperations()[0]
	coordinatorClose(t, a)
	backend.mu.Lock()
	backend.known = false
	backend.mu.Unlock()
	domain := postgresDomain(t, db)
	coordinatorImport(t, db, domain, path)
	var completions atomic.Int32
	configMain := superagent.MainJournalOptions{Background: &options, Worker: func(ctx context.Context, job superagent.Job) (superagent.Outcome, error) {
		if job.BackgroundCompletion == nil || job.BackgroundCompletion.OperationID != original.ID || job.BackgroundCompletion.CallID != original.CallID || job.BackgroundCompletion.DeliveryID != original.DeliveryID || job.TaskID != original.TaskID {
			return superagent.Outcome{}, errors.New("unexpected work crossed background review barrier")
		}
		completions.Add(1)
		return coordinatorWork(ctx, job)
	}}
	j := postgresOpen(t, db, postgresOptions(domain, "background-cutover"))
	b := coordinatorOpen(t, j, configMain)
	coordinatorIdle(t, b)
	op := b.BackgroundOperations()[0]
	if op.ID != original.ID || op.CallID != original.CallID || op.DeliveryID != original.DeliveryID || op.Origin != original.Origin || op.Scope != original.Scope || op.PolicyRevision != original.PolicyRevision || !op.NeedsReview || op.Exited {
		t.Fatal("background cutover lost original identity, authority or review")
	}
	if err := b.ReconcileBackground(op.ID, "assume no effects"); err == nil {
		t.Fatal("an unverified note released the background barrier")
	}
	if accepted, err := b.SubmitTask(coordinatorEvent("withheld"), op.TaskID); !accepted || err != nil {
		t.Fatalf("follow-up admission: %v %v", accepted, err)
	}
	coordinatorIdle(t, b)
	reports := b.ReportsSince(1)
	if len(reports) != 1 || reports[0].Status != "blocked" || completions.Load() != 0 {
		t.Fatal("unknown operation allowed follow-up execution")
	}
	backend.mu.Lock()
	backend.known, backend.exited = true, true
	backend.mu.Unlock()
	if err := b.RefreshBackground(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	coordinatorIdle(t, b)
	terminal := b.BackgroundOperations()[0]
	if !terminal.Exited || terminal.NeedsReview || len(terminal.Receipts) != 1 || !terminal.Receipts[0].Committed || terminal.Receipts[0].CallID != original.CallID || completions.Load() != 1 {
		t.Fatal("late committed evidence was lost or misattributed")
	}
	if err := b.RefreshBackground(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	coordinatorIdle(t, b)
	wantStatus, wantReplies := b.Status(), b.Replies()
	coordinatorClose(t, b)
	next := postgresOpen(t, postgresDB(t, config, nil), postgresOptions(domain, "background-restart"))
	c := coordinatorOpen(t, next, configMain)
	if accepted, err := c.Submit(event); accepted || err != nil {
		t.Fatalf("background duplicate: %v %v", accepted, err)
	}
	coordinatorIdle(t, c)
	backend.mu.Lock()
	starts := backend.starts
	backend.mu.Unlock()
	if starts != 1 || completions.Load() != 1 || !reflect.DeepEqual(c.Status(), wantStatus) || !reflect.DeepEqual(c.Replies(), wantReplies) || !reflect.DeepEqual(c.BackgroundOperations(), []superagent.BackgroundOperation{terminal}) {
		t.Fatal("terminal restart duplicated Start/completion or changed conserved state")
	}
	coordinatorCounts(t, db, domain, next)
}

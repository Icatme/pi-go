package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

// Use the serialized contract so this regression fails on the M2b baseline,
// rather than depending on new Go fields to compile first.
func serializedDeliveryID(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := json.Unmarshal(fields["delivery_id"], &id); err != nil || id == "" {
		t.Fatalf("missing stable delivery_id in %s: %v", raw, err)
	}
	return id
}

func TestDeliveryIdentityFanoutReportsAndReplay(t *testing.T) {
	a, path := threadStoppedFixture(t)
	for _, task := range []string{"task-1", "task-2"} {
		if err := a.SetThreadSubscription(task, "github", "pr-27", true); err != nil {
			t.Fatal(err)
		}
	}
	event := Trigger{ID: "same-event", Source: "github", Thread: "pr-27", Kind: "signal", Text: "review"}
	if ok, err := a.SubmitThread(event); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	jobs := map[string]Job{}
	inputs := map[string]MainInput{}
	route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		if job.Input.Trigger.ID == event.ID && job.Input.Kind == "worker_result" {
			mu.Lock()
			inputs[job.Input.Report.TaskID] = job.Input
			mu.Unlock()
		}
		return mainFixture(ctx, job)
	}
	work := func(ctx context.Context, job Job) (Outcome, error) {
		mu.Lock()
		jobs[job.TaskID] = job
		mu.Unlock()
		out, err := mainWorker(ctx, job)
		if job.TaskID == "task-2" {
			out.Receipts = []ToolReceipt{{Token: 1, Domain: "native", CallID: "write", ToolName: "save", Committed: true, Completed: true}}
			err = errors.New("private failure after commit")
		}
		return out, err
	}
	b, err := OpenMain(t.Context(), path, 2, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	reports := b.ReportsSince(3)
	if len(reports) != 2 {
		t.Fatal(reports)
	}
	seen := map[string]bool{}
	for _, report := range reports {
		id := serializedDeliveryID(t, report)
		if seen[id] {
			t.Fatal("fanout reports collapsed to one delivery", reports)
		}
		seen[id] = true
		mu.Lock()
		job, input := jobs[report.TaskID], inputs[report.TaskID]
		mu.Unlock()
		if serializedDeliveryID(t, job) != id || serializedDeliveryID(t, input) != id || serializedDeliveryID(t, input.Report) != id {
			t.Fatal("job, main input and report lost delivery correlation")
		}
		if report.TaskID == "task-2" && (report.Status != "failed" || !report.NeedsReview || len(report.Receipts) != 1 || !report.Receipts[0].Committed) {
			t.Fatal("independent committed failure erased", report)
		}
		matchingReplies := 0
		for _, reply := range b.Replies() {
			if reply.Report != nil && reply.Report.EventID == event.ID && reply.TaskID == report.TaskID {
				matchingReplies++
				if serializedDeliveryID(t, reply) != id || reply.ID == id {
					t.Fatal("outbox identity conflated with delivery", reply)
				}
			}
		}
		if matchingReplies != 1 {
			t.Fatalf("delivery %s has %d result replies", id, matchingReplies)
		}
	}
	if len(b.ReportsSince(5)) != 0 || len(b.ReportsSince(-1)) != 5 || len(b.ReportsSince(999)) != 0 {
		t.Fatal("ReportsSince count cursor changed")
	}
	before := b.Status()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := OpenMain(t.Context(), path, 2, route, func(context.Context, Job) (Outcome, error) {
		t.Error("terminal delivery was reexecuted")
		return Outcome{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if ok, err := c.SubmitThread(event); ok || err != nil {
		t.Fatal("duplicate ingress changed", ok, err)
	}
	waitIdle(t, c)
	if !reflect.DeepEqual(reports, c.ReportsSince(3)) || !reflect.DeepEqual(before, c.Status()) {
		t.Fatal("replay changed delivery/report/task identity")
	}
}

func TestDeliveryIdentityMainObservations(t *testing.T) {
	for _, kind := range []string{"handback", "control_result"} {
		t.Run(kind, func(t *testing.T) {
			a, path, active := threadSafetyJournal(t)
			expected := a.main.inbox[0].input.DeliveryID
			decide := func(d MainDecision) {
				input := a.main.inbox[a.main.cursor].input
				m := &mainTransition{InputID: input.ID, DecisionID: "main-decision-" + input.ID, Decision: d, Snapshot: json.RawMessage(`{"session_id":"main-test"}`)}
				if d.Action == "clarify" {
					m.ReplyID = "reply-" + input.ID
				}
				threadSafetyCommit(t, a, transition{State: "main_decided", Main: m})
			}
			if kind == "handback" {
				if err := a.workerHandback(t.Context(), 0, active, "Need owner", "Saved summary"); err != nil {
					t.Fatal(err)
				}
			} else {
				correction := Trigger{ID: "correction", Source: "github", Thread: "pr-27", Kind: "request", Text: "Use the recent hour"}
				if ok, err := a.Submit(correction); !ok || err != nil {
					t.Fatal(ok, err)
				}
				expected = a.main.inbox[a.main.cursor].input.DeliveryID
				if expected == a.main.inbox[0].input.DeliveryID {
					t.Fatal("correction and target delivery conflated")
				}
				decide(MainDecision{Action: "steer", Target: &active.target, Text: correction.Text})
				if err := a.commitControls("not_delivered", []string{a.main.controls[0].ID}); err != nil {
					t.Fatal(err)
				}
			}
			input := a.main.inbox[a.main.cursor].input
			if input.Kind != kind || input.DeliveryID == "" || input.DeliveryID != expected {
				t.Fatal("main observation identity", input)
			}
			decide(MainDecision{Action: "clarify", Text: "Please verify the scope."})
			replies := a.Replies()
			if len(replies) != 1 || replies[0].DeliveryID != expected || replies[0].ID == expected {
				t.Fatal("reply identity", replies)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			b, err := openState(t.Context(), path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if !reflect.DeepEqual(replies, b.Replies()) {
				t.Fatal("replay changed observation identities")
			}
		})
	}
}

func TestDeliveryIdentityLegacyJournalUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	a, err := openState(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	event := Trigger{ID: "old", Source: "im", Thread: "chat", Kind: "request", Text: "legacy"}
	// Old records have no explicit delivery IDs, main session or affinity.
	for _, tr := range []transition{
		{State: "received", Trigger: &event}, {State: "routing"},
		{State: "queued", Task: "task-1"}, {State: "running"},
		{State: "completed", Snapshot: json.RawMessage(`{"session_id":"task-1"}`), Text: "old report"},
	} {
		threadSafetyCommit(t, a, tr)
	}
	legacy := a.ReportsSince(0)
	id := serializedDeliveryID(t, legacy[0])
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	provider := make(chan string, 1)
	b, err := OpenMain(t.Context(), path, 1, mainFixture, func(ctx context.Context, job Job) (Outcome, error) {
		provider <- job.ProviderSessionID
		return mainWorker(ctx, job)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !reflect.DeepEqual(legacy, b.ReportsSince(0)) || len(b.Replies()) != 0 || !reflect.DeepEqual(b.ThreadSubscriptions("im", "chat"), []string{"task-1"}) {
		t.Fatal("upgrade rewrote report/binding or resurrected old main work")
	}
	event.ID = "new"
	if ok, err := b.SubmitThread(event); !ok || err != nil {
		t.Fatal(ok, err)
	}
	waitIdle(t, b)
	next := b.ReportsSince(1)
	if len(next) != 1 || next[0].TaskID != "task-1" || serializedDeliveryID(t, next[0]) == id || <-provider != "task-1" {
		t.Fatal("upgrade changed task/provider identity", next)
	}
}

func TestDeliveryIdentityUnassignedOutcomes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ignored.jsonl")
	a, err := Open(t.Context(), path, 1, func(context.Context, Trigger, []TaskInfo) (Route, error) {
		return Route{}, errors.New("router failed")
	}, func(context.Context, Job) (Outcome, error) { t.Error("unassigned work started"); return Outcome{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, kind := range []string{"noise", "request"} {
		submit(t, a, Trigger{ID: kind, Source: "im", Thread: "chat", Kind: kind})
	}
	waitIdle(t, a)
	reports := a.ReportsSince(0)
	if len(reports) != 2 || reports[0].TaskID != "" || reports[1].TaskID != "" || serializedDeliveryID(t, reports[0]) == serializedDeliveryID(t, reports[1]) {
		t.Fatal(reports)
	}
}

func TestDeliveryIdentityProgressAndReviewKeepExactTarget(t *testing.T) {
	a, path := threadStoppedFixture(t)
	for _, task := range []string{"task-1", "task-2"} {
		if err := a.SetThreadSubscription(task, "github", "pr", true); err != nil {
			t.Fatal(err)
		}
	}
	event := Trigger{ID: "shared", Source: "github", Thread: "pr", Kind: "signal"}
	if ok, err := a.SubmitThread(event); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan Job, 2)
	release := make(chan struct{})
	work := func(ctx context.Context, job Job) (Outcome, error) {
		started <- job
		select {
		case <-release:
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
		return Outcome{Summary: "saved", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}}, nil
	}
	b, err := OpenMain(t.Context(), path, 2, mainFixture, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	jobs := map[string]Job{}
	for len(jobs) < 2 {
		job := <-started
		jobs[job.TaskID] = job
	}
	watch, cancel := context.WithCancel(t.Context())
	defer cancel()
	before := b.ThreadSubscriptions("github", "pr")
	for _, row := range b.Status().Tasks {
		job, active := jobs[row.ID]
		if !active {
			continue
		}
		if row.ActiveDeliveryID != job.DeliveryID {
			t.Fatal("status lost exact running delivery", row, job)
		}
		ch, err := b.Subscribe(watch, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p := <-ch; p.DeliveryID != job.DeliveryID || p.Kind != "running" {
			t.Fatal("initial progress lost delivery", p)
		}
		job.NotifyProgress("tool_start")
		if p := <-ch; p.DeliveryID != job.DeliveryID || p.Kind != "tool_start" {
			t.Fatal("live progress lost delivery", p)
		}
	}
	if !reflect.DeepEqual(before, b.ThreadSubscriptions("github", "pr")) {
		t.Fatal("progress subscription changed routing")
	}
	job := jobs["task-2"]
	if err := b.Abort(RunTarget{TaskID: job.TaskID, Source: event.Source, EventID: event.ID}); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitIdle(t, b)
	for _, row := range b.Status().Tasks {
		if row.ID == job.TaskID && (row.ReviewDeliveryID != job.DeliveryID || row.ActiveDeliveryID != "" || row.Status != "needs_review") {
			t.Fatal("review identity lost", row)
		}
	}
	ch, err := b.Subscribe(watch, job.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if p := <-ch; p.DeliveryID != job.DeliveryID || p.Kind != "needs_review" {
		t.Fatal("review progress lost delivery", p)
	}
	if reports := b.ReportsSince(3); len(reports) != 2 || reports[0].DeliveryID == reports[1].DeliveryID {
		t.Fatal(reports)
	}
}

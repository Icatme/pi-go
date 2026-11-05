package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
)

// These are API/journal correctness gates, not wall-clock benchmarks. The first
// callback is held until all submissions finish, making the backlog independent
// of scheduler timing. Timeouts in receive/waitIdle are deadlock guards only.
func evaluationEvents(count int) []Trigger {
	events := make([]Trigger, count)
	for i := range events {
		events[i] = Trigger{ID: fmt.Sprintf("event-%03d", i), Source: "evaluation", Thread: fmt.Sprintf("thread-%03d", i), Kind: "request", Text: "offline fixture"}
	}
	return events
}

func evaluationDuplicates(t *testing.T, a *SuperAgent, events []Trigger) {
	t.Helper()
	for _, event := range events {
		if accepted, err := a.Submit(event); accepted || err != nil {
			t.Fatalf("duplicate %s: accepted=%v err=%v", event.ID, accepted, err)
		}
	}
	changed := events[0]
	changed.Text += " changed"
	if accepted, err := a.Submit(changed); accepted || err == nil {
		t.Fatalf("conflicting duplicate: accepted=%v err=%v", accepted, err)
	}
}

func evaluationReports(t *testing.T, reports []Report, events []Trigger, statuses map[string]string) {
	t.Helper()
	if len(reports) != len(events) {
		t.Fatalf("terminal reports=%d, unique events=%d", len(reports), len(events))
	}
	expected := make(map[string]Trigger, len(events))
	for _, event := range events {
		expected[event.ID] = event
	}
	seenEvents, seenDeliveries := map[string]bool{}, map[string]bool{}
	for _, report := range reports {
		event, exists := expected[report.EventID]
		if !exists || seenEvents[report.EventID] || report.Source != event.Source || report.Thread != event.Thread {
			t.Fatalf("lost/duplicate/miscorrelated event report: %+v", report)
		}
		if report.DeliveryID == "" || seenDeliveries[report.DeliveryID] {
			t.Fatalf("missing/duplicate delivery identity: %+v", report)
		}
		if report.Status != statuses[event.ID] {
			t.Fatalf("event %s: status=%q want=%q", event.ID, report.Status, statuses[event.ID])
		}
		seenEvents[report.EventID], seenDeliveries[report.DeliveryID] = true, true
	}
}

func TestEvaluationMainBatchBacklogConservation(t *testing.T) {
	const unique, duplicates = 256, 44
	for _, maxItems := range []int{1, 8} {
		for _, injectFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("max_%d/failure_%v", maxItems, injectFailure), func(t *testing.T) {
				events := evaluationEvents(unique)
				entered, release := make(chan struct{}), make(chan struct{})
				jobs := make(chan MainBatchJob, unique)
				sentinel := errors.New("offline router failure")
				var calls, workers atomic.Int32
				route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
					call := calls.Add(1)
					jobs <- job
					if call == 1 {
						close(entered)
						select {
						case <-release:
						case <-ctx.Done():
							return MainBatchOutcome{}, ctx.Err()
						}
					}
					if injectFailure && call == 2 {
						return MainBatchOutcome{}, sentinel
					}
					out, err := batchReply(ctx, job)
					// Correlation must use InputID, never the returned decision position.
					slices.Reverse(out.Decisions)
					return out, err
				}
				work := func(ctx context.Context, job Job) (Outcome, error) {
					workers.Add(1)
					return mainWorker(ctx, job)
				}
				options := MainBatchOptions{MaxItems: maxItems, MaxInputBytes: 1 << 20}
				path := filepath.Join(t.TempDir(), "backlog.jsonl")
				a, err := OpenMainBatch(t.Context(), path, 4, options, route, work)
				if err != nil {
					t.Fatal(err)
				}
				defer a.Close()
				submit(t, a, events[0])
				receive(t, entered)
				for _, event := range events[1:] {
					submit(t, a, event)
				}
				evaluationDuplicates(t, a, events[:duplicates])
				if calls.Load() != 1 {
					t.Fatal("first callback did not hold backlog")
				}
				close(release)
				waitIdle(t, a)
				wantCalls := 1 + (unique-1+maxItems-1)/maxItems
				if int(calls.Load()) != wantCalls {
					t.Fatalf("calls=%d want=%d", calls.Load(), wantCalls)
				}
				inputIDs, eventInputs, failedIDs := map[string]bool{}, map[string]string{}, map[string]bool{}
				statuses := map[string]string{}
				offset := 0
				for call := 0; call < wantCalls; call++ {
					job := receive(t, jobs)
					wantSize := min(maxItems, unique-offset)
					if call == 0 {
						wantSize = 1
					}
					encoded, err := json.Marshal(job.Inputs)
					if err != nil || len(encoded) > options.MaxInputBytes || len(job.Inputs) != wantSize {
						t.Fatalf("batch %d: inputs=%d want=%d bytes=%d err=%v", call, len(job.Inputs), wantSize, len(encoded), err)
					}
					for _, input := range job.Inputs {
						if input.ID == "" || inputIDs[input.ID] || input.Kind != "user" || input.Trigger != events[offset] {
							t.Fatalf("input lost, duplicated or reordered at %d: %+v", offset, input)
						}
						inputIDs[input.ID], eventInputs[input.Trigger.ID] = true, input.ID
						statuses[input.Trigger.ID] = "ignored"
						if injectFailure && call == 1 {
							failedIDs[input.ID] = true
							statuses[input.Trigger.ID] = "failed"
							if !errors.Is(a.MainFailureCause(input.ID), sentinel) || !errors.Is(a.FailureCause(input.Trigger.Source, input.Trigger.ID), sentinel) {
								t.Fatalf("failure cause lost: %s", input.ID)
							}
						}
						offset++
					}
				}
				if offset != unique || workers.Load() != 0 || len(a.Status().Tasks) != 0 {
					t.Fatal("input conservation or reply-only dispatch violated")
				}
				evaluationReports(t, a.Status().Reports, events, statuses)
				reportsByEvent := map[string]Report{}
				for _, report := range a.Status().Reports {
					reportsByEvent[report.EventID] = report
				}
				failures, replies := a.MainFailures(), a.Replies()
				if len(failures) != len(failedIDs) || len(replies)+len(failures) != unique {
					t.Fatal("terminal main inputs not conserved")
				}
				seen := map[string]bool{}
				for _, failure := range failures {
					if !failedIDs[failure.InputID] || seen[failure.InputID] || failure.Text == "" {
						t.Fatalf("bad failure: %+v", failure)
					}
					seen[failure.InputID] = true
				}
				for _, reply := range replies {
					report := reportsByEvent[reply.EventID]
					if reply.InputID != eventInputs[reply.EventID] || reply.Text != reply.EventID || seen[reply.InputID] || reply.ID != "reply-"+reply.InputID || reply.DeliveryID != report.DeliveryID || reply.Source != report.Source || reply.Thread != report.Thread || reply.Status != "pending" {
						t.Fatalf("bad reply correlation: %+v", reply)
					}
					seen[reply.InputID] = true
				}
				before := a.Status()
				if err := a.Close(); err != nil {
					t.Fatal(err)
				}
				b, err := OpenMainBatch(t.Context(), path, 4, options, route, work)
				if err != nil {
					t.Fatal(err)
				}
				defer b.Close()
				evaluationDuplicates(t, b, events)
				waitIdle(t, b)
				if int(calls.Load()) != wantCalls || workers.Load() != 0 || !reflect.DeepEqual(before, b.Status()) || !reflect.DeepEqual(replies, b.Replies()) || !reflect.DeepEqual(failures, b.MainFailures()) {
					t.Fatal("reopen or duplicate submission changed terminal results or dispatched work")
				}
				t.Logf("valid_submissions=%d unique=%d max_items=%d router_calls=%d failures=%d replay_dispatches=0", unique+duplicates, unique, maxItems, wantCalls, len(failures))
			})
		}
	}
}

// The older Open API is deliberately tested separately: its Router receives
// one Trigger, not a MainInput batch, and it does not synthesize main replies.
func TestEvaluationLegacyOpenBacklogCompatibility(t *testing.T) {
	events := evaluationEvents(256)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls, workers atomic.Int32
	route := func(ctx context.Context, event Trigger, _ []TaskInfo) (Route, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return Route{}, ctx.Err()
			}
		}
		return Route{Action: "new"}, nil
	}
	work := func(ctx context.Context, job Job) (Outcome, error) { workers.Add(1); return mainWorker(ctx, job) }
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	a, err := Open(t.Context(), path, 4, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, events[0])
	receive(t, entered)
	for _, event := range events[1:] {
		submit(t, a, event)
	}
	evaluationDuplicates(t, a, events[:44])
	close(release)
	waitIdle(t, a)
	statuses := map[string]string{}
	for _, event := range events {
		statuses[event.ID] = "completed"
	}
	evaluationReports(t, a.Status().Reports, events, statuses)
	if calls.Load() != 256 || workers.Load() != 256 || len(a.Status().Tasks) != 256 || len(a.Replies()) != 0 {
		t.Fatal("legacy single-event dispatch changed")
	}
	for _, task := range a.Status().Tasks {
		if task.Status != "completed" || task.Pending != 0 || task.ActiveEventID != "" {
			t.Fatalf("nonterminal legacy task: %+v", task)
		}
	}
	before := a.Status()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.Context(), path, 4, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	evaluationDuplicates(t, b, events)
	waitIdle(t, b)
	if calls.Load() != 256 || workers.Load() != 256 || !reflect.DeepEqual(before, b.Status()) {
		t.Fatal("legacy replay duplicated work or changed reports")
	}
}

func TestEvaluationMainBatchWorkerResultConservation(t *testing.T) {
	const count = 64
	events := evaluationEvents(count)
	entered, release := make(chan struct{}), make(chan struct{})
	jobs := make(chan MainBatchJob, 2*count)
	executions := make(chan Job, count)
	sentinel := errors.New("offline worker failure")
	var calls, workers atomic.Int32
	route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		jobs <- job
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return MainBatchOutcome{}, ctx.Err()
			}
		}
		out := MainBatchOutcome{Snapshot: job.Snapshot}
		for _, input := range job.Inputs {
			decision := MainDecision{Action: "new"}
			if input.Kind == "worker_result" {
				decision = MainDecision{Action: "reply", Text: input.Report.Status}
			}
			out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: decision})
		}
		slices.Reverse(out.Decisions)
		return out, nil
	}
	work := func(ctx context.Context, job Job) (Outcome, error) {
		workers.Add(1)
		executions <- job
		out, err := mainWorker(ctx, job)
		if job.Trigger.ID == events[17].ID {
			return out, sentinel
		}
		return out, err
	}
	options := MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}
	path := filepath.Join(t.TempDir(), "workers.jsonl")
	a, err := OpenMainBatch(t.Context(), path, 4, options, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, events[0])
	receive(t, entered)
	for _, event := range events[1:] {
		submit(t, a, event)
	}
	evaluationDuplicates(t, a, events)
	close(release)
	waitIdle(t, a)
	if workers.Load() != count || len(a.Status().Tasks) != count || len(a.Replies()) != count || len(a.MainFailures()) != 0 {
		t.Fatalf("delegation/result count mismatch: workers=%d tasks=%d replies=%d failures=%v", workers.Load(), len(a.Status().Tasks), len(a.Replies()), a.MainFailures())
	}
	statuses := map[string]string{}
	for _, event := range events {
		statuses[event.ID] = "completed"
	}
	statuses[events[17].ID] = "failed"
	evaluationReports(t, a.Status().Reports, events, statuses)
	if !errors.Is(a.FailureCause(events[17].Source, events[17].ID), sentinel) {
		t.Fatal("worker failure cause lost")
	}
	byEvent, taskIDs := map[string]Job{}, map[string]bool{}
	for range count {
		job := receive(t, executions)
		if _, exists := byEvent[job.Trigger.ID]; exists || taskIDs[job.TaskID] || job.DeliveryID == "" {
			t.Fatalf("duplicate/colliding worker: %+v", job)
		}
		byEvent[job.Trigger.ID], taskIDs[job.TaskID] = job, true
	}
	inputs, users, results := map[string]bool{}, map[string]bool{}, map[string]string{}
	for range int(calls.Load()) {
		job := receive(t, jobs)
		raw, err := json.Marshal(job.Inputs)
		if err != nil || len(raw) > options.MaxInputBytes || len(job.Inputs) < 1 || len(job.Inputs) > options.MaxItems {
			t.Fatal("worker/result batch exceeded bounds")
		}
		for _, input := range job.Inputs {
			if input.ID == "" || inputs[input.ID] {
				t.Fatal("repeated/missing user or worker-result input ID")
			}
			inputs[input.ID] = true
			worker, exists := byEvent[input.Trigger.ID]
			if !exists || worker.Trigger != input.Trigger {
				t.Fatalf("unmatched main input: %+v", input)
			}
			switch input.Kind {
			case "user":
				if users[input.Trigger.ID] {
					t.Fatal("repeated user input")
				}
				users[input.Trigger.ID] = true
			case "worker_result":
				if results[input.Trigger.ID] != "" || input.Report == nil || input.Report.Status != statuses[input.Trigger.ID] || input.Report.TaskID != worker.TaskID || input.Report.DeliveryID != worker.DeliveryID {
					t.Fatalf("worker-result correlation lost: %+v", input)
				}
				results[input.Trigger.ID] = input.ID
			default:
				t.Fatalf("unexpected main input kind %q", input.Kind)
			}
		}
	}
	if len(inputs) != 2*count || len(users) != count || len(results) != count {
		t.Fatal("user/worker-result inputs not conserved")
	}
	seenReplies := map[string]bool{}
	for _, reply := range a.Replies() {
		if seenReplies[reply.EventID] || reply.InputID != results[reply.EventID] || reply.Text != statuses[reply.EventID] || reply.Report == nil || reply.TaskID != byEvent[reply.EventID].TaskID {
			t.Fatalf("reply lost worker result: %+v", reply)
		}
		seenReplies[reply.EventID] = true
	}
	for _, task := range a.Status().Tasks {
		if task.Pending != 0 || task.ActiveEventID != "" || (task.Status != "completed" && task.Status != "failed" && task.Status != "needs_review") {
			t.Fatalf("nonterminal task: %+v", task)
		}
	}
	before, replies, routeCalls := a.Status(), a.Replies(), calls.Load()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMainBatch(t.Context(), path, 4, options, route, work)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	evaluationDuplicates(t, b, events)
	waitIdle(t, b)
	if calls.Load() != routeCalls || workers.Load() != count || !reflect.DeepEqual(before, b.Status()) || !reflect.DeepEqual(replies, b.Replies()) {
		t.Fatal("reopen redispatched main/worker or changed result identity")
	}
}

package superagent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func batchReply(_ context.Context, job MainBatchJob) (MainBatchOutcome, error) {
	out := MainBatchOutcome{Snapshot: job.Snapshot}
	for _, input := range job.Inputs {
		out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: MainDecision{Action: "reply", Text: input.Trigger.ID}})
	}
	return out, nil
}

func TestMainBatchImmediateBacklogAndIdentity(t *testing.T) {
	entered := make(chan MainBatchJob, 10)
	release := make(chan struct{})
	route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		entered <- job
		if job.Inputs[0].Trigger.ID == "first" {
			select {
			case <-release:
			case <-ctx.Done():
				return MainBatchOutcome{}, ctx.Err()
			}
		}
		out, err := batchReply(ctx, job)
		slices.Reverse(out.Decisions)
		return out, err
	}
	path := filepath.Join(t.TempDir(), "state.jsonl")
	a, err := OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 3, MaxInputBytes: 1 << 20}, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
	first := receive(t, entered)
	if len(first.Inputs) != 1 {
		t.Fatal("idle input was not immediate")
	}
	for _, id := range []string{"second", "third", "fourth", "fifth"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
	}
	close(release)
	waitIdle(t, a)
	if got := len(receive(t, entered).Inputs); got != 3 {
		t.Fatalf("backlog size=%d", got)
	}
	if got := len(receive(t, entered).Inputs); got != 1 {
		t.Fatalf("tail size=%d", got)
	}
	var ids []string
	for _, r := range a.Replies() {
		ids = append(ids, r.EventID)
		if r.Text != r.EventID || r.InputID == "" {
			t.Fatalf("wrong correlation: %+v", r)
		}
	}
	if !reflect.DeepEqual(ids, []string{"first", "second", "third", "fourth", "fifth"}) {
		t.Fatal(ids)
	}
	old := a.Replies()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 3, MaxInputBytes: 1 << 20}, batchReply, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	if !reflect.DeepEqual(old, b.Replies()) {
		t.Fatal("batch replay changed identity")
	}
}

func TestMainBatchInvalidGroupHasNoPartialEffects(t *testing.T) {
	for _, bad := range []string{"missing", "duplicate", "unknown", "unauthorized", "snapshot", "late_invalid", "late_signal_new"} {
		t.Run(bad, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
				if job.Inputs[0].Trigger.ID == "first" {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return MainBatchOutcome{}, ctx.Err()
					}
				}
				out, _ := batchReply(ctx, job)
				if len(job.Inputs) > 1 {
					out.Decisions[0].Decision = MainDecision{Action: "new"}
					switch bad {
					case "missing":
						out.Decisions = out.Decisions[:1]
					case "duplicate":
						out.Decisions[1].InputID = out.Decisions[0].InputID
					case "unknown":
						out.Decisions[1].InputID = "other"
					case "unauthorized":
						out.Decisions[1].Decision = MainDecision{Action: "attach", TaskID: "task-999"}
					case "snapshot":
						out.Snapshot.SessionID = "wrong"
					case "late_invalid":
						out.Decisions[1].Decision = MainDecision{Action: "invalid"}
					case "late_signal_new":
						out.Decisions[1].Decision = MainDecision{Action: "new"}
					}
				}
				return out, nil
			}
			a, err := OpenMainBatch(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, MainBatchOptions{MaxItems: 2, MaxInputBytes: 1 << 20}, route, mainWorker)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
			receive(t, entered)
			for _, id := range []string{"bad-one", "bad-two", "later"} {
				kind := "request"
				if bad == "late_signal_new" && id == "bad-two" {
					kind = "signal"
				}
				submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: kind})
			}
			close(release)
			waitIdle(t, a)
			if len(a.Status().Tasks) != 0 || len(a.MainFailures()) != 2 || len(a.Replies()) != 2 || a.Replies()[1].EventID != "later" {
				t.Fatalf("partial effects or blocked tail: %+v, %+v, %+v", a.Status(), a.MainFailures(), a.Replies())
			}
		})
	}
}

func TestMainBatchMultipleDelegationsRemainDistinct(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		if job.Inputs[0].Trigger.ID == "first" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return MainBatchOutcome{}, ctx.Err()
			}
			return batchReply(ctx, job)
		}
		out := MainBatchOutcome{Snapshot: job.Snapshot}
		for _, input := range job.Inputs {
			d := MainDecision{Action: "new"}
			if input.Kind == "worker_result" {
				d = MainDecision{Action: "reply", Text: input.Report.Status}
			}
			out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: d})
		}
		return out, nil
	}
	path := filepath.Join(t.TempDir(), "state.jsonl")
	a, err := OpenMainBatch(t.Context(), path, 2, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
	receive(t, entered)
	for _, id := range []string{"new-one", "new-two"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
	}
	close(release)
	waitIdle(t, a)
	tasks := a.Status().Tasks
	if len(tasks) != 2 || tasks[0].ID == tasks[1].ID || len(a.MainFailures()) != 0 {
		t.Fatalf("colliding batch assignments: %+v %+v", tasks, a.MainFailures())
	}
	before := a.Replies()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMainBatch(t.Context(), path, 2, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, batchReply, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	waitIdle(t, b)
	if !reflect.DeepEqual(before, b.Replies()) || len(b.Status().Tasks) != 2 {
		t.Fatal("duplicate batch replay")
	}
}

func TestMainBatchByteBudgetAndOversizedHead(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	jobs := make(chan MainBatchJob, 8)
	route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		jobs <- job
		if job.Inputs[0].Trigger.ID == "first" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return MainBatchOutcome{}, ctx.Err()
			}
		}
		return batchReply(ctx, job)
	}
	a, err := OpenMainBatch(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1024}, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
	receive(t, entered)
	for _, id := range []string{"one", "two"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("x", 650)})
	}
	submit(t, a, Trigger{ID: "oversized", Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("\x00", 300)})
	submit(t, a, Trigger{ID: "after", Source: "im", Thread: "chat", Kind: "request"})
	close(release)
	waitIdle(t, a)
	for range 4 {
		job := receive(t, jobs)
		raw, _ := json.Marshal(job.Inputs)
		if len(raw) > 1024 || len(job.Inputs) != 1 {
			t.Fatalf("unbounded JSON input: %d %d", len(raw), len(job.Inputs))
		}
	}
	if len(a.MainFailures()) != 1 || len(a.Replies()) != 4 {
		t.Fatalf("oversize input blocked tail: %+v %+v", a.MainFailures(), a.Replies())
	}
}

func TestMainBatchClosedInferencePreservesWholeGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.jsonl")
	first := make(chan struct{})
	release := make(chan struct{})
	batch := make(chan MainBatchJob, 1)
	route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		if job.Inputs[0].Trigger.ID == "first" {
			close(first)
			select {
			case <-release:
			case <-ctx.Done():
				return MainBatchOutcome{}, ctx.Err()
			}
			return batchReply(ctx, job)
		}
		batch <- job
		<-ctx.Done()
		return MainBatchOutcome{}, ctx.Err()
	}
	a, err := OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
	receive(t, first)
	for _, id := range []string{"one", "two"} {
		submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
	}
	close(release)
	before := receive(t, batch)
	if len(before.Inputs) != 2 {
		t.Fatal("not a batch")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	resumed := make(chan MainBatchJob, 1)
	b, err := OpenMainBatch(t.Context(), path, 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		resumed <- job
		return batchReply(ctx, job)
	}, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	after := receive(t, resumed)
	waitIdle(t, b)
	if !reflect.DeepEqual(before.Inputs, after.Inputs) || len(b.Replies()) != 3 || len(b.MainFailures()) != 0 {
		t.Fatal("cancel consumed uncommitted group")
	}
}

func TestMainBatchCannotMutateOfferedAuthority(t *testing.T) {
	for _, mutate := range []string{"input", "task", "mixed_result"} {
		t.Run(mutate, func(t *testing.T) {
			calls := 0
			route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
				calls++
				out, _ := batchReply(ctx, job)
				if calls == 1 {
					out.Decisions[0].Decision = MainDecision{Action: "new"}
					return out, nil
				}
				switch mutate {
				case "input":
					job.Inputs[0].ID = "forged"
					out.Decisions[0].InputID = "forged"
				case "task":
					job.Tasks[0].ID = "forged"
					out.Decisions[0].Decision = MainDecision{Action: "attach", TaskID: "forged"}
				case "mixed_result":
					out.Decisions[0].Decision = MainDecision{Action: "new"}
				}
				return out, nil
			}
			a, err := OpenMainBatch(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, route, mainWorker)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "request", Source: "im", Thread: "chat", Kind: "request"})
			waitIdle(t, a)
			if len(a.MainFailures()) != 1 || len(a.Status().Tasks) != 1 || len(a.Replies()) != 0 {
				t.Fatalf("callback changed authority: %+v %+v", a.MainFailures(), a.Replies())
			}
		})
	}
}

func TestMainBatchBlockedInferenceDoesNotBlockExactAbort(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	started := make(chan Job, 1)
	route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
		if job.Inputs[0].Kind == "user" && job.Inputs[0].Trigger.ID == "slow" {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return MainBatchOutcome{}, ctx.Err()
			}
		}
		out := MainBatchOutcome{Snapshot: job.Snapshot}
		for _, input := range job.Inputs {
			d := MainDecision{Action: "ignore"}
			if input.Kind == "user" {
				d = MainDecision{Action: "new"}
			} else if input.Kind == "worker_result" {
				d = MainDecision{Action: "reply", Text: input.Report.Status}
			}
			out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: d})
		}
		return out, nil
	}
	worker := func(ctx context.Context, job Job) (Outcome, error) {
		if job.Trigger.ID == "follow" {
			started <- job
			<-ctx.Done()
			return Outcome{Snapshot: agent.AgentSnapshot{SessionID: job.TaskID}, Receipts: []ToolReceipt{{Token: 1, Domain: "host", CallID: "write", ToolName: "write", Committed: true, Completed: true}}}, ctx.Err()
		}
		return mainWorker(ctx, job)
	}
	a, err := OpenMainBatch(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 2, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, route, worker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	task := a.Status().Tasks[0].ID
	submit(t, a, Trigger{ID: "slow", Source: "im", Thread: "chat", Kind: "request"})
	receive(t, entered)
	policy := AdmissionPolicy{Version: "v1", Rules: []AdmissionRule{{Source: "git", Principal: "bot", Group: "repo"}}}
	receipt, err := a.Admit(policy, AdmissionEnvelope{Trigger: Trigger{ID: "follow", Source: "git", Thread: "pr", Kind: "signal"}, Principal: "bot", Group: "repo", TaskID: task})
	if err != nil || receipt.Record.Reason != "task_association" {
		t.Fatalf("gate task fast path: %+v %v", receipt, err)
	}
	job := receive(t, started)
	target := RunTarget{TaskID: job.TaskID, Source: job.Trigger.Source, EventID: job.Trigger.ID}
	steering, err := a.Steer(target, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Abort(target); err != nil {
		t.Fatal(err)
	}
	if delivered, err := steering.Wait(t.Context()); err != nil || delivered {
		t.Fatalf("wrong control outcome: %v %v", delivered, err)
	}
	close(release)
	waitIdle(t, a)
	found := false
	for _, reply := range a.Replies() {
		if reply.EventID == "follow" {
			found = reply.Report != nil && reply.Report.Status == "interrupted" && reply.Report.NeedsReview && len(reply.Report.Receipts) == 1 && reply.Report.Receipts[0].Committed
		}
	}
	if !found {
		t.Fatal("batch hid cancelled worker's committed side effects")
	}
}

func TestMainBatchStaleControlFailsEntireGroup(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(strconv.FormatBool(finish), func(t *testing.T) {
			started := make(chan Job, 1)
			workerDone := make(chan struct{})
			gate := make(chan struct{})
			releaseGate := make(chan struct{})
			batchStarted := make(chan MainBatchJob, 1)
			releaseBatch := make(chan struct{})
			route := func(ctx context.Context, job MainBatchJob) (MainBatchOutcome, error) {
				if job.Inputs[0].Kind == "user" && job.Inputs[0].Trigger.ID == "start" {
					return MainBatchOutcome{Snapshot: job.Snapshot, Decisions: []MainInputDecision{{InputID: job.Inputs[0].ID, Decision: MainDecision{Action: "new"}}}}, nil
				}
				if job.Inputs[0].Trigger.ID == "gate" {
					close(gate)
					select {
					case <-releaseGate:
					case <-ctx.Done():
						return MainBatchOutcome{}, ctx.Err()
					}
				}
				out, _ := batchReply(ctx, job)
				if job.Inputs[0].Trigger.ID == "reply-in-group" {
					batchStarted <- job
					select {
					case <-releaseBatch:
					case <-ctx.Done():
						return MainBatchOutcome{}, ctx.Err()
					}
					target := job.Runs[0]
					out.Decisions[1].Decision = MainDecision{Action: "cancel", Target: &target}
				}
				return out, nil
			}
			worker := func(ctx context.Context, job Job) (Outcome, error) {
				started <- job
				select {
				case <-workerDone:
					return mainWorker(ctx, job)
				case <-ctx.Done():
					return Outcome{}, ctx.Err()
				}
			}
			a, err := OpenMainBatch(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, route, worker)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "start", Source: "im", Thread: "chat", Kind: "request"})
			job := receive(t, started)
			submit(t, a, Trigger{ID: "gate", Source: "im", Thread: "chat", Kind: "request"})
			receive(t, gate)
			for _, id := range []string{"reply-in-group", "cancel"} {
				submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
			}
			close(releaseGate)
			offered := receive(t, batchStarted)
			if len(offered.Inputs) != 2 || len(offered.Runs) != 1 {
				t.Fatal("wrong offered control batch")
			}
			if finish {
				close(workerDone)
				// Wait for durable completion, not merely the worker callback's return.
				for {
					a.mu.Lock()
					done := a.events[0].state == "completed"
					ch := a.changed
					a.mu.Unlock()
					if done {
						break
					}
					select {
					case <-ch:
					case <-t.Context().Done():
						t.Fatal(t.Context().Err())
					}
				}
			} else {
				if err := a.Abort(RunTarget{TaskID: job.TaskID, Source: job.Trigger.Source, EventID: job.Trigger.ID}); err != nil {
					t.Fatal(err)
				}
			}
			close(releaseBatch)
			waitIdle(t, a)
			if len(a.MainFailures()) != 2 || len(a.MainControls()) != 0 {
				t.Fatalf("stale control applied: %+v %+v", a.MainFailures(), a.MainControls())
			}
			for _, r := range a.Replies() {
				if r.EventID == "reply-in-group" {
					t.Fatal("stale group partially replied")
				}
			}
		})
	}
}

func BenchmarkMainBatchSnapshotValidation(b *testing.B) {
	for _, count := range []int{1, 64} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			a := &SuperAgent{main: mainState{id: "main", chatRouting: true}, tasks: map[string]*task{}, accepted: map[[2]string]int{}, subscriptions: map[[2]string]string{}, threadSubscriptions: map[[2]string][]string{}}
			raw, proof, err := encodeSnapshot(agent.AgentSnapshot{SessionID: "main", Metadata: map[string]any{"history": strings.Repeat("x", 1<<20)}}, "main")
			if err != nil {
				b.Fatal(err)
			}
			tr := transition{State: "main_batch_decided", Main: &mainTransition{Snapshot: raw}, validatedSnapshot: proof}
			for i := range count {
				id := "input-" + strconv.Itoa(i)
				trigger := Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"}
				a.events = append(a.events, item{trigger: trigger, state: "received", admission: i, chatEntry: true})
				a.pending = append(a.pending, i)
				a.main.inbox = append(a.main.inbox, mainItem{input: MainInput{ID: id, Kind: "user", Trigger: trigger}, event: i})
				tr.Main.Batch = append(tr.Main.Batch, mainTransition{InputID: id, DecisionID: "main-decision-" + id, Decision: MainDecision{Action: "ignore"}})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := a.validateMainBatch(tr); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

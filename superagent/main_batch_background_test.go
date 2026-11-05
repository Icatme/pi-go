package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/background"
)

func backgroundBatch(_ context.Context, j MainBatchJob) (MainBatchOutcome, error) {
	out := MainBatchOutcome{Snapshot: j.Snapshot}
	for _, input := range j.Inputs {
		d := MainDecision{Action: "reply", Text: input.ID}
		if input.Kind == "user" && input.Trigger.ID == "first" {
			d = MainDecision{Action: "new"}
		}
		out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: d})
	}
	// Output order is deliberately independent of the admitted input order.
	slices.Reverse(out.Decisions)
	return out, nil
}

func waitBatchBackgroundReport(t *testing.T, a *SuperAgent, id string) Report {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		a.mu.Lock()
		changed := a.changed
		a.mu.Unlock()
		for _, report := range a.ReportsSince(0) {
			if report.EventID == id {
				return report
			}
		}
		select {
		case <-changed:
		case <-timeout.C:
			t.Fatalf("missing report for %s", id)
		}
	}
}

// M6a already provides this composition. Exercise the public constructor rather
// than making a private configuration change or introducing another opener.
func TestMainBatchBackgroundMixedBacklog(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stopped_%v", stop), func(t *testing.T) {
			backend := newBarrierBackground()
			defer func() {
				select {
				case <-backend.release:
				default:
					close(backend.release)
				}
			}()
			entered, release := make(chan struct{}), make(chan struct{})
			batches := make(chan MainBatchJob, 20)
			route := func(ctx context.Context, j MainBatchJob) (MainBatchOutcome, error) {
				batches <- j
				if j.Inputs[0].Trigger.ID == "hold" {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return MainBatchOutcome{}, ctx.Err()
					}
				}
				return backgroundBatch(ctx, j)
			}
			jobs := make(chan Job, 10)
			var workers atomic.Int32
			worker := func(ctx context.Context, j Job) (Outcome, error) {
				workers.Add(1)
				jobs <- j
				if j.Trigger.ID == "first" {
					if _, err := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "original-call", Arguments: json.RawMessage(`{"private":"PRIVATE ARGUMENT"}`)}); err != nil {
						return Outcome{}, err
					}
				}
				return mainWorker(ctx, j)
			}
			options := backgroundOptions(backend)
			a, err := OpenMainJournal(t.Context(), leasedMemory(t), MainJournalOptions{Parallel: 1, BatchRouter: route, BatchOptions: MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, Worker: worker, Background: &options})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			submit(t, a, Trigger{ID: "first", Source: "im", Thread: "chat", Kind: "request", Text: "start"})
			waitIdle(t, a)
			first := receive(t, jobs)
			op := a.BackgroundOperations()[0]
			submit(t, a, Trigger{ID: "hold", Source: "im", Thread: "chat", Kind: "request"})
			receive(t, entered)
			if ok, err := a.SubmitTask(Trigger{ID: "follow", Source: "im", Thread: "chat", Kind: "request", Text: "continue while background runs"}, op.TaskID); !ok || err != nil {
				t.Fatalf("follow-up: %v %v", ok, err)
			}
			follow := receive(t, jobs)
			waitBatchBackgroundReport(t, a, "follow")
			if follow.TaskID != first.TaskID || follow.ProviderSessionID != first.ProviderSessionID || follow.DeliveryID == first.DeliveryID || follow.BackgroundCompletion != nil || a.BackgroundOperations()[0].Exited {
				t.Fatal("follow-up did not retain Worker ownership while backend remained active")
			}
			if stop {
				if err := a.StopBackground(t.Context(), op.ID); err != nil {
					t.Fatal(err)
				}
				if a.BackgroundOperations()[0].Exited {
					t.Fatal("stop claimed exit before the late body returned")
				}
			}
			close(backend.release)
			receive(t, backend.done)
			if err := a.RefreshBackground(t.Context(), op.ID); err != nil {
				t.Fatal(err)
			}
			report := waitBatchBackgroundReport(t, a, op.ID)
			if report.Background == nil || report.Background.OperationID != op.ID || report.Background.CallID != op.CallID || report.Background.DeliveryID != op.DeliveryID || report.Background.StopRequested != stop || report.Background.NeedsReview != stop || len(report.Background.Output) != 0 || len(report.Background.Receipts) != 1 || !report.Background.Receipts[0].Committed {
				t.Fatalf("completion facts changed: %+v", report)
			}
			if stop {
				if report.Status != "blocked" || workers.Load() != 2 {
					t.Fatal("stopped completion crossed task review barrier")
				}
			} else {
				completion := receive(t, jobs)
				if completion.TaskID != first.TaskID || completion.ProviderSessionID != first.ProviderSessionID || completion.BackgroundCompletion == nil || completion.BackgroundCompletion.CallID != op.CallID || !strings.Contains(string(completion.BackgroundCompletion.Output), "PRIVATE OUTPUT") {
					t.Fatal("private completion was not delivered to its original Worker")
				}
			}
			for _, id := range []string{"cold-one", "cold-two"} {
				submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
			}
			close(release)
			waitIdle(t, a)
			if len(a.MainFailures()) != 0 || len(a.Status().Tasks) != 1 {
				t.Fatalf("mixed batch failed or created extra work: %+v", a.MainFailures())
			}
			close(batches)
			var inputs []MainInput
			mixed := false
			for batch := range batches {
				inputs = append(inputs, batch.Inputs...)
				if len(batch.Inputs) == 6 {
					kinds := make([]string, len(batch.Inputs))
					for i, input := range batch.Inputs {
						kinds[i] = input.Kind
					}
					mixed = reflect.DeepEqual(kinds, []string{"task_input", "worker_result", "task_input", "worker_result", "user", "user"})
				}
			}
			if !mixed || len(inputs) != 9 {
				t.Fatalf("expected ordered mixed backlog: %+v", inputs)
			}
			byID := map[string]MainInput{}
			for i, input := range inputs {
				if !strings.HasSuffix(input.ID, fmt.Sprintf("/input-%d", i+1)) || byID[input.ID].ID != "" {
					t.Fatal("main input identity or order changed")
				}
				raw, err := json.Marshal(input)
				if err != nil || strings.Contains(string(raw), "PRIVATE") {
					t.Fatal("private background payload reached main router")
				}
				byID[input.ID] = input
			}
			if len(a.Replies()) != 8 {
				t.Fatal("main replies lost or duplicated")
			}
			for _, reply := range a.Replies() {
				input := byID[reply.InputID]
				if reply.Text != input.ID || reply.DeliveryID != input.DeliveryID || reply.EventID != input.Trigger.ID || reply.Source != input.Trigger.Source || reply.Thread != input.Trigger.Thread {
					t.Fatalf("reply attached to wrong batch item: %+v", reply)
				}
			}
			if err := a.RefreshBackground(t.Context(), op.ID); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, a)
			wantWorkers := int32(3)
			if stop {
				wantWorkers = 2
			}
			if workers.Load() != wantWorkers || backend.calls != 1 || len(a.Replies()) != 8 {
				t.Fatal("terminal refresh replayed completion")
			}
		})
	}
}

func TestMainBatchBackgroundAuthorityAndUnknownBarriers(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown_%v", unknown), func(t *testing.T) {
			var dispatches, authorizations, workerCalls atomic.Int32
			backend := scriptedBackground{start: func(context.Context, background.Request) error {
				dispatches.Add(1)
				return nil
			}, inspect: func(_ context.Context, id string) (background.Observation, error) {
				state := "running"
				if unknown {
					state = "unknown"
				}
				return background.Observation{ID: id, State: state}, nil
			}}
			options := backgroundOptions(backend)
			options.Tools[0].Authorize = func(_ context.Context, auth BackgroundAuthorization) error {
				authorizations.Add(1)
				if auth.Target.TaskID == "" || auth.DeliveryID == "" || auth.Scope != "workspace-A" || auth.PolicyRevision != "v1" {
					return errors.New("missing host provenance")
				}
				if string(auth.Start.Arguments) == `{"deny":true}` {
					return errors.New("denied")
				}
				return nil
			}
			capabilities := make(chan *BackgroundControl, 3)
			results := make(chan error, 3)
			worker := func(ctx context.Context, j Job) (Outcome, error) {
				workerCalls.Add(1)
				capabilities <- j.Background
				arguments := json.RawMessage(`{}`)
				if j.Trigger.ID == "denied" {
					arguments = json.RawMessage(`{"deny":true}`)
				}
				_, err := j.Background.Start(ctx, BackgroundStart{Tool: "slow", CallID: "same-call-id", Arguments: arguments})
				results <- err
				return mainWorker(ctx, j)
			}
			route := func(_ context.Context, j MainBatchJob) (MainBatchOutcome, error) {
				out := MainBatchOutcome{Snapshot: j.Snapshot}
				for _, input := range j.Inputs {
					d := MainDecision{Action: "ignore"}
					if input.Kind == "user" {
						d.Action = "new"
					}
					out.Decisions = append(out.Decisions, MainInputDecision{InputID: input.ID, Decision: d})
				}
				return out, nil
			}
			a, err := OpenMainJournal(t.Context(), leasedMemory(t), MainJournalOptions{Parallel: 1, BatchRouter: route, BatchOptions: MainBatchOptions{MaxItems: 8, MaxInputBytes: 1 << 20}, Worker: worker, Background: &options})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			for _, id := range []string{"denied", "first", "quota"} {
				submit(t, a, Trigger{ID: id, Source: "im", Thread: "chat", Kind: "request"})
				waitIdle(t, a)
				result, capability := receive(t, results), receive(t, capabilities)
				if (id == "first") != (result == nil) {
					t.Fatalf("launch %s: %v", id, result)
				}
				if _, err := capability.Start(t.Context(), BackgroundStart{Tool: "slow", CallID: "stale", Arguments: json.RawMessage(`{}`)}); err == nil {
					t.Fatal("stale capability launched")
				}
			}
			// WaitIdle excludes independent backend observations. Establish the
			// observation explicitly before asserting unknown-state barriers.
			if err := a.RefreshBackground(t.Context(), a.BackgroundOperations()[0].ID); err != nil {
				t.Fatal(err)
			}
			op := a.BackgroundOperations()[0]
			if dispatches.Load() != 1 || authorizations.Load() != 3 || op.CallID != "same-call-id" {
				t.Fatal("authority or independent quota bypassed")
			}
			if unknown {
				if !op.NeedsReview || op.Exited || op.Reconciled {
					t.Fatal("unknown became terminal")
				}
				if err := a.ReconcileBackground(op.ID, "assume no effects"); err == nil {
					t.Fatal("note released unknown operation")
				}
				if ok, err := a.SubmitTask(Trigger{ID: "withheld", Source: "im", Thread: "chat", Kind: "request"}, op.TaskID); !ok || err != nil {
					t.Fatalf("admission: %v %v", ok, err)
				}
				waitIdle(t, a)
				if report := waitBatchBackgroundReport(t, a, "withheld"); report.Status != "blocked" || workerCalls.Load() != 3 || dispatches.Load() != 1 {
					t.Fatal("unknown barrier replayed work")
				}
			}
		})
	}
}

package schedulereval

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

func TestExperimentReproducibleAndHonest(t *testing.T) {
	suite, err := RunSuite()
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Strategies) != 4 || len(suite.Backlog) != 50 {
		t.Fatal("experiment matrix incomplete")
	}
	for i, s := range suite.Strategies {
		if i >= 2 {
			if s.Status != "missing" || len(s.Runs) != 0 {
				t.Fatal("unimplemented Pi strategy has invented results")
			}
			continue
		}
		if s.Status != "simulated" || len(s.Runs) != 5 {
			t.Fatal("missing repetitions")
		}
		first := s.Runs[0]
		first.Repetition = 0
		for _, r := range s.Runs {
			if r.Metrics.Deliveries != 300 || r.Metrics.Unique != 280 || r.Metrics.Duplicates != 20 || r.Metrics.CorrectEvents != 280 || r.Metrics.Misroutes != 0 || r.Metrics.Missed != 0 || r.Metrics.ActualDelegations != 20 || r.Metrics.CorrectCompleted != 16 || r.Metrics.CorrectCanceled != 4 {
				t.Fatalf("conservation/quality: %+v", r.Metrics)
			}
			if r.Metrics.RealModelCalls != 0 || r.Metrics.InputTokens != nil || r.Metrics.OutputTokens != nil {
				t.Fatal("invented model usage")
			}
			if len(r.CompletionCurve) != 16 || r.CompletionCurve[15].Completed != 16 {
				t.Fatal("wrong completion curve")
			}
			if len(r.Tasks) != 20 || len(r.Events) != 300 {
				t.Fatal("missing event/task ledger")
			}
			for _, event := range r.Events {
				if !event.Correct || event.SettledMS < event.Event.AtMS {
					t.Fatalf("bad event %+v", event)
				}
			}
			for _, batch := range r.Batches {
				if len(batch.Keys) > suite.Budget.MaxBatch || batch.InputBytes > suite.Budget.MaxInputBytes {
					t.Fatal("exceeded budget")
				}
			}
			r.Repetition = 0
			if !reflect.DeepEqual(first, r) {
				t.Fatal("identical deterministic repetitions diverged")
			}
		}
	}
	if suite.Strategies[0].Runs[0].Metrics.ScriptRouterCalls >= suite.Strategies[1].Runs[0].Metrics.ScriptRouterCalls {
		t.Fatal("subscription bypass did not remove Router work")
	}
	a, _ := json.Marshal(suite)
	other, err := RunSuite()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(other)
	if string(a) != string(b) {
		t.Fatal("result bytes depend on wall clock/random identity")
	}
}

func TestOracleCatchesDroppedAndWrongAssignments(t *testing.T) {
	for _, kind := range []string{"drop", "misroute", "wrong_action"} {
		t.Run(kind, func(t *testing.T) {
			w := Fixture()
			bad := func(e Event) Expected {
				if e.Kind == "followup" && e.WorkID == "work-02" {
					switch kind {
					case "drop":
						return Expected{Action: "ignore"}
					case "misroute":
						return Expected{Action: "attach", TaskID: "work-03"}
					default:
						return Expected{Action: "cancel", TaskID: e.WorkID}
					}
				}
				return scriptDecision(e)
			}
			r, err := evaluate(w, DefaultBudget(), true, bad)
			if err != nil {
				t.Fatal(err)
			}
			if r.Metrics.CorrectEvents == 280 || r.Metrics.Missed == 0 {
				t.Fatal("incorrect routing reported perfect")
			}
			if r.Metrics.EffectiveReplies >= 20 {
				t.Fatal("incorrect task reply still counted as effective")
			}
			if r.Metrics.CorrectCompleted >= 16 {
				t.Fatal("task with incorrect input still counted as correct completion")
			}
			if kind == "misroute" && r.Metrics.Misroutes != 1 {
				t.Fatal("wrong task not detected")
			}
		})
	}
}
func TestFixtureValidation(t *testing.T) {
	mutations := []func(*Workload){
		func(w *Workload) { w.Events[0].Sequence = 0 }, func(w *Workload) { w.Events[0].AtMS = -1 }, func(w *Workload) { w.Events[0].WorkID = "missing" }, func(w *Workload) { delete(w.Oracle, w.Events[0].key()) }, func(w *Workload) { w.Tasks[0].ToolMS = 0 }, func(w *Workload) { w.Events[1].ID = w.Events[0].ID },
	}
	for i, mutate := range mutations {
		w := Fixture()
		mutate(&w)
		if w.Validate() == nil {
			t.Fatalf("mutation %d accepted", i)
		}
	}
}
func TestNearestRankQuantiles(t *testing.T) {
	if quantile(nil, 50) != nil {
		t.Fatal("empty sample became zero")
	}
	if *quantile([]int64{5, 1, 3, 2, 4}, 50) != 3 || *quantile([]int64{5, 1, 3, 2, 4}, 95) != 5 {
		t.Fatal("wrong percentile convention")
	}
}
func TestRouterBacklogConservation(t *testing.T) {
	for _, rate := range []int{1, 5, 10, 20, 40} {
		for _, maxBatch := range []int{1, 8} {
			r, err := backlog(rate, maxBatch)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, b := range r.Batches {
				if len(b.Keys) < 1 || len(b.Keys) > maxBatch || b.FinishedMS-b.StartedMS != 2000+200*int64(len(b.Keys)) {
					t.Fatal("bad batch")
				}
				for _, id := range b.Keys {
					if seen[id] {
						t.Fatal("repeated decision")
					}
					seen[id] = true
				}
			}
			if len(seen) != 300 || len(r.Items) != 300 {
				t.Fatal("lost input")
			}
			for _, item := range r.Items {
				if item.StartedMS < item.ArrivedMS || item.SettledMS <= item.StartedMS || !seen[item.ID] {
					t.Fatal("invalid item time")
				}
			}
			if rate == 1 && len(r.Batches) != 300 {
				t.Fatal("idle events debounced")
			}
		}
	}
	one, _ := backlog(40, 1)
	batch, _ := backlog(40, 8)
	if *one.QueueP95MS <= *batch.QueueP95MS || len(one.Batches) <= len(batch.Batches) {
		t.Fatal("fixed-service overload fixture did not create backlog")
	}
}

func TestRejectZeroStopDuration(t *testing.T) {
	b := DefaultBudget()
	b.StopMS = 0
	if _, err := evaluate(Fixture(), b, true, scriptDecision); err == nil {
		t.Fatal("zero stop duration accepted")
	}
}

func TestCheckedInBaseline(t *testing.T) {
	raw, err := os.ReadFile("testdata/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var want Summary
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	suite, err := RunSuite()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Summarize(suite)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("checked-in baseline is stale; review fixture/method changes before regenerating")
	}
}

func TestWorkerStartsFollowAdmissionOrder(t *testing.T) {
	w := Fixture()
	normal, err := evaluate(w, DefaultBudget(), true, scriptDecision)
	if err != nil {
		t.Fatal(err)
	}
	// Once four workers are occupied, the remaining tasks queue. Reversing only
	// the catalogue must not reverse those queued tasks when slots become free.
	slices.Reverse(w.Tasks)
	reversed, err := evaluate(w, DefaultBudget(), true, scriptDecision)
	if err != nil {
		t.Fatal(err)
	}
	starts := map[string]*int64{}
	for _, task := range normal.Tasks {
		starts[task.ID] = task.StartedMS
	}
	for _, task := range reversed.Tasks {
		if !reflect.DeepEqual(starts[task.ID], task.StartedMS) {
			t.Fatalf("catalogue changed queued start for %s: got %v want %v", task.ID, task.StartedMS, starts[task.ID])
		}
	}
}

package schedulereval

import (
	"encoding/json"
	"fmt"
	"slices"
)

type Budget struct {
	Workers       int   `json:"workers"`
	RouterBaseMS  int64 `json:"router_base_ms"`
	RouterItemMS  int64 `json:"router_item_ms"`
	MaxBatch      int   `json:"max_batch"`
	MaxInputBytes int   `json:"max_input_bytes"`
	StopMS        int64 `json:"stop_ms"`
}

func DefaultBudget() Budget {
	return Budget{Workers: 4, RouterBaseMS: 1000, RouterItemMS: 100, MaxBatch: 8, MaxInputBytes: 32768, StopMS: 2000}
}
func (b Budget) validate() error {
	if b.Workers < 1 || b.Workers > 64 || b.RouterBaseMS < 1 || b.RouterItemMS < 0 || b.MaxBatch < 1 || b.MaxBatch > 64 || b.MaxInputBytes < 1024 || b.StopMS < 1 {
		return fmt.Errorf("invalid experiment budget")
	}
	return nil
}

type EventRecord struct {
	Event       Event    `json:"event"`
	Expected    Expected `json:"expected"`
	Observed    Expected `json:"observed"`
	Disposition string   `json:"disposition"`
	Route       string   `json:"route"`
	SettledMS   int64    `json:"settled_ms"`
	Correct     bool     `json:"correct"`
}
type TaskRecord struct {
	ID          string `json:"id"`
	RequestedMS int64  `json:"requested_ms"`
	StartedMS   *int64 `json:"started_ms"`
	TerminalMS  *int64 `json:"terminal_ms"`
	ReplyMS     *int64 `json:"reply_ms"`
	State       string `json:"state"`
	Correct     bool   `json:"correct"`
}
type Batch struct {
	StartedMS   int64    `json:"started_ms"`
	FinishedMS  int64    `json:"finished_ms"`
	Keys        []string `json:"keys"`
	InputBytes  int      `json:"input_bytes"`
	QueueWaitMS []int64  `json:"queue_wait_ms"`
}
type Point struct {
	AtMS      int64 `json:"at_ms"`
	Completed int   `json:"completed"`
}
type Metrics struct {
	Deliveries               int    `json:"deliveries"`
	Unique                   int    `json:"unique_events"`
	Duplicates               int    `json:"duplicate_deliveries"`
	Ignored                  int    `json:"ignored_events"`
	CorrectEvents            int    `json:"correct_events"`
	Misroutes                int    `json:"misroutes"`
	Missed                   int    `json:"missed_routes"`
	ActualDelegations        int    `json:"actual_delegations"`
	ToolStarts               int    `json:"tool_starts"`
	CorrectCompleted         int    `json:"correct_completed"`
	EffectiveReplies         int    `json:"effective_replies"`
	ActualCompleted          int    `json:"actual_completed"`
	ActualCanceled           int    `json:"actual_canceled"`
	CorrectCanceled          int    `json:"correct_canceled"`
	FirstEffectiveReplyP50MS *int64 `json:"first_effective_reply_p50_ms"`
	FirstEffectiveReplyP95MS *int64 `json:"first_effective_reply_p95_ms"`
	CompletionP50MS          *int64 `json:"completion_p50_ms"`
	CompletionP95MS          *int64 `json:"completion_p95_ms"`
	ScriptRouterCalls        int    `json:"script_router_calls"`
	ScriptWorkerCalls        int    `json:"script_worker_calls"`
	RealModelCalls           int    `json:"real_model_calls"`
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	TokenStatus              string `json:"token_status"`
}
type Run struct {
	Repetition      int           `json:"repetition"`
	Events          []EventRecord `json:"events"`
	Tasks           []TaskRecord  `json:"tasks"`
	Batches         []Batch       `json:"router_batches"`
	CompletionCurve []Point       `json:"completion_curve"`
	Metrics         Metrics       `json:"metrics"`
}
type Strategy struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Limitation string `json:"limitation"`
	Runs       []Run  `json:"runs"`
}
type Suite struct {
	Schema             string       `json:"schema"`
	BaseCommit         string       `json:"base_commit"`
	WorkloadDigest     string       `json:"workload_sha256"`
	Workload           Workload     `json:"workload"`
	Budget             Budget       `json:"budget"`
	HistoricalBaseline string       `json:"pre_m1_baseline_status"`
	Strategies         []Strategy   `json:"strategies"`
	Backlog            []BacklogRun `json:"router_backlog"`
}

// RunSuite deliberately leaves the two single-Pi baselines missing. Scripted
// labels cannot measure prompt preference, semantic accuracy or model tokens.
func RunSuite() (Suite, error) {
	w, b := Fixture(), DefaultBudget()
	s := Suite{Schema: "m9a-offline-v1", BaseCommit: BaseCommit, WorkloadDigest: w.Digest(), Workload: w, Budget: b, HistoricalBaseline: "missing: no pre-M1 scheduling measurements were retained; current legacy-API tests are compatibility evidence, not a historical baseline"}
	for _, direct := range []bool{true, false} {
		name := "full_architecture_surrogate"
		if !direct {
			name = "subscription_bypass_disabled_surrogate"
		}
		entry := Strategy{Name: name, Status: "simulated", Limitation: "discrete-event structural surrogate using scripted intent and host associations; not full SuperAgent, Pi inference, model-quality or wall-time measurement"}
		for rep := 1; rep <= 5; rep++ {
			r, err := evaluate(w, b, direct, scriptDecision)
			if err != nil {
				return s, err
			}
			r.Repetition = rep
			entry.Runs = append(entry.Runs, r)
		}
		s.Strategies = append(s.Strategies, entry)
	}
	for _, name := range []string{"single_pi_neutral_subtask_prompt", "single_pi_delegate_first_prompt"} {
		s.Strategies = append(s.Strategies, Strategy{Name: name, Status: "missing", Limitation: "no equivalent single-Pi subtask harness/prompt experiment implemented; a canned model would not measure the prompt policy"})
	}
	for _, rate := range []int{1, 5, 10, 20, 40} {
		for _, batch := range []int{1, 8} {
			for rep := 1; rep <= 5; rep++ {
				r, err := backlog(rate, batch)
				if err != nil {
					return s, err
				}
				r.Repetition = rep
				s.Backlog = append(s.Backlog, r)
			}
		}
	}
	return s, nil
}

type decision func(Event) Expected

func scriptDecision(e Event) Expected {
	switch e.Kind {
	case "request":
		return Expected{"new", e.WorkID}
	case "followup", "callback":
		return Expected{"attach", e.WorkID}
	case "cancel":
		return Expected{"cancel", e.WorkID}
	default:
		return Expected{"ignore", ""}
	}
}

type pending struct {
	index  int
	key    string
	at     int64
	result string
	event  Event
}
type simTask struct {
	spec         Task
	record       TaskRecord
	due          int64
	stopping     bool
	resultQueued bool
	assigned     bool
}

func evaluate(w Workload, b Budget, direct bool, decide decision) (Run, error) {
	var r Run
	if err := w.Validate(); err != nil {
		return r, err
	}
	if err := b.validate(); err != nil {
		return r, err
	}
	tasks := map[string]*simTask{}
	for _, t := range w.Tasks {
		tasks[t.ID] = &simTask{spec: t, record: TaskRecord{ID: t.ID, State: "unassigned"}}
	}
	seen := map[string]bool{}
	queue := []pending{}
	active := []pending{}
	var routerDue, now int64
	next := 0
	activeWorkers := 0
	workerQueue := []string{}
	enqueueResult := func(t *simTask) {
		if !t.resultQueued {
			queue = append(queue, pending{index: -1, key: "result/" + t.spec.ID, at: now, result: t.spec.ID})
			t.resultQueued = true
		}
	}
	apply := func(p pending) error {
		if p.result != "" {
			v := now
			tasks[p.result].record.ReplyMS = &v
			return nil
		}
		record := &r.Events[p.index]
		d := decide(p.event)
		record.Observed = d
		record.SettledMS = now
		record.Disposition = "settled"
		t := tasks[d.TaskID]
		switch d.Action {
		case "new":
			if t == nil || t.assigned {
				record.Disposition = "rejected"
				return nil
			}
			t.assigned = true
			t.record.State = "queued"
			workerQueue = append(workerQueue, d.TaskID)
			t.record.RequestedMS = p.event.AtMS
			r.Metrics.ActualDelegations++
		case "attach":
			if t == nil || !t.assigned {
				record.Disposition = "rejected"
			}
		case "cancel":
			if t == nil || !t.assigned {
				record.Disposition = "rejected"
				return nil
			}
			if t.record.State == "running" {
				t.stopping = true
				t.due = now + b.StopMS
			} else if t.record.State == "queued" {
				t.record.State = "canceled"
				v := now
				t.record.TerminalMS = &v
				enqueueResult(t)
			} else {
				record.Disposition = "rejected"
			}
		case "ignore":
			record.Disposition = "ignored"
		default:
			record.Disposition = "rejected"
		}
		return nil
	}
	for {
		// All times are virtual. At a tie, tool/router completions precede inbound
		// admission, then FIFO Router dispatch and FIFO task starts.
		for _, spec := range w.Tasks {
			t := tasks[spec.ID]
			if t.record.State == "running" && t.due == now {
				activeWorkers--
				v := now
				t.record.TerminalMS = &v
				t.record.State = "completed"
				if t.stopping {
					t.record.State = "canceled"
				}
				enqueueResult(t)
			}
		}
		if len(active) > 0 && routerDue == now {
			for _, p := range active {
				if err := apply(p); err != nil {
					return r, err
				}
			}
			active = nil
		}
		for next < len(w.Events) && w.Events[next].AtMS == now {
			e := w.Events[next]
			next++
			rec := EventRecord{Event: e, Expected: w.Oracle[e.key()], SettledMS: now}
			if seen[e.key()] {
				rec.Disposition = "duplicate"
				rec.Route = "deduplicated"
				rec.Correct = true
				r.Events = append(r.Events, rec)
				continue
			}
			seen[e.key()] = true
			idx := len(r.Events)
			r.Events = append(r.Events, rec)
			p := pending{index: idx, key: e.key(), at: now, event: e}
			if e.Kind == "noise" {
				r.Events[idx].Route = "admission_filter"
				_ = apply(p)
			} else if e.Kind == "cancel" || (direct && (e.Kind == "followup" || e.Kind == "callback") && tasks[e.WorkID].assigned) {
				r.Events[idx].Route = "host_association"
				_ = apply(p)
			} else {
				r.Events[idx].Route = "router"
				queue = append(queue, p)
			}
		}
		if len(active) == 0 && len(queue) > 0 {
			batch := Batch{StartedMS: now}
			for len(queue) > 0 && len(active) < b.MaxBatch {
				candidate := append(slices.Clone(active), queue[0])
				events := make([]Event, 0, len(candidate))
				for _, p := range candidate {
					e := p.event
					if p.result != "" {
						e = Event{Kind: "worker_result", WorkID: p.result}
					}
					events = append(events, e)
				}
				encoded, err := json.Marshal(events)
				if err != nil {
					return r, err
				}
				if len(encoded) > b.MaxInputBytes {
					if len(active) == 0 {
						return r, fmt.Errorf("single input exceeds budget")
					}
					break
				}
				p := queue[0]
				queue = queue[1:]
				active = append(active, p)
				batch.Keys = append(batch.Keys, p.key)
				batch.QueueWaitMS = append(batch.QueueWaitMS, now-p.at)
				batch.InputBytes = len(encoded)
			}
			routerDue = now + b.RouterBaseMS + b.RouterItemMS*int64(len(active))
			batch.FinishedMS = routerDue
			r.Batches = append(r.Batches, batch)
		}
		for len(workerQueue) > 0 && activeWorkers < b.Workers {
			t := tasks[workerQueue[0]]
			workerQueue = workerQueue[1:]
			if t.record.State != "queued" {
				continue
			}
			t.record.State = "running"
			v := now
			t.record.StartedMS = &v
			t.due = now + t.spec.ToolMS
			activeWorkers++
			r.Metrics.ToolStarts++
		}
		future := int64(-1)
		consider := func(v int64) {
			if v > now && (future < 0 || v < future) {
				future = v
			}
		}
		if next < len(w.Events) {
			consider(w.Events[next].AtMS)
		}
		if len(active) > 0 {
			consider(routerDue)
		}
		for _, t := range tasks {
			if t.record.State == "running" {
				consider(t.due)
			}
		}
		if future < 0 {
			break
		}
		now = future
	}
	if next != len(w.Events) || len(queue) != 0 || len(active) != 0 || activeWorkers != 0 {
		return r, fmt.Errorf("simulation did not drain")
	}
	r.Metrics.Deliveries = len(r.Events)
	r.Metrics.Unique = len(seen)
	r.Metrics.ScriptRouterCalls = len(r.Batches)
	r.Metrics.ScriptWorkerCalls = r.Metrics.ToolStarts
	r.Metrics.TokenStatus = "missing: no tokenizer/provider usage; synthetic bytes and call counts are not token or cost measurements"
	badTasks := map[string]bool{}
	for i := range r.Events {
		rec := &r.Events[i]
		if rec.Disposition == "duplicate" {
			r.Metrics.Duplicates++
			continue
		}
		rec.Correct = rec.Expected == rec.Observed && rec.Disposition != "rejected"
		if rec.Correct {
			r.Metrics.CorrectEvents++
		}
		if rec.Disposition == "ignored" {
			r.Metrics.Ignored++
		}
		if !rec.Correct {
			badTasks[rec.Expected.TaskID] = true
			badTasks[rec.Observed.TaskID] = true
			if rec.Observed.TaskID != "" && rec.Observed.TaskID != rec.Expected.TaskID {
				r.Metrics.Misroutes++
			}
			if rec.Expected.Action != "ignore" {
				r.Metrics.Missed++
			}
		}
	}
	var replies, latencies []int64
	for _, spec := range w.Tasks {
		t := tasks[spec.ID]
		t.record.Correct = !badTasks[spec.ID] && ((spec.Cancel && t.record.State == "canceled") || (!spec.Cancel && t.record.State == "completed"))
		r.Tasks = append(r.Tasks, t.record)
		if t.record.State == "completed" {
			r.Metrics.ActualCompleted++
		}
		if t.record.State == "canceled" {
			r.Metrics.ActualCanceled++
		}
		if t.record.Correct && t.record.ReplyMS != nil {
			r.Metrics.EffectiveReplies++
			replies = append(replies, *t.record.ReplyMS-t.record.RequestedMS)
		}
		if !spec.Cancel && t.record.State == "completed" && !badTasks[spec.ID] {
			r.Metrics.CorrectCompleted++
			latencies = append(latencies, *t.record.TerminalMS-t.record.RequestedMS)
			r.CompletionCurve = append(r.CompletionCurve, Point{AtMS: *t.record.TerminalMS})
		}
		if spec.Cancel && t.record.State == "canceled" && !badTasks[spec.ID] {
			r.Metrics.CorrectCanceled++
		}
	}
	slices.SortFunc(r.CompletionCurve, func(a, b Point) int {
		if a.AtMS < b.AtMS {
			return -1
		}
		if a.AtMS > b.AtMS {
			return 1
		}
		return 0
	})
	for i := range r.CompletionCurve {
		r.CompletionCurve[i].Completed = i + 1
	}
	r.Metrics.FirstEffectiveReplyP50MS = quantile(replies, 50)
	r.Metrics.FirstEffectiveReplyP95MS = quantile(replies, 95)
	r.Metrics.CompletionP50MS = quantile(latencies, 50)
	r.Metrics.CompletionP95MS = quantile(latencies, 95)
	return r, nil
}
func quantile(values []int64, p int) *int64 {
	if len(values) == 0 {
		return nil
	}
	v := slices.Clone(values)
	slices.Sort(v)
	index := (len(v)*p+99)/100 - 1
	n := v[index]
	return &n
}

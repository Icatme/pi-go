package superagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"

	"github.com/Icatme/pi-go/agent"
)

// MainBatchRouter is read-only inference over a contiguous inbox prefix. It
// must not execute external effects. Every input requires exactly one decision.
type MainBatchRouter func(context.Context, MainBatchJob) (MainBatchOutcome, error)

type MainBatchJob struct {
	SessionID         string
	ProviderSessionID string
	Inputs            []MainInput
	Snapshot          agent.AgentSnapshot
	Tasks             []TaskInfo
	Runs              []RunTarget
}

type MainInputDecision struct {
	InputID  string       `json:"input_id"`
	Decision MainDecision `json:"decision"`
}

type MainBatchOutcome struct {
	Decisions []MainInputDecision
	Snapshot  agent.AgentSnapshot
}

// MainBatchOptions bounds the serialized Inputs array, not the full model
// context. PiMainBatchRouter independently enforces its total context budget.
type MainBatchOptions struct {
	MaxItems      int // 1–64
	MaxInputBytes int // 1 KiB–1 MiB; a larger single input fails explicitly
}

// OpenMainBatch shares OpenMain's journal and scheduler. No debounce is used.
// Failed groups produce one durable failure per input, no partial effects and
// no automatic retries. Cancellation before commit leaves inputs for reopening.
func OpenMainBatch(ctx context.Context, path string, parallel int, options MainBatchOptions, route MainBatchRouter, work Worker) (*SuperAgent, error) {
	if options.MaxItems < 1 || options.MaxItems > 64 || options.MaxInputBytes < 1024 || options.MaxInputBytes > 1<<20 {
		return nil, errors.New("superagent: main batch requires 1–64 items and 1 KiB–1 MiB input budget")
	}
	return openMain(ctx, path, parallel, nil, route, options, work)
}

func (a *SuperAgent) hasMainRouter() bool { return a.mainRoute != nil || a.mainBatchRoute != nil }

func copyMainInput(input MainInput) MainInput {
	input.Report = copyReport(input.Report)
	input.Control = copyControl(input.Control)
	input.Handback = copyHandback(input.Handback)
	return input
}

func (a *SuperAgent) dispatchMainBatch() bool {
	if a.main.running || a.main.cursor == len(a.main.inbox) {
		return false
	}
	job := MainBatchJob{SessionID: a.main.id, ProviderSessionID: a.main.providerID}
	for _, item := range a.main.inbox[a.main.cursor:] {
		if len(job.Inputs) == a.mainBatchOptions.MaxItems {
			break
		}
		candidate := append(job.Inputs, copyMainInput(item.input))
		encoded, err := json.Marshal(candidate)
		if err != nil || len(encoded) > a.mainBatchOptions.MaxInputBytes {
			if len(job.Inputs) == 0 {
				a.failMainBatch([]string{item.input.ID}, errors.New("main input exceeds batch input budget"), "Main input exceeds the configured batch input budget.")
				return true
			}
			break
		}
		job.Inputs = candidate
	}
	if len(a.main.snapshot) != 0 {
		if err := decodeSnapshot(a.main.snapshot, &job.Snapshot); err != nil {
			a.failMainBatch(batchInputIDs(job.Inputs), err, "Main snapshot could not be read.")
			return true
		}
	} else {
		job.Snapshot.SessionID = a.main.id
	}
	for _, id := range a.taskIDs[max(0, len(a.taskIDs)-maxCandidates):] {
		job.Tasks = append(job.Tasks, TaskInfo{ID: id, Summary: a.tasks[id].summary})
	}
	for _, id := range a.taskIDs {
		if r := a.tasks[id].active; r != nil && !r.returned && !r.aborted {
			job.Runs = append(job.Runs, r.target)
		}
	}
	a.main.running = true
	a.wg.Add(1)
	go a.runMainBatch(job)
	return true
}

func batchInputIDs(inputs []MainInput) []string {
	ids := make([]string, len(inputs))
	for i, input := range inputs {
		ids[i] = input.ID
	}
	return ids
}
func callMainBatch(ctx context.Context, route MainBatchRouter, job MainBatchJob) (out MainBatchOutcome, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = callbackPanic("main batch router", p)
		}
	}()
	return route(ctx, job)
}

func (a *SuperAgent) runMainBatch(job MainBatchJob) {
	defer a.wg.Done()
	inputIDs := batchInputIDs(job.Inputs)
	tasks := make(map[string]bool, len(job.Tasks))
	for _, task := range job.Tasks {
		tasks[task.ID] = true
	}
	runs := make(map[RunTarget]bool, len(job.Runs))
	for _, run := range job.Runs {
		runs[run] = true
	}
	out, err := callMainBatch(a.ctx, a.mainBatchRoute, job)
	decisions := make(map[string]MainDecision, len(out.Decisions))
	if err == nil {
		if len(out.Decisions) != len(inputIDs) {
			err = errors.New("main batch decision count differs from offered inputs")
		}
		for _, entry := range out.Decisions {
			if _, exists := decisions[entry.InputID]; exists || !slices.Contains(inputIDs, entry.InputID) {
				err = errors.New("main batch contains duplicate or unknown input ID")
				break
			}
			d := entry.Decision
			if d.Target != nil {
				target := *d.Target
				d.Target = &target
			}
			decisions[entry.InputID] = d
		}
	}
	var snapshot json.RawMessage
	var validated *validatedSnapshot
	if err == nil {
		snapshot, validated, err = encodeSnapshot(out.Snapshot, job.SessionID)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.main.running = false
	if a.ctx.Err() != nil {
		close(a.changed)
		a.changed = make(chan struct{})
		return
	}
	tr := transition{State: "main_batch_decided", Main: &mainTransition{Snapshot: snapshot}, validatedSnapshot: validated}
	nextTask := len(a.tasks) + 1
	controlled := map[RunTarget]bool{}
	for offset, id := range inputIDs {
		if err != nil {
			break
		}
		d, exists := decisions[id]
		if !exists {
			err = errors.New("main batch omitted an input")
			break
		}
		m := mainTransition{InputID: id, DecisionID: "main-decision-" + id, Decision: d}
		switch d.Action {
		case "new":
			m.Assignment = &transition{State: "queued", Index: a.main.inbox[a.main.cursor+offset].event, Task: "task-" + strconv.Itoa(nextTask), ProviderSessionID: rand.Text()}
			nextTask++
		case "attach":
			if !tasks[d.TaskID] {
				err = errors.New("main batch selected a task not offered to this turn")
			}
			m.Assignment = &transition{State: "queued", Index: a.main.inbox[a.main.cursor+offset].event, Task: d.TaskID}
		case "steer", "cancel":
			if d.Target == nil || !runs[*d.Target] || controlled[*d.Target] {
				err = errors.New("main batch selected an unoffered or repeated control target")
			} else if r, e := a.activeLocked(*d.Target); e != nil || r.aborted {
				err = errors.New("main batch control target is no longer controllable")
			} else {
				controlled[*d.Target] = true
			}
		case "reply", "clarify":
			m.ReplyID = "reply-" + id
		}
		tr.Main.Batch = append(tr.Main.Batch, m)
	}
	if err == nil {
		err = a.commit(tr)
	}
	if err != nil && a.fatal == nil {
		a.failMainBatch(inputIDs, err, "Main batch router failed or returned an invalid decision group.")
	}
}

func (a *SuperAgent) failMainBatch(ids []string, cause error, text string) {
	m := &mainTransition{}
	if a.main.causes == nil {
		a.main.causes = map[string]error{}
	}
	for offset, id := range ids {
		m.Batch = append(m.Batch, mainTransition{InputID: id})
		a.main.causes[id] = cause
		item := a.main.inbox[a.main.cursor+offset]
		if item.input.Kind == "user" {
			a.events[item.event].cause = cause
		}
	}
	a.commit(transition{State: "main_batch_failed", Text: text, Main: m})
}

// Only reducer-owned fields are copied. No callbacks, locks, storage or active
// run handles are invoked by this shadow. Mutable backing arrays/maps and task
// values are independent, including paths touched by legacy binding imports.
func (a *SuperAgent) mainBatchShadow() *SuperAgent {
	s := &SuperAgent{main: a.main, events: slices.Clone(a.events), pending: slices.Clone(a.pending), tasks: make(map[string]*task, len(a.tasks)), taskIDs: slices.Clone(a.taskIDs), accepted: maps.Clone(a.accepted), subscriptions: maps.Clone(a.subscriptions), threadSubscriptions: make(map[[2]string][]string, len(a.threadSubscriptions)), reports: slices.Clone(a.reports), routing: a.routing, running: a.running}
	for id, t := range a.tasks {
		v := *t
		s.tasks[id] = &v
	}
	for key, ids := range a.threadSubscriptions {
		s.threadSubscriptions[key] = slices.Clone(ids)
	}
	for i := range s.events {
		s.events[i].controls = slices.Clone(s.events[i].controls)
	}
	s.main.inbox = slices.Clone(a.main.inbox)
	s.main.replies = slices.Clone(a.main.replies)
	s.main.controls = slices.Clone(a.main.controls)
	s.main.controlIndex = maps.Clone(a.main.controlIndex)
	s.main.failures = slices.Clone(a.main.failures)
	return s
}

func (a *SuperAgent) validateMainBatch(tr transition) error {
	if a.main.id == "" || tr.Main == nil || len(tr.Main.Batch) == 0 || len(tr.Main.Batch) > 64 || len(tr.Main.Batch) > len(a.main.inbox)-a.main.cursor {
		return errors.New("invalid main batch size or session")
	}
	if tr.State != "main_batch_decided" && tr.State != "main_batch_failed" {
		return errors.New("invalid main batch state")
	}
	m := tr.Main
	if m.InputID != "" || m.DecisionID != "" || m.SessionID != "" || m.ProviderSessionID != "" || m.Assignment != nil || m.ReplyID != "" || m.Delivery != nil || m.DeliveryResult != nil || len(m.ControlIDs) != 0 || m.Decision != (MainDecision{}) {
		return errors.New("unexpected main batch envelope fields")
	}
	if tr.State == "main_batch_failed" && len(m.Snapshot) != 0 {
		return errors.New("failed batch cannot replace snapshot")
	}
	controlled := map[RunTarget]bool{}
	for _, child := range m.Batch {
		if child.SessionID != "" || child.ProviderSessionID != "" || child.Delivery != nil || child.DeliveryResult != nil || len(child.ControlIDs) != 0 {
			return errors.New("unexpected main batch item fields")
		}
		if tr.State == "main_batch_failed" && (child.Decision != (MainDecision{}) || child.DecisionID != "" || child.Assignment != nil || child.ReplyID != "") {
			return errors.New("failed batch cannot contain actions")
		}
		if child.Decision.Action == "attach" && a.tasks[child.Decision.TaskID] == nil {
			return errors.New("batch cannot attach to a task created by this group")
		}
		if target := child.Decision.Target; target != nil {
			if controlled[*target] {
				return errors.New("batch repeats a control target")
			}
			controlled[*target] = true
		}
	}
	shadow := a.mainBatchShadow()
	return shadow.reduceMainBatch(tr, false)
}

func (a *SuperAgent) reduceMainBatch(tr transition, replay bool) error {
	// Validate snapshot once and carry the content-bound private proof through
	// each reduction. Replay obtains a fresh proof from persisted bytes.
	proof := tr.validatedSnapshot
	if tr.State == "main_batch_decided" {
		decoded, err := validateSnapshot(tr.Main.Snapshot, a.main.id, proof)
		if err != nil {
			return errors.New("invalid main batch snapshot")
		}
		if proof == nil {
			proof = &validatedSnapshot{raw: string(tr.Main.Snapshot), value: decoded}
		}
	}
	for _, child := range tr.Main.Batch {
		if len(child.Batch) != 0 || len(child.Snapshot) != 0 {
			return errors.New("nested main batch or per-item snapshot")
		}
		child.Snapshot = tr.Main.Snapshot
		state := "main_decided"
		if tr.State == "main_batch_failed" {
			state = "main_failed"
		}
		item := transition{State: state, Text: tr.Text, Main: &child, validatedSnapshot: proof}
		if err := a.validateMain(item); err != nil {
			return err
		}
		if err := a.reduceMain(item, replay, true); err != nil {
			return err
		}
	}
	if tr.State == "main_batch_decided" {
		a.main.snapshot = slices.Clone(tr.Main.Snapshot)
	}
	return nil
}

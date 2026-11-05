package superagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

const journalKind = "superagent.transition"
const maxCandidates = 128
const maxSnapshotBytes = 8 << 20
const maxReportBytes = 64 << 10

type transition struct {
	validatedSnapshot *validatedSnapshot // process-local content proof; never serialized
	Main              *mainTransition    `json:"main,omitempty"`
	State             string             `json:"state"`
	Index             int                `json:"index"`
	Trigger           *Trigger           `json:"trigger,omitempty"`
	Task              string             `json:"task,omitempty"`
	ProviderSessionID string             `json:"provider_session_id,omitempty"`
	Text              string             `json:"text,omitempty"`
	Snapshot          json.RawMessage    `json:"snapshot,omitempty"`
	Receipts          []ToolReceipt      `json:"receipts,omitempty"`
	NeedsReview       bool               `json:"needs_review,omitempty"`
}

type item struct {
	trigger      Trigger
	state, task  string
	entryTask    string   // explicit host association, part of admission identity
	chatEntry    bool     // admitted after the persistent chat routing boundary
	controls     []string // durable controls aimed at this exact invocation
	delegation   string
	mainInput    bool
	mainAdmitted bool
	cause        error // trusted in-process diagnostic; never serialized
}

type task struct {
	delegation        string
	active            *activeRun
	progressSeq       uint64
	id, summary, last string
	providerSessionID string
	snapshot          json.RawMessage
	pending           int
	running           bool
	review            int  // one-based admission index; zero means clear
	admitFrom         int  // events already admitted at reconciliation remain blocked
	legacyReview      bool // pre-barrier journal may already record later executions
}

// SuperAgent owns one exclusive JSONL writer. Call Close to cancel callbacks,
// wait for them to return, and release the journal lock.
type SuperAgent struct {
	progress          progressHub
	mu                sync.Mutex
	wg                sync.WaitGroup
	closeOnce         sync.Once
	ctx               context.Context
	cancel            context.CancelFunc
	store             *session.JSONLStorage
	route             Router
	mainRoute         MainRouter
	main              mainState
	work              Worker
	parallel, running int
	routing           bool
	changed           chan struct{}
	fatal, closeErr   error

	events        []item
	accepted      map[[2]string]int
	pending       []int
	tasks         map[string]*task
	taskIDs       []string
	subscriptions map[[2]string]string
	sequence      int
	reports       []Report
}

// Open resumes queued events. An operation whose start was synced but whose
// outcome was never recorded becomes interrupted and is never reissued.
func Open(ctx context.Context, path string, parallel int, route Router, work Worker) (*SuperAgent, error) {
	if ctx == nil || path == "" || parallel < 1 || parallel > 64 || route == nil || work == nil {
		return nil, errors.New("superagent: require a context, path, 1–64 workers, router and worker")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a, err := openState(ctx, path, true)
	if err != nil {
		return nil, err
	}
	if a.main.id != "" {
		return nil, errors.Join(errors.New("superagent: persistent main journal requires OpenMain"), a.Close())
	}
	if err := a.recoverInterrupted(); err != nil {
		return nil, errors.Join(err, a.Close())
	}
	a.parallel, a.route, a.work = parallel, route, work
	a.wg.Add(1)
	go a.schedule()
	return a, nil
}

func (a *SuperAgent) restore() error {
	if a.store.Header().ID != "superagent" {
		return errors.New("superagent: wrong journal header")
	}
	for _, line := range a.store.Log() {
		e := line.Entry
		if e == nil || e.Lane != session.MainLane || e.Type != session.EntryTypeCustom || e.Custom == nil || e.Custom.CustomType != journalKind {
			return fmt.Errorf("superagent: unexpected journal entry at %d", line.Seq)
		}
		var tr transition
		if err := json.Unmarshal(e.Custom.Payload, &tr); err != nil {
			return err
		}
		if err := a.apply(tr, true); err != nil {
			return fmt.Errorf("superagent: journal entry %d: %w", line.Seq, err)
		}
		a.sequence++
	}
	return nil
}

func (a *SuperAgent) recoverInterrupted() error {
	for _, i := range slices.Clone(a.pending) {
		if a.events[i].state == "running" || a.events[i].state == "routing" {
			if err := a.commit(transition{Index: i, State: "interrupted", NeedsReview: a.events[i].state == "running", Text: "Process stopped before the outcome was recorded. Check effects before continuing."}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validTrigger(t Trigger) error {
	for _, value := range []string{t.ID, t.Source, t.Thread} {
		if strings.TrimSpace(value) == "" || len(value) > 256 || !utf8.ValidString(value) {
			return errors.New("event ID, source and thread must be 1–256 UTF-8 bytes")
		}
	}
	if t.Kind != "request" && t.Kind != "signal" && t.Kind != "noise" {
		return fmt.Errorf("unsupported trigger kind %q", t.Kind)
	}
	if !utf8.ValidString(t.Text) || len(t.Text) > 64<<10 {
		return errors.New("event text must be valid UTF-8, at most 64 KiB")
	}
	return nil
}

func brief(text string) string {
	return boundedText(strings.TrimSpace(text), 1024, "…")
}

func boundedText(text string, limit int, suffix string) string {
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= limit {
		return text
	}
	i := limit - len(suffix)
	for !utf8.RuneStart(text[i]) {
		i--
	}
	return text[:i] + suffix
}

// Submit durably accepts a delivery once. Reusing the same source and ID with
// different contents is an error, even after restart.
// With OpenMain, an ordinary chat event reaches main regardless of earlier
// tasks in that chat. Use SubmitTask for an explicit host task association.
func (a *SuperAgent) Submit(t Trigger) (bool, error) {
	return a.submit(t, "")
}

// SubmitTask admits an update explicitly associated by the trusted host with
// one existing task in this journal's authorization domain. Unlike Submit,
// this bypasses main inference; the main session receives a read-only summary.
// It schedules a serial follow-up, not steering of an in-flight invocation.
// Admission is not execution or delivery. Never infer taskID from untrusted
// message text without host authorization or a validated main decision.
func (a *SuperAgent) SubmitTask(t Trigger, taskID string) (bool, error) {
	if !validIdentity(taskID) {
		return false, errors.New("task input requires a valid task identity")
	}
	return a.submit(t, taskID)
}

func (a *SuperAgent) submit(t Trigger, taskID string) (bool, error) {
	if err := validTrigger(t); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fatal != nil {
		return false, a.fatal
	}
	if err := a.ctx.Err(); err != nil {
		return false, err
	}
	if a.isReplyEcho(t) {
		return false, nil
	}
	if i, ok := a.accepted[[2]string{t.Source, t.ID}]; ok {
		if a.events[i].trigger != t || a.events[i].entryTask != taskID {
			return false, errors.New("event ID already exists with different contents")
		}
		return false, nil
	}
	if taskID != "" && (!a.main.chatRouting || a.tasks[taskID] == nil || t.Kind == "noise") {
		return false, errors.New("task input requires OpenMain, an existing task and a non-noise event")
	}
	err := a.commit(transition{State: "received", Index: len(a.events), Trigger: &t, Task: taskID})
	return err == nil, err
}

func (a *SuperAgent) commit(tr transition) error {
	if a.fatal != nil {
		return a.fatal
	}
	if tr.Main != nil {
		if err := a.validateMain(tr); err != nil {
			return err
		}
	}
	data, err := json.Marshal(tr)
	if err == nil {
		_, err = a.store.AppendEntry(session.MainLane, session.NewEntry{Type: session.EntryTypeCustom, ID: strconv.Itoa(a.sequence + 1), Custom: &session.CustomData{CustomType: journalKind, Payload: data}})
	}
	if err == nil {
		err = a.apply(tr, false)
	}
	if err != nil {
		a.fatal = fmt.Errorf("superagent journal: %w", err)
		a.cancel()
	} else {
		a.sequence++
		// Replaying the journal does not emit live progress. Register active
		// control before publishing the running transition in dispatch.
		if a.work != nil && tr.State != "running" && tr.Main == nil {
			e := a.events[tr.Index]
			if e.task != "" {
				a.publishLocked(RunTarget{TaskID: e.task, Source: e.trigger.Source, EventID: e.trigger.ID}, tr.State)
			}
		}
	}
	close(a.changed)
	a.changed = make(chan struct{})
	return a.fatal
}

func (a *SuperAgent) apply(tr transition, replay bool) error {
	if replay {
		// Disk records never inherit a live validation result.
		tr.validatedSnapshot = nil
	}
	if tr.Main != nil || strings.HasPrefix(tr.State, "main_") || strings.HasPrefix(tr.State, "reply_") || strings.HasPrefix(tr.State, "control_") {
		return a.applyMain(tr, replay)
	}
	if tr.State == "received" {
		if tr.Index != len(a.events) || tr.Trigger == nil {
			return errors.New("invalid event admission")
		}
		if err := validTrigger(*tr.Trigger); err != nil {
			return err
		}
		if tr.Task != "" && (!a.main.chatRouting || a.tasks[tr.Task] == nil || tr.Trigger.Kind == "noise") {
			return errors.New("invalid task-associated admission")
		}
		key := [2]string{tr.Trigger.Source, tr.Trigger.ID}
		if _, exists := a.accepted[key]; exists {
			return errors.New("duplicate event admission")
		}
		a.accepted[key] = tr.Index
		a.events = append(a.events, item{trigger: *tr.Trigger, state: "received", entryTask: tr.Task, chatEntry: a.main.chatRouting, mainAdmitted: a.main.id != ""})
		a.pending = append(a.pending, tr.Index)
		if a.main.id != "" && tr.Trigger.Kind != "noise" && (a.main.chatRouting || a.subscriptions[[2]string{tr.Trigger.Source, tr.Trigger.Thread}] == "") {
			a.enqueueMain(tr.Index, nil)
		}
		return nil
	}
	if tr.Index < 0 || tr.Index >= len(a.events) {
		return errors.New("transition references unknown event")
	}
	e := &a.events[tr.Index]
	t := a.tasks[e.task]
	if tr.State == "reconciled" {
		if t == nil || tr.Task != t.id || t.running || t.review != tr.Index+1 || !validNote(tr.Text) {
			return errors.New("invalid task reconciliation")
		}
		t.review, t.admitFrom, t.legacyReview, t.last = 0, len(a.events), false, "ready"
		return nil
	}
	switch tr.State {
	case "routing":
		if e.state != "received" || a.routing {
			return errors.New("invalid routing start")
		}
		a.routing = true
	case "queued":
		if (e.state != "received" && e.state != "routing") || tr.Task == "" {
			return errors.New("invalid assignment")
		}
		thread := [2]string{e.trigger.Source, e.trigger.Thread}
		if previous := a.subscriptions[thread]; !e.chatEntry && previous != "" && previous != tr.Task {
			return errors.New("thread reassignment")
		}
		t = a.tasks[tr.Task]
		if t == nil {
			if tr.Task != a.nextTaskID() || e.state != "routing" {
				return errors.New("invalid new task ID")
			}
			providerSessionID := tr.ProviderSessionID
			if providerSessionID == "" {
				// Pre-affinity journals used the local task ID on the wire.
				providerSessionID = tr.Task
			}
			t = &task{id: tr.Task, providerSessionID: providerSessionID, summary: brief(e.trigger.Text), delegation: e.delegation}
			a.tasks[tr.Task] = t
			a.taskIDs = append(a.taskIDs, tr.Task)
		} else if tr.ProviderSessionID != "" {
			return errors.New("cannot reassign provider session for an existing task")
		}
		if e.state == "routing" {
			a.routing = false
		}
		if e.entryTask != "" && e.entryTask != tr.Task {
			return errors.New("task admission cannot be reassigned")
		}
		if !e.chatEntry {
			a.subscriptions[thread] = tr.Task
		}
		e.task = tr.Task
		if e.delegation == "" {
			e.delegation = t.delegation
		}
		t.pending++
	case "running":
		if e.state != "queued" || t == nil || t.running || (t.blocks(tr.Index) && !(replay && t.legacyReview)) {
			return errors.New("invalid worker start")
		}
		t.pending--
		t.running = true
		a.running++
	case "ignored", "failed", "interrupted", "completed", "completed_with_tool_errors", "truncated", "blocked":
		switch e.state {
		case "received":
			if tr.State != "ignored" || e.trigger.Kind != "noise" {
				return errors.New("invalid admission outcome")
			}
		case "routing":
			if tr.State != "failed" && tr.State != "interrupted" && tr.State != "ignored" {
				return errors.New("router cannot complete work")
			}
			a.routing = false
		case "queued":
			if tr.State != "blocked" || t == nil || !t.blocks(tr.Index) {
				return errors.New("invalid blocked outcome")
			}
			t.pending--
		case "running":
			if t == nil || !t.running || tr.State == "ignored" || tr.State == "blocked" {
				return errors.New("invalid worker outcome")
			}
			finished := tr.State == "completed" || tr.State == "truncated" || tr.State == "completed_with_tool_errors"
			var snap agent.AgentSnapshot
			if finished || len(tr.Snapshot) > 0 {
				var err error
				snap, err = validateSnapshot(tr.Snapshot, t.id, tr.validatedSnapshot)
				if err != nil {
					return errors.New("invalid worker snapshot")
				}
				t.snapshot = tr.Snapshot
			}
			// Correction evidence only reads the validated terminal snapshot.
			a.recordControlInputs(tr.Index, snap)
			if finished {
				t.summary = brief(tr.Text)
			}
			if tr.NeedsReview || tr.State == "interrupted" {
				t.review = tr.Index + 1
				t.legacyReview = replay && tr.State == "interrupted" && !tr.NeedsReview
				tr.NeedsReview = true
			}
			t.running = false
			t.last = tr.State
			a.running--
		default:
			return errors.New("unexpected terminal transition")
		}
		a.reports = append(a.reports, Report{Source: e.trigger.Source, Thread: e.trigger.Thread, EventID: e.trigger.ID, TaskID: e.task, Status: tr.State, Text: tr.Text, Receipts: tr.Receipts, NeedsReview: tr.NeedsReview})
		if a.main.id != "" && e.mainAdmitted && (e.state == "running" || (e.state == "queued" && tr.State == "blocked")) {
			a.enqueueMain(tr.Index, &a.reports[len(a.reports)-1])
		}
		a.pending = slices.DeleteFunc(a.pending, func(i int) bool { return i == tr.Index })
	default:
		return fmt.Errorf("unknown transition %q", tr.State)
	}
	e.state = tr.State
	return nil
}

func (a *SuperAgent) nextTaskID() string { return "task-" + strconv.Itoa(len(a.tasks)+1) }

func (a *SuperAgent) schedule() {
	defer a.wg.Done()
	// Release pending steering receipts even if a worker ignores cancellation.
	defer func() { a.mu.Lock(); defer a.mu.Unlock(); a.dropSteeringLocked() }()
	for {
		a.mu.Lock()
		if a.ctx.Err() != nil {
			a.mu.Unlock()
			return
		}
		progress := a.dispatch()
		ch := a.changed
		a.mu.Unlock()
		if progress {
			continue
		}
		select {
		case <-a.ctx.Done():
			return
		case <-ch:
		}
	}
}

// Called under mu. Only the scheduler starts callbacks, after syncing their
// start. The scheduler itself stays alive in wg until it stops admitting work.
func (a *SuperAgent) dispatch() bool {
	if a.dispatchMainControl() {
		return true
	}
	for _, i := range a.pending {
		e := a.events[i]
		switch e.state {
		case "received":
			if e.trigger.Kind == "noise" {
				a.commit(transition{Index: i, State: "ignored", Text: "Noise event ignored."})
				return true
			}
			if a.mainRoute != nil && e.mainInput {
				continue
			}
			id := e.entryTask
			if !e.chatEntry {
				id = a.subscriptions[[2]string{e.trigger.Source, e.trigger.Thread}]
			}
			if id != "" {
				a.commit(transition{Index: i, State: "queued", Task: id})
				return true
			}
			if a.routing {
				continue
			}
			if a.mainRoute != nil {
				continue
			}
			if a.commit(transition{Index: i, State: "routing"}) == nil {
				var candidates []TaskInfo
				for _, id := range a.taskIDs[max(0, len(a.taskIDs)-maxCandidates):] {
					candidates = append(candidates, TaskInfo{ID: id, Summary: a.tasks[id].summary})
				}
				a.wg.Add(1)
				go a.routeEvent(i, e.trigger, candidates)
			}
			return true
		case "queued":
			t := a.tasks[e.task]
			if t.blocks(i) {
				a.commit(transition{State: "blocked", Index: i, Text: "Delivery withheld by the task review barrier. Submit a new instruction after reconciliation."})
				return true
			}
			if t.running || a.running >= a.parallel {
				continue
			}
			if a.commit(transition{Index: i, State: "running"}) == nil {
				job := Job{TaskID: t.id, ProviderSessionID: t.providerSessionID, Trigger: e.trigger}
				if len(t.snapshot) > 0 {
					if err := decodeSnapshot(t.snapshot, &job.Snapshot); err != nil {
						a.commit(transition{Index: i, State: "failed", Text: brief(err.Error())})
						return true
					}
				}
				ctx, cancel := context.WithCancel(a.ctx)
				active := &activeRun{target: RunTarget{TaskID: t.id, Source: e.trigger.Source, EventID: e.trigger.ID}, ctx: ctx, cancel: cancel}
				t.active = active
				job.TakeSteering = func(ctx context.Context, mode agent.QueueMode) ([]agent.Message, error) {
					return a.takeSteering(ctx, active, mode)
				}
				job.NotifyProgress = func(kind string) { a.notifyWorkerProgress(active, kind) }
				a.publishLocked(active.target, "running")
				a.wg.Add(1)
				go a.runJob(i, job, active)
			}
			return true
		}
	}
	return a.dispatchMain()
}

func (a *SuperAgent) routeEvent(i int, trig Trigger, candidates []TaskInfo) {
	defer a.wg.Done()
	// Keep an independent list of the task IDs actually offered to the callback.
	ids := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		ids[c.ID] = true
	}
	decision, err := callRouter(a.ctx, a.route, trig, candidates)
	a.mu.Lock()
	defer a.mu.Unlock()
	tr := transition{Index: i, State: "queued"}
	switch {
	case a.ctx.Err() != nil:
		tr.State, tr.Text = "interrupted", "Routing interrupted."
		err = errors.Join(a.ctx.Err(), err)
	case err != nil:
		tr.State, tr.Text = "failed", "Router failed."
	case decision.Action == "new" && decision.TaskID == "":
		tr.Task = a.nextTaskID()
		tr.ProviderSessionID = rand.Text()
	case decision.Action == "attach" && ids[decision.TaskID]:
		tr.Task = decision.TaskID
	case decision.Action == "ignore" && decision.TaskID == "":
		tr.State, tr.Text = "ignored", "Router ignored this event."
	default:
		tr.State, tr.Text = "failed", "Router returned an invalid action or task."
		err = errors.New(tr.Text)
	}
	a.events[i].cause = err
	a.commit(tr)
}

func callRouter(ctx context.Context, route Router, trigger Trigger, candidates []TaskInfo) (r Route, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = callbackPanic("router", p)
		}
	}()
	return route(ctx, trigger, candidates)
}

func callWorker(ctx context.Context, work Worker, job Job) (out Outcome, err error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	defer func() {
		if p := recover(); p != nil {
			out.NeedsReview = true
			err = callbackPanic("worker", p)
		}
	}()
	return work(ctx, job)
}

func (a *SuperAgent) runJob(i int, job Job, active *activeRun) {
	defer a.wg.Done()
	defer active.cancel()
	out, err := callWorker(active.ctx, a.work, job)
	a.mu.Lock()
	if active.ctx.Err() != nil {
		active.aborted = true
	}
	active.returned = true
	a.dropRunSteeringLocked(active)
	a.publishLocked(active.target, "finalizing")
	a.mu.Unlock()
	tr := transition{Index: i, State: out.Status, Receipts: slices.Clone(out.Receipts), NeedsReview: out.NeedsReview, Text: boundedText(out.Summary, maxReportBytes, "\n[Report truncated at 64 KiB. See task snapshot: "+job.TaskID+"]")}
	if tr.State == "" {
		tr.State = "completed"
	}
	if tr.State != "completed" && tr.State != "completed_with_tool_errors" && tr.State != "truncated" {
		err = errors.Join(err, errors.New("invalid worker outcome status"))
		tr.NeedsReview = true
	}
	if err == nil || !reflect.ValueOf(out.Snapshot).IsZero() {
		snapshot, validated, snapshotErr := encodeSnapshot(out.Snapshot, job.TaskID)
		if snapshotErr != nil {
			err = errors.Join(err, snapshotErr)
			tr.NeedsReview = true
		} else {
			tr.Snapshot, tr.validatedSnapshot = snapshot, validated
		}
	}
	for _, r := range tr.Receipts {
		if !r.Completed {
			tr.NeedsReview = true
		}
	}
	if err != nil {
		tr.State, tr.Text = "failed", "Worker failed."
		tr.NeedsReview = tr.NeedsReview || len(tr.Receipts) > 0
		if a.ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			tr.State = "interrupted"
			tr.Text = "Worker interrupted. Check effects before continuing."
			tr.NeedsReview = true
		}
	} else if strings.TrimSpace(tr.Text) == "" {
		tr.Text = "Task completed."
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Abort and terminal publication are serialized. An executor may ignore
	// cancellation and return success; retain observations but require review.
	if active.aborted {
		tr.State, tr.Text, tr.NeedsReview = "interrupted", "Worker interrupted. Check effects before continuing.", true
		err = errors.Join(err, context.Canceled)
	}
	a.tasks[job.TaskID].active = nil
	a.events[i].cause = err
	a.commit(tr)
}

func callbackPanic(stage string, p any) error {
	if err, ok := p.(error); ok {
		return fmt.Errorf("%s panicked: %w", stage, err)
	}
	return fmt.Errorf("%s panicked: %v", stage, p)
}

func belongsTo(s agent.AgentSnapshot, taskID string) bool {
	return s.SessionID == taskID || (s.SessionID == "" && len(s.Messages) == 0)
}

func (a *SuperAgent) Status() View {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := View{Reports: cloneReports(a.reports)}
	for _, id := range a.taskIDs {
		t := a.tasks[id]
		status := t.last
		switch {
		case t.review != 0:
			status = "needs_review"
		case t.active != nil:
			status = t.active.phase()
		case t.running:
			status = "running"
		case t.pending > 0:
			status = "queued"
		}
		row := TaskStatus{ID: id, Status: status, Summary: t.summary, Pending: t.pending}
		if t.active != nil && !t.active.returned {
			row.ActiveSource, row.ActiveEventID = t.active.target.Source, t.active.target.EventID
		}
		if t.review != 0 {
			cause := a.events[t.review-1].trigger
			row.ReviewSource, row.ReviewEventID = cause.Source, cause.ID
		}
		v.Tasks = append(v.Tasks, row)
	}
	return v
}

// Done closes when the coordinator is stopping, including on journal failure.
// WaitIdle returns the underlying journal error even while input is still open.
func (a *SuperAgent) Done() <-chan struct{} { return a.ctx.Done() }

// WaitIdle waits until each accepted event has a terminal report. Inspect
// Status for routing and worker failures, including interrupted calls.
func (a *SuperAgent) WaitIdle(ctx context.Context) error {
	if ctx == nil {
		return errors.New("superagent: WaitIdle requires a context")
	}
	for {
		a.mu.Lock()
		err := a.fatal
		pending := len(a.pending) > 0 || a.main.cursor < len(a.main.inbox)
		for _, control := range a.main.controls {
			if control.Status == "pending" {
				pending = true
				break
			}
		}
		if err == nil && pending {
			err = a.ctx.Err()
		}
		done := !pending
		ch := a.changed
		a.mu.Unlock()
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.ctx.Done():
			continue
		case <-ch:
		}
	}
}

func (a *SuperAgent) Close() error {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.cancel()
		a.dropSteeringLocked()
		a.closeProgressLocked()
		a.mu.Unlock()
		a.wg.Wait()
		a.closeErr = errors.Join(a.fatal, a.store.Close())
	})
	return a.closeErr
}
